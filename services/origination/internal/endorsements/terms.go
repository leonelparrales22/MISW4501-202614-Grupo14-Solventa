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
	// endorsed reads the last applied endorsement (HU-W1.4-02); nil before
	// any endorsement can be applied.
	endorsed EndorsedTermsReader
	// cancellations tells whether a policy was cancelled for non-payment
	// (HU-W1.6-02); nil when nothing cancels policies.
	cancellations ports.CancellationChecker
}

// EndorsedTermsReader reads the terms of the last endorsement that governs a
// policy (HU-W1.4-02): its own or, for a renewal without endorsements of its
// own, the last one of the policy it renews.
type EndorsedTermsReader interface {
	LatestTerms(ctx context.Context, policyID string) (domain.LatestTerms, error)
}

// NewTerms builds a Terms. partnerNames may be nil.
func NewTerms(origins Origins, partnerNames map[string]string) *Terms {
	return &Terms{origins: origins, partnerNames: partnerNames}
}

// WithEndorsements lays the applied endorsements over the terms (HU-W1.4-02,
// decision D-3 a): every reader of the terms sees the last one.
func (t *Terms) WithEndorsements(r EndorsedTermsReader) *Terms {
	t.endorsed = r
	return t
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
// registered until an endorsement registers them).
func (t *Terms) Of(ctx context.Context, p domain.Policy) (domain.PolicyTerms, error) {
	terms, _, err := t.Current(ctx, p)
	return terms, err
}

// Current returns the terms of Of and the version of the last endorsement of
// the policy itself (0 without any), which the next endorsement follows
// (HU-W1.4-02). The last endorsement lays its beneficiaries and contact data
// over the terms; its insured value and e-mail only when it is the policy's
// own, because a renewal already starts from the ones of the policy it renews
// (decision D-3 a+).
func (t *Terms) Current(ctx context.Context, p domain.Policy) (domain.PolicyTerms, int, error) {
	origin, err := t.origins.Find(ctx, p)
	if err != nil {
		return domain.PolicyTerms{}, 0, fmt.Errorf("find the origin of policy %s: %w", p.ID, err)
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
	if t.endorsed == nil {
		return terms, 0, nil
	}
	latest, err := t.endorsed.LatestTerms(ctx, p.ID)
	if err != nil {
		return domain.PolicyTerms{}, 0, fmt.Errorf("read the endorsements of policy %s: %w", p.ID, err)
	}
	if !latest.Found {
		return terms, 0, nil
	}
	terms.Beneficiaries = append([]domain.Beneficiary{}, latest.Terms.Beneficiaries...)
	terms.Contact.Address, terms.Contact.Phone = copyText(latest.Terms.Contact.Address), copyText(latest.Terms.Contact.Phone)
	if latest.Own {
		terms.InsuredAmount = latest.Terms.InsuredAmount
		if latest.Terms.Contact.Email != nil {
			terms.Contact.Email = copyText(latest.Terms.Contact.Email)
		}
	}
	return terms, latest.Version, nil
}

func copyText(s *string) *string {
	if s == nil {
		return nil
	}
	v := *s
	return &v
}
