// Package payments bridges the shared payment gateway client
// (pkg/adapters/payments: Idempotency-Key, 700 ms timeout, circuit breaker)
// to the ports.PaymentGateway of the premium charge (HA-D02).
package payments

import (
	"context"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/payments"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// Charger is the part of the shared client the gateway needs.
type Charger interface {
	Charge(ctx context.Context, req payments.ChargeRequest) (payments.Charge, error)
}

// Gateway is the ports.PaymentGateway over the shared client. The client's
// errors already wrap the classification of pkg/idempotency (rejected,
// ambiguous, not sent), so they pass through unchanged.
type Gateway struct {
	client Charger
}

var _ ports.PaymentGateway = (*Gateway)(nil)

// NewGateway builds a Gateway.
func NewGateway(client Charger) *Gateway { return &Gateway{client: client} }

// Charge charges the premium of the charge with its idempotency key; the
// reference is the offer and the amount is in minor units. The token of the
// payment method travels too when there is one (HU-W1.3-02): it is what tells
// the gateway which card to charge.
func (g *Gateway) Charge(ctx context.Context, c domain.Charge) (string, error) {
	ch, err := g.client.Charge(ctx, payments.ChargeRequest{
		IdempotencyKey: c.IdempotencyKey,
		Reference:      c.OfferID,
		CustomerID:     c.CustomerID,
		AmountMinor:    c.AmountMinor,
		Currency:       c.Currency,
		PaymentToken:   c.PaymentMethod.Token,
	})
	if err != nil {
		return "", err
	}
	return ch.ID, nil
}

var _ ports.InstallmentGateway = (*Gateway)(nil)

// ChargeInstallment charges a monthly instalment of the policy (HU-W1.6-02)
// to the card of its issuance. The key of the attempt is also the reference
// of the charge, which is how the gateway's test card for instalments tells
// them from the charge of an issuance.
func (g *Gateway) ChargeInstallment(ctx context.Context, p domain.Policy, i domain.Installment, key string,
	m domain.PaymentMethod) (string, error) {
	ch, err := g.client.Charge(ctx, payments.ChargeRequest{
		IdempotencyKey: key,
		Reference:      key,
		CustomerID:     p.CustomerID,
		AmountMinor:    i.AmountMinor,
		Currency:       i.Currency,
		PaymentToken:   m.Token,
	})
	if err != nil {
		return "", err
	}
	return ch.ID, nil
}
