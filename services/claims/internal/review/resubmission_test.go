package review_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/review"
)

// HU-W2.1-04: el asegurado corrige la radicación devuelta y la reenvía.

// almacenReenvios es el almacén en memoria con una falla a la medida.
type almacenReenvios struct {
	*memory.ReviewStore
	err error
}

func (a *almacenReenvios) Resubmit(ctx context.Context, r domain.Resubmission) (domain.Resubmission, error) {
	if a.err != nil {
		return domain.Resubmission{}, a.err
	}
	return a.ReviewStore.Resubmit(ctx, r)
}

type conReenvio struct {
	*escenario
	almacen *almacenReenvios
}

// devuelto es el caso del móvil devuelto por el asesor: fotos borrosas y sin
// informe de tránsito, listo para que el asegurado lo corrija.
func devuelto(t *testing.T) conReenvio {
	t.Helper()
	e := nuevo(t)
	store := memory.NewReviewStore(e.casos)
	almacen := &almacenReenvios{ReviewStore: store}
	e.svc = review.NewService(e.casos, store, store, e.lector, reloj{radicadoEn.Add(2 * time.Hour)}, nil).
		WithResubmissions(almacen, e.casos)
	_, err := e.svc.Return(context.Background(), e.claimID, asesor, devolucion())
	require.NoError(t, err)
	return conReenvio{escenario: e, almacen: almacen}
}

// subir deja un soporte confirmado del asegurado.
func (e conReenvio) subir(t *testing.T, n int, owner string, format domain.FileFormat) string {
	t.Helper()
	ctx := context.Background()
	id := fmt.Sprintf("sup-%032x", n)
	require.NoError(t, e.casos.Save(ctx, domain.SupportFile{ID: id, OwnerSubject: owner, FileName: "f", Format: format,
		ByteSize: 10, SHA256: "x", UploadedAt: radicadoEn}))
	_, err := e.casos.Confirm(ctx, id, radicadoEn)
	require.NoError(t, err)
	return id
}

// correccion reemplaza las fotos y sube el informe de tránsito.
func (e conReenvio) correccion(t *testing.T) domain.ResubmissionRequest {
	t.Helper()
	photos := domain.SupportGroup{RequirementCode: domain.SupportVehiclePhotos}
	for n := 1; n <= 4; n++ {
		photos.SupportIDs = append(photos.SupportIDs, e.subir(t, n, "sub-carolina", domain.FormatJPEG))
	}
	return domain.ResubmissionRequest{ReviewSeq: 1, Supports: []domain.SupportGroup{photos,
		{RequirementCode: domain.SupportTrafficReport, SupportIDs: []string{e.subir(t, 5, "sub-carolina", domain.FormatPDF)}}}}
}

func TestReenviarLaRadicacionDevueltaLaVuelveAActivaConElMismoNumero(t *testing.T) {
	e := devuelto(t)
	ctx := context.Background()
	antes, err := e.casos.Get(ctx, e.claimID)
	require.NoError(t, err)

	r, created, err := e.svc.Resubmit(ctx, e.claimID, "sub-carolina", e.correccion(t))

	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, 1, r.Seq)
	assert.Equal(t, radicadoEn.Add(2*time.Hour), r.ResubmittedAt)
	assert.Equal(t, domain.StatusActive, e.estado(t))
	despues, err := e.casos.Get(ctx, e.claimID)
	require.NoError(t, err)
	assert.Equal(t, antes.FilingNumber, despues.FilingNumber)
	checklist := domain.Checklist(despues)
	assert.Equal(t, 4, checklist[0].Files, "las fotos nuevas reemplazan las del móvil")

	h, err := e.svc.History(ctx, e.claimID, "sub-carolina")
	require.NoError(t, err)
	require.Len(t, h.Resubmissions, 1)
	assert.Nil(t, h.LastReturn, "ya no está devuelta")
	assert.Equal(t, []domain.Status{domain.StatusActive, domain.StatusReturned, domain.StatusActive},
		[]domain.Status{h.Changes[0].Status, h.Changes[1].Status, h.Changes[2].Status})
}

