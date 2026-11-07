package review_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/evaluation"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/review"
)

// HU-W2.1-03: la revisión del asesor sobre la radicación, con los almacenes
// en memoria que tienen las mismas reglas que PostgreSQL.

var radicadoEn = time.Date(2026, 11, 9, 14, 0, 0, 0, time.UTC)

type reloj struct{ now time.Time }

func (r reloj) Now() time.Time { return r.now }

// lectorFalso es la lectura de la evaluación: solo cuenta lo que se pidió.
type lectorFalso struct {
	err     error
	pedidos []string
}

func (l *lectorFalso) ReviewDetail(_ context.Context, id string) (evaluation.Evaluation, error) {
	l.pedidos = append(l.pedidos, id)
	return evaluation.Evaluation{Claim: domain.ClaimRecord{Claim: domain.Claim{ID: id}}}, l.err
}

func (l *lectorFalso) ReviewQueue(context.Context, int, string) (evaluation.QueuePage, error) {
	return evaluation.QueuePage{}, l.err
}

type escenario struct {
	casos   *memory.CaseStore
	svc     *review.Service
	lector  *lectorFalso
	claimID string
	avisos  int
}

func nuevo(t *testing.T) *escenario {
	t.Helper()
	casos := memory.NewCaseStore()
	stored, _, err := casos.CreateWeb(context.Background(), domain.ClaimRecord{
		Claim: domain.Claim{ID: "cl-1", Subject: "sub-carolina", FilingKey: "k1", PolicyID: "HG-2025-880231",
			IncidentType: domain.IncidentWaterDamage, OccurredAt: radicadoEn.Add(-time.Hour), Place: "Calle 1",
			Description: "Filtración en la cocina.", Status: domain.StatusActive, FiledAt: radicadoEn},
		Channel: domain.ChannelWeb, CatalogVersion: "claim-catalog-v1",
	})
	require.NoError(t, err)
	e := &escenario{casos: casos, lector: &lectorFalso{}, claimID: stored.ID}
	store := memory.NewReviewStore(casos)
	e.svc = review.NewService(casos, store, store, e.lector, reloj{radicadoEn.Add(time.Hour)}, func() { e.avisos++ })
	return e
}

func (e *escenario) estado(t *testing.T) domain.Status {
	t.Helper()
	c, err := e.casos.Get(context.Background(), e.claimID)
	require.NoError(t, err)
	return c.Status
}

func devolucion() domain.ReturnRequest {
	return domain.ReturnRequest{Reason: "La factura no es legible.", MissingSupports: []string{domain.SupportRepairQuote}}
}

func TestAprobarLaRadicacionLaPasaAEvaluacionYDespiertaAlTriage(t *testing.T) {
	e := nuevo(t)

	r, err := e.svc.Approve(context.Background(), e.claimID, "asesor@solventa.example")

	require.NoError(t, err)
	assert.Equal(t, 1, r.Seq)
	assert.Equal(t, domain.ReviewApproved, r.Outcome)
	assert.NotEmpty(t, r.FilingNumber)
	assert.Equal(t, domain.StatusInEvaluation, e.estado(t))
	assert.Equal(t, 1, e.avisos)

	_, err = e.svc.Approve(context.Background(), e.claimID, "otro")
	assert.ErrorIs(t, err, domain.ErrNotReviewable, "una radicación se revisa una vez")
	assert.Equal(t, 1, e.avisos)
}

func TestDevolverLaRadicacionGuardaElMotivoYNoLaEvalua(t *testing.T) {
	e := nuevo(t)

	r, err := e.svc.Return(context.Background(), e.claimID, "asesor", devolucion())

	require.NoError(t, err)
	assert.Equal(t, domain.ReviewReturned, r.Outcome)
	assert.Equal(t, domain.StatusReturned, e.estado(t))
	assert.Zero(t, e.avisos, "el triage no tiene nada que evaluar")

	reviews, err := memory.NewReviewStore(e.casos).Reviews(context.Background(), e.claimID)
	require.NoError(t, err)
	assert.Empty(t, reviews, "otro almacén no comparte las revisiones")
}

