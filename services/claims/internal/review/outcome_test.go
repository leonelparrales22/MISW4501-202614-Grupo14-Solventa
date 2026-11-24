package review_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// HU-W2.1-02: el historial lleva la decisión y el prestador como los lee el
// asegurado (A8).

type decisionFalsa struct {
	d   domain.Decision
	err error
}

func (f decisionFalsa) Decision(context.Context, string) (domain.Decision, error) { return f.d, f.err }

type asignacionFalsa struct {
	c   ports.AssignmentCase
	err error
}

func (f asignacionFalsa) Case(context.Context, string) (ports.AssignmentCase, error) {
	return f.c, f.err
}

func TestElHistorialTraeLaDecisionYElPrestadorSinDatosSensibles(t *testing.T) {
	e := nuevo(t)
	pagado := radicadoEn.Add(72 * time.Hour)
	phone := "+576015550102"
	e.svc.WithOutcome(
		decisionFalsa{d: domain.Decision{Outcome: domain.OutcomeApproved, DecidedAt: radicadoEn.Add(48 * time.Hour),
			Payout: &domain.Payout{AmountMinor: 435_000_000, Currency: "COP", Status: idempotency.StatusConfirmed,
				ConfirmedAt: pagado, Destination: domain.PayoutAccount{AccountNumber: "0012345678"}}}},
		asignacionFalsa{c: ports.AssignmentCase{Assignment: &domain.Assignment{ProviderName: "Servicios Hogar Norte",
			DistanceKm: 2.3, AssignedAt: radicadoEn.Add(24 * time.Hour), ContactPhone: &phone}}})

	h, err := e.svc.History(context.Background(), e.claimID, "sub-carolina")

	require.NoError(t, err)
	require.NotNil(t, h.Decision)
	assert.Equal(t, int64(435_000_000), h.Decision.AmountMinor)
	require.NotNil(t, h.Decision.PaidAt)
	assert.Equal(t, pagado, *h.Decision.PaidAt)
	require.NotNil(t, h.Provider)
	assert.Equal(t, "Servicios Hogar Norte", h.Provider.Name)
	assert.Equal(t, 2.3, h.Provider.DistanceKm)
}

func TestUnCasoSinDecisionNiPrestadorNoTraeNinguno(t *testing.T) {
	e := nuevo(t)
	e.svc.WithOutcome(decisionFalsa{err: ports.ErrDecisionNotFound}, asignacionFalsa{})

	h, err := e.svc.History(context.Background(), e.claimID, "sub-carolina")

	require.NoError(t, err)
	assert.Nil(t, h.Decision)
	assert.Nil(t, h.Provider)
}

func TestUnFalloAlLeerLaDecisionOLaAsignacionSeDevuelve(t *testing.T) {
	e := nuevo(t)
	e.svc.WithOutcome(decisionFalsa{err: errors.New("la base no responde")}, nil)
	_, err := e.svc.History(context.Background(), e.claimID, "")
	assert.ErrorContains(t, err, "la base no responde")

	e.svc.WithOutcome(nil, asignacionFalsa{err: errors.New("la asignación no responde")})
	_, err = e.svc.History(context.Background(), e.claimID, "")
	assert.ErrorContains(t, err, "la asignación no responde")
}
