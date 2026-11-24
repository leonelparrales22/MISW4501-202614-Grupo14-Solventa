// Package parametric is the parametric engine of claims (HU-M2.4-01): when an
// external event crosses the threshold of a parametric policy (the delay of a
// flight of a travel policy), it files the claim, resolves it automatically
// and leaves its payout due. The payout reuses the decision of HU-W2.2-03
// (internal/decision): the same worker pays it to the gateway with one key per
// claim, and when the gateway confirms it the store writes
// ParametricPayoutCompleted to the outbox, which notifications consumes
// (HU-M3.1-01).
//
// This is the minimal engine: the observation arrives already matched to the
// policy of the insured (event ParametricTriggerObserved), and the rules are
// the ones of the prototype (domain.DefaultParametricRules).
package parametric

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// ErrPolicyUnavailable means the policy core did not answer: the observation
// is retried.
var ErrPolicyUnavailable = errors.New("claims: the policy core is unavailable")

// Outcome is what an observation did.
type Outcome string

const (
	// OutcomeResolved: a new claim was filed, resolved and its payout is due.
	OutcomeResolved Outcome = "resolved"
	// OutcomeDuplicate: the observation had already been resolved.
	OutcomeDuplicate Outcome = "duplicate"
	// OutcomeIgnored: the observation does not pay (below the threshold, a
	// parameter the policy does not cover, a policy not in force, or a policy
	// or insured the core does not know).
	OutcomeIgnored Outcome = "ignored"
)

// Result is the outcome of an observation and the claim it resolved.
type Result struct {
	Outcome Outcome
	Claim   domain.ClaimRecord
	// Reason is why an ignored observation does not pay.
	Reason error
}

// Service resolves the observations.
type Service struct {
	terms ports.PolicyTermsReader
	store ports.ParametricStore
	rules domain.ParametricRules
	clock ports.Clock
	wake  func()
	newID func() (string, error)
}

// NewService builds the engine. wake, when set, is called after a claim is
// resolved so the payout worker pays it at once instead of on its next poll.
func NewService(terms ports.PolicyTermsReader, store ports.ParametricStore, rules domain.ParametricRules,
	clock ports.Clock, wake func()) *Service {
	if wake == nil {
		wake = func() {}
	}
	return &Service{terms: terms, store: store, rules: rules, clock: clock, wake: wake, newID: newID}
}

// Observe resolves an observation. A malformed observation is
// domain.ErrInvalidObservation and a core that does not answer is
// ErrPolicyUnavailable: both are errors, so the message is retried and ends in
// the dead-letter queue. An observation that does not pay is not an error.
func (s *Service) Observe(ctx context.Context, o domain.Observation) (Result, error) {
	if err := o.Validate(); err != nil {
		return Result{}, err
	}
	terms, err := s.terms.PolicyTerms(ctx, o.Subject, o.PolicyID)
	if err != nil {
		return s.coreError(ctx, o, err)
	}
	holder, err := s.terms.Holder(ctx, o.Subject)
	if err != nil {
		return s.coreError(ctx, o, err)
	}
	id, err := s.newID()
	if err != nil {
		return Result{}, err
	}
	c, resolution, d, err := domain.ResolveParametric(id, o, s.rules, terms, holder, s.clock.Now())
	switch {
	case errors.Is(err, domain.ErrInvalidObservation):
		return Result{}, err
	case err != nil:
		return s.ignored(ctx, o, err), nil
	}
	stored, created, err := s.store.Resolve(ctx, c, resolution, d)
	if err != nil {
		return Result{}, fmt.Errorf("resolve the observation %s: %w", o.ID, err)
	}
	if !created {
		slog.InfoContext(ctx, "parametric observation already resolved", "observation_id", o.ID, "claim_id", stored.ID)
		return Result{Outcome: OutcomeDuplicate, Claim: stored}, nil
	}
	slog.InfoContext(ctx, "parametric claim resolved automatically", "observation_id", o.ID, "claim_id", stored.ID,
		"filing_number", stored.FilingNumber, "parameter", o.Parameter, "observed", o.Observed,
		"threshold", resolution.Trigger.Threshold)
	s.wake()
	return Result{Outcome: OutcomeResolved, Claim: stored}, nil
}

// coreError tells a policy or insured the core does not know, which does not
// pay, from a core that does not answer, which is retried.
func (s *Service) coreError(ctx context.Context, o domain.Observation, err error) (Result, error) {
	if errors.Is(err, ports.ErrPolicyNotFound) || errors.Is(err, ports.ErrHolderNotFound) {
		return s.ignored(ctx, o, err), nil
	}
	return Result{}, fmt.Errorf("%w: %w", ErrPolicyUnavailable, err)
}

func (s *Service) ignored(ctx context.Context, o domain.Observation, reason error) Result {
	slog.WarnContext(ctx, "parametric observation does not pay", "observation_id", o.ID, "policy_id", o.PolicyID,
		"parameter", o.Parameter, "observed", o.Observed, "reason", reason)
	return Result{Outcome: OutcomeIgnored, Reason: reason}
}

// newID returns a random UUID (version 4).
func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("could not generate an id: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
