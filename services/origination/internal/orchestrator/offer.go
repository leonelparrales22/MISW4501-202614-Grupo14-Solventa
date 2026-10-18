package orchestrator

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/observability"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/rating"
)

// Errors of the personalized offer use cases (HU-W1.2-03).
var (
	// ErrConsentMismatch means the consent the partner named is not the active
	// consent of the customer.
	ErrConsentMismatch = errors.New("the consent does not belong to the customer")
	// ErrConsentUnverifiable means the consent could not be verified with the
	// audit service; no source was queried (HU-W1.2-01, fail-closed).
	ErrConsentUnverifiable = ports.ErrConsentUnverifiable
)

// OfferStillValidError is returned when a renewal is requested while the offer
// can still be accepted.
type OfferStillValidError struct {
	OfferID   string
	ExpiresAt time.Time
}

func (e *OfferStillValidError) Error() string {
	return fmt.Sprintf("offer %s is valid until %s", e.OfferID, e.ExpiresAt.UTC().Format(time.RFC3339))
}

// OfferSupersededError is returned when a renewal is requested for an offer a
// requote already replaced: the customer must use the newer one.
type OfferSupersededError struct {
	OfferID      string
	SupersededBy string
}

func (e *OfferSupersededError) Error() string {
	return fmt.Sprintf("offer %s was replaced by %s", e.OfferID, e.SupersededBy)
}

// OfferKind tells which story produced an offer read through GET /v1/offers.
type OfferKind string

const (
	OfferKindPersonalized OfferKind = "personalized"
	// OfferKindAdjusted is the offer of an advisor adjust decision (HU-W1.1-02).
	OfferKindAdjusted OfferKind = "adjusted"
	// OfferKindDemo is a demonstration offer (HU-W1.3-01).
	OfferKindDemo OfferKind = "demo"
)

// OfferView is what a reader gets for an offer id. A personalized offer comes
// with all its data; an adjusted or demo offer only has the reduced view that
// the other stories' offers can give (premium, currency, mode and validity).
type OfferView struct {
	Kind         OfferKind
	Personalized *domain.PersonalizedOffer
	// Reduced is set when Personalized is nil.
	Reduced   domain.Offer
	Status    domain.OfferStatus
	CanAccept bool
	CanRenew  bool
}

// OfferDeps are the collaborators of the OfferService.
type OfferDeps struct {
	Store ports.PersonalizedOfferStore
	// Offers serves every offer id, including the adjusted and demo offers of
	// other stories (the OfferRouter).
	Offers ports.OfferRepository
	// Consents verifies the consent with the audit service. Without it nothing
	// can be offered: an unverifiable consent fails closed.
	Consents ports.ConsentVerifier
	Profiles ProfileResolver
	Rater    ports.Rater
	Clock    ports.Clock
}

// OfferService is the use case of HU-W1.2-03: it generates the personalized
// offer of the embedded journey, serves it to its owner, and renews it once
// its validity is over.
type OfferService struct {
	d      OfferDeps
	pricer offerPricer

	generated metric.Int64Counter
	renewed   metric.Int64Counter
}

// NewOfferService builds the service.
func NewOfferService(d OfferDeps) *OfferService {
	m := observability.Meter(instrumentation)
	generated, _ := m.Int64Counter("offers.generated",
		metric.WithDescription("Personalized offers generated, by profile mode and source"), metric.WithUnit("{offer}"))
	renewed, _ := m.Int64Counter("offers.renewed",
		metric.WithDescription("Expired personalized offers renewed"), metric.WithUnit("{offer}"))
	return &OfferService{d: d, pricer: offerPricer{profiles: d.Profiles, rater: d.Rater, newID: newOfferID}, generated: generated, renewed: renewed}
}

