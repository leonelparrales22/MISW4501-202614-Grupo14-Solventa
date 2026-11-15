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

func endorsementNotice(outcome domain.NoticeOutcome) domain.EndorsementNotice {
	n := domain.EndorsementNotice{RequestID: "SOL-MOD-2026-0042", PolicyID: "pol-acc-1", ChangeType: domain.ChangeBeneficiaries,
		Outcome: outcome, To: "carolina@example.com", Name: "Carolina Pérez Ruiz",
		DecidedAt: time.Date(2026, 9, 16, 21, 20, 0, 0, time.UTC)}
	switch outcome {
	case domain.NoticeApplied:
		n.EndorsementID = "END-2026-0102"
	default:
		n.Observations = "Corrige el documento de Lucía y radica de nuevo"
	}
	return n
}

func TestTheInsuredIsToldEachDecisionWithItsKey(t *testing.T) {
	for _, tc := range []struct {
		outcome domain.NoticeOutcome
		subject string
		want    []string
	}{
		{domain.NoticeApplied, "Aplicamos el cambio de tu póliza", []string{"SOL-MOD-2026-0042", "beneficiarios", "pol-acc-1",
			"END-2026-0102", "16/09/2026 a las 21:20 (UTC)", "en el portal y en la app"}},
		{domain.NoticeRejected, "Tu solicitud de cambio no fue aprobada", []string{"no fue aprobada",
			"Motivo: Corrige el documento de Lucía y radica de nuevo", "conserva sus condiciones"}},
		{domain.NoticeReturned, "Tu solicitud de cambio necesita ajustes", []string{"no se pudo aplicar",
			"Qué ajustar: Corrige el documento de Lucía y radica de nuevo", "radicar una nueva solicitud"}},
	} {
		s := &recordingSender{}

		require.NoError(t, emailadapter.NewEndorsementNotifier(s).NotifyEndorsementOutcome(context.Background(), endorsementNotice(tc.outcome)))

		assert.Equal(t, "carolina@example.com", s.got.To)
		assert.Equal(t, "endorsement-outcome:SOL-MOD-2026-0042", s.got.IdempotencyKey)
		assert.Equal(t, tc.subject, s.got.Subject)
		for _, want := range append(tc.want, "Hola Carolina Pérez Ruiz") {
			assert.Contains(t, s.got.Body, want, tc.outcome)
		}
		assert.NotContains(t, s.got.Body, "http", "no link")
	}
}

func TestAnUnknownOutcomeIsNotSent(t *testing.T) {
	s := &recordingSender{}

	err := emailadapter.NewEndorsementNotifier(s).NotifyEndorsementOutcome(context.Background(), endorsementNotice("otro"))

	assert.ErrorIs(t, err, emailadapter.ErrUnknownNoticeOutcome)
	assert.Empty(t, s.got.To)
}
