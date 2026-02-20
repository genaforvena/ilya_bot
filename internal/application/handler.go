package application

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/genaforvena/ilya_bot/internal/domain"
)

// DB defines the database operations needed by the handler.
type DB interface {
	FindOrCreateUser(ctx context.Context, telegramID int64) (*domain.User, error)
	FindAvailableSlots(ctx context.Context) ([]domain.AvailabilitySlot, error)
	BookSlot(ctx context.Context, recruiterID, slotID int) (*domain.Booking, error)
}

// LLM defines the LLM operations needed by the handler.
type LLM interface {
	ClassifyIntent(ctx context.Context, message string) (*domain.Intent, error)
	GenerateResponse(ctx context.Context, userMessage, context_ string) (string, error)
}

// Telegram defines the Telegram operations needed by the handler.
type Telegram interface {
	SendMessage(ctx context.Context, chatID int64, text string) error
}

// Handler orchestrates the bot's business logic.
type Handler struct {
	db                  DB
	llm                 LLM
	telegram            Telegram
	candidateTelegramID int64
}

// NewHandler creates a new Handler.
func NewHandler(db DB, llm LLM, telegram Telegram, candidateTelegramID int64) *Handler {
	return &Handler{
		db:                  db,
		llm:                 llm,
		telegram:            telegram,
		candidateTelegramID: candidateTelegramID,
	}
}

// HandleMessage processes an incoming Telegram message.
func (h *Handler) HandleMessage(ctx context.Context, msg *domain.TelegramMessage) {
	log := slog.With("telegram_id", msg.From.ID, "chat_id", msg.Chat.ID)

	user, err := h.db.FindOrCreateUser(ctx, msg.From.ID)
	if err != nil {
		log.Error("FindOrCreateUser failed", "err", err)
		h.escalate(ctx, msg, "database error")
		return
	}

	intent, err := h.llm.ClassifyIntent(ctx, msg.Text)
	if err != nil {
		log.Error("ClassifyIntent failed", "err", err)
		h.escalate(ctx, msg, "LLM error")
		return
	}

	if h.shouldEscalate(intent) {
		log.Info("escalating message", "intent", intent.Intent, "confidence", intent.Confidence)
		h.escalate(ctx, msg, "uncertain or sensitive topic")
		return
	}

	reply, err := h.buildReply(ctx, user, msg, intent)
	if err != nil {
		log.Error("buildReply failed", "err", err)
		h.escalate(ctx, msg, "reply generation error")
		return
	}

	if sendErr := h.telegram.SendMessage(ctx, msg.Chat.ID, reply); sendErr != nil {
		log.Error("SendMessage failed", "err", sendErr)
	}
}

// shouldEscalate returns true when the intent requires escalation.
func (h *Handler) shouldEscalate(intent *domain.Intent) bool {
	if intent.Confidence < 0.6 {
		return true
	}
	if domain.TopicSensitive(intent.QuestionTopic) {
		return true
	}
	return false
}

// escalate forwards the recruiter's message to the candidate and informs the recruiter.
func (h *Handler) escalate(ctx context.Context, msg *domain.TelegramMessage, reason string) {
	slog.Info("escalating", "reason", reason, "chat_id", msg.Chat.ID)

	fwd := fmt.Sprintf("📨 Recruiter message (from chat %d):\n%s", msg.Chat.ID, msg.Text)
	if err := h.telegram.SendMessage(ctx, h.candidateTelegramID, fwd); err != nil {
		slog.Error("forward to candidate failed", "err", err)
	}

	reply := "I've forwarded this directly. He will reply shortly."
	if err := h.telegram.SendMessage(ctx, msg.Chat.ID, reply); err != nil {
		slog.Error("send escalation notice failed", "err", err)
	}
}

// buildReply constructs the bot's response based on intent.
func (h *Handler) buildReply(ctx context.Context, user *domain.User, msg *domain.TelegramMessage, intent *domain.Intent) (string, error) {
	switch intent.Intent {
	case domain.IntentSchedule:
		return h.handleSchedule(ctx, user, msg, intent)
	case domain.IntentQuestion:
		return h.handleQuestion(ctx, msg, intent)
	case domain.IntentSmalltalk:
		return h.handleSmalltalk(ctx, msg)
	default:
		return h.fallbackTemplate(intent), nil
	}
}

