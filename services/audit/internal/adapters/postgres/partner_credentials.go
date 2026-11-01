package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/ports"
)

// PartnerCredentialStore implements ports.PartnerCredentialStore over the
// partner_credential_record table (HU-W3.1-01, migration 0007). The table is
// append-only: its triggers reject UPDATE, DELETE and TRUNCATE, and this
// adapter has no statement that issues them.
type PartnerCredentialStore struct {
	db *sql.DB
}

// NewPartnerCredentialStore builds the store over an opened and migrated
// database.
func NewPartnerCredentialStore(db *sql.DB) *PartnerCredentialStore {
	return &PartnerCredentialStore{db: db}
}

var _ ports.PartnerCredentialStore = (*PartnerCredentialStore)(nil)

const partnerRecordColumns = `event_id, event_type, partner_id, partner_code, partner_name, credential_id, client_id,
	previous_credential_id, previous_client_id, previous_expires_at, grace_period_hours, reason,
	daily_limit, rate_limit, burst_limit, actor_type, actor_id, actor_label, occurred_at`

// Append inserts the record; an event already recorded is left as it is.
func (s *PartnerCredentialStore) Append(ctx context.Context, r domain.PartnerCredentialRecord) (bool, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO partner_credential_record (`+partnerRecordColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19)
		ON CONFLICT (event_id) DO NOTHING`,
		r.EventID, r.EventType, r.PartnerID, r.PartnerCode, r.PartnerName, r.CredentialID, r.ClientID,
		nullString(r.PreviousCredentialID), nullString(r.PreviousClientID), nullTime(r.PreviousExpiresAt),
		nullInt(r.GracePeriodHours), r.Reason, r.Quota.DailyLimit, r.Quota.RateLimit, r.Quota.BurstLimit,
		r.Actor.Type, r.Actor.ID, r.Actor.Label, r.OccurredAt.UTC())
	if err != nil {
		return false, fmt.Errorf("insert the partner credential record %s: %w", r.EventID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("insert the partner credential record %s: %w", r.EventID, err)
	}
	return n == 1, nil
}

// ListByPartner returns the records of the partner, newest first.
func (s *PartnerCredentialStore) ListByPartner(ctx context.Context, partnerID string) ([]domain.PartnerCredentialRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+partnerRecordColumns+`, recorded_at
		FROM partner_credential_record WHERE partner_id = $1
		ORDER BY occurred_at DESC, record_id DESC`, partnerID)
	if err != nil {
		return nil, fmt.Errorf("list the partner credential records: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []domain.PartnerCredentialRecord
	for rows.Next() {
		var (
			r                          domain.PartnerCredentialRecord
			prevCredential, prevClient sql.NullString
			prevExpires                sql.NullTime
			grace                      sql.NullInt64
		)
		if err := rows.Scan(&r.EventID, &r.EventType, &r.PartnerID, &r.PartnerCode, &r.PartnerName, &r.CredentialID,
			&r.ClientID, &prevCredential, &prevClient, &prevExpires, &grace, &r.Reason, &r.Quota.DailyLimit,
			&r.Quota.RateLimit, &r.Quota.BurstLimit, &r.Actor.Type, &r.Actor.ID, &r.Actor.Label, &r.OccurredAt,
			&r.RecordedAt); err != nil {
			return nil, fmt.Errorf("read a partner credential record: %w", err)
		}
		r.PreviousCredentialID, r.PreviousClientID = prevCredential.String, prevClient.String
		if prevExpires.Valid {
			r.PreviousExpiresAt = prevExpires.Time.UTC()
		}
		r.GracePeriodHours = int(grace.Int64)
		r.OccurredAt, r.RecordedAt = r.OccurredAt.UTC(), r.RecordedAt.UTC()
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the partner credential records: %w", err)
	}
	return out, nil
}

// nullTime stores the zero instant as NULL.
func nullTime(t time.Time) sql.NullTime { return sql.NullTime{Time: t.UTC(), Valid: !t.IsZero()} }

// nullInt stores zero as NULL.
func nullInt(n int) sql.NullInt64 { return sql.NullInt64{Int64: int64(n), Valid: n != 0} }
