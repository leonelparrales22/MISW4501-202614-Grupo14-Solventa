package billing

import (
	"errors"
	"fmt"
	"strings"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

// ErrIncompleteTerms is returned for a personalized or adjusted offer whose
// coverage terms cannot activate a coverage: nothing is charged with invented
// values.
var ErrIncompleteTerms = errors.New("the offer has no complete coverage terms")

// OfferTerms returns the TermsFunc of the charge (HA-D02, decision D-16). A
// personalized offer (HU-W1.2-03, id "off-…") activates the terms it was priced
// with: the insured amount, the term and the coverages of the rating engine,
// so the contractual document shows the offer the customer accepted. The
// adjusted offer of an advisor decision (HU-W1.1-02, id "adj-…") carries the
// terms of the offer it adjusts (decision D-5 a of the fix C5): it activates
// them when it has them and the demonstration terms when it has none (the
// adjusted offer of a demo offer). Any other offer (the demo-… offers, which
// carry no terms) activates the given demonstration terms. A personalized or
// adjusted offer with incomplete terms is an error, so its acceptance stays
// registered and nothing is charged.
func OfferTerms(demo domain.CoverageTerms) TermsFunc {
	return func(offer domain.Offer) (domain.CoverageTerms, error) {
		switch {
		case strings.HasPrefix(offer.ID, domain.PersonalizedOfferIDPrefix):
		case strings.HasPrefix(offer.ID, domain.AdjustedOfferIDPrefix) && hasTerms(offer.Terms):
		default:
			return cloneTerms(demo), nil
		}
		if err := validateTerms(offer.Terms); err != nil {
			return domain.CoverageTerms{}, fmt.Errorf("%w: offer %s: %w", ErrIncompleteTerms, offer.ID, err)
		}
		return cloneTerms(offer.Terms), nil
	}
}

// hasTerms tells whether an offer carries coverage terms at all.
func hasTerms(t domain.CoverageTerms) bool {
	return t.InsuredAmount != 0 || t.TermMonths != 0 || len(t.Coverages) > 0
}

func validateTerms(t domain.CoverageTerms) error {
	switch {
	case t.InsuredAmount <= 0:
		return errors.New("no insured amount")
	case t.TermMonths <= 0:
		return errors.New("no term")
	case len(t.Coverages) == 0:
		return errors.New("no coverages")
	}
	for _, c := range t.Coverages {
		if c.Code == "" || c.InsuredAmount <= 0 {
			return fmt.Errorf("coverage %q has no code or insured amount", c.Code)
		}
	}
	return nil
}

func cloneTerms(t domain.CoverageTerms) domain.CoverageTerms {
	t.Coverages = append([]domain.Coverage(nil), t.Coverages...)
	return t
}
