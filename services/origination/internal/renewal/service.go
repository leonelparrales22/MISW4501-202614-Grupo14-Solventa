// Package renewal lets the insured renew a policy that is about to end from
// the portal (HU-W1.5-02): it shows the renewed conditions before the insured
// accepts, charges the premium and issues the renewed certificate with the
// same flow as any other issuance (HA-D02, HU-W1.3-03), and keeps one outcome
// per period of each policy.
//
// A renewal has no offer or acceptance behind it: the insured's acceptance of
// the conditions is the renewal itself, and its charge is stored with the
// channel "renewal" so the monitor of HU-W1.3-04 lists it like any other
// issuance. Worker closes the windows nobody answered and the payments that
// were not fixed in time.
package renewal

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// ProfileResolver is the profiling use case the renewal re-prices with.
type ProfileResolver interface {
	Resolve(ctx context.Context, customerID string, consentActive bool) (domain.ProfileResolution, error)
}

// ClaimsHistory tells how many claims a policy had in its last year. The
// good-experience discount depends on it. Origination has no claims data, so
// the service uses NoClaims until the claims service feeds it (decision of
// HU-W1.5-02).
type ClaimsHistory interface {
	ClaimsInLastYear(ctx context.Context, policyID string) (int, error)
}

// NoClaims is the ClaimsHistory that reports no claims for every policy.
type NoClaims struct{}

// ClaimsInLastYear always returns 0.
func (NoClaims) ClaimsInLastYear(context.Context, string) (int, error) { return 0, nil }

// PaymentMethodHistory says what payment method the insured paid a policy
// with, so the renewal charges the registered one.
type PaymentMethodHistory interface {
	Get(ctx context.Context, acceptanceID string) (domain.Charge, error)
}

// Service is the use case behind the renewal routes.
type Service struct {
	policies  ports.PolicyRepository
	renewals  ports.RenewalRepository
	charges   ports.ChargeRepository
	customers ports.CustomerBySubject
	profiles  ProfileResolver
	rater     ports.Rater
	claims    ClaimsHistory
	notifier  ports.RenewalNotifier
	rules     domain.RenewalPolicy
	clock     ports.Clock
	wake      func()
}

// Config is what the service needs to be built.
type Config struct {
	Policies  ports.PolicyRepository
	Renewals  ports.RenewalRepository
	Charges   ports.ChargeRepository
	Customers ports.CustomerBySubject
	Profiles  ProfileResolver
	Rater     ports.Rater
	// Claims defaults to NoClaims.
	Claims ClaimsHistory
	// Notifier is optional: without it a lapsed policy is recorded but nobody
	// is told.
	Notifier ports.RenewalNotifier
	Rules    domain.RenewalPolicy
	Clock    ports.Clock
	// Wake, when set, wakes the charge worker after a renewal is stored.
	Wake func()
}

// NewService builds a Service.
func NewService(c Config) (*Service, error) {
	if c.Policies == nil || c.Renewals == nil || c.Charges == nil || c.Rater == nil || c.Clock == nil {
		return nil, errors.New("renewal: policies, renewals, charges, rater and clock are required")
	}
	if err := c.Rules.Validate(); err != nil {
		return nil, fmt.Errorf("renewal: %w", err)
	}
	if c.Claims == nil {
		c.Claims = NoClaims{}
	}
	if c.Wake == nil {
		c.Wake = func() {}
	}
	return &Service{policies: c.Policies, renewals: c.Renewals, charges: c.Charges, customers: c.Customers,
		profiles: c.Profiles, rater: c.Rater, claims: c.Claims, notifier: c.Notifier, rules: c.Rules,
		clock: c.Clock, wake: c.Wake}, nil
}

// Rules returns the renewal rules the service applies.
func (s *Service) Rules() domain.RenewalPolicy { return s.rules }

// PolicyView is a policy with where its renewal stands, as the list shows it.
type PolicyView struct {
	Policy         domain.Policy
	Renewal        *domain.Renewal
	CanRenew       bool
	WindowOpensAt  time.Time
	WindowClosesAt time.Time
}

