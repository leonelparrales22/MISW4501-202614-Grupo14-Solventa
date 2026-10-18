package billing

import (
	"errors"
	"fmt"
	"strings"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

// ErrIncompleteTerms is returned for a personalized offer whose coverage terms
// cannot activate a coverage: nothing is charged with invented values.
var ErrIncompleteTerms = errors.New("the personalized offer has no complete coverage terms")

// OfferTerms returns the TermsFunc of the charge (HA-D02, decision D-16). A
// personalized offer (HU-W1.2-03, id "off-…") activates the terms it was priced
// with: the insured amount, the term and the coverages of the rating engine,
// so the contractual document shows the offer the customer accepted. Any other
// offer (the demo-… and adj-… offers, which carry no terms) activates the given
// demonstration terms. A personalized offer with incomplete terms is an error,
// so its acceptance stays registered and nothing is charged.
func OfferTerms(demo domain.CoverageTerms) TermsFunc {
	return func(offer domain.Offer) (domain.CoverageTerms, error) {
		if !strings.HasPrefix(offer.ID, domain.PersonalizedOfferIDPrefix) {
			return cloneTerms(demo), nil
		}
		if err := validateTerms(offer.Terms); err != nil {
			return domain.CoverageTerms{}, fmt.Errorf("%w: offer %s: %w", ErrIncompleteTerms, offer.ID, err)
		}
		return cloneTerms(offer.Terms), nil
	}
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
