package memory

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// PersonalizedOfferStore is the in-memory ports.PersonalizedOfferStore the
// service uses without DATABASE_URL (HU-W1.2-03). Like the PostgreSQL one, it
// never changes a stored offer: the owner, the replacement and the renewal are
// kept apart and joined when the offer is read.
type PersonalizedOfferStore struct {
	mu     sync.Mutex
	offers map[string]domain.PersonalizedOffer
	owners map[string]string
	// replacedBy and renewedBy map an offer id to the offer that replaced or
	// renewed it.
	replacedBy map[string]string
	renewedBy  map[string]string
}

var _ ports.PersonalizedOfferStore = (*PersonalizedOfferStore)(nil)

// NewPersonalizedOfferStore builds an empty store.
func NewPersonalizedOfferStore() *PersonalizedOfferStore {
	return &PersonalizedOfferStore{
		offers: map[string]domain.PersonalizedOffer{}, owners: map[string]string{},
		replacedBy: map[string]string{}, renewedBy: map[string]string{},
	}
}

// Create stores the offer unless its id exists.
func (s *PersonalizedOfferStore) Create(_ context.Context, o domain.PersonalizedOffer) (domain.PersonalizedOffer, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.offers[o.ID]; ok {
		return s.joinLocked(existing), false, nil
	}
	s.insertLocked(o)
	return s.joinLocked(s.offers[o.ID]), true, nil
}

// Get returns the offer with its owner, replacement and renewal.
func (s *PersonalizedOfferStore) Get(_ context.Context, offerID string) (domain.PersonalizedOffer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.offers[offerID]
	if !ok {
		return domain.PersonalizedOffer{}, ports.ErrOfferNotFound
	}
	return s.joinLocked(o), nil
}

// Claim sets the owner of the offer if it has none.
func (s *PersonalizedOfferStore) Claim(_ context.Context, offerID, ownerID string) (domain.PersonalizedOffer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.offers[offerID]
	if !ok {
		return domain.PersonalizedOffer{}, ports.ErrOfferNotFound
	}
	if _, claimed := s.owners[offerID]; !claimed && ownerID != "" {
		s.owners[offerID] = ownerID
	}
	return s.joinLocked(o), nil
}

// Supersede stores the replacement of oldID unless it already has one.
func (s *PersonalizedOfferStore) Supersede(_ context.Context, oldID string, newOffer domain.PersonalizedOffer) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.offers[oldID]; !ok {
		return false, ports.ErrOfferNotFound
	}
	if _, replaced := s.replacedBy[oldID]; replaced {
		return false, nil
	}
	newOffer.Supersedes = oldID
	s.insertLocked(newOffer)
	s.replacedBy[oldID] = newOffer.ID
	s.inheritOwnerLocked(oldID, newOffer.ID)
	return true, nil
}

// Renew stores the renewal of oldID unless it already has one.
func (s *PersonalizedOfferStore) Renew(_ context.Context, oldID string, newOffer domain.PersonalizedOffer) (domain.PersonalizedOffer, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.offers[oldID]; !ok {
		return domain.PersonalizedOffer{}, false, ports.ErrOfferNotFound
	}
	if renewal, renewed := s.renewedBy[oldID]; renewed {
		return s.joinLocked(s.offers[renewal]), false, nil
	}
	newOffer.Renews = oldID
	s.insertLocked(newOffer)
	s.renewedBy[oldID] = newOffer.ID
	s.inheritOwnerLocked(oldID, newOffer.ID)
	return s.joinLocked(s.offers[newOffer.ID]), true, nil
}

// ListRequotable returns the valid provisional offers nobody replaced.
func (s *PersonalizedOfferStore) ListRequotable(_ context.Context, now time.Time, limit int) ([]domain.PersonalizedOffer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.PersonalizedOffer
	for id, o := range s.offers {
		if _, replaced := s.replacedBy[id]; replaced || o.Mode != domain.ModeProvisional || !now.Before(o.ExpiresAt) {
			continue
		}
		out = append(out, s.joinLocked(o))
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *PersonalizedOfferStore) insertLocked(o domain.PersonalizedOffer) {
	// The derived fields are never stored with the offer.
	o.OwnerID, o.SupersededBy, o.SupersededAt, o.RenewedBy = "", "", time.Time{}, ""
	s.offers[o.ID] = o
}

func (s *PersonalizedOfferStore) inheritOwnerLocked(oldID, newID string) {
	if owner, ok := s.owners[oldID]; ok {
		s.owners[newID] = owner
	}
}

// joinLocked adds the derived fields to a copy of the stored offer.
func (s *PersonalizedOfferStore) joinLocked(o domain.PersonalizedOffer) domain.PersonalizedOffer {
	o.OwnerID = s.owners[o.ID]
	if next, ok := s.replacedBy[o.ID]; ok {
		o.SupersededBy, o.SupersededAt = next, s.offers[next].CreatedAt
	}
	o.RenewedBy = s.renewedBy[o.ID]
	return o
}

// OfferRouter is the ports.OfferRepository and ports.OfferWriter that the
// acceptance, the advisor inbox and the charge share (HU-W1.2-03). The ids of
// the demo (demo-*) and adjusted (adj-*) offers of other stories go to the
// OfferStub; any other id is a personalized offer. A personalized offer is
// read through AsOffer, so the flows that consume it do not know the
// difference.
type OfferRouter struct {
	stub     *OfferStub
	personal ports.PersonalizedOfferStore
}

var (
	_ ports.OfferRepository = (*OfferRouter)(nil)
	_ ports.OfferWriter     = (*OfferRouter)(nil)
)

// NewOfferRouter builds an OfferRouter.
func NewOfferRouter(stub *OfferStub, personal ports.PersonalizedOfferStore) *OfferRouter {
	return &OfferRouter{stub: stub, personal: personal}
}

// IsStubOffer tells whether the id belongs to the offers of the OfferStub.
func IsStubOffer(offerID string) bool {
	return strings.HasPrefix(offerID, domain.DemoOfferIDPrefix) || strings.HasPrefix(offerID, domain.AdjustedOfferIDPrefix)
}

// Get returns the offer with the given id.
func (r *OfferRouter) Get(ctx context.Context, offerID string) (domain.Offer, error) {
	if IsStubOffer(offerID) {
		return r.stub.Get(ctx, offerID)
	}
	o, err := r.personal.Get(ctx, offerID)
	if err != nil {
		return domain.Offer{}, err
	}
	return o.AsOffer(), nil
}

// Create stores the adjusted offer of an advisor decision (HU-W1.1-02) in the
// OfferStub, as before.
func (r *OfferRouter) Create(ctx context.Context, offer domain.Offer) error {
	return r.stub.Create(ctx, offer)
}
