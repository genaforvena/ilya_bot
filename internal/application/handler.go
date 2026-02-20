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
	AddAvailabilitySlot(ctx context.Context, start, end time.Time) (*domain.AvailabilitySlot, error)
	DeleteAvailabilitySlot(ctx context.Context, slotID int) error
	StoreEscalation(ctx context.Context, recruiterChatID int64, questionText string, adminMsgID int, reason string) (*domain.Escalation, error)
	FindEscalationByAdminMsgID(ctx context.Context, adminMsgID int) (*domain.Escalation, error)
	ResolveEscalation(ctx context.Context, id int) error
	StoreLearnedAnswer(ctx context.Context, questionText, answerText string, embedding []float32) error
	FindSimilarAnswer(ctx context.Context, embedding []float32, threshold float64) (*domain.LearnedAnswer, error)
}

// LLM defines the LLM operations needed by the handler.
type LLM interface {
	ClassifyIntent(ctx context.Context, message string) (*domain.Intent, error)
	GenerateResponse(ctx context.Context, userMessage, context_ string) (string, error)
}

// Telegram defines the Telegram operations needed by the handler.
type Telegram interface {
	SendMessage(ctx context.Context, chatID int64, text string) (int, error)
}

// Embedder computes a vector embedding for a text string.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
}

// Handler orchestrates the bot's business logic.
type Handler struct {
	db                  DB
	llm                 LLM
	telegram            Telegram
	embedder            Embedder
	candidateTelegramID int64
	similarityThreshold float64
}

// NewHandler creates a new Handler.
func NewHandler(db DB, llm LLM, telegram Telegram, candidateTelegramID int64) *Handler {
	return &Handler{
		db:                  db,
		llm:                 llm,
		telegram:            telegram,
		candidateTelegramID: candidateTelegramID,
		similarityThreshold: 0.85,
	}
}

// WithEmbedder attaches an optional embedder and similarity threshold to the handler.
func (h *Handler) WithEmbedder(e Embedder, threshold float64) *Handler {
	h.embedder = e
	h.similarityThreshold = threshold
	return h
}

// HandleMessage processes an incoming Telegram message.
func (h *Handler) HandleMessage(ctx context.Context, msg *domain.TelegramMessage) {
	log := slog.With("telegram_id", msg.From.ID, "chat_id", msg.Chat.ID)

	// Admin commands from the bot owner.
	if msg.From.ID == h.candidateTelegramID && strings.HasPrefix(msg.Text, "/") {
		h.handleAdminCommand(ctx, msg)
		return
	}

	// Admin reply to a bot-forwarded escalation message.
	if msg.From.ID == h.candidateTelegramID && msg.ReplyToMessage != nil {
		h.handleAdminReply(ctx, msg)
		return
	}

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
		// Check for a learned answer before escalating.
		if answer := h.tryLearnedAnswer(ctx, msg); answer != "" {
			if _, sendErr := h.telegram.SendMessage(ctx, msg.Chat.ID, answer); sendErr != nil {
				log.Error("SendMessage (learned answer) failed", "err", sendErr)
			}
			return
		}
		h.escalate(ctx, msg, "uncertain or sensitive topic")
		return
	}

	reply, err := h.buildReply(ctx, user, msg, intent)
	if err != nil {
		log.Error("buildReply failed", "err", err)
		h.escalate(ctx, msg, "reply generation error")
		return
	}

	if _, sendErr := h.telegram.SendMessage(ctx, msg.Chat.ID, reply); sendErr != nil {
		log.Error("SendMessage failed", "err", sendErr)
	}
}

// handleAdminCommand processes commands sent by the bot owner.
func (h *Handler) handleAdminCommand(ctx context.Context, msg *domain.TelegramMessage) {
	parts := strings.Fields(msg.Text)
	if len(parts) == 0 {
		return
	}
	switch parts[0] {
	case "/addslot":
		h.handleAddSlot(ctx, msg, parts[1:])
	case "/deleteslot":
		h.handleDeleteSlot(ctx, msg, parts[1:])
	case "/listslots":
		h.handleListSlots(ctx, msg)
	default:
		reply := "Unknown admin command. Use /addslot, /deleteslot <id>, or /listslots."
		_, _ = h.telegram.SendMessage(ctx, msg.Chat.ID, reply)
	}
}