// ListPolicies returns the policies of the caller with their renewal state.
func (s *Service) ListPolicies(ctx context.Context) ([]PolicyView, error) {
	claims, ok := security.ClaimsFromContext(ctx)
	if !ok {
		return nil, security.ErrUnauthenticated
	}
	policies, err := s.policies.ListByCustomer(ctx, claims.Subject)
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(policies))
	for i, p := range policies {
		ids[i] = p.ID
	}
	renewals, err := s.renewals.ListByPolicies(ctx, ids)
	if err != nil {
		return nil, err
	}
	byPeriod := make(map[string]domain.Renewal, len(renewals))
	for _, r := range renewals {
		byPeriod[r.PolicyID+"|"+r.PeriodEnd.Format(time.RFC3339)] = r
	}
	now := s.clock.Now().UTC()
	out := make([]PolicyView, 0, len(policies))
	for _, p := range policies {
		v := PolicyView{Policy: p, WindowOpensAt: s.rules.WindowOpensAt(p), WindowClosesAt: p.CoverageEnd}
		if r, found := byPeriod[p.ID+"|"+p.CoverageEnd.Format(time.RFC3339)]; found {
			r = s.settle(ctx, r, now)
			v.Renewal = &r
		}
		v.CanRenew = v.Renewal == nil && s.rules.InWindow(p, now)
		out = append(out, v)
	}
	return out, nil
}

// Conditions is what the insured sees before accepting a renewal.
type Conditions struct {
	domain.RenewalConditions
	// PaymentMethod is the method the renewal charges: the one the policy was
	// paid with. The token never leaves the service.
	PaymentMethod *domain.PaymentMethod
}

// Conditions returns the current and the renewed conditions of a policy that
// is inside its renewal window (acceptance criterion 1). Outside the window it
// returns ErrRenewalNotAvailable (criterion 3); once the period has an
// outcome, ErrRenewalClosed.
func (s *Service) Conditions(ctx context.Context, policyID string) (Conditions, error) {
	p, err := s.ownedPolicy(ctx, policyID)
	if err != nil {
		return Conditions{}, err
	}
	now := s.clock.Now().UTC()
	if existing, err := s.renewals.Get(ctx, p.ID, p.CoverageEnd); err == nil {
		return Conditions{}, closedError(existing)
	} else if !errors.Is(err, ports.ErrRenewalNotFound) {
		return Conditions{}, err
	}
	if !s.rules.InWindow(p, now) {
		return Conditions{}, domain.ErrRenewalNotAvailable
	}
	q := s.requote(ctx, p, now)
	return Conditions{RenewalConditions: s.conditionsOf(p, q), PaymentMethod: s.registeredMethod(ctx, p)}, nil
}

// quote is the renewed side of the conditions.
type quote struct {
	terms        domain.CoverageTerms
	premium      float64
	currency     string
	discountRate float64
	requoted     bool
}

func (s *Service) conditionsOf(p domain.Policy, q quote) domain.RenewalConditions {
	start := p.CoverageEnd
	return domain.RenewalConditions{
		Policy: p,
		Current: domain.RenewalTerms{Premium: p.Premium, Currency: p.Currency, InsuredAmount: p.InsuredAmount,
			Coverages: p.Coverages, CoverageStart: p.CoverageStart, CoverageEnd: p.CoverageEnd},
		Renewal: domain.RenewalTerms{Premium: q.premium, Currency: q.currency, InsuredAmount: q.terms.InsuredAmount,
			Coverages: q.terms.Coverages, CoverageStart: start, CoverageEnd: start.AddDate(0, q.terms.TermMonths, 0)},
		WindowOpensAt: s.rules.WindowOpensAt(p), WindowClosesAt: p.CoverageEnd,
		Requoted: q.requoted, DiscountRate: q.discountRate,
	}
}

