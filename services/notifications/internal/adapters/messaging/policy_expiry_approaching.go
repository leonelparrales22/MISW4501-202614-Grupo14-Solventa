package messaging

import (
	"context"
	"fmt"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/events"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/domain"
)

// EventTypePolicyExpiryApproaching is the event origination publishes when an
// active policy reaches 30, 15 or 5 days before its end (HU-M3.2-01).
const EventTypePolicyExpiryApproaching = "PolicyExpiryApproaching"

// ExpiryRecorder records the notification of an expiry alert
// (application.Service).
type ExpiryRecorder interface {
	RecordExpiryAlert(ctx context.Context, e domain.ExpiryAlert) error
}

// policyExpiryApproachingPayload is the payload of PolicyExpiryApproaching v1.
type policyExpiryApproachingPayload struct {
	PolicyID      string    `json:"policy_id"`
	Subject       string    `json:"subject"`
	Source        string    `json:"source"`
	Line          string    `json:"line"`
	Product       *string   `json:"product"`
	InsuredObject *string   `json:"insured_object"`
	CoverageEnd   time.Time `json:"coverage_end"`
	MilestoneDays int       `json:"milestone_days"`
	DaysLeft      int       `json:"days_left"`
	DetectedAt    time.Time `json:"detected_at"`
}

// NewPolicyExpiryApproachingHandler handles PolicyExpiryApproaching: it
// records the notification of the alert and its pushes, idempotently by
// policy, period and milestone. A payload that cannot be decoded or validated
// is an error, so the message is retried and ends in the dead-letter queue of
// the notifications queue.
func NewPolicyExpiryApproachingHandler(svc ExpiryRecorder) events.Handler {
	return events.HandlerFunc(func(ctx context.Context, e events.Envelope) error {
		var p policyExpiryApproachingPayload
		if err := e.DecodePayload(&p); err != nil {
			return err
		}
		a := domain.ExpiryAlert{
			PolicyID: p.PolicyID, Subject: p.Subject, Source: p.Source, Line: p.Line, Product: valueOf(p.Product),
			InsuredObject: valueOf(p.InsuredObject), CoverageEnd: p.CoverageEnd.UTC(), MilestoneDays: p.MilestoneDays,
			DaysLeft: p.DaysLeft, DetectedAt: p.DetectedAt.UTC(),
		}
		if a.DetectedAt.IsZero() {
			a.DetectedAt = e.OccurredAt
		}
		if err := svc.RecordExpiryAlert(ctx, a); err != nil {
			return fmt.Errorf("event %s: %w", e.EventID, err)
		}
		return nil
	})
}

func valueOf(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
