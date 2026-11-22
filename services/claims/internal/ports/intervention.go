package ports

import (
	"context"
	"errors"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
)

// ErrStepConflict means the step was recorded first by somebody else, or the
// one before it is not there any more (HU-W2.3-02): two analysts recorded the
// same progress at the same time and this one lost.
var ErrStepConflict = errors.New("claims: the step of the intervention was recorded first by somebody else")

// InterventionCase is a claim with its assignment, the steps of the
// intervention in order and its closing (nil when there are none).
type InterventionCase struct {
	Claim      domain.ClaimRecord
	Assignment *domain.Assignment
	Steps      []domain.StepRecord
	Closure    *domain.Closure
}

// InterventionStore keeps the steps of the intervention of the provider and
// the closing of the case (HU-W2.3-02). It only adds them: a step or a
// closing is never corrected nor deleted.
type InterventionStore interface {
	// Intervention returns the claim with its intervention, or
	// ErrClaimNotFound.
	Intervention(ctx context.Context, claimID string) (InterventionCase, error)
	// RecordStep adds the step after the one before it. It returns
	// domain.ErrCaseClosed when the case was closed meanwhile and
	// ErrStepConflict when the step is taken or the one before is missing.
	RecordStep(ctx context.Context, r domain.StepRecord) error
	// Close records the closing and releases the provider busy with the
	// claim, all or nothing. It returns domain.ErrCaseClosed when the case
	// was closed first and domain.ErrInterventionNotFinished when the
	// intervention has not been reported finished.
	Close(ctx context.Context, c domain.Closure) error
}