// handleAdminReply processes a reply from the admin to a forwarded escalation message.
// It forwards the reply to the original recruiter, marks the escalation resolved, and
// stores the Q&A pair as a learned answer.
func (h *Handler) handleAdminReply(ctx context.Context, msg *domain.TelegramMessage) {
	adminMsgID := msg.ReplyToMessage.MessageID
	esc, err := h.db.FindEscalationByAdminMsgID(ctx, adminMsgID)
	if err != nil {
		slog.Error("FindEscalationByAdminMsgID failed", "err", err)
		return
	}
	if esc == nil {
		slog.Info("admin reply does not match any pending escalation", "admin_msg_id", adminMsgID)
		if _, err := h.telegram.SendMessage(ctx, h.candidateTelegramID,
			"ℹ️ This reply doesn't correspond to any pending escalation."); err != nil {
			slog.Error("admin notify failed", "err", err)
		}
		return
	}

	if _, err := h.telegram.SendMessage(ctx, esc.RecruiterChatID, msg.Text); err != nil {
		slog.Error("forward admin reply to recruiter failed", "err", err)
		return
	}

	if err := h.db.ResolveEscalation(ctx, esc.ID); err != nil {
		slog.Error("ResolveEscalation failed", "err", err)
	}

	var embedding []float32
	if h.embedder != nil {
		if emb, embErr := h.embedder.Embed(ctx, esc.QuestionText); embErr == nil {
			embedding = emb
		} else {
			slog.Warn("embed question failed", "err", embErr)
		}
	}
	if err := h.db.StoreLearnedAnswer(ctx, esc.QuestionText, msg.Text, embedding); err != nil {
		slog.Error("StoreLearnedAnswer failed", "err", err)
	}

	if _, err := h.telegram.SendMessage(ctx, h.candidateTelegramID, "✅ Reply forwarded and answer stored."); err != nil {
		slog.Error("admin ack failed", "err", err)
	}
}

// tryLearnedAnswer returns a stored answer if a similar question exists above the
// similarity threshold, or an empty string if no match is found.
func (h *Handler) tryLearnedAnswer(ctx context.Context, msg *domain.TelegramMessage) string {
	if h.embedder == nil {
		return ""
	}
	emb, err := h.embedder.Embed(ctx, msg.Text)
	if err != nil {
		slog.Warn("embed for similarity check failed", "err", err)
		return ""
	}
	answer, err := h.db.FindSimilarAnswer(ctx, emb, h.similarityThreshold)
	if err != nil {
		slog.Warn("FindSimilarAnswer failed", "err", err)
		return ""
	}
	if answer == nil {
		return ""
	}
	return answer.AnswerText
}

// handleAddSlot adds an availability slot.
// Usage: /addslot 2006-01-02 15:04 2006-01-02 15:04 (UTC)
func (h *Handler) handleAddSlot(ctx context.Context, msg *domain.TelegramMessage, args []string) {
	const layout = "2006-01-02 15:04"
	if len(args) != 4 {
		_, _ = h.telegram.SendMessage(ctx, msg.Chat.ID,
			"Usage: /addslot YYYY-MM-DD HH:MM YYYY-MM-DD HH:MM (UTC)")
		return
	}
	startStr := args[0] + " " + args[1]
	endStr := args[2] + " " + args[3]
	start, err := time.Parse(layout, startStr)
	if err != nil {
		_, _ = h.telegram.SendMessage(ctx, msg.Chat.ID,
			fmt.Sprintf("Invalid start time %q: %v", startStr, err))
		return
	}
	end, err := time.Parse(layout, endStr)
	if err != nil {
		_, _ = h.telegram.SendMessage(ctx, msg.Chat.ID,
			fmt.Sprintf("Invalid end time %q: %v", endStr, err))
		return
	}
	if !end.After(start) {
		_, _ = h.telegram.SendMessage(ctx, msg.Chat.ID, "End time must be after start time.")
		return
	}
	slot, err := h.db.AddAvailabilitySlot(ctx, start.UTC(), end.UTC())
	if err != nil {
		slog.Error("AddAvailabilitySlot failed", "err", err)
		_, _ = h.telegram.SendMessage(ctx, msg.Chat.ID, "Failed to add slot: "+err.Error())
		return
	}
	_, _ = h.telegram.SendMessage(ctx, msg.Chat.ID,
		fmt.Sprintf("✅ Slot #%d added: %s – %s UTC",
			slot.ID,
			slot.StartTime.UTC().Format("Mon Jan 2, 2006 15:04"),
			slot.EndTime.UTC().Format("15:04"),
		))
}

