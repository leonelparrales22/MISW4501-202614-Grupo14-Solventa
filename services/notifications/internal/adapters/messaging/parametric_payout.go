// Package messaging holds the consumers of the domain events the
// notifications service receives from its SQS queue (docs/events/README.md).
// They are idempotent: the queue delivers at least once and without order.
package messaging

import (
	"context"
	"fmt"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/events"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/domain"
)

// EventTypeParametricPayoutCompleted is the event the claims service
// publishes when the automatic payment of a parametric claim is confirmed
// (HU-M3.1-01).
const EventTypeParametricPayoutCompleted = "ParametricPayoutCompleted"

// PayoutRecorder records the notification of a payout (application.Service).
type PayoutRecorder interface {
	RecordPayout(ctx context.Context, p domain.ParametricPayout) error
}

// parametricPayoutPayload is the payload of ParametricPayoutCompleted v1.
type parametricPayoutPayload struct {
	PayoutID    string `json:"payout_id"`
	ClaimID     string `json:"claim_id"`
	PolicyID    string `json:"policy_id"`
	Subject     string `json:"subject"`
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
	Trigger     struct {
		Parameter      string    `json:"parameter"`
		EventReference string    `json:"event_reference"`
		Threshold      float64   `json:"threshold"`
		Observed       float64   `json:"observed"`
		Unit           string    `json:"unit"`
		ObservedAt     time.Time `json:"observed_at"`
	} `json:"trigger"`
	PaymentMethod struct {
		Brand string `json:"brand"`
		Last4 string `json:"last4"`
	} `json:"payment_method"`
	IdempotencyKey string    `json:"idempotency_key"`
	PaidAt         time.Time `json:"paid_at"`
}

// NewParametricPayoutHandler handles ParametricPayoutCompleted: it records the
// notification of the payment and its pushes, idempotently by payout. A
// payload that cannot be decoded or validated is an error, so the message is
// retried and ends in the dead-letter queue of the notifications queue.
func NewParametricPayoutHandler(svc PayoutRecorder) events.Handler {
	return events.HandlerFunc(func(ctx context.Context, e events.Envelope) error {
		var p parametricPayoutPayload
		if err := e.DecodePayload(&p); err != nil {
			return err
		}
		t := p.Trigger
		payout := domain.ParametricPayout{
			PayoutID: p.PayoutID, ClaimID: p.ClaimID, PolicyID: p.PolicyID, Subject: p.Subject,
			AmountMinor: p.AmountMinor, Currency: p.Currency,
			Trigger: domain.Trigger{Parameter: t.Parameter, EventReference: t.EventReference, Threshold: t.Threshold,
				Observed: t.Observed, Unit: t.Unit, ObservedAt: t.ObservedAt.UTC()},
			Method:         domain.PaymentMethod{Brand: p.PaymentMethod.Brand, Last4: p.PaymentMethod.Last4},
			IdempotencyKey: p.IdempotencyKey, PaidAt: p.PaidAt.UTC(),
		}
		if payout.PaidAt.IsZero() {
			payout.PaidAt = e.OccurredAt
		}
		if err := svc.RecordPayout(ctx, payout); err != nil {
			return fmt.Errorf("event %s: %w", e.EventID, err)
		}
		return nil
	})
}