// requote prices the renewal with the current profile of the insured. When the
// engine cannot (no customer registered under the subject, no profile, a rating
// error) the renewal repeats the current conditions: it is never blocked and
// never priced with invented data.
func (s *Service) requote(ctx context.Context, p domain.Policy, now time.Time) quote {
	same := quote{
		terms:    domain.CoverageTerms{InsuredAmount: p.InsuredAmount, TermMonths: s.rules.TermMonths, Coverages: append([]domain.Coverage(nil), p.Coverages...)},
		premium:  p.Premium,
		currency: p.Currency,
	}
	if s.customers == nil || s.profiles == nil {
		return same
	}
	cust, err := s.customers.GetBySubject(ctx, p.CustomerID)
	if err != nil {
		if !errors.Is(err, ports.ErrCustomerNotFound) {
			slog.WarnContext(ctx, "the renewal keeps the current conditions: customer lookup failed", "policy_id", p.ID, "error", err)
		}
		return same
	}
	// The insured asks for the renewal from their own session: their consent
	// is the request itself, as the advisor's is in an assisted quote.
	res, err := s.profiles.Resolve(ctx, cust.ID, true)
	if err != nil {
		slog.WarnContext(ctx, "the renewal keeps the current conditions: no risk profile", "policy_id", p.ID, "error", err)
		return same
	}
	rated, err := s.rater.Rate(ctx, ports.RatingInput{
		Age: domain.AgeAt(cust.BirthDate, now), InsuredAmount: p.InsuredAmount, TermMonths: s.rules.TermMonths, Profile: res.Profile,
	})
	if err != nil || rated.Premium <= 0 {
		slog.WarnContext(ctx, "the renewal keeps the current conditions: rating failed", "policy_id", p.ID, "error", err)
		return same
	}
	q := quote{terms: same.terms, premium: rated.Premium, currency: rated.Currency, requoted: true}
	if q.currency == "" {
		q.currency = p.Currency
	}
	if len(rated.Coverages) > 0 {
		q.terms.Coverages = append([]domain.Coverage(nil), rated.Coverages...)
	}
	if n, err := s.claims.ClaimsInLastYear(ctx, p.ID); err == nil && n <= domain.MaxNoClaimsYear {
		q.discountRate = domain.DiscountRate
		q.premium = math.Round(q.premium*(1-domain.DiscountRate)/100) * 100
	}
	return q
}

// registeredMethod is the payment method the policy was paid with, without
// its token, or nil when none was captured.
func (s *Service) registeredMethod(ctx context.Context, p domain.Policy) *domain.PaymentMethod {
	orig, err := s.charges.Get(ctx, p.AcceptanceID)
	if err != nil || !orig.PaymentMethod.Captured() {
		return nil
	}
	m := orig.PaymentMethod
	m.Token = ""
	return &m
}

// closedError is what a period with an outcome answers: a lapse means the
// option is no longer available; an accepted or renewed period already has its
// answer.
func closedError(r domain.Renewal) error {
	if r.Status == domain.RenewalNotRenewed {
		return domain.ErrRenewalNotAvailable
	}
	return domain.ErrRenewalClosed
}

func (s *Service) ownedPolicy(ctx context.Context, policyID string) (domain.Policy, error) {
	p, err := s.policies.Get(ctx, policyID)
	if err != nil {
		return domain.Policy{}, err
	}
	// No privileged roles: renewing is the insured's decision and payment.
	if err := security.RequireOwnerOrRole(ctx, p.CustomerID); err != nil {
		return domain.Policy{}, err
	}
	return p, nil
}

// Accepted is the outcome of accepting a renewal.
type Accepted struct {
	Renewal domain.Renewal
	Charge  domain.Charge
	// Created is false when the insured already accepted this period.
	Created bool
}

// Accept registers the acceptance of the renewed conditions and starts the
// charge (criteria 2 and 4). It is idempotent per policy and period: accepting
// twice, or retrying after a crash, finds the same renewal and the same charge.
// The renewal is stored before the charge, and the charge is created from the
// stored renewal, so a retry after a crash in between completes it.
func (s *Service) Accept(ctx context.Context, policyID string) (Accepted, error) {
	p, err := s.ownedPolicy(ctx, policyID)
	if err != nil {
		return Accepted{}, err
	}
	now := s.clock.Now().UTC()
	existing, err := s.renewals.Get(ctx, p.ID, p.CoverageEnd)
	switch {
	case err == nil:
		if existing.Status == domain.RenewalNotRenewed {
			return Accepted{}, closedError(existing)
		}
		return s.finish(ctx, p, existing, false)
	case !errors.Is(err, ports.ErrRenewalNotFound):
		return Accepted{}, err
	}
	if !s.rules.InWindow(p, now) {
		return Accepted{}, domain.ErrRenewalNotAvailable
	}
	q := s.requote(ctx, p, now)
	id := domain.RenewalID(p.ID, p.CoverageEnd)
	r := domain.Renewal{
		ID: id, PolicyID: p.ID, CustomerID: p.CustomerID, PeriodEnd: p.CoverageEnd, Status: domain.RenewalAccepted,
		Terms: q.terms, Premium: q.premium, Currency: q.currency, DiscountRate: q.discountRate, Requoted: q.requoted,
		ChargeID: domain.RenewalChargeID(id), AcceptedAt: now, RetryUntil: now.Add(s.rules.RetryWithin), CreatedAt: now,
	}
	if err := r.Validate(); err != nil {
		return Accepted{}, err
	}
	stored, created, err := s.renewals.Create(ctx, r)
	if err != nil {
		return Accepted{}, err
	}
	if !created && stored.Status == domain.RenewalNotRenewed {
		return Accepted{}, closedError(stored)
	}
	return s.finish(ctx, p, stored, created)
}

