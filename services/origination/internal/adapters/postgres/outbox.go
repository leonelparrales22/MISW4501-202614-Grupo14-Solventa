package postgres

import (
	"context"
	"fmt"
	"time"
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
