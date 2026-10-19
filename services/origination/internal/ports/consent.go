package ports

import (
	"context"
	"errors"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

// ErrConsentUnverifiable means the consent could not be verified: the audit
// service did not answer in time, answered with an error or its circuit is
// open. The caller must fail closed: no source is queried (HU-W1.2-01 CA-3).
var ErrConsentUnverifiable = errors.New("the customer consent could not be verified")

// ConsentVerifier asks the owner of the consents (the audit service) whether a
// customer has an active explicit consent now (HU-W1.2-01).
type ConsentVerifier interface {
	// Verify returns the status of the customer's consent. A customer without a
	// consent is not an error: Active is false. Any failure to get an answer is
	// ErrConsentUnverifiable.
	Verify(ctx context.Context, customerID string) (domain.ConsentStatus, error)
}
