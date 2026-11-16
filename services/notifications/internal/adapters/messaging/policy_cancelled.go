package messaging

import (
	"context"
	"fmt"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/events"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/domain"
)

// EventTypePolicyCancelledForNonPayment is the event origination publishes
// when it cancels a policy because an instalment of its premium was not paid
// within the grace period (HU-W1.6-02).
const EventTypePolicyCancelledForNonPayment = "PolicyCancelledForNonPayment"

// CancellationRecorder records the notification of a cancellation
// (application.Service).
type CancellationRecorder interface {
	RecordCancellation(ctx context.Context, c domain.PolicyCancellation) error
}

// policyCancelledPayload is the payload of PolicyCancelledForNonPayment v1.
type policyCancelledPayload struct {
	PolicyID       string    `json:"policy_id"`
	Subject        string    `json:"subject"`
	Reason         string    `json:"reason"`
	Line           string    `json:"line"`
	InstallmentSeq int       `json:"installment_seq"`
	DueAt          time.Time `json:"due_at"`
	GraceEndsAt    time.Time `json:"grace_ends_at"`
	CancelledAt    time.Time `json:"cancelled_at"`
	AmountMinor    int64     `json:"amount_minor"`
	Currency       string    `json:"currency"`
}

// NewPolicyCancelledHandler handles PolicyCancelledForNonPayment: it records
// the notification of the cancellation and its pushes, once per policy. A
// payload that cannot be decoded or validated is an error, so the message is
// retried and ends in the dead-letter queue of the notifications queue.
func NewPolicyCancelledHandler(svc CancellationRecorder) events.Handler {
	return events.HandlerFunc(func(ctx context.Context, e events.Envelope) error {
		var p policyCancelledPayload
		if err := e.DecodePayload(&p); err != nil {
			return err
		}
		c := domain.PolicyCancellation{
			PolicyID: p.PolicyID, Subject: p.Subject, Reason: p.Reason, Line: p.Line, InstallmentSeq: p.InstallmentSeq,
			DueAt: p.DueAt.UTC(), GraceEndsAt: p.GraceEndsAt.UTC(), CancelledAt: p.CancelledAt.UTC(),
			AmountMinor: p.AmountMinor, Currency: p.Currency,
		}
		if c.CancelledAt.IsZero() {
			c.CancelledAt = e.OccurredAt
		}
		if err := svc.RecordCancellation(ctx, c); err != nil {
			return fmt.Errorf("event %s: %w", e.EventID, err)
		}
		return nil
	})
}