// Generate validates the request, verifies the consent with audit, resolves the
// risk profile, rates it and persists the offer with a validity of 24 hours
// (CA-1, CA-2). When the sources are down it still answers, in provisional
// mode, and says why (CA-3). A *domain.ValidationError names the invalid
// fields; ErrConsentRequired, ErrConsentMismatch and ErrConsentUnverifiable
// mean no source was queried.
func (s *OfferService) Generate(ctx context.Context, req domain.EmbeddedOfferRequest) (domain.PersonalizedOffer, error) {
	ctx, span := observability.Tracer(instrumentation).Start(ctx, "offer.generate")
	defer span.End()

	now := s.d.Clock.Now().UTC()
	holder, birth, err := req.Validate(now)
	if err != nil {
		return domain.PersonalizedOffer{}, err
	}
	status, err := s.verifyConsent(ctx, req.CustomerID)
	if err != nil {
		return domain.PersonalizedOffer{}, err
	}
	if status.ConsentID != req.ConsentID {
		return domain.PersonalizedOffer{}, ErrConsentMismatch
	}

	offer, err := s.pricer.build(ctx, domain.PersonalizedOffer{
		CustomerID: req.CustomerID, Channel: domain.OfferChannelEmbedded, ConsentID: status.ConsentID,
		PartnerID: req.PartnerID, ApplicationID: req.ApplicationID,
		Applicant: holder, BirthDate: birth,
		Terms: domain.CoverageTerms{InsuredAmount: req.InsuredAmount, TermMonths: req.TermMonths},
	}, now)
	if err != nil {
		return domain.PersonalizedOffer{}, fail(span, "could not price the offer", err)
	}
	stored, _, err := s.d.Store.Create(ctx, offer)
	if err != nil {
		return domain.PersonalizedOffer{}, fail(span, "could not save the offer", err)
	}

	span.SetAttributes(attribute.String("offer.mode", string(stored.Mode)), attribute.String("offer.profile_source", string(stored.ProfileSource)))
	s.generated.Add(ctx, 1, metric.WithAttributes(
		attribute.String("mode", string(stored.Mode)), attribute.String("profile_source", string(stored.ProfileSource))))
	slog.InfoContext(ctx, "personalized offer generated", "offer_id", stored.ID, "customer_id", stored.CustomerID,
		"partner_id", stored.PartnerID, "mode", stored.Mode, "profile_source", stored.ProfileSource,
		"provisional_reason", stored.ProvisionalReason, "premium", stored.Premium, "rater_version", stored.RaterVersion)
	return stored, nil
}

// Get returns an offer to its owner, an advisor or an operations analyst. The
// first insured who opens a personalized offer with a session claims it (D4);
// from then on only they (and the staff) can read it, so someone who learned
// the id cannot see or accept it. The adjusted and demo offers of other
// stories are served with a reduced view and are never claimed.
func (s *OfferService) Get(ctx context.Context, offerID string) (OfferView, error) {
	claims, ok := security.ClaimsFromContext(ctx)
	if !ok {
		return OfferView{}, security.ErrUnauthenticated
	}
	now := s.d.Clock.Now().UTC()

	offer, err := s.d.Store.Get(ctx, offerID)
	if errors.Is(err, ports.ErrOfferNotFound) {
		return s.reducedView(ctx, offerID, now)
	}
	if err != nil {
		return OfferView{}, err
	}
	if !claims.HasAnyRole(security.RoleAdvisor, security.RoleOperations) {
		if offer.OwnerID == "" {
			if offer, err = s.d.Store.Claim(ctx, offerID, claims.Subject); err != nil {
				return OfferView{}, err
			}
		}
		if err := security.RequireOwnerOrRole(ctx, offer.OwnerID); err != nil {
			return OfferView{}, err
		}
	}
	return personalizedView(offer, now), nil
}

