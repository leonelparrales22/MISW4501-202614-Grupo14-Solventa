package evaluation_test

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
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/filing"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/triage"
)

var radicadoEn = time.Date(2026, 9, 3, 19, 20, 0, 0, time.UTC)

type triageFalso struct {
	pedidos []string
	err     error
}

func (f *triageFalso) Summaries(_ context.Context, ids []string) ([]triage.Summary, error) {
	f.pedidos = ids
	out := make([]triage.Summary, 0, len(ids))
	for _, id := range ids {
		out = append(out, triage.Summary{ClaimID: id, State: domain.TriagePending})
	}
	return out, f.err
}

func (f *triageFalso) View(_ context.Context, id string) (triage.View, error) {
	return triage.View{ClaimID: id, State: domain.TriagePending}, f.err
}

type firmasFalsas struct{}

func (firmasFalsas) EvidenceLinks(_ context.Context, c domain.ClaimRecord) []filing.EvidenceLink {
	return make([]filing.EvidenceLink, len(c.Evidence))
}

type polizasFalsas struct {
	terms     domain.PolicyTerms
	holder    domain.Holder
	errTerms  error
	errHolder error
}

func (p polizasFalsas) PolicyTerms(context.Context, string, string) (domain.PolicyTerms, error) {
	return p.terms, p.errTerms
}

func (p polizasFalsas) Holder(context.Context, string) (domain.Holder, error) {
	return p.holder, p.errHolder
}

type almacenFalso struct{ ports.ObjectStore }

func (almacenFalso) SignDownload(_ context.Context, key string, ttl time.Duration) (ports.SignedDownload, error) {
	return ports.SignedDownload{URL: "https://firmada/" + key, ExpiresAt: radicadoEn.Add(ttl)}, nil
}

// radicar guarda un siniestro web en el almacén en memoria y devuelve su id.
func radicar(t *testing.T, store *memory.CaseStore, key string, at time.Time, status domain.Status) string {
	t.Helper()
	c := domain.ClaimRecord{
		Claim: domain.Claim{ID: "claim-" + key, Subject: "sub-carolina", FilingKey: key, PolicyID: "HG-2025-880231",
			IncidentType: domain.IncidentWaterDamage, OccurredAt: at.Add(-time.Hour), Place: "Calle 1",
			Description: "Filtración en la cocina.", Status: status, FiledAt: at},
		Channel: domain.ChannelWeb,
	}
	stored, _, err := store.CreateWeb(context.Background(), c)
	require.NoError(t, err)
	return stored.ID
}

func servicio(store *memory.CaseStore, p ports.PolicyTermsReader, tri *triageFalso) *evaluation.Service {
	return evaluation.NewService(store, store, tri, firmasFalsas{}, p, almacenFalso{})
}

func TestLaColaTraeLosSiniestrosEnEvaluacionDelMasAntiguoAlMasNuevo(t *testing.T) {
	store := memory.NewCaseStore()
	segundo := radicar(t, store, "k2", radicadoEn.Add(time.Hour), domain.StatusActive)
	primero := radicar(t, store, "k1", radicadoEn, domain.StatusInEvaluation)
	radicar(t, store, "k3", radicadoEn.Add(2*time.Hour), domain.Status("paid"))
	tri := &triageFalso{}

	page, err := servicio(store, nil, tri).Queue(context.Background(), 0, "")

	require.NoError(t, err)
	require.Len(t, page.Items, 2, "un siniestro que salió de evaluación no está en la cola")
	assert.Equal(t, primero, page.Items[0].Claim.ID)
	assert.Equal(t, segundo, page.Items[1].Claim.ID)
	assert.Equal(t, []string{primero, segundo}, tri.pedidos, "la prioridad se pide en una sola lectura")
	assert.Equal(t, domain.TriagePending, page.Items[0].Triage.State)
	assert.Empty(t, page.NextPageToken)
}

func TestLaColaSePagina(t *testing.T) {
	store := memory.NewCaseStore()
	var ids []string
	for i := 0; i < 5; i++ {
		ids = append(ids, radicar(t, store, "k"+string(rune('a'+i)), radicadoEn.Add(time.Duration(i)*time.Minute),
			domain.StatusActive))
	}
	svc := servicio(store, nil, &triageFalso{})

	var vistos []string
	token := ""
	for {
		page, err := svc.Queue(context.Background(), 2, token)
		require.NoError(t, err)
		for _, it := range page.Items {
			vistos = append(vistos, it.Claim.ID)
		}
		if page.NextPageToken == "" {
			break
		}
		token = page.NextPageToken
	}
	assert.Equal(t, ids, vistos)
}

func TestLaColaRechazaUnaPaginaInvalida(t *testing.T) {
	svc := servicio(memory.NewCaseStore(), nil, &triageFalso{})
	for _, tc := range []struct {
		size  int
		token string
	}{{-1, ""}, {evaluation.MaxPageSize + 1, ""}, {0, "%%%"}, {0, "c2luLWN1cnNvcg"}} {
		_, err := svc.Queue(context.Background(), tc.size, tc.token)
		assert.ErrorIs(t, err, evaluation.ErrInvalidQuery, tc)
	}
}

