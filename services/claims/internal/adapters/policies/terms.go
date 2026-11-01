package policies

import (
	"context"
	"errors"
	"fmt"

	corepolicies "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/policies"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// TermsClient is what the evaluation needs from the policy core client.
type TermsClient interface {
	HolderPolicy(ctx context.Context, holderID, policyID string) (corepolicies.Policy, error)
	Holder(ctx context.Context, holderID string) (corepolicies.Holder, error)
}

// Terms implements ports.PolicyTermsReader over the policy core (HU-W2.2-03).
type Terms struct {
	client TermsClient
}

// NewTerms builds the reader over a policy core client.
func NewTerms(client TermsClient) *Terms { return &Terms{client: client} }

var _ ports.PolicyTermsReader = (*Terms)(nil)

// PolicyTerms returns the policy with the terms of each coverage.
func (t *Terms) PolicyTerms(ctx context.Context, holder, policyID string) (domain.PolicyTerms, error) {
	p, err := t.client.HolderPolicy(ctx, holder, policyID)
	switch {
	case errors.Is(err, corepolicies.ErrPolicyNotFound):
		return domain.PolicyTerms{}, ports.ErrPolicyNotFound
	case err != nil:
		return domain.PolicyTerms{}, fmt.Errorf("%w: %w", ports.ErrPolicyDirectoryUnavailable, err)
	}
	out := domain.PolicyTerms{Policy: policyOf(p), Coverages: make([]domain.Coverage, 0, len(p.Coverages))}
	for _, c := range p.Coverages {
		out.Coverages = append(out.Coverages, domain.Coverage{
			Incident: domain.IncidentType(c.Incident), InsuredValueMinor: c.InsuredValueMinor, Currency: c.Currency,
			DeductibleBasisPoints: c.DeductibleBasisPoints,
		})
	}
	return out, nil
}

// Holder returns the profile of the holder.
func (t *Terms) Holder(ctx context.Context, holder string) (domain.Holder, error) {
	h, err := t.client.Holder(ctx, holder)
	switch {
	case errors.Is(err, corepolicies.ErrHolderNotFound):
		return domain.Holder{}, ports.ErrHolderNotFound
	case err != nil:
		return domain.Holder{}, fmt.Errorf("%w: %w", ports.ErrPolicyDirectoryUnavailable, err)
	}
	a := h.PayoutAccount
	return domain.Holder{
		FullName: h.FullName, DocumentType: h.DocumentType, DocumentID: h.DocumentID, Email: h.Email, Phone: h.Phone,
		PayoutAccount: domain.PayoutAccount{BankCode: a.BankCode, BankName: a.BankName, AccountType: a.AccountType,
			AccountNumber: a.AccountNumber},
	}, nil
}
