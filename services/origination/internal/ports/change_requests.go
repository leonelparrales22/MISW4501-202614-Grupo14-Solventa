package ports

import (
	"context"
	"errors"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

// Errors of the change requests (HU-W1.4-01).
var (
	// ErrChangeRequestNotFound is returned when a request id is unknown.
	ErrChangeRequestNotFound = errors.New("change request not found")
	// ErrChangeRequestOpen is returned when the policy already has an open
	// request of the same type (decision D-6 a). The error is a
	// *ChangeRequestOpenError that names it.
	ErrChangeRequestOpen = errors.New("the policy already has an open change request of this type")
)

// ChangeRequestOpenError names the open request that keeps a new one of the
// same policy and type from being filed. RequestID is empty when it was closed
// before it could be read.
type ChangeRequestOpenError struct {
	RequestID string
}

func (e *ChangeRequestOpenError) Error() string {
	return ErrChangeRequestOpen.Error() + ": " + e.RequestID
}

// Unwrap makes the error match ErrChangeRequestOpen.
func (e *ChangeRequestOpenError) Unwrap() error { return ErrChangeRequestOpen }

// ChangeRequestRepository keeps the change requests of the policies
// (HU-W1.4-01). HU-W1.4-02 changes their status; there is deliberately no
// Delete, and the content of a request never changes.
type ChangeRequestRepository interface {
	// Create gives the request the next number of the civil year of its
	// filing (SOL-MOD-<year>-<number>) and stores it, both in one
	// transaction, and returns it with its id. When the policy already has an
	// open request of the same type it stores nothing, gives the number back
	// and returns a *ChangeRequestOpenError: of two requests filed at once,
	// exactly one is stored.
	Create(ctx context.Context, r domain.ChangeRequest) (domain.ChangeRequest, error)
	// Get returns ErrChangeRequestNotFound for an unknown id.
	Get(ctx context.Context, requestID string) (domain.ChangeRequest, error)
	// ListOpen returns the open requests of a policy: at most one per type.
	ListOpen(ctx context.Context, policyID string) ([]domain.ChangeRequest, error)
	// ListByCustomer returns the page of the requests of an insured that
	// pass the filter, the latest filed first, with the total that pass it,
	// the indicators (whatever the filter) and the policies that have
	// requests, all read in one snapshot.
	ListByCustomer(ctx context.Context, customerID string, f domain.ChangeRequestFilter) (domain.ChangeRequestPage, error)
}
