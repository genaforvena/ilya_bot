package infrastructure

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/genaforvena/ilya_bot/internal/domain"
)

// DB wraps a pgxpool.Pool for database operations.
type DB struct {
	pool *pgxpool.Pool
}

// NewDB creates a new DB from a connection string and runs schema migration.
func NewDB(ctx context.Context, dsn string) (*DB, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("pgxpool.New: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("db ping: %w", err)
	}
	db := &DB{pool: pool}
	if err := db.migrate(ctx); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return db, nil
}

// Close closes the underlying pool.
func (db *DB) Close() {
	db.pool.Close()
}

func (db *DB) migrate(ctx context.Context) error {
	_, err := db.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS users (
			id serial primary key,
			telegram_id bigint unique not null,
			company text not null default '',
			role text not null default '',
			created_at timestamp not null default now()
		);
		CREATE TABLE IF NOT EXISTS availability (
			id serial primary key,
			start_time timestamp not null,
			end_time timestamp not null
		);
		CREATE TABLE IF NOT EXISTS bookings (
			id serial primary key,
			recruiter_id int references users(id),
			start_time timestamp not null,
			end_time timestamp not null,
			status text not null default 'confirmed',
			created_at timestamp not null default now()
		);
	`)
	if err != nil {
		return err
	}

	// Escalation tracking (no vector dependency).
	_, err = db.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS escalations (
			id serial primary key,
			recruiter_chat_id bigint not null,
			question_text text not null,
			admin_msg_id int not null,
			reason text not null default '',
			status text not null default 'pending',
			created_at timestamp not null default now(),
			resolved_at timestamp
		);
		CREATE INDEX IF NOT EXISTS escalations_admin_msg_id_idx ON escalations(admin_msg_id);
	`)
	if err != nil {
		return fmt.Errorf("migrate escalations: %w", err)
	}

	// pgvector extension + learned_answers (optional; bot still works without it).
	if _, extErr := db.pool.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS vector`); extErr != nil {
		slog.Warn("pgvector extension not available, similarity search disabled"+
			" – install pgvector on PostgreSQL to enable it (https://github.com/pgvector/pgvector#installation)",
			"err", extErr)
		return nil
	}
	if _, err = db.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS learned_answers (
			id serial primary key,
			question_text text not null,
			answer_text text not null,
			embedding vector(1536),
			created_at timestamp not null default now()
		);
		CREATE INDEX IF NOT EXISTS learned_answers_embedding_idx
			ON learned_answers USING hnsw (embedding vector_cosine_ops);
	`); err != nil {
		slog.Warn("failed to create learned_answers table", "err", err)
	}
	return nil
}

// FindOrCreateUser upserts a user by telegram_id.
func (db *DB) FindOrCreateUser(ctx context.Context, telegramID int64) (*domain.User, error) {
	row := db.pool.QueryRow(ctx, `
		INSERT INTO users (telegram_id) VALUES ($1)
		ON CONFLICT (telegram_id) DO UPDATE SET telegram_id = EXCLUDED.telegram_id
		RETURNING id, telegram_id, company, role, created_at
	`, telegramID)

	u := &domain.User{}
	if err := row.Scan(&u.ID, &u.TelegramID, &u.Company, &u.Role, &u.CreatedAt); err != nil {
		return nil, fmt.Errorf("FindOrCreateUser scan: %w", err)
	}
	return u, nil
}

// FindAvailableSlots returns availability blocks not already booked.
func (db *DB) FindAvailableSlots(ctx context.Context) ([]domain.AvailabilitySlot, error) {
	rows, err := db.pool.Query(ctx, `
		SELECT a.id, a.start_time, a.end_time
		FROM availability a
		WHERE NOT EXISTS (
			SELECT 1 FROM bookings b
			WHERE b.status = 'confirmed'
			  AND b.start_time < a.end_time
			  AND b.end_time > a.start_time
		)
		ORDER BY a.start_time
	`)
	if err != nil {
		return nil, fmt.Errorf("FindAvailableSlots query: %w", err)
	}
	defer rows.Close()

	var slots []domain.AvailabilitySlot
	for rows.Next() {
		var s domain.AvailabilitySlot
		if err := rows.Scan(&s.ID, &s.StartTime, &s.EndTime); err != nil {
			return nil, fmt.Errorf("FindAvailableSlots scan: %w", err)
		}
		slots = append(slots, s)
	}
	return slots, rows.Err()
}

// AddAvailabilitySlot inserts a new availability slot.
func (db *DB) AddAvailabilitySlot(ctx context.Context, start, end time.Time) (*domain.AvailabilitySlot, error) {
	s := &domain.AvailabilitySlot{}
	err := db.pool.QueryRow(ctx, `
		INSERT INTO availability (start_time, end_time)
		VALUES ($1, $2)
		RETURNING id, start_time, end_time
	`, start, end).Scan(&s.ID, &s.StartTime, &s.EndTime)
	if err != nil {
		return nil, fmt.Errorf("AddAvailabilitySlot: %w", err)
	}
	return s, nil
}

