package messaging

import (
	"context"
	"fmt"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/events"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/domain"
)

// EventTypeClaimStageChanged is the event the claims service publishes when a
// claim moves on to a stage the insured waits for (HU-M3.3-01).
const EventTypeClaimStageChanged = "ClaimStageChanged"

// StageRecorder records the notification of a change of stage
// (application.Service).
type StageRecorder interface {
	RecordStageChange(ctx context.Context, c domain.ClaimStageChange) error
}

// claimStageChangedPayload is the payload of ClaimStageChanged v1.
type claimStageChangedPayload struct {
	ChangeID     int64      `json:"change_id"`
	ClaimID      string     `json:"claim_id"`
	FilingNumber string     `json:"filing_number"`
	PolicyID     string     `json:"policy_id"`
	Subject      string     `json:"subject"`
	Stage        string     `json:"stage"`
	ChangedAt    time.Time  `json:"changed_at"`
	AmountMinor  *int64     `json:"amount_minor"`
	Currency     string     `json:"currency"`
	PaidAt       *time.Time `json:"paid_at"`
}

// NewClaimStageChangedHandler handles ClaimStageChanged: it records the
// notification of the change and its pushes, idempotently by change. A payload
// that cannot be decoded or validated is an error, so the message is retried
// and ends in the dead-letter queue of the notifications queue.
func NewClaimStageChangedHandler(svc StageRecorder) events.Handler {
	return events.HandlerFunc(func(ctx context.Context, e events.Envelope) error {
		var p claimStageChangedPayload
		if err := e.DecodePayload(&p); err != nil {
			return err
		}
		c := domain.ClaimStageChange{
			ChangeID: p.ChangeID, ClaimID: p.ClaimID, FilingNumber: p.FilingNumber, PolicyID: p.PolicyID,
			Subject: p.Subject, Stage: p.Stage, ChangedAt: p.ChangedAt.UTC(), AmountMinor: p.AmountMinor,
			Currency: p.Currency,
		}
		if c.ChangedAt.IsZero() {
			c.ChangedAt = e.OccurredAt
		}
		if p.PaidAt != nil {
			at := p.PaidAt.UTC()
			c.PaidAt = &at
		}
		if err := svc.RecordStageChange(ctx, c); err != nil {
			return fmt.Errorf("event %s: %w", e.EventID, err)
		}
		return nil
	})
}