// Renew creates a new offer from an expired one, with the same credit data
// and the age recalculated, once the customer asks for it (CA-5). Only the
// owner can renew. It is idempotent: renewing again returns the same renewal
// with created=false. An offer that is still valid answers
// *OfferStillValidError and one a requote replaced *OfferSupersededError.
func (s *OfferService) Renew(ctx context.Context, offerID string) (domain.PersonalizedOffer, bool, error) {
	ctx, span := observability.Tracer(instrumentation).Start(ctx, "offer.renew")
	defer span.End()

	old, err := s.d.Store.Get(ctx, offerID)
	if err != nil {
		return domain.PersonalizedOffer{}, false, err
	}
	if err := security.RequireOwnerOrRole(ctx, old.OwnerID); err != nil {
		return domain.PersonalizedOffer{}, false, err
	}
	now := s.d.Clock.Now().UTC()
	switch {
	case old.SupersededBy != "":
		return domain.PersonalizedOffer{}, false, &OfferSupersededError{OfferID: old.ID, SupersededBy: old.SupersededBy}
	case old.RenewedBy != "":
		existing, err := s.d.Store.Get(ctx, old.RenewedBy)
		return existing, false, err
	case old.Status(now) == domain.OfferActive:
		return domain.PersonalizedOffer{}, false, &OfferStillValidError{OfferID: old.ID, ExpiresAt: old.ExpiresAt}
	}

	status, err := s.verifyConsent(ctx, old.CustomerID)
	if err != nil {
		return domain.PersonalizedOffer{}, false, err
	}
	if err := domain.ValidateBirthDate(old.BirthDate, now); err != nil {
		return domain.PersonalizedOffer{}, false, err
	}
	offer, err := s.pricer.build(ctx, domain.PersonalizedOffer{
		CustomerID: old.CustomerID, Channel: old.Channel, QuoteID: old.QuoteID, ConsentID: status.ConsentID,
		PartnerID: old.PartnerID, ApplicationID: old.ApplicationID,
		Applicant: old.Applicant, BirthDate: old.BirthDate,
		Terms: domain.CoverageTerms{InsuredAmount: old.Terms.InsuredAmount, TermMonths: old.Terms.TermMonths},
	}, now)
	if err != nil {
		return domain.PersonalizedOffer{}, false, fail(span, "could not price the renewal", err)
	}
	stored, created, err := s.d.Store.Renew(ctx, old.ID, offer)
	if err != nil {
		return domain.PersonalizedOffer{}, false, fail(span, "could not save the renewal", err)
	}
	if created {
		s.renewed.Add(ctx, 1, metric.WithAttributes(attribute.String("mode", string(stored.Mode))))
		slog.InfoContext(ctx, "personalized offer renewed", "offer_id", old.ID, "renewal_id", stored.ID, "mode", stored.Mode)
	}
	return stored, created, nil
}

// Now returns the service clock, so callers compute a status against the same
// time source.
func (s *OfferService) Now() time.Time { return s.d.Clock.Now().UTC() }

// verifyConsent asks audit for the active consent of the customer. A customer
// without one is ErrConsentRequired; no answer is ErrConsentUnverifiable.
func (s *OfferService) verifyConsent(ctx context.Context, customerID string) (domain.ConsentStatus, error) {
	return verifyConsent(ctx, s.d.Consents, customerID)
}

func verifyConsent(ctx context.Context, verifier ports.ConsentVerifier, customerID string) (domain.ConsentStatus, error) {
	if verifier == nil {
		return domain.ConsentStatus{}, ErrConsentUnverifiable
	}
	status, err := verifier.Verify(ctx, customerID)
	if err != nil {
		return domain.ConsentStatus{}, fmt.Errorf("verify the consent: %w", err)
	}
	if !status.Active {
		return domain.ConsentStatus{}, ErrConsentRequired
	}
	return status, nil
}

