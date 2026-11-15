package ports

import (
	"context"
	"errors"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
)

// ErrAssistanceKeyReused means the subject already used the Idempotency-Key
// for another request for assistance (HU-M2.3-01, assumption S6).
var ErrAssistanceKeyReused = errors.New("claims: the key was used for another request for assistance")

// ErrAssistanceRequestNotFound means there is no request for assistance with
// that id, or it is not the reader's (assumption S9).
var ErrAssistanceRequestNotFound = errors.New("claims: the request for assistance does not exist")

// AssistanceStore keeps the requests for on-site assistance (HU-M2.3-01).
// What was sent is never rewritten (decision D7).
type AssistanceStore interface {
	// Create stores the request and assigns its number. When the subject
	// already sent a request with the same key it returns that one and
	// created=false if the fingerprints match, or ErrAssistanceKeyReused if
	// they do not.
	Create(ctx context.Context, r domain.AssistanceRequest) (stored domain.AssistanceRequest, created bool, err error)
	// Get returns the request with the id, whoever owns it, or
	// ErrAssistanceRequestNotFound.
	Get(ctx context.Context, id string) (domain.AssistanceRequest, error)
}