func TestElDetalleTraeLaCoberturaDelIncidenteYElAsegurado(t *testing.T) {
	store := memory.NewCaseStore()
	id := radicar(t, store, "k1", radicadoEn, domain.StatusActive)
	agua := domain.Coverage{Incident: domain.IncidentWaterDamage, InsuredValueMinor: 32_000_000_000, Currency: "COP",
		DeductibleBasisPoints: 500}
	p := polizasFalsas{
		terms:  domain.PolicyTerms{Policy: domain.Policy{ID: "HG-2025-880231"}, Coverages: []domain.Coverage{agua}},
		holder: domain.Holder{FullName: "Carolina Pérez Ruiz"},
	}

	e, err := servicio(store, p, &triageFalso{}).Detail(context.Background(), id)

	require.NoError(t, err)
	assert.Equal(t, id, e.Claim.ID)
	require.NotNil(t, e.Coverage)
	assert.Equal(t, agua, *e.Coverage)
	require.NotNil(t, e.Holder)
	assert.Equal(t, "Carolina Pérez Ruiz", e.Holder.FullName)
	assert.False(t, e.PolicyUnavailable)
	assert.Equal(t, domain.TriagePending, e.Triage.State)
}

func TestUnIncidenteQueLaPolizaNoCubreNoTraeCobertura(t *testing.T) {
	store := memory.NewCaseStore()
	id := radicar(t, store, "k1", radicadoEn, domain.StatusActive)
	p := polizasFalsas{terms: domain.PolicyTerms{Coverages: []domain.Coverage{{Incident: domain.IncidentCollision}}}}

	e, err := servicio(store, p, &triageFalso{}).Detail(context.Background(), id)

	require.NoError(t, err)
	assert.NotNil(t, e.Terms)
	assert.Nil(t, e.Coverage)
}

func TestSiElNucleoDePolizasNoRespondeElDetalleLoDice(t *testing.T) {
	store := memory.NewCaseStore()
	id := radicar(t, store, "k1", radicadoEn, domain.StatusActive)
	for nombre, p := range map[string]ports.PolicyTermsReader{
		"sin núcleo":    nil,
		"póliza caída":  polizasFalsas{errTerms: ports.ErrPolicyDirectoryUnavailable},
		"titular caído": polizasFalsas{errHolder: ports.ErrPolicyDirectoryUnavailable},
	} {
		e, err := servicio(store, p, &triageFalso{}).Detail(context.Background(), id)

		require.NoError(t, err, nombre)
		assert.True(t, e.PolicyUnavailable, nombre)
		assert.Equal(t, id, e.Claim.ID, "%s: el resto del siniestro se sigue mostrando", nombre)
	}
}

func TestUnaPolizaQueElNucleoYaNoConoceNoEsUnaFalla(t *testing.T) {
	store := memory.NewCaseStore()
	id := radicar(t, store, "k1", radicadoEn, domain.StatusActive)
	p := polizasFalsas{errTerms: ports.ErrPolicyNotFound, errHolder: ports.ErrHolderNotFound}

	e, err := servicio(store, p, &triageFalso{}).Detail(context.Background(), id)

	require.NoError(t, err)
	assert.False(t, e.PolicyUnavailable)
	assert.Nil(t, e.Terms)
	assert.Nil(t, e.Holder)
}

func TestElDetalleDeUnSiniestroQueNoExiste(t *testing.T) {
	_, err := servicio(memory.NewCaseStore(), nil, &triageFalso{}).Detail(context.Background(), "no-existe")

	assert.ErrorIs(t, err, ports.ErrClaimNotFound)
}

func TestSiElTriageNoSeLeeElDetalleFalla(t *testing.T) {
	store := memory.NewCaseStore()
	id := radicar(t, store, "k1", radicadoEn, domain.StatusActive)

	_, err := servicio(store, nil, &triageFalso{err: errors.New("se cayó")}).Detail(context.Background(), id)

	assert.Error(t, err)
}

func TestLasExcepcionesDePagoSeListanAparte(t *testing.T) {
	store := memory.NewCaseStore()
	radicar(t, store, "k1", radicadoEn, domain.StatusActive)
	excepcion := radicar(t, store, "k2", radicadoEn.Add(time.Hour), domain.StatusPaymentException)

	page, err := servicio(store, nil, &triageFalso{}).Exceptions(context.Background(), 0, "")

	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, excepcion, page.Items[0].Claim.ID)
}

type decisionesFalsas struct {
	d   domain.Decision
	err error
}

func (f decisionesFalsas) Decision(context.Context, string) (domain.Decision, error) {
	return f.d, f.err
}

func TestElDetalleTraeLaDecisionCuandoLaHay(t *testing.T) {
	store := memory.NewCaseStore()
	id := radicar(t, store, "k1", radicadoEn, domain.StatusRejected)

	sin, err := servicio(store, nil, &triageFalso{}).WithDecisions(decisionesFalsas{err: ports.ErrDecisionNotFound}).
		Detail(context.Background(), id)
	require.NoError(t, err)
	assert.Nil(t, sin.Decision)

	con, err := servicio(store, nil, &triageFalso{}).WithDecisions(decisionesFalsas{d: domain.Decision{ClaimID: id,
		Outcome: domain.OutcomeRejected}}).Detail(context.Background(), id)
	require.NoError(t, err)
	require.NotNil(t, con.Decision)
	assert.Equal(t, domain.OutcomeRejected, con.Decision.Outcome)

	_, err = servicio(store, nil, &triageFalso{}).WithDecisions(decisionesFalsas{err: errors.New("se cayó")}).
		Detail(context.Background(), id)
	assert.Error(t, err)
}
