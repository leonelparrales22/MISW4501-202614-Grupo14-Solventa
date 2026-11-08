package renewal

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
	"go.opentelemetry.io/otel/trace"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/observability"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/formalization"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/profiling"
)

// The assisted renewal (HU-W1.5-03): the advisor requotes a policy about to
// expire with the rating engine, records how the customer accepted and
// formalizes the renewal on their behalf. The renewal is the one of
// HU-W1.5-02 (one per policy and period, whatever the channel), so it is
// charged and issued exactly as an online renewal: same charge, same worker,
// same certificate.

const assistedInstrumentation = "origination/renewal"

// FreshProfileResolver is the profiling the advisor requotes with: it queries
// the sources first and falls back to the cache (profiling.Resolver).
type FreshProfileResolver interface {
	ResolveFresh(ctx context.Context, customerID string, consentActive bool) (domain.ProfileResolution, error)
}

// ConsentMode tells the screen how the consent to requote a policy is
// obtained (decisions D-8 and D-13 c).
type ConsentMode string

const (
	// ConsentModeAudit: the policy was born in the embedded channel (or its
	// channel is unknown); audit verifies the consent.
	ConsentModeAudit ConsentMode = "audit"
	// ConsentModeAdvisorAffirmation: the policy was born in the assisted
	// channel; the advisor affirms the customer's consent with a checkbox.
	ConsentModeAdvisorAffirmation ConsentMode = "advisor_affirmation"
)

// BlockedReason tells why the advisor cannot renew a policy now.
type BlockedReason string

const (
	// BlockedNone: nothing blocks the renewal.
	BlockedNone BlockedReason = ""
	// BlockedNotAvailable: outside the window, or the period ended as not
	// renewed.
	BlockedNotAvailable BlockedReason = "renewal_not_available"
	// BlockedClosed: the period already has a renewal (accepted or renewed).
	BlockedClosed BlockedReason = "renewal_closed"
	// BlockedPaymentMethodMissing: the policy has no registered payment
	// method to charge (decision D-10 a); the insured can renew online.
	BlockedPaymentMethodMissing BlockedReason = "payment_method_missing"
)

// RenewalChannel tells who renewed a period.
type RenewalChannel string

const (
	// ChannelAdvisor: an advisor formalized the renewal.
	ChannelAdvisor RenewalChannel = "advisor"
	// ChannelInsured: the insured renewed online (HU-W1.5-02).
	ChannelInsured RenewalChannel = "insured"
)

// AssistedConfig is what the AssistedService needs.
type AssistedConfig struct {
	// Renewals is the online renewal of HU-W1.5-02, whose stores, rules,
	// charge and settlement the assisted renewal shares.
	Renewals *Service
	Requotes ports.RenewalRequoteRepository
	Assisted ports.AssistedRenewalRepository
	Origins  OriginFinder
	Profiles FreshProfileResolver
	// Consents verifies the consent of the policies born in the embedded
	// channel. Without it their requote fails closed with
	// ports.ErrConsentUnverifiable, as POST /v1/offers does without audit.
	Consents ports.ConsentVerifier
	// Challenges keeps the confirmation codes of the renewals and Sender
	// e-mails them (decision D-9 a).
	Challenges ports.ConsentChallengeStore
	Sender     ports.ConsentCodeSender
	// NewCode and NewID default to random values.
	NewCode func() (string, error)
	NewID   func() (string, error)
}

// AssistedService is the use case behind the assisted renewal routes. Only an
// advisor uses it.
type AssistedService struct {
	svc        *Service
	requotes   ports.RenewalRequoteRepository
	assisted   ports.AssistedRenewalRepository
	origins    OriginFinder
	profiles   FreshProfileResolver
	consents   ports.ConsentVerifier
	challenges ports.ConsentChallengeStore
	sender     ports.ConsentCodeSender
	newCode    func() (string, error)
	newID      func() (string, error)
	requoted   metric.Int64Counter
}

// NewAssistedService builds an AssistedService.
func NewAssistedService(c AssistedConfig) (*AssistedService, error) {
	if c.Renewals == nil || c.Requotes == nil || c.Assisted == nil || c.Profiles == nil || c.Challenges == nil || c.Sender == nil {
		return nil, errors.New("renewal: the assisted renewal needs the renewals, requotes, profiles, codes and their sender")
	}
	if c.NewCode == nil {
		c.NewCode = formalization.NewConsentCode
	}
	if c.NewID == nil {
		c.NewID = newRequoteID
	}
	counter, _ := observability.Meter(assistedInstrumentation).Int64Counter("assisted_renewal.requotes",
		metric.WithDescription("Requotes of a renewal made by an advisor, by mode and profile source"))
	return &AssistedService{svc: c.Renewals, requotes: c.Requotes, assisted: c.Assisted, origins: c.Origins,
		profiles: c.Profiles, consents: c.Consents, challenges: c.Challenges, sender: c.Sender,
		newCode: c.NewCode, newID: c.NewID, requoted: counter}, nil
}

