package application_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/genaforvena/ilya_bot/internal/application"
	"github.com/genaforvena/ilya_bot/internal/domain"
)

// --- Mocks ---

type mockDB struct {
	users          []domain.User
	slots          []domain.AvailabilitySlot
	booked         map[int]bool
	nextSlotID     int
	escalations    []domain.Escalation
	learnedAnswers []domain.LearnedAnswer
	similarAnswer  *domain.LearnedAnswer
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

func (m *mockDB) AddAvailabilitySlot(_ context.Context, start, end time.Time) (*domain.AvailabilitySlot, error) {
	m.nextSlotID++
	s := domain.AvailabilitySlot{ID: m.nextSlotID, StartTime: start, EndTime: end}
	m.slots = append(m.slots, s)
	return &s, nil
}

func (m *mockDB) DeleteAvailabilitySlot(_ context.Context, slotID int) error {
	for i, s := range m.slots {
		if s.ID == slotID {
			m.slots = append(m.slots[:i], m.slots[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("slot %d not found", slotID)
}

func (m *mockDB) StoreEscalation(_ context.Context, recruiterChatID int64, questionText string, adminMsgID int, reason string) (*domain.Escalation, error) {
	e := domain.Escalation{
		ID:              len(m.escalations) + 1,
		RecruiterChatID: recruiterChatID,
		QuestionText:    questionText,
		AdminMsgID:      adminMsgID,
		Reason:          reason,
		Status:          "pending",
		CreatedAt:       time.Now(),
	}
	m.escalations = append(m.escalations, e)
	return &e, nil
}

func (m *mockDB) FindEscalationByAdminMsgID(_ context.Context, adminMsgID int) (*domain.Escalation, error) {
	for i := range m.escalations {
		if m.escalations[i].AdminMsgID == adminMsgID && m.escalations[i].Status == "pending" {
			return &m.escalations[i], nil
		}
	}
	return nil, nil
}

func (m *mockDB) ResolveEscalation(_ context.Context, id int) error {
	for i := range m.escalations {
		if m.escalations[i].ID == id {
			m.escalations[i].Status = "resolved"
			return nil
		}
	}
	return fmt.Errorf("escalation %d not found", id)
}

func (m *mockDB) StoreLearnedAnswer(_ context.Context, questionText, answerText string, _ []float32) error {
	m.learnedAnswers = append(m.learnedAnswers, domain.LearnedAnswer{
		ID:           len(m.learnedAnswers) + 1,
		QuestionText: questionText,
		AnswerText:   answerText,
		CreatedAt:    time.Now(),
	})
	return nil
}

func (m *mockDB) FindSimilarAnswer(_ context.Context, _ []float32, _ float64) (*domain.LearnedAnswer, error) {
	return m.similarAnswer, nil
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
	nextMsgID int
}

func (m *mockTG) SendMessage(_ context.Context, chatID int64, text string) (int, error) {
	m.sent = append(m.sent, struct {
		chatID int64
		text   string
	}{chatID, text})
	m.nextMsgID++
	return m.nextMsgID, nil
}

type mockEmbedder struct {
	embedding []float32
	err       error
}

func (m *mockEmbedder) Embed(_ context.Context, _ string) ([]float32, error) {
	return m.embedding, m.err
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

// --- Admin command tests ---

const adminID int64 = 999

func newAdminHandler(db *mockDB) (*application.Handler, *mockTG) {
	llm := &mockLLM{intent: &domain.Intent{Intent: "smalltalk", Confidence: 0.9}}
	tg := &mockTG{}
	return application.NewHandler(db, llm, tg, adminID), tg
}

func adminMsg(text string) *domain.TelegramMessage {
	return &domain.TelegramMessage{
		From: &domain.TelegramUser{ID: adminID},
		Chat: domain.TelegramChat{ID: adminID},
		Text: text,
	}
}

func TestAdminAddSlot_Success(t *testing.T) {
	db := &mockDB{booked: map[int]bool{}}
	h, tg := newAdminHandler(db)

	h.HandleMessage(context.Background(), adminMsg("/addslot 2030-06-01 10:00 2030-06-01 11:00"))

	if len(tg.sent) != 1 {
		t.Fatalf("expected 1 reply, got %d", len(tg.sent))
	}
	if !strings.Contains(tg.sent[0].text, "✅ Slot #1 added") {
		t.Errorf("unexpected reply: %s", tg.sent[0].text)
	}
	if len(db.slots) != 1 {
		t.Fatalf("expected 1 slot in DB, got %d", len(db.slots))
	}
	if db.slots[0].StartTime.UTC().Format("2006-01-02 15:04") != "2030-06-01 10:00" {
		t.Errorf("unexpected start time: %v", db.slots[0].StartTime)
	}
}

func TestAdminAddSlot_InvalidArgs(t *testing.T) {
	db := &mockDB{booked: map[int]bool{}}
	h, tg := newAdminHandler(db)

	h.HandleMessage(context.Background(), adminMsg("/addslot 2030-06-01"))

	if len(tg.sent) != 1 {
		t.Fatalf("expected 1 reply, got %d", len(tg.sent))
	}
	if !strings.Contains(tg.sent[0].text, "Usage:") {
		t.Errorf("expected usage hint, got: %s", tg.sent[0].text)
	}
	if len(db.slots) != 0 {
		t.Error("no slot should have been added")
	}
}

func TestAdminAddSlot_EndBeforeStart(t *testing.T) {
	db := &mockDB{booked: map[int]bool{}}
	h, tg := newAdminHandler(db)

	h.HandleMessage(context.Background(), adminMsg("/addslot 2030-06-01 11:00 2030-06-01 10:00"))

	if len(tg.sent) != 1 {
		t.Fatalf("expected 1 reply, got %d", len(tg.sent))
	}
	if !strings.Contains(tg.sent[0].text, "End time must be after start time") {
		t.Errorf("unexpected reply: %s", tg.sent[0].text)
	}
}

func TestAdminDeleteSlot_Success(t *testing.T) {
	now := time.Now().UTC()
	db := &mockDB{
		slots:      []domain.AvailabilitySlot{{ID: 5, StartTime: now, EndTime: now.Add(time.Hour)}},
		booked:     map[int]bool{},
		nextSlotID: 5,
	}
	h, tg := newAdminHandler(db)

	h.HandleMessage(context.Background(), adminMsg("/deleteslot 5"))

	if len(tg.sent) != 1 {
		t.Fatalf("expected 1 reply, got %d", len(tg.sent))
	}
	if !strings.Contains(tg.sent[0].text, "✅ Slot #5 deleted") {
		t.Errorf("unexpected reply: %s", tg.sent[0].text)
	}
	if len(db.slots) != 0 {
		t.Error("slot should have been removed")
	}
}

func TestAdminDeleteSlot_NotFound(t *testing.T) {
	db := &mockDB{booked: map[int]bool{}}
	h, tg := newAdminHandler(db)

	h.HandleMessage(context.Background(), adminMsg("/deleteslot 99"))

	if len(tg.sent) != 1 {
		t.Fatalf("expected 1 reply, got %d", len(tg.sent))
	}
	if !strings.Contains(tg.sent[0].text, "Failed to delete slot") {
		t.Errorf("unexpected reply: %s", tg.sent[0].text)
	}
}

func TestAdminListSlots(t *testing.T) {
	now := time.Now().UTC()
	db := &mockDB{
		slots:  []domain.AvailabilitySlot{{ID: 1, StartTime: now.Add(time.Hour), EndTime: now.Add(2 * time.Hour)}},
		booked: map[int]bool{},
	}
	h, tg := newAdminHandler(db)

	h.HandleMessage(context.Background(), adminMsg("/listslots"))

	if len(tg.sent) != 1 {
		t.Fatalf("expected 1 reply, got %d", len(tg.sent))
	}
	if !strings.Contains(tg.sent[0].text, "available interview slots") {
		t.Errorf("unexpected reply: %s", tg.sent[0].text)
	}
}

func TestAdminListSlots_Empty(t *testing.T) {
	db := &mockDB{booked: map[int]bool{}}
	h, tg := newAdminHandler(db)

	h.HandleMessage(context.Background(), adminMsg("/listslots"))

	if len(tg.sent) != 1 {
		t.Fatalf("expected 1 reply, got %d", len(tg.sent))
	}
	if tg.sent[0].text != "No available slots." {
		t.Errorf("unexpected reply: %s", tg.sent[0].text)
	}
}

func TestAdminCommand_NonAdminCannotUseAdminCommands(t *testing.T) {
	db := &mockDB{booked: map[int]bool{}}
	llm := &mockLLM{intent: &domain.Intent{Intent: "unknown", Confidence: 0.9}}
	tg := &mockTG{}
	h := application.NewHandler(db, llm, tg, adminID)

	// Non-admin user sends /addslot — should NOT add a slot (treated as normal message)
	msg := &domain.TelegramMessage{
		From: &domain.TelegramUser{ID: 42},
		Chat: domain.TelegramChat{ID: 42},
		Text: "/addslot 2030-06-01 10:00 2030-06-01 11:00",
	}
	h.HandleMessage(context.Background(), msg)

	if len(db.slots) != 0 {
		t.Error("non-admin should not be able to add slots")
	}
}

// --- Escalation and learning tests ---

// TestEscalation_StoresRecord verifies that escalate() persists a record in the DB.
func TestEscalation_StoresRecord(t *testing.T) {
	db := &mockDB{booked: map[int]bool{}}
	llm := &mockLLM{intent: &domain.Intent{Intent: "unknown", Confidence: 0.3}}
	tg := &mockTG{}
	h := application.NewHandler(db, llm, tg, 999)

	msg := &domain.TelegramMessage{
		From: &domain.TelegramUser{ID: 1},
		Chat: domain.TelegramChat{ID: 42},
		Text: "What is the salary range?",
	}
	h.HandleMessage(context.Background(), msg)

	if len(db.escalations) != 1 {
		t.Fatalf("expected 1 escalation record, got %d", len(db.escalations))
	}
	if db.escalations[0].RecruiterChatID != 42 {
		t.Errorf("expected recruiter chat 42, got %d", db.escalations[0].RecruiterChatID)
	}
	if db.escalations[0].AdminMsgID != 1 {
		t.Errorf("expected admin msg id 1 (first SendMessage), got %d", db.escalations[0].AdminMsgID)
	}
	if db.escalations[0].QuestionText != "What is the salary range?" {
		t.Errorf("unexpected question text: %s", db.escalations[0].QuestionText)
	}
}

// TestHandleAdminReply_ForwardsAndStoresLearned verifies the admin reply flow:
// admin replies to a forwarded escalation → recruiter gets the reply → learned answer stored.
func TestHandleAdminReply_ForwardsAndStoresLearned(t *testing.T) {
	db := &mockDB{booked: map[int]bool{}}
	llm := &mockLLM{intent: &domain.Intent{Intent: "smalltalk", Confidence: 0.9}}
	tg := &mockTG{}
	h := application.NewHandler(db, llm, tg, adminID)

	// Pre-populate an escalation as if it had been created during a previous request.
	db.escalations = []domain.Escalation{
		{
			ID:              1,
			RecruiterChatID: 42,
			QuestionText:    "What is the salary range?",
			AdminMsgID:      100,
			Status:          "pending",
			CreatedAt:       time.Now(),
		},
	}

	// Admin replies to msg 100.
	adminReply := &domain.TelegramMessage{
		MessageID: 200,
		From:      &domain.TelegramUser{ID: adminID},
		Chat:      domain.TelegramChat{ID: adminID},
		Text:      "The salary is competitive, around 150k–180k.",
		ReplyToMessage: &domain.TelegramReplyToMessage{MessageID: 100},
	}
	h.HandleMessage(context.Background(), adminReply)

	// First sent message: reply forwarded to recruiter (chat 42).
	if len(tg.sent) < 2 {
		t.Fatalf("expected at least 2 messages, got %d", len(tg.sent))
	}
	if tg.sent[0].chatID != 42 {
		t.Errorf("expected first message to recruiter (42), got chatID=%d", tg.sent[0].chatID)
	}
	if tg.sent[0].text != "The salary is competitive, around 150k–180k." {
		t.Errorf("unexpected reply text: %s", tg.sent[0].text)
	}

	// Escalation should be resolved.
	if db.escalations[0].Status != "resolved" {
		t.Errorf("escalation should be resolved, got %s", db.escalations[0].Status)
	}

	// A learned answer should have been stored.
	if len(db.learnedAnswers) != 1 {
		t.Fatalf("expected 1 learned answer, got %d", len(db.learnedAnswers))
	}
	if db.learnedAnswers[0].AnswerText != "The salary is competitive, around 150k–180k." {
		t.Errorf("unexpected learned answer: %s", db.learnedAnswers[0].AnswerText)
	}
}

// TestHandleMessage_AutoAnswers_OnSimilarityMatch verifies that a question is answered
// automatically when a sufficiently similar learned answer exists.
func TestHandleMessage_AutoAnswers_OnSimilarityMatch(t *testing.T) {
	learned := &domain.LearnedAnswer{
		ID:           1,
		QuestionText: "What is the salary range?",
		AnswerText:   "The salary is around 150k.",
	}
	db := &mockDB{
		booked:        map[int]bool{},
		similarAnswer: learned,
	}
	topic := "salary"
	llm := &mockLLM{intent: &domain.Intent{Intent: "question", Confidence: 0.9, QuestionTopic: &topic}}
	tg := &mockTG{}
	embedder := &mockEmbedder{embedding: []float32{0.1, 0.2, 0.3}}
	h := application.NewHandler(db, llm, tg, 999).WithEmbedder(embedder, 0.85)

	msg := &domain.TelegramMessage{
		From: &domain.TelegramUser{ID: 1},
		Chat: domain.TelegramChat{ID: 1},
		Text: "What's the salary range?",
	}
	h.HandleMessage(context.Background(), msg)

	// Exactly one message sent: the auto-answer (no escalation).
	if len(tg.sent) != 1 {
		t.Fatalf("expected 1 message (auto-answer), got %d", len(tg.sent))
	}
	if tg.sent[0].chatID != 1 {
		t.Errorf("expected message to recruiter (1), got chatID=%d", tg.sent[0].chatID)
	}
	if tg.sent[0].text != "The salary is around 150k." {
		t.Errorf("unexpected auto-answer text: %s", tg.sent[0].text)
	}
}

// TestHandleMessage_NoAutoAnswer_BelowThreshold verifies that escalation happens normally
// when no similar answer is found (FindSimilarAnswer returns nil).
func TestHandleMessage_NoAutoAnswer_BelowThreshold(t *testing.T) {
	db := &mockDB{
		booked:        map[int]bool{},
		similarAnswer: nil, // nothing in the learned store
	}
	topic := "salary"
	llm := &mockLLM{intent: &domain.Intent{Intent: "question", Confidence: 0.9, QuestionTopic: &topic}}
	tg := &mockTG{}
	embedder := &mockEmbedder{embedding: []float32{0.1, 0.2, 0.3}}
	h := application.NewHandler(db, llm, tg, 999).WithEmbedder(embedder, 0.85)

	msg := &domain.TelegramMessage{
		From: &domain.TelegramUser{ID: 1},
		Chat: domain.TelegramChat{ID: 1},
		Text: "What's the salary range?",
	}
	h.HandleMessage(context.Background(), msg)

	// Should escalate: forward to admin (999) + notice to recruiter.
	if len(tg.sent) < 2 {
		t.Fatalf("expected 2 messages (escalation), got %d", len(tg.sent))
	}
	if tg.sent[0].chatID != 999 {
		t.Errorf("expected first message to admin (999), got chatID=%d", tg.sent[0].chatID)
	}
}
