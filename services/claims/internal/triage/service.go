// Package triage gives every claim that enters evaluation its priority level
// and recalculates it when the information it came from changes (HU-W2.2-01).
// It reads the claims from the database of the service: it is not called by
// the filing nor by the evaluation, so any story that changes a claim gets the
// recalculation without knowing the triage exists.
package triage

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// Service calculates, records and reads the levels of the claims.
type Service struct {
	store  ports.TriageStore
	clock  ports.Clock
	policy domain.TriagePolicy
}

// NewService builds the service over the policy triage-policy-v1.
func NewService(store ports.TriageStore, clock ports.Clock) *Service {
	return &Service{store: store, clock: clock, policy: domain.TriagePolicyV1()}
}

// WithPolicy replaces the policy, so a test can change its version.
func (s *Service) WithPolicy(p domain.TriagePolicy) *Service {
	s.policy = p
	return s
}

// Policy returns the policy in force.
func (s *Service) Policy() domain.TriagePolicy { return s.policy }

// Assess calculates the level of the claim and records it when the claim is in
// evaluation and its input changed since the last calculation. recorded is
// false when there was nothing to record, also when another task recorded the
// same calculation first.
func (s *Service) Assess(ctx context.Context, c ports.TriageCandidate) (r domain.TriageRecord, recorded bool, err error) {
	if !domain.Triageable(c.Claim.Status) {
		return domain.TriageRecord{}, false, nil
	}
	a, known := s.policy.Assess(s.policy.InputOf(c.Claim))
	if !known {
		slog.WarnContext(ctx, "the claim type is not in the severity table of the triage: assessed as medium",
			"claim_id", c.Claim.ID, "incident_type", string(c.Claim.IncidentType), "policy_version", s.policy.Version)
	}
	r, changed := domain.NextTriage(c.Claim.ID, a, c.Last, s.clock.Now().Truncate(time.Microsecond))
	if !changed {
		return domain.TriageRecord{}, false, nil
	}
	switch err := s.store.Record(ctx, r); {
	case errors.Is(err, ports.ErrTriageConflict):
		return domain.TriageRecord{}, false, nil
	case err != nil:
		return domain.TriageRecord{}, false, err
	}
	return r, true, nil
}

// Sweep assesses up to limit claims that are waiting for a calculation and
// returns how many it recorded. A claim that fails is logged and left for the
// next sweep; it does not stop the others.
func (s *Service) Sweep(ctx context.Context, limit int) (int, error) {
	pending, err := s.store.Pending(ctx, domain.TriageableStatuses(), s.policy.Version, limit)
	if err != nil {
		return 0, fmt.Errorf("read the claims to assess: %w", err)
	}
	recorded := 0
	for _, c := range pending {
		r, ok, err := s.Assess(ctx, c)
		if err != nil {
			slog.ErrorContext(ctx, "could not assess the priority of the claim", "claim_id", c.Claim.ID, "error", err)
			continue
		}
		if !ok {
			continue
		}
		recorded++
		// Never the amount nor anything of the insured: the id, the level and
		// how long the claim waited since it was filed (criterion 1).
		slog.InfoContext(ctx, "claim priority assessed", "claim_id", r.ClaimID, "level", int(r.Assessment.Level),
			"seq", r.Seq, "causes", r.Causes, "seconds_since_filed", r.AssessedAt.Sub(c.Claim.FiledAt).Seconds())
	}
	return recorded, nil
}

// View is the triage of one claim: where it stands, its last calculation and
// every calculation, newest first.
type View struct {
	ClaimID string
	State   domain.TriageState
	Current *domain.TriageRecord
	History []domain.TriageRecord
}

// View returns the triage of the claim to operations or to the insured who
// filed it. Anyone else gets security.ErrForbidden; an unknown id,
// ports.ErrClaimNotFound.
func (s *Service) View(ctx context.Context, claimID string) (View, error) {
	c, err := s.store.Candidate(ctx, claimID)
	if err != nil {
		return View{}, err
	}
	if err := security.RequireOwnerOrRole(ctx, c.Claim.Subject, security.RoleOperations); err != nil {
		return View{}, err
	}
	history, err := s.store.History(ctx, claimID)
	if err != nil {
		return View{}, err
	}
	v := View{ClaimID: claimID, State: domain.TriageStateOf(c.Claim.Status, c.Last), History: history}
	if len(history) > 0 {
		v.Current = &history[0]
	}
	return v, nil
}

// Summary is the triage of one claim of a page.
type Summary struct {
	ClaimID string
	State   domain.TriageState
	Current *domain.TriageRecord
}

// Summaries returns the triage of each claim, in the order of the ids. A claim
// that does not exist comes back as not_found.
func (s *Service) Summaries(ctx context.Context, ids []string) ([]Summary, error) {
	found, err := s.store.Current(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]Summary, len(ids))
	for i, id := range ids {
		c, ok := found[id]
		if !ok {
			out[i] = Summary{ClaimID: id, State: domain.TriageNotFound}
			continue
		}
		out[i] = Summary{ClaimID: id, State: domain.TriageStateOf(c.Claim.Status, c.Last), Current: c.Last}
	}
	return out, nil
}
