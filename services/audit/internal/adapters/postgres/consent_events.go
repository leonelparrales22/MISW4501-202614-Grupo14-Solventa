package postgres

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/ports"
)

// ConsentEventStore reads the append-only consent_event table (HU-M1.3-01).
// Its triggers reject UPDATE, DELETE and TRUNCATE; this adapter only reads.
type ConsentEventStore struct {
	db *sql.DB
}

// NewConsentEventStore builds the reader over an opened and migrated database.
func NewConsentEventStore(db *sql.DB) *ConsentEventStore { return &ConsentEventStore{db: db} }

var _ ports.ConsentEventReader = (*ConsentEventStore)(nil)

// ListByConsents returns the events of the consents, keyed by consent id, in
// the order they happened.
func (s *ConsentEventStore) ListByConsents(ctx context.Context, consentIDs []string) (map[string][]domain.ConsentEvent, error) {
	out := map[string][]domain.ConsentEvent{}
	if len(consentIDs) == 0 {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT event_id, consent_id, entity_code, status, occurred_at, recorded_at,
			actor_type, actor_id, channel, device, receipt_id, reason, reference, details
		FROM consent_event WHERE consent_id = ANY($1::text[])
		ORDER BY occurred_at, recorded_at, event_id`, consentIDs)
	if err != nil {
		return nil, fmt.Errorf("list the consent events: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			e                                                    domain.ConsentEvent
			entity, actorType, actorID, channel, device, receipt sql.NullString
			reason, reference                                    sql.NullString
			details                                              []byte
		)
		if err := rows.Scan(&e.EventID, &e.ConsentID, &entity, &e.Status, &e.OccurredAt, &e.RecordedAt,
			&actorType, &actorID, &channel, &device, &receipt, &reason, &reference, &details); err != nil {
			return nil, fmt.Errorf("read a consent event: %w", err)
		}
		e.EntityCode, e.ActorType, e.ActorID, e.Channel = entity.String, actorType.String, actorID.String, channel.String
		e.Device, e.ReceiptID, e.Reason, e.Reference = device.String, receipt.String, reason.String, reference.String
		e.Details = details
		e.OccurredAt, e.RecordedAt = e.OccurredAt.UTC(), e.RecordedAt.UTC()
		out[e.ConsentID] = append(out[e.ConsentID], e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list the consent events: %w", err)
	}
	return out, nil
}
