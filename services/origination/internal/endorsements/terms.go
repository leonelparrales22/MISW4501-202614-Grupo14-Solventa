package endorsements

import (
	"context"
	"fmt"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/renewal"
)

// Origins follows a policy back to the offer it was bought from;
// renewal.OriginFinder implements it.
type Origins interface {
	Find(ctx context.Context, p domain.Policy) (renewal.Origin, error)
}

// Terms builds the terms of a policy the insured can change, as they stand
// now. It is the only place that builds them (S-7): HU-W1.4-02 lays the
// applied endorsements over them here, so every reader sees the same terms.
// It checks no role (adjustment A-3): the caller does, so the analyst of
// HU-W1.4-02 can reuse it.
type Terms struct {
	origins Origins
	// partnerNames are the display names of the partners, by code
	// (PARTNER_DISPLAY_NAMES, decision D-14 a); it may be empty.
	partnerNames map[string]string
}

// NewTerms builds a Terms. partnerNames may be nil.
func NewTerms(origins Origins, partnerNames map[string]string) *Terms {
	return &Terms{origins: origins, partnerNames: partnerNames}
}

// Of returns the current terms of the policy: its insured value, the
// beneficiaries designated for the remainder (none until HU-W1.4-02 applies a
// change), the lender of its original offer as the onerous beneficiary and
// its contact data (the contact e-mail of the policy; no address nor phone is
// registered yet).
func (t *Terms) Of(ctx context.Context, p domain.Policy) (domain.PolicyTerms, error) {
	origin, err := t.origins.Find(ctx, p)
	if err != nil {
		return domain.PolicyTerms{}, fmt.Errorf("find the origin of policy %s: %w", p.ID, err)
	}
	// An advisor sale has no partner behind it: the lender is not known.
	partner := origin.PartnerID
	if partner == domain.AdvisorPartnerID {
		partner = ""
	}
	terms := domain.PolicyTerms{
		InsuredAmount: p.InsuredAmount,
		Currency:      p.Currency,
		Beneficiaries: []domain.Beneficiary{},
		Onerous:       &domain.OnerousBeneficiary{PartnerID: partner, Name: t.partnerNames[partner], Basis: domain.OnerousBasisOutstandingBalance},
	}
	if partner == "" {
		terms.Onerous.Name = ""
	}
	if p.Holder.Email != "" {
		email := p.Holder.Email
		terms.Contact.Email = &email
	}
	return terms, nil
}
