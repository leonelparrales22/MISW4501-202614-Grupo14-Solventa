// Package billing charges the premium of an accepted risk without losing or
// duplicating a charge when the payment gateway fails (HA-D02), and activates
// the coverage only once the payment is confirmed (CA-4).
//
// Starter is the ports.IssuanceStarter of the approved acceptances: it stores
// the pending charge with its snapshot. Worker charges it in the background
// with the shared idempotent mechanism (pkg/idempotency: deterministic key
// "cobro-" + acceptance id, atomic claim in PostgreSQL, growing waits within a
// 24-hour window) and, once the charge is confirmed, publishes the
// CoverageActivated event in process to the contractual document of
// HU-W1.3-03. Service answers the charge state, the issuance exceptions and
// their resumption.
package billing

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// TermsFunc returns the coverage terms an offer activates once it is paid
// (insured amount, term and coverages). OfferTerms is the one the service uses:
// the terms of a personalized offer (HU-W1.2-03) or, for the demo and adjusted
// offers, demonstration terms (decision D-16).
type TermsFunc func(offer domain.Offer) (domain.CoverageTerms, error)

// Starter starts the issuance of an approved acceptance by storing its
// pending premium charge. It never calls the gateway: the acceptance is the
// no-loss point (HU-W1.3-01) and the charge is persisted before any call
// (CA-1). It is idempotent by acceptance.
type Starter struct {
	repo   ports.ChargeRepository
	terms  TermsFunc
	clock  ports.Clock
	policy resilience.RetryPolicy
	notify func()
}

var _ ports.IssuanceStarter = (*Starter)(nil)

// NewStarter builds a Starter. notify, when not nil, wakes the worker after a
// new charge is stored.
func NewStarter(repo ports.ChargeRepository, terms TermsFunc, clock ports.Clock, policy resilience.RetryPolicy, notify func()) *Starter {
	if notify == nil {
		notify = func() {}
	}
	return &Starter{repo: repo, terms: terms, clock: clock, policy: policy, notify: notify}
}

// Start stores the pending charge of the acceptance. When the snapshot could
// not activate the coverage (decision D-17: e.g. no e-mail of the holder) it
// stores nothing and returns an error, so the acceptance stays registered and
// is retried, and nothing is charged that cannot be activated.
func (s *Starter) Start(ctx context.Context, a domain.Acceptance, offer domain.Offer) error {
	terms, err := s.terms(offer)
	if err != nil {
		return fmt.Errorf("could not get the coverage terms of offer %s: %w", offer.ID, err)
	}
	now := s.clock.Now().UTC()
	c := domain.NewCharge(a, offer, terms, now, s.policy.WindowEnd(now))
	if c.AmountMinor <= 0 {
		return fmt.Errorf("the offer %s has no premium to charge", offer.ID)
	}
	if err := c.ValidateActivation(now); err != nil {
		slog.ErrorContext(ctx, "the premium is not charged: the coverage could not be activated",
			"acceptance_id", a.ID, "offer_id", offer.ID, "error", err)
		return fmt.Errorf("the premium of acceptance %s is not charged because its coverage could not be activated: %w", a.ID, err)
	}

	stored, created, err := s.repo.CreatePending(ctx, c)
	if err != nil {
		return fmt.Errorf("could not store the premium charge of acceptance %s: %w", a.ID, err)
	}
	if created {
		slog.InfoContext(ctx, "premium charge registered", "acceptance_id", stored.AcceptanceID,
			"idempotency_key", stored.IdempotencyKey, "retry_window_ends_at", stored.WindowEndsAt)
		s.notify()
	}
	return nil
}
