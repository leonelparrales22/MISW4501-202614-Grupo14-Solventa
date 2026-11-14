package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// StageNoticeStore is the ports.StageNoticeStore over claim_status_change,
// claim_stage_notice and outbox_event (HU-M3.3-01).
//
// A change is taken with FOR UPDATE SKIP LOCKED, so two tasks never hand the
// same change; and it is looked at by its absence from claim_stage_notice,
// not by a cursor, so a change whose transaction commits after a later one
// (the ids are taken when the row is inserted) is not skipped.
type StageNoticeStore struct {
	db *sql.DB
}

var _ ports.StageNoticeStore = (*StageNoticeStore)(nil)

// StageNoticeStoreOf builds the store over the database of the claims.
func StageNoticeStoreOf(records *ClaimRecordStore) *StageNoticeStore {
	return &StageNoticeStore{db: records.db}
}

// NoticePending implements ports.StageNoticeStore.
func (s *StageNoticeStore) NoticePending(ctx context.Context, limit int,
	toEvent func(domain.StageChange) (*ports.OutboxEvent, error)) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin the stage notices: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	changes, err := pendingChanges(ctx, tx, limit)
	if err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	for _, c := range changes {
		ev, err := toEvent(c)
		if err != nil {
			return 0, fmt.Errorf("build the notice of the change %d: %w", c.ChangeID, err)
		}
		var eventID sql.NullString
		if ev != nil {
			eventID = sql.NullString{String: ev.ID, Valid: true}
			if _, err := tx.ExecContext(ctx, `INSERT INTO outbox_event (event_id, event_type, payload, occurred_at, next_attempt_at)
				VALUES ($1, $2, $3, $4, $5) ON CONFLICT (event_id) DO NOTHING`,
				ev.ID, ev.Type, ev.Body, ev.OccurredAt.UTC(), now); err != nil {
				return 0, fmt.Errorf("write the notice of the change %d to the outbox: %w", c.ChangeID, err)
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO claim_stage_notice (change_id, event_id, processed_at)
			VALUES ($1, $2, $3) ON CONFLICT (change_id) DO NOTHING`, c.ChangeID, eventID, now); err != nil {
			return 0, fmt.Errorf("mark the change %d as looked at: %w", c.ChangeID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit the stage notices: %w", err)
	}
	return len(changes), nil
}

// pendingChanges reads and locks the changes not looked at yet, with their
// claim and its decision.
func pendingChanges(ctx context.Context, tx *sql.Tx, limit int) ([]domain.StageChange, error) {
	rows, err := tx.QueryContext(ctx, `SELECT h.id, h.claim_id, h.status, COALESCE(h.previous_status, ''), h.changed_at,
			c.filing_number, c.policy_id, c.cognito_sub, c.channel,
			d.payout_amount_minor, d.currency, d.paid_at
		FROM claim_status_change h
		JOIN claim c ON c.id = h.claim_id
		LEFT JOIN claim_decision d ON d.claim_id = h.claim_id
		WHERE h.id >= (SELECT first_change_id FROM claim_stage_notice_start)
		  AND NOT EXISTS (SELECT 1 FROM claim_stage_notice n WHERE n.change_id = h.id)
		ORDER BY h.id
		LIMIT $1
		FOR UPDATE OF h SKIP LOCKED`, limit)
	if err != nil {
		return nil, fmt.Errorf("read the pending changes of stage: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []domain.StageChange
	for rows.Next() {
		var (
			c                         domain.StageChange
			status, previous, channel string
			payout                    sql.NullInt64
			currency                  sql.NullString
			paidAt                    sql.NullTime
		)
		if err := rows.Scan(&c.ChangeID, &c.ClaimID, &status, &previous, &c.ChangedAt, &c.FilingNumber, &c.PolicyID,
			&c.Subject, &channel, &payout, &currency, &paidAt); err != nil {
			return nil, fmt.Errorf("read a pending change of stage: %w", err)
		}
		c.Status, c.PreviousStatus, c.Channel = domain.Status(status), domain.Status(previous), domain.Channel(channel)
		c.ChangedAt, c.Currency = c.ChangedAt.UTC(), currency.String
		if payout.Valid {
			c.PayoutMinor = &payout.Int64
		}
		if paidAt.Valid {
			at := paidAt.Time.UTC()
			c.PaidAt = &at
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
