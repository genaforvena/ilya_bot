package infrastructure

import (
	"context"
	"fmt"
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
	return err
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
