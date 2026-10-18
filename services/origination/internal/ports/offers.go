package ports

import (
	"context"
	"errors"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

// ErrOfferSuperseded is returned by PersonalizedOfferStore.Supersede and Renew
// when the offer was already replaced by another one (another task won the
// race).
var ErrOfferSuperseded = errors.New("offer already superseded")

// PersonalizedOfferStore persists the personalized offers (HU-W1.2-03). An
// offer is immutable once stored: the owner, the replacement and the renewal
// are separate records, and Get joins them. There is deliberately no Update
// or Delete.
type PersonalizedOfferStore interface {
	// Create stores the offer unless its id exists; then it returns the stored
	// one with created=false.
	Create(ctx context.Context, o domain.PersonalizedOffer) (stored domain.PersonalizedOffer, created bool, err error)
	// Get returns ErrOfferNotFound for an unknown id.
	Get(ctx context.Context, offerID string) (domain.PersonalizedOffer, error)
	// Claim sets the owner only if the offer has none, and returns the offer
	// with its effective owner. Two callers racing for the same offer get the
	// same winner.
	Claim(ctx context.Context, offerID, ownerID string) (domain.PersonalizedOffer, error)
	// Supersede stores newOffer (which has Supersedes=oldID) as the replacement
	// of the offer oldID, atomically, and gives it the owner of the old one. An
	// offer has at most one replacement: the second call returns created=false.
	Supersede(ctx context.Context, oldID string, newOffer domain.PersonalizedOffer) (created bool, err error)
	// Renew stores newOffer (which has Renews=oldID) as the renewal of the
	// expired offer oldID, atomically, giving it the owner of the old one. An
	// offer has at most one renewal: the second call returns the existing one
	// with created=false.
	Renew(ctx context.Context, oldID string, newOffer domain.PersonalizedOffer) (stored domain.PersonalizedOffer, created bool, err error)
	// ListRequotable returns up to limit provisional offers still valid at now
	// that nobody replaced, oldest first.
	ListRequotable(ctx context.Context, now time.Time, limit int) ([]domain.PersonalizedOffer, error)
}

// RequoteNotifier tells the customer that a provisional offer was replaced by
// a normal one (HU-W1.2-03 CA-4). Notify is idempotent by the notice key.
type RequoteNotifier interface {
	Notify(ctx context.Context, n domain.RequoteNotice) error
}