// DeleteAvailabilitySlot removes an availability slot by ID.
func (db *DB) DeleteAvailabilitySlot(ctx context.Context, slotID int) error {
	tag, err := db.pool.Exec(ctx, `DELETE FROM availability WHERE id = $1`, slotID)
	if err != nil {
		return fmt.Errorf("DeleteAvailabilitySlot: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("slot %d not found", slotID)
	}
	return nil
}
// Returns the booking, or (nil, nil) if the slot is already taken (idempotent).
func (db *DB) BookSlot(ctx context.Context, recruiterID, slotID int) (*domain.Booking, error) {
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Lock the availability slot
	var start, end time.Time
	err = tx.QueryRow(ctx, `
		SELECT start_time, end_time FROM availability WHERE id = $1 FOR UPDATE
	`, slotID).Scan(&start, &end)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("slot %d not found", slotID)
		}
		return nil, fmt.Errorf("lock slot: %w", err)
	}

	// Check for conflicting booking
	var conflictCount int
	err = tx.QueryRow(ctx, `
		SELECT COUNT(*) FROM bookings
		WHERE status = 'confirmed'
		  AND start_time < $1
		  AND end_time > $2
	`, end, start).Scan(&conflictCount)
	if err != nil {
		return nil, fmt.Errorf("conflict check: %w", err)
	}
	if conflictCount > 0 {
		// Slot already booked — idempotent, not an error
		return nil, nil
	}

	b := &domain.Booking{}
	err = tx.QueryRow(ctx, `
		INSERT INTO bookings (recruiter_id, start_time, end_time, status)
		VALUES ($1, $2, $3, 'confirmed')
		RETURNING id, recruiter_id, start_time, end_time, status, created_at
	`, recruiterID, start, end).Scan(
		&b.ID, &b.RecruiterID, &b.StartTime, &b.EndTime, &b.Status, &b.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("insert booking: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit booking: %w", err)
	}
	return b, nil
}

// StoreEscalation inserts a new escalation record and returns it.
func (db *DB) StoreEscalation(ctx context.Context, recruiterChatID int64, questionText string, adminMsgID int, reason string) (*domain.Escalation, error) {
	e := &domain.Escalation{}
	err := db.pool.QueryRow(ctx, `
		INSERT INTO escalations (recruiter_chat_id, question_text, admin_msg_id, reason)
		VALUES ($1, $2, $3, $4)
		RETURNING id, recruiter_chat_id, question_text, admin_msg_id, reason, status, created_at
	`, recruiterChatID, questionText, adminMsgID, reason).Scan(
		&e.ID, &e.RecruiterChatID, &e.QuestionText, &e.AdminMsgID, &e.Reason, &e.Status, &e.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("StoreEscalation: %w", err)
	}
	return e, nil
}

// FindEscalationByAdminMsgID returns the pending escalation whose forwarded message
// has the given Telegram message ID, or (nil, nil) if not found.
func (db *DB) FindEscalationByAdminMsgID(ctx context.Context, adminMsgID int) (*domain.Escalation, error) {
	e := &domain.Escalation{}
	err := db.pool.QueryRow(ctx, `
		SELECT id, recruiter_chat_id, question_text, admin_msg_id, reason, status, created_at
		FROM escalations
		WHERE admin_msg_id = $1 AND status = 'pending'
		LIMIT 1
	`, adminMsgID).Scan(
		&e.ID, &e.RecruiterChatID, &e.QuestionText, &e.AdminMsgID, &e.Reason, &e.Status, &e.CreatedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("FindEscalationByAdminMsgID: %w", err)
	}
	return e, nil
}

// ResolveEscalation marks an escalation as resolved.
func (db *DB) ResolveEscalation(ctx context.Context, id int) error {
	_, err := db.pool.Exec(ctx, `
		UPDATE escalations SET status = 'resolved', resolved_at = now() WHERE id = $1
	`, id)
	return err
}

// StoreLearnedAnswer saves an admin-approved Q&A pair with an optional embedding.
func (db *DB) StoreLearnedAnswer(ctx context.Context, questionText, answerText string, embedding []float32) error {
	var err error
	if len(embedding) > 0 {
		_, err = db.pool.Exec(ctx, `
			INSERT INTO learned_answers (question_text, answer_text, embedding)
			VALUES ($1, $2, $3::vector)
		`, questionText, answerText, floatsToVector(embedding))
	} else {
		_, err = db.pool.Exec(ctx, `
			INSERT INTO learned_answers (question_text, answer_text)
			VALUES ($1, $2)
		`, questionText, answerText)
	}
	if err != nil {
		return fmt.Errorf("StoreLearnedAnswer: %w", err)
	}
	return nil
}

// FindSimilarAnswer returns the most similar learned answer whose cosine similarity
// to the given embedding is at or above threshold, or (nil, nil) if none qualifies.
func (db *DB) FindSimilarAnswer(ctx context.Context, embedding []float32, threshold float64) (*domain.LearnedAnswer, error) {
	if len(embedding) == 0 {
		return nil, nil
	}
	a := &domain.LearnedAnswer{}
	var similarity float64
	err := db.pool.QueryRow(ctx, `
		SELECT id, question_text, answer_text, created_at,
		       1 - (embedding <=> $1::vector) AS similarity
		FROM learned_answers
		WHERE embedding IS NOT NULL
		ORDER BY embedding <=> $1::vector
		LIMIT 1
	`, floatsToVector(embedding)).Scan(
		&a.ID, &a.QuestionText, &a.AnswerText, &a.CreatedAt, &similarity,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("FindSimilarAnswer: %w", err)
	}
	if similarity < threshold {
		return nil, nil
	}
	return a, nil
}

// floatsToVector serialises a float32 slice to pgvector text format, e.g. "[1.0,2.0,3.0]".
func floatsToVector(v []float32) string {
	sb := strings.Builder{}
	sb.WriteByte('[')
	for i, f := range v {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(strconv.FormatFloat(float64(f), 'f', -1, 32))
	}
	sb.WriteByte(']')
	return sb.String()
}
