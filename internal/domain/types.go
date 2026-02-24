package domain

import "time"

// TelegramUpdate represents an incoming Telegram update.
type TelegramUpdate struct {
	UpdateID int             `json:"update_id"`
	Message  *TelegramMessage `json:"message"`
}

// TelegramReplyToMessage holds minimal info about the replied-to message.
type TelegramReplyToMessage struct {
	MessageID int `json:"message_id"`
}

// TelegramMessage represents a Telegram message.
type TelegramMessage struct {
	MessageID      int                     `json:"message_id"`
	From           *TelegramUser           `json:"from"`
	Chat           TelegramChat            `json:"chat"`
	Text           string                  `json:"text"`
	Date           int64                   `json:"date"`
	ReplyToMessage *TelegramReplyToMessage `json:"reply_to_message"`
}

// TelegramUser represents a Telegram user.
type TelegramUser struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
}

// TelegramChat represents a Telegram chat.
type TelegramChat struct {
	ID int64 `json:"id"`
}

// Intent represents the classified intent from LLM.
type Intent struct {
	Intent            string          `json:"intent"`
	Confidence        float64         `json:"confidence"`
	ProposedTimeWindow TimeWindow      `json:"proposed_time_window"`
	QuestionTopic     *string         `json:"question_topic"`
}

// TimeWindow represents a start/end time pair, both optional.
type TimeWindow struct {
	Start *time.Time `json:"start"`
	End   *time.Time `json:"end"`
}

// User represents a recruiter stored in the database.
type User struct {
	ID         int
	TelegramID int64
	Company    string
	Role       string
	CreatedAt  time.Time
}

// AvailabilitySlot represents a time block the candidate is available.
type AvailabilitySlot struct {
	ID        int
	StartTime time.Time
	EndTime   time.Time
}

// Booking represents a confirmed interview booking.
type Booking struct {
	ID          int
	RecruiterID int
	StartTime   time.Time
	EndTime     time.Time
	Status      string
	CreatedAt   time.Time
}

// IntentKind enumerates known intent values.
const (
	IntentSchedule  = "schedule"
	IntentQuestion  = "question"
	IntentSmalltalk = "smalltalk"
	IntentUnknown   = "unknown"
)

// Escalation represents a pending recruiter-question escalation forwarded to admin.
type Escalation struct {
	ID             int
	RecruiterChatID int64
	QuestionText   string
	AdminMsgID     int
	Reason         string
	Status         string
	CreatedAt      time.Time
	ResolvedAt     *time.Time
}

// LearnedAnswer is an admin-approved Q&A pair stored for future auto-answering.
type LearnedAnswer struct {
	ID           int
	QuestionText string
	AnswerText   string
	CreatedAt    time.Time
}

// TopicSensitive returns true if the topic requires escalation.
func TopicSensitive(topic *string) bool {
	if topic == nil {
		return false
	}
	return *topic == "salary" || *topic == "relocation"
}
