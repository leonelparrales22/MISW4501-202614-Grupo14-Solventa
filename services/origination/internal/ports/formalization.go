package ports

import (
	"context"
	"errors"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

// Errors of the formalization of a purchase (HU-W1.1-03).
var (
	// ErrFormalizationNotFound: the quote was not formalized.
	ErrFormalizationNotFound = errors.New("formalization not found")
	// ErrConsentChallengeNotFound: no code was sent for the quote.
	ErrConsentChallengeNotFound = errors.New("consent challenge not found")
	// ErrConsentCodeLimit: the quote reached the codes it may be sent.
	ErrConsentCodeLimit = errors.New("consent code limit reached")
)

// FormalizationStore keeps the formalizations. There is deliberately no
// Update or Delete: a formalization is a record.
type FormalizationStore interface {
	// Create stores the formalization unless its quote already has one; then
	// it returns the stored one with created=false. Two advisors formalizing
	// the same quote at once get the same winner (CA-6).
	Create(ctx context.Context, f domain.Formalization) (stored domain.Formalization, created bool, err error)
	// GetByQuote returns ErrFormalizationNotFound when the quote has none.
	GetByQuote(ctx context.Context, quoteID string) (domain.Formalization, error)
}

// ConsentChallengeStore keeps the confirmation code sent to the customer.
type ConsentChallengeStore interface {
	// Issue stores the challenge as the code of its quote, replacing the
	// previous one and keeping the counters, and returns it with Sends
	// counted. It returns ErrConsentCodeLimit when the quote already had
	// domain.MaxConsentCodeSends codes.
	Issue(ctx context.Context, c domain.ConsentChallenge) (domain.ConsentChallenge, error)
	// Get returns ErrConsentChallengeNotFound when no code was sent.
	Get(ctx context.Context, quoteID string) (domain.ConsentChallenge, error)
	// RegisterAttempt counts one typed code and returns the attempts so far,
	// atomically, so concurrent guesses are bounded too.
	RegisterAttempt(ctx context.Context, quoteID string) (int, error)
}

// ConsentCodeSender delivers the confirmation code to the customer. A failure
// is returned as is so the advisor can ask for the code again.
type ConsentCodeSender interface {
	SendConsentCode(ctx context.Context, n domain.ConsentCodeNotice) error
}
