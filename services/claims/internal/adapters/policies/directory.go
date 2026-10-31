// Package policies bridges the policy core client of pkg/adapters/policies to
// the PolicyDirectory port of the claims service (HU-W2.1-01).
package policies

import (
	"context"
	"fmt"
	"log/slog"

	corepolicies "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/policies"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// Client is what the directory needs from the policy core client.
type Client interface {
	HolderPolicies(ctx context.Context, holderID string) ([]corepolicies.Policy, error)
}

// Directory implements ports.PolicyDirectory over the policy core. The holder
// is the subject of the token (assumption S1 of the story).
type Directory struct {
	client Client
}

// NewDirectory builds the directory over a policy core client.
func NewDirectory(client Client) *Directory { return &Directory{client: client} }

var _ ports.PolicyDirectory = (*Directory)(nil)

// HolderPolicies returns the subject's policies. Any failure of the core is
// ports.ErrPolicyDirectoryUnavailable: the claim is not filed (fail-closed). A
// policy the core describes badly (no id, or a validity that ends before it
// starts) is left out and logged: it cannot take a claim.
func (d *Directory) HolderPolicies(ctx context.Context, subject string) ([]domain.Policy, error) {
	got, err := d.client.HolderPolicies(ctx, subject)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ports.ErrPolicyDirectoryUnavailable, err)
	}
	out := make([]domain.Policy, 0, len(got))
	for _, p := range got {
		if p.PolicyID == "" || !p.ValidTo.After(p.ValidFrom) {
			slog.WarnContext(ctx, "the policy core returned an invalid policy; it is left out", "policy_id", p.PolicyID)
			continue
		}
		out = append(out, policyOf(p))
	}
	return out, nil
}

// policyOf maps a policy of the core to the domain.
func policyOf(p corepolicies.Policy) domain.Policy {
	covered := make([]domain.IncidentType, 0, len(p.CoveredIncidents))
	for _, t := range p.CoveredIncidents {
		covered = append(covered, domain.IncidentType(t))
	}
	return domain.Policy{
		ID: p.PolicyID, Line: domain.PolicyLine(p.Line), Product: p.Product, InsuredObject: p.InsuredObject,
		Status: domain.PolicyStatus(p.Status), ValidFrom: p.ValidFrom.UTC(), ValidTo: p.ValidTo.UTC(),
		CoveredIncidents: covered,
	}
}
