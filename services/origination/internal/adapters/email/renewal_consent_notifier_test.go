package emailadapter_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/email"
	emailadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/email"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

func TestRenewalConsentNotifierSendsTheCodeOfTheRenewalToTheInsured(t *testing.T) {
	s := &recordingSender{}

	err := emailadapter.NewRenewalConsentNotifier(s).SendConsentCode(context.Background(), domain.ConsentCodeNotice{
		To: "carolina@example.com", CustomerName: "Carolina Pérez", Code: "482913",
		ExpiresAt:      time.Date(2026, 9, 16, 21, 15, 0, 0, time.UTC),
		IdempotencyKey: "renewal-consent-code-rq-1-1",
	})

	require.NoError(t, err)
	assert.Equal(t, "carolina@example.com", s.got.To)
	assert.Equal(t, "renewal-consent-code-rq-1-1", s.got.IdempotencyKey)
	assert.Equal(t, "Código para confirmar la renovación de tu póliza de Solventa", s.got.Subject)
	for _, want := range []string{"Carolina Pérez", "va a renovar tu póliza en tu nombre", "482913", "16/09/2026 a las 21:15 (UTC)",
		"dale este código al asesor"} {
		assert.Contains(t, s.got.Body, want)
	}
}

func TestRenewalConsentNotifierPropagatesTheProviderFailure(t *testing.T) {
	s := &recordingSender{err: fmt.Errorf("%w: unexpected status 503", email.ErrUnavailable)}

	err := emailadapter.NewRenewalConsentNotifier(s).SendConsentCode(context.Background(), domain.ConsentCodeNotice{To: "a@example.com"})

	assert.ErrorIs(t, err, email.ErrUnavailable, "the advisor asks for the code again")
}
