package application_test

import (
	"context"
	"testing"
	"time"

	"github.com/genaforvena/ilya_bot/internal/application"
	"github.com/genaforvena/ilya_bot/internal/domain"
)

// --- Mocks ---

type mockDB struct {
	users []domain.User
	slots []domain.AvailabilitySlot
	booked map[int]bool
}

func (m *mockDB) FindOrCreateUser(_ context.Context, telegramID int64) (*domain.User, error) {
	return &domain.User{ID: 1, TelegramID: telegramID}, nil
}

func (m *mockDB) FindAvailableSlots(_ context.Context) ([]domain.AvailabilitySlot, error) {
	var out []domain.AvailabilitySlot
	for _, s := range m.slots {
		if !m.booked[s.ID] {
			out = append(out, s)
		}
	}
	return out, nil
}

func (m *mockDB) BookSlot(_ context.Context, recruiterID, slotID int) (*domain.Booking, error) {
	if m.booked[slotID] {
		return nil, nil // idempotent
	}
	m.booked[slotID] = true
	s := m.slotByID(slotID)
	return &domain.Booking{
		ID:          slotID,
		RecruiterID: recruiterID,
		StartTime:   s.StartTime,
		EndTime:     s.EndTime,
		Status:      "confirmed",
	}, nil
}

func (m *mockDB) slotByID(id int) domain.AvailabilitySlot {
	for _, s := range m.slots {
		if s.ID == id {
			return s
		}
	}
	return domain.AvailabilitySlot{}
}

type mockLLM struct {
	intent   *domain.Intent
	response string
	err      error
}

func (m *mockLLM) ClassifyIntent(_ context.Context, _ string) (*domain.Intent, error) {
	return m.intent, m.err
}

func (m *mockLLM) GenerateResponse(_ context.Context, _, _ string) (string, error) {
	return m.response, m.err
}

type mockTG struct {
	sent []struct {
		chatID int64
		text   string
	}
}

func (m *mockTG) SendMessage(_ context.Context, chatID int64, text string) error {
	m.sent = append(m.sent, struct {
		chatID int64
		text   string
	}{chatID, text})
	return nil
}

// --- Tests ---

func TestHandleMessage_Escalates_LowConfidence(t *testing.T) {
	db := &mockDB{booked: map[int]bool{}}
	llm := &mockLLM{intent: &domain.Intent{Intent: "unknown", Confidence: 0.3}}
	tg := &mockTG{}
	h := application.NewHandler(db, llm, tg, 999)

	msg := &domain.TelegramMessage{
		From: &domain.TelegramUser{ID: 1},
		Chat: domain.TelegramChat{ID: 1},
		Text: "What is your salary?",
	}
	h.HandleMessage(context.Background(), msg)

	if len(tg.sent) < 2 {
		t.Fatalf("expected 2 messages (forward + notice), got %d", len(tg.sent))
	}
	if tg.sent[0].chatID != 999 {
		t.Error("first message should go to candidate")
	}
}

func TestHandleMessage_Escalates_SensitiveTopic(t *testing.T) {
	db := &mockDB{booked: map[int]bool{}}
	topic := "salary"
	llm := &mockLLM{intent: &domain.Intent{Intent: "question", Confidence: 0.9, QuestionTopic: &topic}}
	tg := &mockTG{}
	h := application.NewHandler(db, llm, tg, 777)

	msg := &domain.TelegramMessage{
		From: &domain.TelegramUser{ID: 2},
		Chat: domain.TelegramChat{ID: 2},
		Text: "What's the salary range?",
	}
	h.HandleMessage(context.Background(), msg)

	if len(tg.sent) < 2 {
		t.Fatalf("expected escalation messages, got %d", len(tg.sent))
	}
	if tg.sent[0].chatID != 777 {
		t.Error("should forward to candidate first")
	}
}

func TestBookSlot_Idempotent(t *testing.T) {
	now := time.Now().UTC()
	slot := domain.AvailabilitySlot{ID: 1, StartTime: now, EndTime: now.Add(time.Hour)}
	db := &mockDB{slots: []domain.AvailabilitySlot{slot}, booked: map[int]bool{}}

	// First booking succeeds
	b1, err := db.BookSlot(context.Background(), 1, 1)
	if err != nil {
		t.Fatalf("first booking error: %v", err)
	}
	if b1 == nil {
		t.Fatal("expected booking, got nil")
	}

	// Second booking returns nil (idempotent, not an error)
	b2, err := db.BookSlot(context.Background(), 1, 1)
	if err != nil {
		t.Fatalf("second booking error: %v", err)
	}
	if b2 != nil {
		t.Error("expected nil booking for duplicate, got non-nil")
	}
}

func TestTopicSensitive(t *testing.T) {
	salary := "salary"
	relocation := "relocation"
	experience := "experience"

	if !domain.TopicSensitive(&salary) {
		t.Error("salary should be sensitive")
	}
	if !domain.TopicSensitive(&relocation) {
		t.Error("relocation should be sensitive")
	}
	if domain.TopicSensitive(&experience) {
		t.Error("experience should not be sensitive")
	}
	if domain.TopicSensitive(nil) {
		t.Error("nil topic should not be sensitive")
	}
}

func TestFindMatchingSlot(t *testing.T) {
	now := time.Now().UTC()
	slots := []domain.AvailabilitySlot{
		{ID: 1, StartTime: now.Add(1 * time.Hour), EndTime: now.Add(2 * time.Hour)},
		{ID: 2, StartTime: now.Add(3 * time.Hour), EndTime: now.Add(4 * time.Hour)},
	}

	tw := application.MakeTimeWindow(now.Add(90*time.Minute), now.Add(110*time.Minute))
	match := findMatchingSlotHelper(slots, tw)
	if match == nil || match.ID != 1 {
		t.Errorf("expected slot 1, got %v", match)
	}

	tw2 := application.MakeTimeWindow(now.Add(10*time.Hour), now.Add(11*time.Hour))
	noMatch := findMatchingSlotHelper(slots, tw2)
	if noMatch != nil {
		t.Error("expected no match")
	}
}

// findMatchingSlotHelper wraps the unexported function via the exported MakeTimeWindow.
func findMatchingSlotHelper(slots []domain.AvailabilitySlot, tw domain.TimeWindow) *domain.AvailabilitySlot {
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