func TestUnaDevolucionInvalidaNoCambiaNada(t *testing.T) {
	e := nuevo(t)

	_, err := e.svc.Return(context.Background(), e.claimID, "asesor", domain.ReturnRequest{Reason: "Falta algo"})

	assert.ErrorIs(t, err, domain.ErrNothingRequested)
	assert.Equal(t, domain.StatusActive, e.estado(t))
	h, err := e.svc.History(context.Background(), e.claimID, "")
	require.NoError(t, err)
	assert.Len(t, h.Changes, 1)
}

func TestUnSiniestroQueNoExisteNoSeRevisa(t *testing.T) {
	e := nuevo(t)

	_, err := e.svc.Approve(context.Background(), "no-existe", "asesor")
	assert.ErrorIs(t, err, ports.ErrClaimNotFound)
	_, err = e.svc.Return(context.Background(), "no-existe", "asesor", devolucion())
	assert.ErrorIs(t, err, ports.ErrClaimNotFound)
	_, err = e.svc.History(context.Background(), "no-existe", "")
	assert.ErrorIs(t, err, ports.ErrClaimNotFound)
}

func TestElDetalleTraeLaRadicacionYSusRevisiones(t *testing.T) {
	e := nuevo(t)
	_, err := e.svc.Return(context.Background(), e.claimID, "asesor", devolucion())
	require.NoError(t, err)

	d, err := e.svc.Detail(context.Background(), e.claimID)

	require.NoError(t, err)
	assert.Equal(t, []string{e.claimID}, e.lector.pedidos)
	require.Len(t, d.Reviews, 1)
	assert.Equal(t, "La factura no es legible.", d.Reviews[0].Reason)

	e.lector.err = errors.New("se cayó")
	_, err = e.svc.Detail(context.Background(), e.claimID)
	assert.Error(t, err)
	_, err = e.svc.Queue(context.Background(), 0, "")
	assert.Error(t, err)
}

func TestElHistorialTraeCadaCambioYLaUltimaDevolucion(t *testing.T) {
	e := nuevo(t)
	_, err := e.svc.Return(context.Background(), e.claimID, "asesor", devolucion())
	require.NoError(t, err)

	h, err := e.svc.History(context.Background(), e.claimID, "sub-carolina")

	require.NoError(t, err)
	require.Len(t, h.Changes, 2)
	assert.Equal(t, domain.StatusActive, h.Changes[0].Status)
	assert.Empty(t, h.Changes[0].PreviousStatus)
	assert.True(t, h.Changes[0].ChangedAt.Equal(radicadoEn), "la radicación tiene la hora en que se radicó")
	assert.Equal(t, domain.StatusReturned, h.Changes[1].Status)
	assert.Equal(t, domain.StatusActive, h.Changes[1].PreviousStatus)
	assert.True(t, h.Changes[1].ChangedAt.Equal(radicadoEn.Add(time.Hour)))
	require.NotNil(t, h.LastReturn)
	assert.Equal(t, []string{domain.SupportRepairQuote}, h.LastReturn.MissingSupports)
}

func TestElHistorialDeUnCasoAprobadoNoTraeDevolucion(t *testing.T) {
	e := nuevo(t)
	_, err := e.svc.Approve(context.Background(), e.claimID, "asesor")
	require.NoError(t, err)

	h, err := e.svc.History(context.Background(), e.claimID, "")

	require.NoError(t, err)
	assert.Nil(t, h.LastReturn)
	assert.Equal(t, domain.StatusInEvaluation, h.Changes[len(h.Changes)-1].Status)
}

func TestUnAseguradoSoloLeeElHistorialDeSusSiniestros(t *testing.T) {
	e := nuevo(t)

	_, err := e.svc.History(context.Background(), e.claimID, "sub-otro")

	assert.ErrorIs(t, err, review.ErrNotOwner)
}
