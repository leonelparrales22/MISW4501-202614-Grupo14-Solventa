// Package payouts pays the indemnity of an approved claim through the payment
// gateway (HU-W2.2-03), with the shared client of pkg/adapters/payments: its
// errors already carry the classification pkg/idempotency needs.
package payouts

import (
	"context"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/payments"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// Client is the part of the payments client this adapter uses.
type Client interface {
	Payout(ctx context.Context, req payments.PayoutRequest) (payments.Payout, error)
}

// Gateway is the ports.PayoutGateway over the payment gateway.
type Gateway struct {
	client Client
}

var _ ports.PayoutGateway = (*Gateway)(nil)

// NewGateway builds the adapter.
func NewGateway(client Client) *Gateway { return &Gateway{client: client} }

// Payout sends the payout of the approval under its key and returns the
// gateway's id of the payout.
func (g *Gateway) Payout(ctx context.Context, d domain.Decision) (string, error) {
	p := d.Payout
	out, err := g.client.Payout(ctx, payments.PayoutRequest{
		IdempotencyKey: p.Key, Reference: d.FilingNumber, BeneficiaryID: p.BeneficiaryID,
		AmountMinor: p.AmountMinor, Currency: p.Currency,
		Destination: payments.PayoutDestination{BankCode: p.Destination.BankCode, AccountType: p.Destination.AccountType,
			AccountNumber: p.Destination.AccountNumber},
	})
	if err != nil {
		return "", err
	}
	return out.ID, nil
}
