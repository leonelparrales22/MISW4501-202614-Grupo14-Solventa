// Package messaging holds the consumers of the domain events audit receives
// from its SQS queue (docs/events/README.md). They are idempotent: the queue
// delivers at least once and without order.
package messaging

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/events"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
)

// EventTypeCustomerLinked is the event origination publishes when the account
// of an insured is linked to their document (HU-M1.3-01).
const EventTypeCustomerLinked = "CustomerLinked"

// HolderLinker records a holder link (consents.HolderService).
type HolderLinker interface {
	Link(ctx context.Context, l domain.HolderLink) error
}

// customerLinkedPayload is the payload of CustomerLinked v1.
type customerLinkedPayload struct {
	CustomerID string    `json:"customer_id"`
	Subject    string    `json:"subject"`
	DocumentID string    `json:"document_id"`
	LinkedAt   time.Time `json:"linked_at"`
}

// NewCustomerLinkedHandler handles CustomerLinked: it records the link of the
// account to the document, idempotently by subject. A payload that cannot be
// decoded or validated is an error, so the message is retried and ends in the
// dead-letter queue of the audit queue. The document is never logged.
func NewCustomerLinkedHandler(svc HolderLinker) events.Handler {
	return events.HandlerFunc(func(ctx context.Context, e events.Envelope) error {
		var p customerLinkedPayload
		if err := e.DecodePayload(&p); err != nil {
			return err
		}
		l := domain.HolderLink{
			Subject: p.Subject, DocumentID: domain.NormalizeDocument(p.DocumentID), CustomerID: p.CustomerID,
			LinkedAt: p.LinkedAt, EventID: e.EventID,
		}
		if l.LinkedAt.IsZero() {
			l.LinkedAt = e.OccurredAt
		}
		if err := l.Validate(); err != nil {
			return fmt.Errorf("event %s: %w", e.EventID, err)
		}
		if err := svc.Link(ctx, l); err != nil {
			return fmt.Errorf("event %s: %w", e.EventID, err)
		}
		slog.InfoContext(ctx, "holder linked", "event_id", e.EventID, "customer_id", p.CustomerID)
		return nil
	})
}
