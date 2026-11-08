package renewal

import (
	"context"
	"log/slog"
	"math"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/rating"
)

// profileFunc resolves the risk profile a renewal is priced with: Resolve
// when the insured renews online (HU-W1.5-02), ResolveFresh when the advisor
// requotes (HU-W1.5-03, decision D-5 b).
type profileFunc func(ctx context.Context, customerID string, consentActive bool) (domain.ProfileResolution, error)

// pricing is the renewed side of the conditions and how the engine priced it.
type pricing struct {
	quote
	// resolution is the profile that priced the renewal; nil when the
	// conditions repeat the current ones.
	resolution *domain.ProfileResolution
	// rules are the factors of the rating engine (rating.Explain) and version
	// its version; empty when the conditions repeat the current ones.
	rules   []domain.AppliedRule
	version string
}

// sameQuote repeats the current conditions of the policy for a new term.
func (s *Service) sameQuote(p domain.Policy) quote {
	return quote{
		terms:    domain.CoverageTerms{InsuredAmount: p.InsuredAmount, TermMonths: s.rules.TermMonths, Coverages: append([]domain.Coverage(nil), p.Coverages...)},
		premium:  p.Premium,
		currency: p.Currency,
	}
}

// price prices the renewal of the policy with the profile of the customer
// (customerID keys the profile) born at birth, resolved by resolve with the
// consent already settled by the caller. The good-experience discount of
// HU-W1.5-02 applies. When the engine cannot (no profile, a rating error) the
// renewal repeats the current conditions.
func (s *Service) price(ctx context.Context, p domain.Policy, customerID string, birth time.Time, resolve profileFunc,
	now time.Time) pricing {
	same := pricing{quote: s.sameQuote(p)}
	res, err := resolve(ctx, customerID, true)
	if err != nil {
		slog.WarnContext(ctx, "the renewal keeps the current conditions: no risk profile", "policy_id", p.ID, "error", err)
		return same
	}
	in := ports.RatingInput{Age: domain.AgeAt(birth, now), InsuredAmount: p.InsuredAmount, TermMonths: s.rules.TermMonths, Profile: res.Profile}
	rated, err := s.rater.Rate(ctx, in)
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
	return pricing{quote: q, resolution: &res, rules: rating.Explain(in), version: rated.Version}
}
