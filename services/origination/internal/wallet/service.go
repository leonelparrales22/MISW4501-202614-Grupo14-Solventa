// Package wallet lists every policy of the insured in one place (HU-M1.2-01):
// the ones origination issued, with where their renewal stands (HU-W1.5-02),
// and the auto, home and travel policies of the policy core, with the insured
// value of each coverage and the main insured. The mobile app keeps a copy of
// this list to show it without a connection.
//
// Both sources are optional: without the premium charge origination issues
// no policies, and without the policy core only origination's are listed.
// When a configured source does not answer, the list says it is partial
// instead of failing, so a client never mistakes a missing policy for a
// cancelled one.
package wallet

import (
	"context"
	"errors"
	"log/slog"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/renewal"
)

// RenewalLister lists the policies origination issued to the caller, with
// their renewal state (renewal.Service).
type RenewalLister interface {
	ListPolicies(ctx context.Context) ([]renewal.PolicyView, error)
}

// Item is one policy of the wallet. Renewal is set only for a policy
// origination issued: the others cannot be renewed here.
type Item struct {
	Policy  domain.WalletPolicy
	Renewal *renewal.PolicyView
}

// Wallet is the list of the caller. Partial is true when a configured source
// did not answer and its policies are missing.
type Wallet struct {
	Items   []Item
	Partial bool
}

// Service is the use case behind GET /v1/policies.
type Service struct {
	renewals  RenewalLister
	portfolio ports.PolicyPortfolio
	clock     ports.Clock
}

// NewService builds a Service. renewals and portfolio may be nil (that
// source is not configured), but not both.
func NewService(renewals RenewalLister, portfolio ports.PolicyPortfolio, clock ports.Clock) (*Service, error) {
	switch {
	case clock == nil:
		return nil, errors.New("wallet: a clock is required")
	case renewals == nil && portfolio == nil:
		return nil, errors.New("wallet: at least one source of policies is required")
	}
	return &Service{renewals: renewals, portfolio: portfolio, clock: clock}, nil
}

// List returns the policies of the caller: origination's first, then the
// policy core's. A failure of origination's own store is an error; a failure
// of the policy core only makes the list partial.
func (s *Service) List(ctx context.Context) (Wallet, error) {
	claims, ok := security.ClaimsFromContext(ctx)
	if !ok {
		return Wallet{}, security.ErrUnauthenticated
	}
	var out Wallet
	if s.renewals != nil {
		views, err := s.renewals.ListPolicies(ctx)
		if err != nil {
			return Wallet{}, err
		}
		now := s.clock.Now().UTC()
		for i := range views {
			out.Items = append(out.Items, Item{Policy: domain.WalletPolicyOf(views[i].Policy, now), Renewal: &views[i]})
		}
	}
	if s.portfolio != nil {
		core, err := s.corePolicies(ctx, claims.Subject)
		if err != nil {
			slog.WarnContext(ctx, "the policy core did not answer; the wallet is partial", "error", err)
			out.Partial = true
		}
		for _, p := range core {
			out.Items = append(out.Items, Item{Policy: p})
		}
	}
	if out.Items == nil {
		out.Items = []Item{}
	}
	return out, nil
}

// corePolicies returns the holder's policies of the policy core, each with
// the holder's profile as its main insured. The profile is only asked when
// there are policies; without it the policies are not listed, because a
// policy without its insured is not what the insured asked to see.
func (s *Service) corePolicies(ctx context.Context, subject string) ([]domain.WalletPolicy, error) {
	policies, err := s.portfolio.HolderPolicies(ctx, subject)
	if err != nil || len(policies) == 0 {
		return nil, err
	}
	holder, err := s.portfolio.Holder(ctx, subject)
	if err != nil {
		return nil, err
	}
	for i := range policies {
		policies[i].Holder = holder
	}
	return policies, nil
}
