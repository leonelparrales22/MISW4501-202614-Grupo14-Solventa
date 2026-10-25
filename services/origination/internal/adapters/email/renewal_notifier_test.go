package emailadapter_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	emailadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/email"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

func renewalNotice(reason domain.NotRenewedReason, grace int) domain.RenewalNotice {
	return domain.RenewalNotice{
		To: "carolina@example.com", HolderName: "Carolina Pérez", PolicyID: "pol-1",
		CoverageEnded: time.Date(2026, 10, 2, 23, 59, 0, 0, time.UTC), Reason: reason, GraceDays: grace,
		IdempotencyKey: "renewal-lapsed:ren-pol-1-20261002",
	}
}

func TestRenewalNotifierTellsTheInsuredTheCoverageEnded(t *testing.T) {
	s := &recordingSender{}

	require.NoError(t, emailadapter.NewRenewalNotifier(s).Notify(context.Background(), renewalNotice(domain.NotRenewedNoResponse, 0)))

	assert.Equal(t, "carolina@example.com", s.got.To)
	assert.Equal(t, "renewal-lapsed:ren-pol-1-20261002", s.got.IdempotencyKey)
	for _, want := range []string{"Carolina Pérez", "pol-1", "02/10/2026", "No recibimos tu respuesta", "No aplica ningún período de gracia"} {
		assert.Contains(t, s.got.Body, want)
	}
}

func TestRenewalNotifierSaysWhyAndTheGracePeriod(t *testing.T) {
	s := &recordingSender{}

	require.NoError(t, emailadapter.NewRenewalNotifier(s).Notify(context.Background(), renewalNotice(domain.NotRenewedPaymentFailed, 15)))

	assert.Contains(t, s.got.Body, "El pago de la renovación no se completó")
	assert.Contains(t, s.got.Body, "período de gracia de 15 días")
	assert.NotContains(t, s.got.Body, "http", "no link")
}
