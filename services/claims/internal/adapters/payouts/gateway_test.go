package payouts_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/payments"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/payouts"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
)

type cliente struct {
	got payments.PayoutRequest
	err error
}

func (c *cliente) Payout(_ context.Context, r payments.PayoutRequest) (payments.Payout, error) {
	c.got = r
	return payments.Payout{ID: "po-1"}, c.err
}

func TestThePayoutTravelsWithItsKeyAndTheFullAccount(t *testing.T) {
	c := &cliente{}
	d := domain.Decision{ClaimID: "cl-1", FilingNumber: "SIN-2026-01655", Payout: &domain.Payout{
		Key: "indemnizacion-cl-1", BeneficiaryID: "sub-carolina", AmountMinor: 413_250_000, Currency: "COP",
		Destination: domain.PayoutAccount{BankCode: "1007", BankName: "Banco Aliado", AccountType: "savings",
			AccountNumber: "0123-4567-12"},
	}}

	ref, err := payouts.NewGateway(c).Payout(context.Background(), d)

	require.NoError(t, err)
	assert.Equal(t, "po-1", ref)
	assert.Equal(t, payments.PayoutRequest{
		IdempotencyKey: "indemnizacion-cl-1", Reference: "SIN-2026-01655", BeneficiaryID: "sub-carolina",
		AmountMinor: 413_250_000, Currency: "COP",
		Destination: payments.PayoutDestination{BankCode: "1007", AccountType: "savings", AccountNumber: "0123-4567-12"},
	}, c.got)

	c.err = payments.ErrAmbiguous
	_, err = payouts.NewGateway(c).Payout(context.Background(), d)
	assert.True(t, errors.Is(err, payments.ErrAmbiguous), "the classification of the client is kept")
}