// newRequoteID returns "rq-" and 128 random bits.
func newRequoteID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("could not generate the requote id: %w", err)
	}
	return domain.RenewalRequoteIDPrefix + hex.EncodeToString(b), nil
}

func advisorClaims(ctx context.Context) (security.Claims, error) {
	if err := security.RequireAnyRole(ctx, security.RoleAdvisor); err != nil {
		return security.Claims{}, err
	}
	claims, ok := security.ClaimsFromContext(ctx)
	if !ok {
		return security.Claims{}, security.ErrUnauthenticated
	}
	return claims, nil
}

// AssistedView is what the advisor's screen shows of a policy (S7, S7b and
// "Ver" of a renewal in progress).
type AssistedView struct {
	Policy         domain.Policy
	WindowOpensAt  time.Time
	WindowClosesAt time.Time
	WindowOpen     bool
	// PaymentMethod is the method the renewal charges, without its token;
	// nil when the policy has none.
	PaymentMethod *domain.PaymentMethod
	// Requote is the last requote of the period, nil when there is none.
	Requote       *domain.RenewalRequote
	RequoteStatus domain.RequoteStatus
	// Renewal is the renewal of the period, settled, nil when there is none.
	Renewal *domain.Renewal
	// Channel says who renewed: an advisor (with Formalization) or the insured.
	Channel       RenewalChannel
	Formalization *domain.RenewalFormalization
	// Charge is the charge of the renewal (with its retry deadline in
	// Renewal.RetryUntil), nil when there is no renewal.
	Charge        *domain.Charge
	ConsentMode   ConsentMode
	CanRequote    bool
	CanFormalize  bool
	BlockedReason BlockedReason
}

// View returns where the renewal of the current period of a policy stands,
// settling it first (as the online renewal does) so the screen shows the
// latest state of its charge (decision 01·D-3 a+).
func (a *AssistedService) View(ctx context.Context, policyID string) (AssistedView, error) {
	if _, err := advisorClaims(ctx); err != nil {
		return AssistedView{}, err
	}
	p, err := a.svc.policies.Get(ctx, policyID)
	if err != nil {
		return AssistedView{}, err
	}
	return a.view(ctx, p)
}

func (a *AssistedService) view(ctx context.Context, p domain.Policy) (AssistedView, error) {
	now := a.svc.clock.Now().UTC()
	rules := a.svc.rules
	v := AssistedView{Policy: p, WindowOpensAt: rules.WindowOpensAt(p), WindowClosesAt: p.CoverageEnd,
		WindowOpen: rules.InWindow(p, now), PaymentMethod: a.svc.registeredMethod(ctx, p), ConsentMode: ConsentModeAudit}

	switch r, err := a.svc.renewals.Get(ctx, p.ID, p.CoverageEnd); {
	case err == nil:
		r = a.svc.settle(ctx, r, now)
		v.Renewal, v.Channel = &r, ChannelInsured
		switch f, err := a.assisted.GetFormalization(ctx, r.ID); {
		case err == nil:
			v.Formalization, v.Channel = &f, ChannelAdvisor
		case !errors.Is(err, ports.ErrRenewalFormalizationNotFound):
			return AssistedView{}, err
		}
		if r.ChargeID != "" {
			switch c, err := a.svc.charges.Get(ctx, r.ChargeID); {
			case err == nil:
				c.PaymentMethod.Token = ""
				v.Charge = &c
			case !errors.Is(err, ports.ErrChargeNotFound):
				return AssistedView{}, err
			}
		}
	case !errors.Is(err, ports.ErrRenewalNotFound):
		return AssistedView{}, err
	}

	switch rq, err := a.requotes.Latest(ctx, p.ID, p.CoverageEnd); {
	case err == nil:
		v.Requote, v.RequoteStatus = &rq, rq.Status(now)
	case !errors.Is(err, ports.ErrRenewalRequoteNotFound):
		return AssistedView{}, err
	}

	origin, err := a.origins.Find(ctx, p)
	if err != nil {
		return AssistedView{}, err
	}
	if origin.Channel == domain.OfferChannelAssisted {
		v.ConsentMode = ConsentModeAdvisorAffirmation
	}

	switch {
	case v.Renewal != nil && v.Renewal.Status == domain.RenewalNotRenewed:
		v.BlockedReason = BlockedNotAvailable
	case v.Renewal != nil:
		v.BlockedReason = BlockedClosed
	case !v.WindowOpen:
		v.BlockedReason = BlockedNotAvailable
	case v.PaymentMethod == nil:
		v.BlockedReason = BlockedPaymentMethodMissing
	}
	v.CanRequote = v.Renewal == nil && v.WindowOpen
	v.CanFormalize = v.BlockedReason == BlockedNone && v.Requote != nil && v.RequoteStatus == domain.RequoteValid
	return v, nil
}

