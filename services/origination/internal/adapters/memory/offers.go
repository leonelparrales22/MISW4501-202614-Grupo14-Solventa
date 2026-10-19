package memory

import (
	"context"
	"regexp"
	"sync"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// Demo scenarios served by OfferStub. An offer id has the form
// demo-<scenario>-<suffix>, e.g. demo-active-001, so a client can try a
// scenario again with a new suffix.
const (
	// ScenarioActive is a valid normal offer that underwriting approves.
	ScenarioActive = "active"
	// ScenarioReview is a valid provisional offer that underwriting routes to
	// a human decision (reason provisional_offer).
	ScenarioReview = "review"
	// ScenarioHighRisk is a valid normal offer with a high risk score (0.9)
	// that underwriting routes to a human decision (reason high_risk_score).
	ScenarioHighRisk = "highrisk"
	// ScenarioExpired is an offer whose validity ended an hour before its
	// first lookup.
	ScenarioExpired = "expired"
	// ScenarioFlaky is a valid normal offer whose underwriting fails the first
	// attempts (UnderwriterStub), to show the resumed journey.
	ScenarioFlaky = "flaky"
)

// OfferValidity is the validity of an offer (HU-W1.2-03: 24 hours).
const OfferValidity = 24 * time.Hour

var demoOfferID = regexp.MustCompile(`^demo-(active|review|highrisk|expired|flaky)-[A-Za-z0-9_-]{1,64}$`)

// OfferStub is an in-memory ports.OfferRepository that serves the demo
// scenarios, and the ports.OfferWriter of the offers an advisor adjusts
// (HU-W1.1-02). PENDING: the offers generated and persisted by HU-W1.2-03.
//
// A demo offer is created on its first lookup and belongs to the
// authenticated caller of that lookup (the subject of its token), with its
// validity counted from that moment. Ids outside the demo form are not found
// unless they were stored with Create.
type OfferStub struct {
	clock  ports.Clock
	mu     sync.Mutex
	offers map[string]domain.Offer
}

// NewOfferStub builds an OfferStub.
func NewOfferStub(clock ports.Clock) *OfferStub {
	return &OfferStub{clock: clock, offers: map[string]domain.Offer{}}
}

// Get returns the demo offer with the given id.
func (s *OfferStub) Get(ctx context.Context, offerID string) (domain.Offer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if o, ok := s.offers[offerID]; ok {
		return o, nil
	}
	m := demoOfferID.FindStringSubmatch(offerID)
	caller, ok := security.ClaimsFromContext(ctx)
	if m == nil || !ok || caller.Subject == "" {
		return domain.Offer{}, ports.ErrOfferNotFound
	}

	now := s.clock.Now().UTC()
	o := domain.Offer{
		ID:         offerID,
		CustomerID: caller.Subject,
		Applicant: domain.Applicant{
			FullName:   orDefault(caller.Username, caller.Email),
			DocumentID: "demo-document",
			Email:      caller.Email,
		},
		Premium:      89500,
		Currency:     "COP",
		Mode:         domain.ModeNormal,
		ProfileScore: 0.72,
		ExpiresAt:    now.Add(OfferValidity),
	}
	switch m[1] {
	case ScenarioReview:
		o.Mode = domain.ModeProvisional
		o.ProfileScore = domain.BaseActuarialProfile(caller.Subject).Score
	case ScenarioHighRisk:
		o.ProfileScore = 0.9
	case ScenarioExpired:
		o.ExpiresAt = now.Add(-time.Hour)
	}
	s.offers[offerID] = o
	return o, nil
}

// Create stores the offer as it is. It is idempotent by id: an offer that
// already exists is kept unchanged.
func (s *OfferStub) Create(_ context.Context, offer domain.Offer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.offers[offer.ID]; !ok {
		s.offers[offer.ID] = offer
	}
	return nil
}

func orDefault(v, d string) string {
	if v != "" {
		return v
	}
	return d
}
