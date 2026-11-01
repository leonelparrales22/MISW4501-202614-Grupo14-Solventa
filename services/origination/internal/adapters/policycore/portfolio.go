// Package policycore bridges the policy core client of pkg/adapters/policies
// to the PolicyPortfolio port of the wallet (HU-M1.2-01): the auto, home and travel
// policies of the insured, with the insured value of each coverage, and the
// holder's profile as their main insured.
package policycore

import (
	"context"
	"fmt"
	"log/slog"

	corepolicies "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/policies"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// Client is what the portfolio needs from the policy core client.
type Client interface {
	HolderPolicies(ctx context.Context, holderID string) ([]corepolicies.Policy, error)
	Holder(ctx context.Context, holderID string) (corepolicies.Holder, error)
}

// Portfolio implements ports.PolicyPortfolio over the policy core. The holder is
// the subject of the token, as in the claims service.
type Portfolio struct {
	client Client
}

// NewPortfolio builds the portfolio over a policy core client.
func NewPortfolio(client Client) *Portfolio { return &Portfolio{client: client} }

var _ ports.PolicyPortfolio = (*Portfolio)(nil)

// HolderPolicies returns the holder's policies. Any failure of the core is
// ports.ErrPolicyPortfolioUnavailable. A policy the core describes badly (no id, or
// a validity that ends before it starts) is left out and logged.
func (p *Portfolio) HolderPolicies(ctx context.Context, holderID string) ([]domain.WalletPolicy, error) {
	got, err := p.client.HolderPolicies(ctx, holderID)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ports.ErrPolicyPortfolioUnavailable, err)
	}
	out := make([]domain.WalletPolicy, 0, len(got))
	for _, c := range got {
		if c.PolicyID == "" || !c.ValidTo.After(c.ValidFrom) {
			slog.WarnContext(ctx, "the policy core returned an invalid policy; it is left out", "policy_id", c.PolicyID)
			continue
		}
		out = append(out, policyOf(c))
	}
	return out, nil
}

// Holder returns the holder's profile, without their contact or payout
// account: the wallet only shows who the main insured is. A holder the core
// does not know is ports.ErrPolicyPortfolioUnavailable too: the core listed their
// policies, so not knowing them is an inconsistent answer.
func (p *Portfolio) Holder(ctx context.Context, holderID string) (domain.PolicyHolder, error) {
	h, err := p.client.Holder(ctx, holderID)
	if err != nil {
		return domain.PolicyHolder{}, fmt.Errorf("%w: %w", ports.ErrPolicyPortfolioUnavailable, err)
	}
	return domain.PolicyHolder{FullName: h.FullName, DocumentType: h.DocumentType, DocumentID: h.DocumentID}, nil
}

// policyOf maps a policy of the core to the wallet. Amounts arrive in minor
// units and leave in units of the currency, like the rest of the list; the
// insured amount of the policy is the largest of its coverages.
func policyOf(c corepolicies.Policy) domain.WalletPolicy {
	p := domain.WalletPolicy{
		ID: c.PolicyID, Source: domain.SourcePolicyCore, Line: domain.PolicyLine(c.Line), Product: c.Product,
		InsuredObject: c.InsuredObject, Status: domain.PolicyStatus(c.Status),
		CoverageStart: c.ValidFrom.UTC(), CoverageEnd: c.ValidTo.UTC(),
		Coverages: make([]domain.Coverage, 0, len(c.Coverages)),
	}
	for _, cov := range c.Coverages {
		amount := float64(cov.InsuredValueMinor) / 100
		p.Coverages = append(p.Coverages, domain.Coverage{Code: cov.Incident, InsuredAmount: amount})
		if amount > p.InsuredAmount {
			p.InsuredAmount = amount
		}
		if p.Currency == "" {
			p.Currency = cov.Currency
		}
	}
	return p
}
