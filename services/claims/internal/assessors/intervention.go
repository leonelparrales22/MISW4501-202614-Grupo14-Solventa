package assessors

import (
	"context"
	"errors"
	"log/slog"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// InterventionService follows the intervention of the assigned provider and
// closes the case (HU-W2.3-02): the analyst records each progress the
// provider reports and, once it reports the intervention finished, closes the
// case with its result. The status of the claim does not change, and closing
// never approves, rejects nor pays it.
type InterventionService struct {
	store ports.InterventionStore
	clock ports.Clock
}

// NewInterventionService builds the service.
func NewInterventionService(store ports.InterventionStore, clock ports.Clock) *InterventionService {
	return &InterventionService{store: store, clock: clock}
}

// InterventionView is the intervention of a claim with everything the screen
// "Intervención del prestador" (O8) shows.
type InterventionView struct {
	Claim      domain.ClaimRecord
	State      domain.InterventionState
	Assignment *domain.Assignment
	// Steps are the steps recorded, in order.
	Steps []domain.StepRecord
	// Next is the step the intervention records next; empty when it is
	// finished or there is no provider.
	Next    domain.InterventionStep
	Report  *domain.ProviderReport
	Closure *domain.Closure
	// Missing are the steps still to record before the case can be closed
	// (criterion 4); empty when it can be closed or is closed.
	Missing []domain.InterventionStep
}

// Closable tells whether the case can be closed now.
func (v InterventionView) Closable() bool { return v.State == domain.InterventionReadyToClose }

func interventionViewOf(k ports.InterventionCase) InterventionView {
	v := InterventionView{Claim: k.Claim, Assignment: k.Assignment, Steps: k.Steps, Closure: k.Closure,
		State: domain.InterventionStateOf(k.Assignment, k.Steps, k.Closure), Report: domain.ReportOf(k.Steps),
		Missing: []domain.InterventionStep{}}
	if v.Steps == nil {
		v.Steps = []domain.StepRecord{}
	}
	if v.State == domain.InterventionInProgress {
		v.Next, v.Missing = domain.NextStep(k.Steps), domain.MissingSteps(k.Steps)
	}
	return v
}

// View returns the intervention of a claim, or ports.ErrClaimNotFound.
func (s *InterventionService) View(ctx context.Context, claimID string) (InterventionView, error) {
	k, err := s.store.Intervention(ctx, claimID)
	if err != nil {
		return InterventionView{}, err
	}
	return interventionViewOf(k), nil
}

// RecordStep records a progress the provider reported (criterion 1) and
// returns the intervention as it is now. A step another analyst recorded at
// the same time is out of order.
func (s *InterventionService) RecordStep(ctx context.Context, claimID string, in domain.StepRequest,
	by domain.Analyst) (InterventionView, error) {
	k, err := s.store.Intervention(ctx, claimID)
	if err != nil {
		return InterventionView{}, err
	}
	r, err := domain.RecordStep(k.Assignment, k.Steps, k.Closure, in, by, s.clock.Now())
	if err != nil {
		return InterventionView{}, err
	}
	err = s.store.RecordStep(ctx, r)
	if errors.Is(err, ports.ErrStepConflict) {
		return InterventionView{}, domain.ErrStepOutOfOrder
	}
	if err != nil {
		return InterventionView{}, err
	}
	slog.InfoContext(ctx, "intervention step recorded", "claim_id", claimID, "step", r.Step)
	return s.View(ctx, claimID)
}

// Close closes the case with its result (criteria 3 to 5) and releases the
// provider. It returns a *domain.NotFinishedError naming what is missing
// when the provider has not reported the intervention finished.
func (s *InterventionService) Close(ctx context.Context, claimID string, in domain.CloseRequest,
	by domain.Analyst) (InterventionView, error) {
	k, err := s.store.Intervention(ctx, claimID)
	if err != nil {
		return InterventionView{}, err
	}
	c, err := domain.CloseCase(k.Assignment, k.Steps, k.Closure, in, by, s.clock.Now())
	if err != nil {
		return InterventionView{}, err
	}
	if err := s.store.Close(ctx, c); err != nil {
		return InterventionView{}, err
	}
	slog.InfoContext(ctx, "case closed", "claim_id", claimID, "result", c.Result)
	return s.View(ctx, claimID)
}