// openPeriod checks that the period of the policy can still be renewed by
// the advisor: inside the window and without a renewal.
func (a *AssistedService) openPeriod(ctx context.Context, p domain.Policy, now time.Time) error {
	existing, err := a.svc.renewals.Get(ctx, p.ID, p.CoverageEnd)
	switch {
	case err == nil:
		return closedError(existing)
	case !errors.Is(err, ports.ErrRenewalNotFound):
		return err
	}
	if !a.svc.rules.InWindow(p, now) {
		return domain.ErrRenewalNotAvailable
	}
	return nil
}

// RequoteInput is what the advisor sends to requote.
type RequoteInput struct {
	// ConsentAffirmed is the advisor's affirmation that the customer
	// authorized the consultation; it counts only for a policy born in the
	// assisted channel (decision D-13 c).
	ConsentAffirmed bool
}

// RequoteResult is a stored requote with the policy it renews.
type RequoteResult struct {
	Policy  domain.Policy
	Requote domain.RenewalRequote
}

// Requote requotes the renewal of the current period of a policy with the
// rating engine (CA-1) and stores it. The consent comes first (decisions D-8
// and D-13 c): nothing is consulted, neither the sources nor the cache, until
// audit answered that the customer's consent is active (a policy born in the
// embedded channel or of unknown channel) or the advisor affirmed it (a
// policy born in the assisted channel). The sources are queried first and the
// cache only backs them (decision D-5 b); a degraded profile is recorded and
// told (CA-5, decision D-7).
func (a *AssistedService) Requote(ctx context.Context, policyID string, in RequoteInput) (RequoteResult, error) {
	ctx, span := observability.Tracer(assistedInstrumentation).Start(ctx, "assisted_renewal.requote",
		trace.WithAttributes(attribute.String("policy_id", policyID)))
	defer span.End()
	claims, err := advisorClaims(ctx)
	if err != nil {
		return RequoteResult{}, err
	}
	p, err := a.svc.policies.Get(ctx, policyID)
	if err != nil {
		return RequoteResult{}, err
	}
	now := a.svc.clock.Now().UTC()
	if err := a.openPeriod(ctx, p, now); err != nil {
		return RequoteResult{}, err
	}
	origin, err := a.origins.Find(ctx, p)
	if err != nil {
		return RequoteResult{}, err
	}

	id, err := a.newID()
	if err != nil {
		return RequoteResult{}, err
	}
	r := domain.RenewalRequote{ID: id, PolicyID: p.ID, PeriodEnd: p.CoverageEnd, AdvisorID: claims.Subject,
		ProfileCustomerID: origin.CustomerID, OriginChannel: origin.Channel, CreatedAt: now, ExpiresAt: domain.RenewalRequoteExpiry(now)}
	if origin.CustomerID != "" {
		if err := a.settleConsent(ctx, origin, in, claims.Subject, now, &r); err != nil {
			return RequoteResult{}, err
		}
	}

	pr := pricing{quote: a.svc.sameQuote(p)}
	if origin.CustomerID != "" && !origin.BirthDate.IsZero() {
		pr = a.svc.price(ctx, p, origin.CustomerID, origin.BirthDate, a.profiles.ResolveFresh, now)
	}
	r.Terms, r.Premium, r.Currency, r.DiscountRate, r.Requoted = pr.terms, pr.premium, pr.currency, pr.discountRate, pr.requoted
	r.RatingVersion, r.AppliedRules = pr.version, pr.rules
	if res := pr.resolution; res != nil {
		r.Mode, r.ProfileSource, r.ProfileNotice = res.Mode, res.Source, domain.ProfileNoticeOf(res.Mode, res.Source)
		r.ProfileScore, r.ProfileCategory = res.Profile.Score, res.Profile.Category
		r.UnavailableSource, r.Sources = res.UnavailableSource, res.Sources
		if res.Source == domain.SourceCache {
			r.ProfileDate = res.Profile.CalculatedAt
		}
	}
	if err := r.Validate(); err != nil {
		return RequoteResult{}, err
	}
	if err := a.requotes.Create(ctx, r); err != nil {
		return RequoteResult{}, err
	}
	a.requoted.Add(ctx, 1, metric.WithAttributes(attribute.String("mode", string(r.Mode)),
		attribute.String("profile_source", string(r.ProfileSource))))
	span.SetAttributes(attribute.String("requote_id", r.ID), attribute.String("mode", string(r.Mode)))
	slog.InfoContext(ctx, "assisted renewal requoted", "policy_id", p.ID, "requote_id", r.ID, "advisor_id", claims.Subject,
		"mode", r.Mode, "profile_source", r.ProfileSource, "requoted", r.Requoted)
	return RequoteResult{Policy: p, Requote: r}, nil
}