// finish makes sure the stored renewal has its charge.
func (s *Service) finish(ctx context.Context, p domain.Policy, r domain.Renewal, created bool) (Accepted, error) {
	now := s.clock.Now().UTC()
	c := domain.NewRenewalCharge(p, r, now)
	if err := c.ValidateActivation(now); err != nil {
		return Accepted{}, fmt.Errorf("the renewal of %s cannot activate its coverage: %w", p.ID, err)
	}
	stored, chargeCreated, err := s.charges.CreatePending(ctx, c)
	if err != nil {
		return Accepted{}, fmt.Errorf("could not store the charge of renewal %s: %w", r.ID, err)
	}
	if chargeCreated {
		// Charge the method the policy was paid with, as the screen promises;
		// without one the insured hands it over (screen E4).
		if orig, err := s.charges.Get(ctx, p.AcceptanceID); err == nil && orig.PaymentMethod.Captured() {
			m := orig.PaymentMethod
			if attached, err := s.charges.AttachPaymentMethod(ctx, r.ChargeID, m, now); err == nil {
				stored = attached
			} else if !errors.Is(err, ports.ErrChargeNotPayable) {
				return Accepted{}, fmt.Errorf("could not attach the payment method of renewal %s: %w", r.ID, err)
			}
		}
		slog.InfoContext(ctx, "policy renewal accepted", "policy_id", p.ID, "renewal_id", r.ID, "charge_id", r.ChargeID)
		s.wake()
	}
	return Accepted{Renewal: r, Charge: stored, Created: created}, nil
}

// Status returns the renewal of the current period of a policy with its
// charge, settling it first so the insured sees the latest state.
func (s *Service) Status(ctx context.Context, policyID string) (domain.Renewal, domain.Charge, error) {
	p, err := s.ownedPolicy(ctx, policyID)
	if err != nil {
		return domain.Renewal{}, domain.Charge{}, err
	}
	r, err := s.renewals.Get(ctx, p.ID, p.CoverageEnd)
	if err != nil {
		return domain.Renewal{}, domain.Charge{}, err
	}
	r = s.settle(ctx, r, s.clock.Now().UTC())
	var c domain.Charge
	if r.ChargeID != "" {
		if c, err = s.charges.Get(ctx, r.ChargeID); err != nil && !errors.Is(err, ports.ErrChargeNotFound) {
			return domain.Renewal{}, domain.Charge{}, err
		}
	}
	return r, c, nil
}

// settle moves an accepted renewal to its outcome when its charge has one:
// confirmed means renewed; a payment that is not through at the deadline
// means not renewed. A charge still being worked is left to finish.
func (s *Service) settle(ctx context.Context, r domain.Renewal, now time.Time) domain.Renewal {
	if r.Status != domain.RenewalAccepted || r.ChargeID == "" {
		return r
	}
	c, err := s.charges.Get(ctx, r.ChargeID)
	if err != nil {
		return r
	}
	switch {
	case c.Status == domain.ChargeConfirmed:
		if changed, err := s.renewals.MarkRenewed(ctx, r.ID, c.PolicyID, now); err == nil && changed {
			r.Status, r.NewPolicyID, r.ClosedAt = domain.RenewalRenewed, c.PolicyID, now
		}
	case !now.Before(r.RetryUntil) && (c.Status == domain.ChargeFailed || (c.Status == domain.ChargePending && c.Attempts == 0)):
		if changed, err := s.renewals.MarkNotRenewed(ctx, r.ID, domain.NotRenewedPaymentFailed, now); err == nil && changed {
			r.Status, r.Reason, r.ClosedAt = domain.RenewalNotRenewed, domain.NotRenewedPaymentFailed, now
		}
	}
	return r
}
