// Package ports declares what the claims use cases need from the outside.
// The adapters of internal/adapters implement them.
package ports

import (
	"context"
	"errors"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
)

// ErrFilingKeyReused is returned when the same filing key arrives with a
// different filing.
var ErrFilingKeyReused = errors.New("claims: the filing key was used for another filing")

// ClaimRepository stores the filed cases.
type ClaimRepository interface {
	// Create stores the claim and assigns its filing number. When the subject
	// already filed with the same key it returns that case and created=false,
	// or ErrFilingKeyReused when the filing is a different one.
	Create(ctx context.Context, c domain.Claim) (stored domain.Claim, created bool, err error)
}

// Clock is the time the service reads, so the tests can fix it.
type Clock interface {
	Now() time.Time
}