// settleConsent records on the requote on what consent the customer's data
// is consulted, or refuses: the advisor's affirmation for a policy born in
// the assisted channel, audit's answer for any other.
func (a *AssistedService) settleConsent(ctx context.Context, origin Origin, in RequoteInput, advisorID string, now time.Time,
	r *domain.RenewalRequote) error {
	if origin.Channel == domain.OfferChannelAssisted {
		// audit keeps only the consents of the embedded channel: the advisor
		// affirms this one, as in an assisted quote.
		if err := domain.RequireConsentAffirmation(in.ConsentAffirmed); err != nil {
			return err
		}
		r.ConsentBasis, r.ConsentAffirmedBy, r.ConsentAffirmedAt = domain.ConsentBasisAdvisorAffirmed, advisorID, now
		return nil
	}
	if a.consents == nil {
		return fmt.Errorf("%w: audit is not configured", ports.ErrConsentUnverifiable)
	}
	status, err := a.consents.Verify(ctx, origin.CustomerID)
	if err != nil {
		if errors.Is(err, ports.ErrConsentUnverifiable) {
			return err
		}
		return fmt.Errorf("%w: %w", ports.ErrConsentUnverifiable, err)
	}
	if !status.Active {
		return profiling.ErrConsentRequired
	}
	r.ConsentBasis, r.ConsentID = domain.ConsentBasisAuditVerified, status.ConsentID
	return nil
}

// usableRequote returns the requote of the policy's current period that the
// advisor cites, if it is the last one and has not expired.
func (a *AssistedService) usableRequote(ctx context.Context, p domain.Policy, requoteID string, now time.Time) (domain.RenewalRequote, error) {
	r, err := a.requotes.Get(ctx, requoteID)
	if err != nil {
		return domain.RenewalRequote{}, err
	}
	if r.PolicyID != p.ID || !r.PeriodEnd.Equal(p.CoverageEnd) {
		return domain.RenewalRequote{}, ports.ErrRenewalRequoteNotFound
	}
	latest, err := a.requotes.Latest(ctx, p.ID, p.CoverageEnd)
	if err != nil {
		return domain.RenewalRequote{}, err
	}
	switch r.StatusAmong(latest.ID, now) {
	case domain.RequoteSuperseded:
		return domain.RenewalRequote{}, domain.ErrRequoteSuperseded
	case domain.RequoteExpired:
		return domain.RenewalRequote{}, domain.ErrRequoteExpired
	}
	return r, nil
}

// ConsentCodeSent says where the code of a requote went.
type ConsentCodeSent struct {
	RequoteID string
	SentTo    string
	ExpiresAt time.Time
}

