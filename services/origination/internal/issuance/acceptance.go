// Package issuance owns the acceptance of the offer and the start of the
// policy issuance (HU-W1.3-01). The acceptance is the no-loss point of the
// journey: it is persisted first, and underwriting and the later steps run
// asynchronously from it, so a failure never makes the customer repeat it.
package issuance

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// OfferExpiredError is returned when the customer accepts an offer whose
// validity is over (CA-4); the customer must request a new offer.
type OfferExpiredError struct {
	OfferID   string
	ExpiresAt time.Time
}

func (e *OfferExpiredError) Error() string {
	return fmt.Sprintf("offer %s expired at %s", e.OfferID, e.ExpiresAt.UTC().Format(time.RFC3339))
}

// AcceptanceService registers acceptances and reads their state.
type AcceptanceService struct {
	offers      ports.OfferRepository
	acceptances ports.AcceptanceRepository
	clock       ports.Clock
	// registered is called after a new acceptance is persisted, to process it
	// right away instead of waiting for the next poll.
	registered func()
}

// NewAcceptanceService builds an AcceptanceService. registered may be nil.
func NewAcceptanceService(offers ports.OfferRepository, acceptances ports.AcceptanceRepository, clock ports.Clock, registered func()) *AcceptanceService {
	if registered == nil {
		registered = func() {}
	}
	return &AcceptanceService{offers: offers, acceptances: acceptances, clock: clock, registered: registered}
}

// Accept registers the caller's acceptance of the offer (CA-1) and returns it
// in AcceptanceRegistered; underwriting runs afterwards. Accepting an offer
// that already has an acceptance returns that acceptance with created=false,
// even if the offer has expired since, so retrying never repeats the process.
// Only the owner of the offer may accept it.
func (s *AcceptanceService) Accept(ctx context.Context, offerID string, channel domain.Channel) (domain.Acceptance, bool, error) {
	return s.accept(ctx, offerID, channel, func(offer domain.Offer) error {
		return security.RequireOwnerOrRole(ctx, offer.CustomerID)
	})
}

// AcceptForCustomer registers the acceptance of the offer on behalf of its
// customer. Only an advisor may, and only when the customer consented to the
// purchase: the formalization of HU-W1.1-03 checks it and records it before
// calling. It is idempotent like Accept.
func (s *AcceptanceService) AcceptForCustomer(ctx context.Context, offerID string) (domain.Acceptance, bool, error) {
	return s.accept(ctx, offerID, domain.ChannelAdvisor, func(domain.Offer) error {
		return security.RequireAnyRole(ctx, security.RoleAdvisor)
	})
}

func (s *AcceptanceService) accept(ctx context.Context, offerID string, channel domain.Channel,
	authorize func(domain.Offer) error) (domain.Acceptance, bool, error) {
	offer, err := s.offers.Get(ctx, offerID)
	if err != nil {
		return domain.Acceptance{}, false, err
	}
	if err := authorize(offer); err != nil {
		return domain.Acceptance{}, false, err
	}

	existing, err := s.acceptances.GetByOffer(ctx, offerID)
	switch {
	case err == nil:
		return existing, false, nil
	case !errors.Is(err, ports.ErrAcceptanceNotFound):
		return domain.Acceptance{}, false, err
	}

	now := s.clock.Now().UTC()
	if offer.IsExpired(now) {
		return domain.Acceptance{}, false, &OfferExpiredError{OfferID: offer.ID, ExpiresAt: offer.ExpiresAt}
	}

	id, err := newAcceptanceID()
	if err != nil {
		return domain.Acceptance{}, false, err
	}
	stored, created, err := s.acceptances.Create(ctx, domain.Acceptance{
		ID:            id,
		OfferID:       offer.ID,
		CustomerID:    offer.CustomerID,
		Channel:       channel,
		AcceptedAt:    now,
		Status:        domain.AcceptanceRegistered,
		NextAttemptAt: now,
		UpdatedAt:     now,
	})
	if err != nil {
		return domain.Acceptance{}, false, err
	}
	if created {
		s.registered()
	}
	return stored, created, nil
}

// Get returns an acceptance to its owner, an advisor or an operations analyst.
func (s *AcceptanceService) Get(ctx context.Context, acceptanceID string) (domain.Acceptance, error) {
	a, err := s.acceptances.Get(ctx, acceptanceID)
	if err != nil {
		return domain.Acceptance{}, err
	}
	return a, authorizeRead(ctx, a)
}

// GetByOffer returns the acceptance of an offer, so a client that only kept
// the offer id can resume the journey (CA-5).
func (s *AcceptanceService) GetByOffer(ctx context.Context, offerID string) (domain.Acceptance, error) {
	a, err := s.acceptances.GetByOffer(ctx, offerID)
	if err != nil {
		return domain.Acceptance{}, err
	}
	return a, authorizeRead(ctx, a)
}

func authorizeRead(ctx context.Context, a domain.Acceptance) error {
	return security.RequireOwnerOrRole(ctx, a.CustomerID, security.RoleAdvisor, security.RoleOperations)
}

// newAcceptanceID returns a random UUID (version 4).
func newAcceptanceID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("could not generate the acceptance id: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