// handleDeleteSlot removes an availability slot by ID.
// Usage: /deleteslot <id>
func (h *Handler) handleDeleteSlot(ctx context.Context, msg *domain.TelegramMessage, args []string) {
	if len(args) != 1 {
		_, _ = h.telegram.SendMessage(ctx, msg.Chat.ID, "Usage: /deleteslot <id>")
		return
	}
	var slotID int
	if _, err := fmt.Sscanf(args[0], "%d", &slotID); err != nil {
		_, _ = h.telegram.SendMessage(ctx, msg.Chat.ID, "Invalid slot ID: "+args[0])
		return
	}
	if err := h.db.DeleteAvailabilitySlot(ctx, slotID); err != nil {
		slog.Error("DeleteAvailabilitySlot failed", "err", err)
		_, _ = h.telegram.SendMessage(ctx, msg.Chat.ID, "Failed to delete slot: "+err.Error())
		return
	}
	_, _ = h.telegram.SendMessage(ctx, msg.Chat.ID, fmt.Sprintf("✅ Slot #%d deleted.", slotID))
}

// handleListSlots lists all available (unbooked) slots for the admin.
func (h *Handler) handleListSlots(ctx context.Context, msg *domain.TelegramMessage) {
	slots, err := h.db.FindAvailableSlots(ctx)
	if err != nil {
		slog.Error("FindAvailableSlots failed", "err", err)
		_, _ = h.telegram.SendMessage(ctx, msg.Chat.ID, "Failed to list slots: "+err.Error())
		return
	}
	if len(slots) == 0 {
		_, _ = h.telegram.SendMessage(ctx, msg.Chat.ID, "No available slots.")
		return
	}
	_, _ = h.telegram.SendMessage(ctx, msg.Chat.ID, formatSlots(slots))
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

// escalate forwards the recruiter's message to the candidate, stores an escalation
// record, and informs the recruiter.
func (h *Handler) escalate(ctx context.Context, msg *domain.TelegramMessage, reason string) {
	slog.Info("escalating", "reason", reason, "chat_id", msg.Chat.ID)

	fwd := fmt.Sprintf("📨 Recruiter message (from chat %d):\n%s", msg.Chat.ID, msg.Text)
	adminMsgID, err := h.telegram.SendMessage(ctx, h.candidateTelegramID, fwd)
	if err != nil {
		slog.Error("forward to candidate failed", "err", err)
	}

	if adminMsgID > 0 {
		if _, dbErr := h.db.StoreEscalation(ctx, msg.Chat.ID, msg.Text, adminMsgID, reason); dbErr != nil {
			slog.Error("StoreEscalation failed", "err", dbErr)
		}
	}

	reply := "I've forwarded this directly. He will reply shortly."
	if _, err := h.telegram.SendMessage(ctx, msg.Chat.ID, reply); err != nil {
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

// MakeTimeWindow helper for tests.
func MakeTimeWindow(start, end time.Time) domain.TimeWindow {
	return domain.TimeWindow{Start: &start, End: &end}
}