// RequestConsentCode sends the insured a confirmation code for a requote
// (decision D-9 a): the customer gives it to the advisor to prove they agree.
// The code is valid for domain.ConsentCodeTTL, never beyond the requote, and
// at most domain.MaxConsentCodeSends are sent per requote.
func (a *AssistedService) RequestConsentCode(ctx context.Context, policyID, requoteID string) (ConsentCodeSent, error) {
	ctx, span := observability.Tracer(assistedInstrumentation).Start(ctx, "assisted_renewal.consent_code",
		trace.WithAttributes(attribute.String("policy_id", policyID), attribute.String("requote_id", requoteID)))
	defer span.End()
	claims, err := advisorClaims(ctx)
	if err != nil {
		return ConsentCodeSent{}, err
	}
	p, err := a.svc.policies.Get(ctx, policyID)
	if err != nil {
		return ConsentCodeSent{}, err
	}
	now := a.svc.clock.Now().UTC()
	if err := a.openForTheAdvisor(ctx, p, now); err != nil {
		return ConsentCodeSent{}, err
	}
	r, err := a.usableRequote(ctx, p, requoteID, now)
	if err != nil {
		return ConsentCodeSent{}, err
	}
	if a.svc.registeredMethod(ctx, p) == nil {
		return ConsentCodeSent{}, domain.ErrPaymentMethodMissing
	}

	code, err := a.newCode()
	if err != nil {
		return ConsentCodeSent{}, err
	}
	expires := now.Add(domain.ConsentCodeTTL)
	if expires.After(r.ExpiresAt) {
		expires = r.ExpiresAt
	}
	challenge, err := a.challenges.Issue(ctx, domain.ConsentChallenge{
		QuoteID: r.ID, AdvisorID: claims.Subject, CodeHash: domain.HashConsentCode(r.ID, code), CreatedAt: now, ExpiresAt: expires,
	})
	if err != nil {
		return ConsentCodeSent{}, err
	}
	if err := a.sender.SendConsentCode(ctx, domain.ConsentCodeNotice{
		To: p.Holder.Email, CustomerName: p.Holder.FullName, Code: code, ExpiresAt: expires,
		IdempotencyKey: fmt.Sprintf("renewal-consent-code-%s-%d", r.ID, challenge.Sends),
	}); err != nil {
		if errors.Is(err, formalization.ErrCodeDeliveryUnavailable) {
			return ConsentCodeSent{}, err
		}
		return ConsentCodeSent{}, fmt.Errorf("%w: %w", formalization.ErrCodeDeliveryUnavailable, err)
	}
	slog.InfoContext(ctx, "assisted renewal consent code sent", "policy_id", p.ID, "requote_id", r.ID,
		"advisor_id", claims.Subject, "sends", challenge.Sends)
	return ConsentCodeSent{RequoteID: r.ID, SentTo: formalization.MaskEmail(p.Holder.Email), ExpiresAt: expires}, nil
}

// openForTheAdvisor is openPeriod, telling apart a period an advisor already
// formalized (ErrAssistedRenewalTaken) from one the insured renewed.
func (a *AssistedService) openForTheAdvisor(ctx context.Context, p domain.Policy, now time.Time) error {
	err := a.openPeriod(ctx, p, now)
	if !errors.Is(err, domain.ErrRenewalClosed) {
		return err
	}
	existing, gerr := a.svc.renewals.Get(ctx, p.ID, p.CoverageEnd)
	if gerr != nil {
		return gerr
	}
	switch _, ferr := a.assisted.GetFormalization(ctx, existing.ID); {
	case ferr == nil:
		return domain.ErrAssistedRenewalTaken
	case errors.Is(ferr, ports.ErrRenewalFormalizationNotFound):
		return err
	default:
		return ferr
	}
}

// FormalizeInput is what the advisor gives to formalize: the requote the
// customer accepted, how and when they accepted (CA-2) and, with the
// confirmation code method, the code the customer received.
type FormalizeInput struct {
	RequoteID  string
	Acceptance domain.ConsentAcceptance
	Code       string
}