// reducedView serves an offer of another story (adjusted or demo) through the
// shared offer repository.
func (s *OfferService) reducedView(ctx context.Context, offerID string, now time.Time) (OfferView, error) {
	offer, err := s.d.Offers.Get(ctx, offerID)
	if err != nil {
		return OfferView{}, err
	}
	if err := security.RequireOwnerOrRole(ctx, offer.CustomerID, security.RoleAdvisor, security.RoleOperations); err != nil {
		return OfferView{}, err
	}
	kind := OfferKindDemo
	if strings.HasPrefix(offerID, domain.AdjustedOfferIDPrefix) {
		kind = OfferKindAdjusted
	}
	status := domain.OfferActive
	if offer.IsExpired(now) {
		status = domain.OfferExpired
	}
	return OfferView{Kind: kind, Reduced: offer, Status: status, CanAccept: status == domain.OfferActive}, nil
}

func personalizedView(o domain.PersonalizedOffer, now time.Time) OfferView {
	return OfferView{
		Kind: OfferKindPersonalized, Personalized: &o, Status: o.Status(now),
		CanAccept: o.CanAccept(now), CanRenew: o.CanRenew(now),
	}
}

// offerPricer resolves the profile of a customer and prices the offer: the
// steps that generating, renewing and requoting have in common.
type offerPricer struct {
	profiles ProfileResolver
	rater    ports.Rater
	newID    func() (string, error)
}

// build completes the template (customer, credit and holder data) with a new
// id, the resolved profile, the price, the rules and the conditions. The
// consent must have been verified by the caller.
func (p offerPricer) build(ctx context.Context, t domain.PersonalizedOffer, now time.Time) (domain.PersonalizedOffer, error) {
	// The consent was verified just before, so the resolver is told it is active.
	res, err := p.profiles.Resolve(ctx, t.CustomerID, true)
	if err != nil {
		if errors.Is(err, ErrConsentRequired) {
			return domain.PersonalizedOffer{}, ErrConsentRequired
		}
		return domain.PersonalizedOffer{}, fmt.Errorf("resolve the risk profile: %w", err)
	}
	age := domain.AgeAt(t.BirthDate, now)
	in := ports.RatingInput{Age: age, InsuredAmount: t.Terms.InsuredAmount, TermMonths: t.Terms.TermMonths, Profile: res.Profile}
	r, err := p.rater.Rate(ctx, in)
	if err != nil {
		return domain.PersonalizedOffer{}, fmt.Errorf("rate the offer: %w", err)
	}
	id, err := p.newID()
	if err != nil {
		return domain.PersonalizedOffer{}, err
	}

	t.ID, t.AgeAtQuote = id, age
	t.Terms.Coverages = r.Coverages
	t.Premium, t.Currency = r.Premium, r.Currency
	t.Conditions = rating.Conditions(res.Mode)
	t.Mode, t.ProfileSource, t.UnavailableSource = res.Mode, res.Source, res.UnavailableSource
	t.ProvisionalReason, t.ProfileDate = "", time.Time{}
	if res.Mode == domain.ModeProvisional {
		t.ProvisionalReason = domain.ProvisionalBaseProfile
		if res.Source == domain.SourceCache {
			t.ProvisionalReason, t.ProfileDate = domain.ProvisionalCachedProfile, res.Profile.CalculatedAt.UTC()
		}
	}
	t.Profile = domain.ProfileSnapshot{
		Score: res.Profile.Score, Category: res.Profile.Category, CalculatedAt: res.Profile.CalculatedAt.UTC(),
		Partial: res.Profile.Partial, Sources: res.Sources,
	}
	t.Rules, t.RaterVersion = rating.Explain(in), r.Version
	t.CreatedAt, t.ExpiresAt = now, now.Add(domain.OfferValidity)
	return t, nil
}

// newOfferID returns "off-" and 128 random bits in hex: unguessable, because
// the id is the only thing the partner's redirect carries.
func newOfferID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("could not generate the offer id: %w", err)
	}
	return domain.PersonalizedOfferIDPrefix + hex.EncodeToString(b[:]), nil
}
