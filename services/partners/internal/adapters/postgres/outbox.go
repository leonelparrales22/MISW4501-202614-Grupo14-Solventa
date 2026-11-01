package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/outbox"
)

// insertOutboxEvent writes an encoded envelope to outbox_event, due at
// occurredAt (the clock of the service, so the relay that shares it finds it
// due at once). An event id already present is left as it is.
func insertOutboxEvent(ctx context.Context, q execer, eventID, eventType string, body []byte, occurredAt time.Time) error {
	if _, err := q.ExecContext(ctx, `INSERT INTO outbox_event (event_id, event_type, payload, occurred_at, next_attempt_at)
		VALUES ($1, $2, $3, $4, $4)
		ON CONFLICT (event_id) DO NOTHING`,
		eventID, eventType, body, occurredAt.UTC()); err != nil {
		return fmt.Errorf("write event %s to the outbox: %w", eventID, err)
	}
	return nil
}

// OutboxStore is the outbox.Store over the outbox_event table (HU-W3.1-01, same design as origination).
// Several tasks of partners run a relay each over the same table: Claim is
// a single statement that locks the due rows with FOR UPDATE SKIP LOCKED and
// sets their lease, so a row with a live lease is never handed to another
// relay; MarkPublished and Reschedule only touch a row whose lease is still
// the one of the caller's claim.
type OutboxStore struct {
	db *sql.DB
}

var _ outbox.Store = (*OutboxStore)(nil)

// NewOutboxStore builds the store over an open, migrated database.
func NewOutboxStore(db *sql.DB) *OutboxStore { return &OutboxStore{db: db} }

// Claim implements outbox.Store.
func (s *OutboxStore) Claim(ctx context.Context, now time.Time, lease time.Duration, limit int) ([]outbox.Message, error) {
	// PostgreSQL keeps microseconds: the lease is truncated so the value the
	// caller gets back is the stored one, the key of its claim.
	claimedUntil := now.Add(lease).UTC().Truncate(time.Microsecond)
	rows, err := s.db.QueryContext(ctx, `UPDATE outbox_event
		SET claimed_until = $2, attempts = attempts + 1
		WHERE event_id IN (
			SELECT event_id FROM outbox_event
			WHERE published_at IS NULL AND next_attempt_at <= $1
				AND (claimed_until IS NULL OR claimed_until < $1)
			ORDER BY created_at, event_id
			LIMIT $3
			FOR UPDATE SKIP LOCKED)
		RETURNING event_id, event_type, payload, attempts, claimed_until, created_at`,
		now.UTC(), claimedUntil, limit)
	if err != nil {
		return nil, fmt.Errorf("claim the outbox: %w", err)
	}
	defer func() { _ = rows.Close() }()
	type claimed struct {
		outbox.Message
		createdAt time.Time
	}
	var all []claimed
	for rows.Next() {
		var c claimed
		if err := rows.Scan(&c.EventID, &c.EventType, &c.Body, &c.Attempts, &c.ClaimedUntil, &c.createdAt); err != nil {
			return nil, fmt.Errorf("read a claimed outbox event: %w", err)
		}
		c.ClaimedUntil = c.ClaimedUntil.UTC()
		all = append(all, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the claimed outbox events: %w", err)
	}
	// RETURNING does not keep the order of the subquery: oldest first again.
	sort.Slice(all, func(i, j int) bool {
		if !all[i].createdAt.Equal(all[j].createdAt) {
			return all[i].createdAt.Before(all[j].createdAt)
		}
		return all[i].EventID < all[j].EventID
	})
	out := make([]outbox.Message, len(all))
	for i, c := range all {
		out[i] = c.Message
	}
	return out, nil
}

// MarkPublished implements outbox.Store.
func (s *OutboxStore) MarkPublished(ctx context.Context, m outbox.Message, at time.Time) error {
	return s.release(ctx, m, `UPDATE outbox_event SET published_at = $3, claimed_until = NULL, last_error = ''
		WHERE event_id = $1 AND claimed_until = $2 AND published_at IS NULL`, at.UTC())
}

// Reschedule implements outbox.Store.
func (s *OutboxStore) Reschedule(ctx context.Context, m outbox.Message, next time.Time, cause string) error {
	return s.release(ctx, m, `UPDATE outbox_event SET next_attempt_at = $3, last_error = $4, claimed_until = NULL
		WHERE event_id = $1 AND claimed_until = $2 AND published_at IS NULL`, next.UTC(), cause)
}

// release runs a write that only applies while the claim is the caller's.
func (s *OutboxStore) release(ctx context.Context, m outbox.Message, query string, args ...any) error {
	res, err := s.db.ExecContext(ctx, query, append([]any{m.EventID, m.ClaimedUntil.UTC()}, args...)...)
	if err != nil {
		return fmt.Errorf("update outbox event %s: %w", m.EventID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update outbox event %s: %w", m.EventID, err)
	}
	if n == 0 {
		return outbox.ErrClaimLost
	}
	return nil
}
