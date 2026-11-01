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

var decidedAt = time.Date(2026, 10, 16, 15, 20, 0, 0, time.UTC)

func reviewNotice(outcome domain.DecisionType) domain.ReviewOutcomeNotice {
	n := domain.ReviewOutcomeNotice{
		To: "ana@example.com", CustomerName: "Ana Pérez", AcceptanceID: "acc-1", Outcome: outcome,
		DecidedAt: decidedAt, IdempotencyKey: "review-outcome:acc-1",
	}
	if outcome == domain.DecisionAdjust {
		n.AdjustedOffer = &domain.AdjustedOffer{ID: "adj-acc-1", Premium: 98450, Currency: "COP", ExpiresAt: decidedAt.Add(24 * time.Hour)}
	}
	return n
}

func TestReviewNotifierSendsTheNewOfferOfAnAdjust(t *testing.T) {
	s := &recordingSender{}

	require.NoError(t, emailadapter.NewReviewNotifier(s).NotifyReviewOutcome(context.Background(), reviewNotice(domain.DecisionAdjust)))

	assert.Equal(t, "ana@example.com", s.got.To)
	assert.Equal(t, "review-outcome:acc-1", s.got.IdempotencyKey)
	assert.Equal(t, "Revisamos tu solicitud de seguro: tienes una nueva oferta", s.got.Subject)
	for _, want := range []string{"Ana Pérez", "acc-1", "ajustó la prima", "$ 98.450 COP", "16/10/2026 a las 15:20 (UTC)",
		"hasta el 17/10/2026 a las 15:20 (UTC) (24 horas)", "mismo flujo de aceptación", "no se cobra"} {
		assert.Contains(t, s.got.Body, want)
	}
	assert.NotContains(t, s.got.Body, "http", "no link (decision D-3 c)")
}

func TestReviewNotifierSendsTheRejection(t *testing.T) {
	s := &recordingSender{}

	require.NoError(t, emailadapter.NewReviewNotifier(s).NotifyReviewOutcome(context.Background(), reviewNotice(domain.DecisionReject)))

	assert.Equal(t, "Tu solicitud de seguro no fue aprobada", s.got.Subject)
	assert.Equal(t, "review-outcome:acc-1", s.got.IdempotencyKey)
	for _, want := range []string{"Ana Pérez", "acc-1", "el riesgo no fue aceptado", "No se realizó ningún cobro", "16/10/2026 a las 15:20 (UTC)"} {
		assert.Contains(t, s.got.Body, want)
	}
	assert.NotContains(t, s.got.Body, "http")
}

func TestReviewNotifierNeverSendsTheAdvisorObservations(t *testing.T) {
	// The notice type has no observations field at all: this checks that the
	// two messages only use what the notice carries.
	for _, outcome := range []domain.DecisionType{domain.DecisionAdjust, domain.DecisionReject} {
		s := &recordingSender{}
		require.NoError(t, emailadapter.NewReviewNotifier(s).NotifyReviewOutcome(context.Background(), reviewNotice(outcome)))
		assert.NotContains(t, s.got.Body, "observ", outcome)
	}
}

func TestReviewNotifierFormatsCents(t *testing.T) {
	s := &recordingSender{}
	n := reviewNotice(domain.DecisionAdjust)
	n.AdjustedOffer.Premium = 96212.5

	require.NoError(t, emailadapter.NewReviewNotifier(s).NotifyReviewOutcome(context.Background(), n))
	assert.Contains(t, s.got.Body, "$ 96.212,50 COP")

	n.AdjustedOffer.Premium = 1234567.05
	require.NoError(t, emailadapter.NewReviewNotifier(s).NotifyReviewOutcome(context.Background(), n))
	assert.Contains(t, s.got.Body, "$ 1.234.567,05 COP")

	n.AdjustedOffer.Premium = 950
	require.NoError(t, emailadapter.NewReviewNotifier(s).NotifyReviewOutcome(context.Background(), n))
	assert.Contains(t, s.got.Body, "$ 950 COP")
}

func TestReviewNotifierRefusesAnAcceptOrAnAdjustWithoutOffer(t *testing.T) {
	s := &recordingSender{}
	notifier := emailadapter.NewReviewNotifier(s)

	assert.ErrorIs(t, notifier.NotifyReviewOutcome(context.Background(), reviewNotice(domain.DecisionAccept)), emailadapter.ErrUnsupportedOutcome)
	n := reviewNotice(domain.DecisionAdjust)
	n.AdjustedOffer = nil
	assert.ErrorIs(t, notifier.NotifyReviewOutcome(context.Background(), n), emailadapter.ErrUnsupportedOutcome)
	assert.Empty(t, s.got.To, "nothing was sent")
}

func TestReviewNotifierPropagatesTheProviderFailure(t *testing.T) {
	s := &recordingSender{err: fmt.Errorf("%w: unexpected status 503", email.ErrUnavailable)}

	err := emailadapter.NewReviewNotifier(s).NotifyReviewOutcome(context.Background(), reviewNotice(domain.DecisionReject))
	assert.ErrorIs(t, err, email.ErrUnavailable, "the worker retries with the same key")
}

func TestConsentNotifierSendsTheCodeToTheCustomer(t *testing.T) {
	s := &recordingSender{}

	err := emailadapter.NewConsentNotifier(s).SendConsentCode(context.Background(), domain.ConsentCodeNotice{
		To: "laura@example.com", CustomerName: "Laura Gómez", Code: "482913", ExpiresAt: decidedAt.Add(10 * time.Minute),
		IdempotencyKey: "consent-code-q-1-1",
	})

	require.NoError(t, err)
	assert.Equal(t, "laura@example.com", s.got.To)
	assert.Equal(t, "consent-code-q-1-1", s.got.IdempotencyKey)
	assert.Equal(t, "Código para confirmar tu seguro de Solventa", s.got.Subject)
	for _, want := range []string{"Laura Gómez", "482913", "16/10/2026 a las 15:30 (UTC)", "dale este código al asesor"} {
		assert.Contains(t, s.got.Body, want)
	}
}
