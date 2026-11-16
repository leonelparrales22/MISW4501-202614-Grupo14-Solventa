package endorsements

import (
	"context"
	"fmt"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
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
	// cancellations tells whether a policy was cancelled for non-payment
	// (HU-W1.6-02); nil when nothing cancels policies.
	cancellations ports.CancellationChecker
}

// NewTerms builds a Terms. partnerNames may be nil.
func NewTerms(origins Origins, partnerNames map[string]string) *Terms {
	return &Terms{origins: origins, partnerNames: partnerNames}
}

// WithCancellations makes InForce read whether the policy was cancelled for
// non-payment (HU-W1.6-02). c may be nil: then nothing is cancelled.
func (t *Terms) WithCancellations(c ports.CancellationChecker) *Terms {
	t.cancellations = c
	return t
}

// InForce tells whether the policy can change at now (adjustment A-9 of
// HU-W1.4-01): its coverage is running (domain.Policy.InForceAt) and it was
// not cancelled for non-payment (HU-W1.6-02), the same check the renewal
// makes. A cancellation keeps the dates of the coverage, so the dates alone
// are not enough. It is the rule of every change of a policy: the insured
// filing it here and the analyst of HU-W1.4-02 applying it.
func (t *Terms) InForce(ctx context.Context, p domain.Policy, now time.Time) (bool, error) {
	if !p.InForceAt(now) {
		return false, nil
	}
	if t.cancellations == nil {
		return true, nil
	}
	cancelled, err := t.cancellations.IsCancelled(ctx, p.ID)
	if err != nil {
		return false, err
	}
	return !cancelled, nil
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
