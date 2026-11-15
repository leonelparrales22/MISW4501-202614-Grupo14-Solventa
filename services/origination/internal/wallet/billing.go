package wallet

import (
	"context"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// HU-W1.6-02: where the premium of each policy origination issued stands
// (up to date, in its grace period or cancelled for non-payment), and the
// detail of one policy (screens A2 and A2b).

// BillingReader reads the instalments and the cancellation of policies.
type BillingReader interface {
	Billing(ctx context.Context, policyIDs []string) (map[string]ports.BillingRecord, error)
}

// IssuanceReader reads the charge of the issuance of a policy: the first
// month, and the card the instalments are charged to.
type IssuanceReader interface {
	IssuanceCharge(ctx context.Context, acceptanceID string) (domain.Charge, bool, error)
}

// WithBilling makes the wallet say where the premium of each policy
// origination issued stands, with the rules of the instalments. Without it
// the wallet lists the policies as before.
func (s *Service) WithBilling(billing BillingReader, issuance IssuanceReader, rules domain.InstallmentRules) *Service {
	s.billing, s.issuance, s.installmentRules = billing, issuance, rules
	return s
}

// withBilling sets the billing of the items origination issued. A cancelled
// policy is listed as cancelled and cannot be renewed.
func (s *Service) withBilling(ctx context.Context, items []Item) error {
	if s.billing == nil {
		return nil
	}
	var ids []string
	for _, it := range items {
		if it.Renewal != nil {
			ids = append(ids, it.Policy.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	records, err := s.billing.Billing(ctx, ids)
	if err != nil {
		return err
	}
	now := s.clock.Now().UTC()
	for k := range items {
		it := &items[k]
		if it.Renewal == nil {
			continue
		}
		rec := records[it.Policy.ID]
		b := domain.BillingOf(it.Renewal.Policy, s.installmentRules, rec.Installments, rec.Cancellation, now)
		it.Billing = &b
		if b.Status == domain.PaymentCancelled {
			it.Policy.Status = domain.PolicyCancelled
			it.Renewal.CanRenew = false
		}
	}
	return nil
}

// Detail is one policy of the caller with everything screen A2 shows: for a
// policy origination issued, where its premium stands, every month billed and
// the card it is charged to; for one of the policy core, what the core says.
type Detail struct {
	Item
	// PaymentMethod is the card or the PSE of the issuance; nil when it is
	// unknown or the policy is the core's.
	PaymentMethod *domain.PaymentMethod
	// Issuance is the charge of the issuance (the first month), when it is
	// known and confirmed.
	Issuance *domain.Charge
}

// Get returns one policy of the caller, ports.ErrPolicyNotFound when they have
// no such policy. A policy core that does not answer is an error here: the
// detail of one policy cannot be partial.
func (s *Service) Get(ctx context.Context, policyID string) (Detail, error) {
	claims, ok := security.ClaimsFromContext(ctx)
	if !ok {
		return Detail{}, security.ErrUnauthenticated
	}
	if s.renewals != nil {
		views, err := s.renewals.ListPolicies(ctx)
		if err != nil {
			return Detail{}, err
		}
		for i := range views {
			if views[i].Policy.ID != policyID {
				continue
			}
			items := []Item{{Policy: domain.WalletPolicyOf(views[i].Policy, s.clock.Now().UTC()), Renewal: &views[i]}}
			if err := s.withBilling(ctx, items); err != nil {
				return Detail{}, err
			}
			return s.ownDetail(ctx, items[0])
		}
	}
	if s.portfolio != nil {
		core, err := s.corePolicies(ctx, claims.Subject)
		if err != nil {
			return Detail{}, err
		}
		for _, p := range core {
			if p.ID == policyID {
				return Detail{Item: Item{Policy: p}}, nil
			}
		}
	}
	return Detail{}, ports.ErrPolicyNotFound
}

// ownDetail completes a policy origination issued with its issuance.
func (s *Service) ownDetail(ctx context.Context, it Item) (Detail, error) {
	d := Detail{Item: it}
	if s.issuance == nil {
		return d, nil
	}
	c, found, err := s.issuance.IssuanceCharge(ctx, it.Renewal.Policy.AcceptanceID)
	if err != nil || !found {
		return d, err
	}
	if c.PaymentMethod.Captured() {
		m := c.PaymentMethod
		m.Token = ""
		d.PaymentMethod = &m
	}
	if c.Status == domain.ChargeConfirmed {
		c.PaymentMethod.Token = ""
		d.Issuance = &c
	}
	return d, nil
}