func TestUnReintentoDelReenvioRespondeElMismoSinRepetirlo(t *testing.T) {
	e := devuelto(t)
	ctx := context.Background()
	in := e.correccion(t)
	first, _, err := e.svc.Resubmit(ctx, e.claimID, "sub-carolina", in)
	require.NoError(t, err)

	again, created, err := e.svc.Resubmit(ctx, e.claimID, "sub-carolina", in)

	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, first.Seq, again.Seq)
	assert.Equal(t, domain.StatusActive, again.Claim.Status)
	h, err := e.svc.History(ctx, e.claimID, "")
	require.NoError(t, err)
	assert.Len(t, h.Resubmissions, 1)
}

func TestSoloElDuenoReenviaSuRadicacion(t *testing.T) {
	e := devuelto(t)
	ctx := context.Background()

	_, _, err := e.svc.Resubmit(ctx, e.claimID, "sub-otro", e.correccion(t))
	assert.ErrorIs(t, err, review.ErrNotOwner)
	_, _, err = e.svc.Resubmit(ctx, "no-existe", "sub-carolina", domain.ResubmissionRequest{ReviewSeq: 1})
	assert.ErrorIs(t, err, ports.ErrClaimNotFound)
	assert.Equal(t, domain.StatusReturned, e.estado(t))
}

func TestUnaRadicacionQueNoEstaDevueltaNoSeReenvia(t *testing.T) {
	e := nuevo(t)
	store := memory.NewReviewStore(e.casos)
	svc := review.NewService(e.casos, store, store, e.lector, reloj{radicadoEn}, nil).WithResubmissions(store, e.casos)

	_, _, err := svc.Resubmit(context.Background(), e.claimID, "sub-carolina", domain.ResubmissionRequest{ReviewSeq: 1})

	assert.ErrorIs(t, err, domain.ErrNotReturned)
}

func TestUnReenvioIncompletoNoCambiaNada(t *testing.T) {
	e := devuelto(t)

	_, _, err := e.svc.Resubmit(context.Background(), e.claimID, "sub-carolina", domain.ResubmissionRequest{ReviewSeq: 1})

	var invalid *domain.ValidationError
	require.ErrorAs(t, err, &invalid)
	assert.Len(t, invalid.Fields, 2, "las fotos sin reemplazar y el informe que falta")
	assert.Equal(t, domain.StatusReturned, e.estado(t))
}

func TestUnSoporteQueOtraRadicacionTomoSeInformaComoNoEncontrado(t *testing.T) {
	e := devuelto(t)
	e.almacen.err = ports.ErrSupportAlreadyAttached

	_, _, err := e.svc.Resubmit(context.Background(), e.claimID, "sub-carolina", e.correccion(t))

	var invalid *domain.ValidationError
	require.ErrorAs(t, err, &invalid)
	assert.Equal(t, []domain.FieldError{{Field: "supports", Code: domain.CodeNotFound}}, invalid.Fields)
}

func TestSiOtroCambioGanaLaCarreraYNoHayReenvioSuyoSeInforma(t *testing.T) {
	e := devuelto(t)
	// Entre la lectura y la escritura, el caso dejó de estar devuelto sin un
	// reenvío de este asegurado.
	e.almacen.err = domain.ErrNotReturned

	r, created, err := e.svc.Resubmit(context.Background(), e.claimID, "sub-carolina", e.correccion(t))

	assert.ErrorIs(t, err, domain.ErrNotReturned)
	assert.False(t, created)
	assert.Zero(t, r.Seq)
}

func TestUnFalloDelAlmacenSeDevuelve(t *testing.T) {
	e := devuelto(t)
	e.almacen.err = errors.New("la base no responde")

	_, _, err := e.svc.Resubmit(context.Background(), e.claimID, "sub-carolina", e.correccion(t))

	assert.ErrorContains(t, err, "la base no responde")
}

func TestSinAlmacenDeReenviosNoSeReenvia(t *testing.T) {
	e := nuevo(t)

	_, _, err := e.svc.Resubmit(context.Background(), e.claimID, "sub-carolina", domain.ResubmissionRequest{ReviewSeq: 1})

	assert.ErrorContains(t, err, "not configured")
}
