package memory

import (
	"context"
	"sync"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// InterventionStore keeps the steps of the intervention and the closing of
// the case of the claims of an AssignmentStore (HU-W2.3-02), with the same
// rules as PostgreSQL: each step goes after the one before it, a case closes
// once, after the intervention is finished, and closing it releases its
// provider. Everything is lost when the service restarts.
type InterventionStore struct {
	assignments *AssignmentStore

	mu       sync.Mutex
	steps    map[string][]domain.StepRecord
	closures map[string]domain.Closure
}

// NewInterventionStore builds the store over the assignments of assignments.
func NewInterventionStore(assignments *AssignmentStore) *InterventionStore {
	return &InterventionStore{assignments: assignments, steps: map[string][]domain.StepRecord{},
		closures: map[string]domain.Closure{}}
}

var _ ports.InterventionStore = (*InterventionStore)(nil)

// Intervention returns the claim with its intervention.
func (s *InterventionStore) Intervention(ctx context.Context, claimID string) (ports.InterventionCase, error) {
	k, err := s.assignments.Case(ctx, claimID)
	if err != nil {
		return ports.InterventionCase{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := ports.InterventionCase{Claim: k.Claim, Assignment: k.Assignment, Steps: []domain.StepRecord{}}
	for _, r := range s.steps[claimID] {
		out.Steps = append(out.Steps, copyStep(r))
	}
	if c, ok := s.closures[claimID]; ok {
		out.Closure = &c
	}
	return out, nil
}

// RecordStep adds the step after the one before it.
func (s *InterventionStore) RecordStep(_ context.Context, r domain.StepRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, closed := s.closures[r.ClaimID]; closed {
		return domain.ErrCaseClosed
	}
	if domain.NextStep(s.steps[r.ClaimID]) != r.Step {
		return ports.ErrStepConflict
	}
	s.steps[r.ClaimID] = append(s.steps[r.ClaimID], copyStep(r))
	return nil
}

// Close records the closing and releases the provider of the claim.
func (s *InterventionStore) Close(_ context.Context, c domain.Closure) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, closed := s.closures[c.ClaimID]; closed {
		return domain.ErrCaseClosed
	}
	if domain.NextStep(s.steps[c.ClaimID]) != "" {
		return domain.ErrInterventionNotFinished
	}
	s.closures[c.ClaimID] = c
	s.assignments.providers.release(c.ClaimID, c.ClosedBy.Email, c.RecordedAt)
	return nil
}

// copyStep returns a step that shares no pointer with r.
func copyStep(r domain.StepRecord) domain.StepRecord {
	if r.Report != nil {
		report := *r.Report
		r.Report = &report
	}
	return r
}