// Formalize formalizes the renewal of the current period of a policy on
// behalf of the customer (CA-3, CA-4): it records the renewal with the
// advisor, the date, the acceptance and the requote applied, then charges it
// with the registered payment method and issues it through the flow of
// HU-W1.5-02. Repeating it by the advisor who formalized returns the same
// renewal whatever the input (created=false); another advisor gets
// domain.ErrAssistedRenewalTaken and, after the insured renewed online,
// domain.ErrRenewalClosed.
func (a *AssistedService) Formalize(ctx context.Context, policyID string, in FormalizeInput) (AssistedView, bool, error) {
	ctx, span := observability.Tracer(assistedInstrumentation).Start(ctx, "assisted_renewal.formalize",
		trace.WithAttributes(attribute.String("policy_id", policyID), attribute.String("requote_id", in.RequoteID)))
	defer span.End()
	claims, err := advisorClaims(ctx)
	if err != nil {
		return AssistedView{}, false, err
	}
	p, err := a.svc.policies.Get(ctx, policyID)
	if err != nil {
		return AssistedView{}, false, err
	}
	now := a.svc.clock.Now().UTC()

	switch existing, err := a.svc.renewals.Get(ctx, p.ID, p.CoverageEnd); {
	case err == nil:
		return a.resume(ctx, p, existing, claims.Subject)
	case !errors.Is(err, ports.ErrRenewalNotFound):
		return AssistedView{}, false, err
	}
	if !a.svc.rules.InWindow(p, now) {
		return AssistedView{}, false, domain.ErrRenewalNotAvailable
	}

	acceptance := in.Acceptance
	acceptance.Reference = strings.TrimSpace(acceptance.Reference)
	if strings.TrimSpace(in.RequoteID) == "" {
		return AssistedView{}, false, domain.ValidateRenewalAcceptance(in.RequoteID, acceptance, now, time.Time{})
	}
	r, err := a.usableRequote(ctx, p, in.RequoteID, now)
	if err != nil {
		return AssistedView{}, false, err
	}
	if err := domain.ValidateRenewalAcceptance(r.ID, acceptance, now, r.CreatedAt); err != nil {
		return AssistedView{}, false, err
	}
	if a.svc.registeredMethod(ctx, p) == nil {
		return AssistedView{}, false, domain.ErrPaymentMethodMissing
	}
	if acceptance.Method == domain.ConsentConfirmationCode {
		if err := formalization.VerifyConsentCode(ctx, a.challenges, r.ID, in.Code, now); err != nil {
			return AssistedView{}, false, err
		}
	}

	id := domain.RenewalID(p.ID, p.CoverageEnd)
	renewal := domain.Renewal{
		ID: id, PolicyID: p.ID, CustomerID: p.CustomerID, PeriodEnd: p.CoverageEnd, Status: domain.RenewalAccepted,
		Terms: r.Terms, Premium: r.Premium, Currency: r.Currency, DiscountRate: r.DiscountRate, Requoted: r.Requoted,
		ChargeID: domain.RenewalChargeID(id), AcceptedAt: now, RetryUntil: now.Add(a.svc.rules.RetryWithin), CreatedAt: now,
	}
	if err := renewal.Validate(); err != nil {
		return AssistedView{}, false, err
	}
	record := domain.RenewalFormalization{RenewalID: id, RequoteID: r.ID, AdvisorID: claims.Subject, AdvisorEmail: claims.Email,
		AdvisorName: strings.TrimSpace(claims.Name), Acceptance: acceptance, FormalizedAt: now}
	stored, existing, created, err := a.assisted.CreateAssisted(ctx, renewal, record)
	if err != nil {
		return AssistedView{}, false, err
	}
	if !created {
		// Someone got there between the check and here.
		return a.resumeWith(ctx, p, stored, existing, claims.Subject)
	}
	if _, err := a.svc.finish(ctx, p, stored, true); err != nil {
		return AssistedView{}, false, err
	}
	slog.InfoContext(ctx, "assisted renewal formalized", "policy_id", p.ID, "renewal_id", stored.ID, "requote_id", r.ID,
		"advisor_id", claims.Subject, "method", acceptance.Method)
	v, err := a.view(ctx, p)
	return v, true, err
}

// resume answers a formalization of a period that already has a renewal.
func (a *AssistedService) resume(ctx context.Context, p domain.Policy, r domain.Renewal, advisorID string) (AssistedView, bool, error) {
	switch f, err := a.assisted.GetFormalization(ctx, r.ID); {
	case err == nil:
		return a.resumeWith(ctx, p, r, &f, advisorID)
	case errors.Is(err, ports.ErrRenewalFormalizationNotFound):
		return a.resumeWith(ctx, p, r, nil, advisorID)
	default:
		return AssistedView{}, false, err
	}
}

// resumeWith gives the advisor who formalized the period their renewal back,
// completing its charge if the process stopped before; anyone else is refused.
func (a *AssistedService) resumeWith(ctx context.Context, p domain.Policy, r domain.Renewal, f *domain.RenewalFormalization,
	advisorID string) (AssistedView, bool, error) {
	switch {
	case f == nil:
		return AssistedView{}, false, closedError(r)
	case f.AdvisorID != advisorID:
		return AssistedView{}, false, domain.ErrAssistedRenewalTaken
	}
	if r.Status == domain.RenewalAccepted {
		if _, err := a.svc.finish(ctx, p, r, false); err != nil {
			return AssistedView{}, false, err
		}
	}
	v, err := a.view(ctx, p)
	return v, false, err
}