func (h *Handler) handleSchedule(ctx context.Context, user *domain.User, msg *domain.TelegramMessage, intent *domain.Intent) (string, error) {
	slots, err := h.db.FindAvailableSlots(ctx)
	if err != nil {
		return "", fmt.Errorf("FindAvailableSlots: %w", err)
	}

	if len(slots) == 0 {
		return "Unfortunately, there are no available slots at the moment. I'll escalate to Ilya directly.", nil
	}

	// If no proposed time window, list available slots.
	if intent.ProposedTimeWindow.Start == nil {
		return formatSlots(slots), nil
	}

	// Try to match the proposed time window to an available slot.
	matchSlot := findMatchingSlot(slots, intent.ProposedTimeWindow)
	if matchSlot == nil {
		return "That time doesn't match any available slots. Here are the available times:\n\n" + formatSlots(slots), nil
	}

	booking, err := h.db.BookSlot(ctx, user.ID, matchSlot.ID)
	if err != nil {
		return "", fmt.Errorf("BookSlot: %w", err)
	}
	if booking == nil {
		return "That slot was just taken. Here are the remaining available times:\n\n" + formatSlotsFromDB(ctx, h.db), nil
	}

	// Try LLM-generated confirmation
	ctxStr := fmt.Sprintf("Booked slot: %s – %s UTC",
		booking.StartTime.UTC().Format("Mon Jan 2 15:04"),
		booking.EndTime.UTC().Format("15:04"),
	)
	resp, err := h.llm.GenerateResponse(ctx, msg.Text, ctxStr)
	if err != nil {
		slog.Warn("LLM response generation failed, using template", "err", err)
		return fmt.Sprintf("✅ Interview confirmed for %s – %s UTC. See you then!",
			booking.StartTime.UTC().Format("Mon Jan 2, 2006 15:04"),
			booking.EndTime.UTC().Format("15:04"),
		), nil
	}
	return resp, nil
}

func (h *Handler) handleQuestion(ctx context.Context, msg *domain.TelegramMessage, intent *domain.Intent) (string, error) {
	resp, err := h.llm.GenerateResponse(ctx, msg.Text, "")
	if err != nil {
		slog.Warn("LLM response generation failed, using template", "err", err)
		return questionTemplate(intent.QuestionTopic), nil
	}
	return resp, nil
}

func (h *Handler) handleSmalltalk(ctx context.Context, msg *domain.TelegramMessage) (string, error) {
	resp, err := h.llm.GenerateResponse(ctx, msg.Text, "")
	if err != nil {
		return "Hi! I'm Ilya's scheduling assistant. How can I help you today?", nil
	}
	return resp, nil
}

func (h *Handler) fallbackTemplate(intent *domain.Intent) string {
	return fmt.Sprintf("I'm not sure how to help with that (intent: %s). "+
		"You can ask about scheduling an interview or Ilya's background.", intent.Intent)
}

// formatSlots returns a human-readable list of available slots.
func formatSlots(slots []domain.AvailabilitySlot) string {
	var sb strings.Builder
	sb.WriteString("Here are the available interview slots (UTC):\n\n")
	for i, s := range slots {
		sb.WriteString(fmt.Sprintf("%d. %s – %s\n",
			i+1,
			s.StartTime.UTC().Format("Mon Jan 2, 2006 15:04"),
			s.EndTime.UTC().Format("15:04"),
		))
	}
	sb.WriteString("\nReply with your preferred time to book a slot.")
	return sb.String()
}

func formatSlotsFromDB(ctx context.Context, db DB) string {
	slots, err := db.FindAvailableSlots(ctx)
	if err != nil || len(slots) == 0 {
		return "No available slots at the moment."
	}
	return formatSlots(slots)
}

// findMatchingSlot finds the first slot that overlaps the proposed time window.
func findMatchingSlot(slots []domain.AvailabilitySlot, tw domain.TimeWindow) *domain.AvailabilitySlot {
	if tw.Start == nil || tw.End == nil {
		return nil
	}
	for i, s := range slots {
		if !s.StartTime.After(*tw.End) && !s.EndTime.Before(*tw.Start) {
			return &slots[i]
		}
	}
	return nil
}

func questionTemplate(topic *string) string {
	if topic == nil {
		return "I can answer questions about Ilya's background and experience. What would you like to know?"
	}
	switch *topic {
	case "experience":
		return "Ilya has 8+ years of experience in backend development, primarily in Go."
	case "tech_stack":
		return "Ilya's main stack is Go, PostgreSQL, Kubernetes, and distributed systems."
	case "availability":
		return "I can help with scheduling! Please ask about available interview slots."
	default:
		return "I can answer questions about Ilya's background and experience. What would you like to know?"
	}
}

// TimeWindow helper for tests.
func MakeTimeWindow(start, end time.Time) domain.TimeWindow {
	return domain.TimeWindow{Start: &start, End: &end}
}
