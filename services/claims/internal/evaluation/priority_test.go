package evaluation_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/evaluation"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// HU-W2.2-02: la cola de operaciones ordenada por prioridad (O5).

type reloj struct{ ahora time.Time }

func (r reloj) Now() time.Time { return r.ahora }

// escenarioCola tiene casos en evaluación con su nivel y el servicio que los
// ordena.
type escenarioCola struct {
	casos      *memory.CaseStore
	decisiones *memory.DecisionStore
	niveles    map[string]int
	svc        *evaluation.Service
	lecturas   atomic.Int32
}

// asegurados lee el asegurado de cada sujeto y cuenta las lecturas.
type asegurados struct {
	e   *escenarioCola
	err error
}

func (a asegurados) PolicyTerms(context.Context, string, string) (domain.PolicyTerms, error) {
	return domain.PolicyTerms{}, nil
}

func (a asegurados) Holder(_ context.Context, sujeto string) (domain.Holder, error) {
	a.e.lecturas.Add(1)
	if a.err != nil {
		return domain.Holder{}, a.err
	}
	return domain.Holder{FullName: "Asegurado " + sujeto, DocumentType: "CC", DocumentID: "1"}, nil
}

func nuevaCola(t *testing.T, errAsegurado error) *escenarioCola {
	t.Helper()
	e := &escenarioCola{casos: memory.NewCaseStore(), niveles: map[string]int{}}
	e.decisiones = memory.NewDecisionStore(e.casos)
	niveles := func(_ context.Context, ids []string) (map[string]int, error) {
		out := map[string]int{}
		for _, id := range ids {
			if n, ok := e.niveles[id]; ok {
				out[id] = n
			}
		}
		return out, nil
	}
	cola := memory.NewPriorityQueue(e.casos, e.decisiones, niveles)
	e.svc = evaluation.NewService(e.casos, e.casos, &triageFalso{}, firmasFalsas{}, asegurados{e: e, err: errAsegurado}, almacenFalso{}).
		WithPriorityQueue(cola, reloj{radicadoEn.Add(48 * time.Hour)})
	return e
}

// radicarCon guarda un caso en evaluación de un tipo, con su nivel (0: sin
// nivel todavía) y la hora en que se radicó.
func (e *escenarioCola) radicarCon(t *testing.T, clave string, tipo domain.IncidentType, nivel int, hace time.Duration) string {
	t.Helper()
	stored, _, err := e.casos.CreateWeb(context.Background(), domain.ClaimRecord{
		Claim: domain.Claim{ID: "claim-" + clave, Subject: "sub-" + clave[:1], FilingKey: clave, IncidentType: tipo,
			Status: domain.StatusInEvaluation, FiledAt: radicadoEn.Add(-hace)},
		Channel: domain.ChannelWeb,
	})
	require.NoError(t, err)
	if nivel > 0 {
		e.niveles[stored.ID] = nivel
	}
	return stored.ID
}

func ids(p evaluation.PriorityPage) []string {
	out := make([]string, 0, len(p.Items))
	for _, it := range p.Items {
		out = append(out, it.Claim.ID)
	}
	return out
}

func TestLaColaVaPorTriage1Luego2Luego3YEnCadaNivelElQueMasEspera(t *testing.T) {
	e := nuevaCola(t, nil)
	t3 := e.radicarCon(t, "a3", domain.IncidentGlass, 3, 50*time.Hour)
	t2nuevo := e.radicarCon(t, "b2", domain.IncidentWaterDamage, 2, time.Hour)
	t1 := e.radicarCon(t, "c1", domain.IncidentTheft, 1, 2*time.Hour)
	t2viejo := e.radicarCon(t, "d2", domain.IncidentCollision, 2, 30*time.Hour)
	sinNivel := e.radicarCon(t, "e0", domain.IncidentCollision, 0, 90*time.Hour)
	radicar(t, e.casos, "devuelto", radicadoEn, domain.StatusReturned)
	radicar(t, e.casos, "activo", radicadoEn, domain.StatusActive)

	page, err := e.svc.PriorityQueue(context.Background(), 0, "", evaluation.QueueFilter{})

	require.NoError(t, err)
	assert.Equal(t, []string{t1, t2viejo, t2nuevo, t3, sinNivel}, ids(page),
		"Triage 1, 2 y 3, en cada uno el que más espera, y al final lo que el triage aún no calcula")
	assert.Equal(t, evaluation.QueueSummary{ByLevel: map[int]int{1: 1, 2: 2, 3: 1}, Unassessed: 1, Total: 5, Matching: 5},
		page.Summary, "lo devuelto o pendiente del asesor no es de la cola")
}

func TestUnCambioDeNivelMueveElCasoEnLaSiguienteConsulta(t *testing.T) {
	e := nuevaCola(t, nil)
	primero := e.radicarCon(t, "a1", domain.IncidentTheft, 1, time.Hour)
	segundo := e.radicarCon(t, "b2", domain.IncidentCollision, 2, 10*time.Hour)

	page, err := e.svc.PriorityQueue(context.Background(), 0, "", evaluation.QueueFilter{})
	require.NoError(t, err)
	require.Equal(t, []string{primero, segundo}, ids(page))

	e.niveles[segundo] = 1
	page, err = e.svc.PriorityQueue(context.Background(), 0, "", evaluation.QueueFilter{})

	require.NoError(t, err)
	assert.Equal(t, []string{segundo, primero}, ids(page), "ahora los dos son Triage 1 y espera más el segundo")
}

func TestLaColaSePaginaSinRepetirNiSaltarCasos(t *testing.T) {
	e := nuevaCola(t, nil)
	var todos []string
	for i, nivel := range []int{3, 1, 2, 1, 0, 2, 3} {
		todos = append(todos, e.radicarCon(t, string(rune('a'+i))+"x", domain.IncidentCollision, nivel, time.Duration(i)*time.Hour))
	}

	var vistos []string
	token := ""
	for {
		page, err := e.svc.PriorityQueue(context.Background(), 2, token, evaluation.QueueFilter{})
		require.NoError(t, err)
		vistos = append(vistos, ids(page)...)
		if page.NextPageToken == "" {
			break
		}
		token = page.NextPageToken
	}

	assert.ElementsMatch(t, todos, vistos)
	assert.Len(t, vistos, len(todos))
	assert.Equal(t, todos[3], vistos[0], "el Triage 1 que más espera")
	assert.Equal(t, todos[4], vistos[len(vistos)-1], "el que no tiene nivel va al final")
}

func TestLosFiltrosDeNivelYProductoCuentanLoQueCoincide(t *testing.T) {
	e := nuevaCola(t, nil)
	e.radicarCon(t, "a1", domain.IncidentTheft, 1, time.Hour)
	agua := e.radicarCon(t, "b2", domain.IncidentWaterDamage, 2, time.Hour)
	choque := e.radicarCon(t, "c2", domain.IncidentCollision, 2, 2*time.Hour)

	page, err := e.svc.PriorityQueue(context.Background(), 0, "", evaluation.QueueFilter{Level: 2})
	require.NoError(t, err)
	assert.Equal(t, []string{choque, agua}, ids(page))
	assert.Equal(t, 2, page.Summary.Matching)
	assert.Equal(t, 3, page.Summary.Total, "los indicadores son de toda la cola")

	page, err = e.svc.PriorityQueue(context.Background(), 0, "", evaluation.QueueFilter{Line: domain.LineHome})
	require.NoError(t, err)
	assert.Equal(t, []string{agua}, ids(page))
	assert.Equal(t, 1, page.Summary.Matching)

	page, err = e.svc.PriorityQueue(context.Background(), 0, "",
		evaluation.QueueFilter{Level: ports.RankUnassessed, Status: domain.StatusInEvaluation})
	require.NoError(t, err)
	assert.Empty(t, page.Items)
	assert.Zero(t, page.Summary.Matching)
}

func TestUnaColaVaciaNoInventaIndicadores(t *testing.T) {
	e := nuevaCola(t, nil)

	page, err := e.svc.PriorityQueue(context.Background(), 0, "", evaluation.QueueFilter{})

	require.NoError(t, err)
	assert.Empty(t, page.Items)
	assert.Empty(t, page.NextPageToken)
	assert.Equal(t, evaluation.QueueSummary{ByLevel: map[int]int{1: 0, 2: 0, 3: 0}}, page.Summary)
	assert.Nil(t, page.Summary.MeanResolution, "sin casos decididos no hay tiempo medio")
}

func TestElTiempoMedioDeResolucionEsDeLosUltimos30Dias(t *testing.T) {
	e := nuevaCola(t, nil)
	decidir := func(clave string, radicado, decidido time.Time) {
		id := radicar(t, e.casos, clave, radicado, domain.StatusInEvaluation)
		_, err := e.decisiones.Decide(context.Background(), domain.Decision{ClaimID: id, Outcome: domain.OutcomeRejected,
			DecidedBy: "analista", DecidedAt: decidido, Reason: "x", Notice: &domain.Notice{To: "c@correo.example"}})
		require.NoError(t, err)
	}
	ahora := radicadoEn.Add(48 * time.Hour)
	decidir("k1", ahora.Add(-5*24*time.Hour), ahora.Add(-1*24*time.Hour))   // 4 días
	decidir("k2", ahora.Add(-10*24*time.Hour), ahora.Add(-5*24*time.Hour))  // 5 días
	decidir("k3", ahora.Add(-90*24*time.Hour), ahora.Add(-40*24*time.Hour)) // fuera de la ventana

	page, err := e.svc.PriorityQueue(context.Background(), 0, "", evaluation.QueueFilter{})

	require.NoError(t, err)
	require.NotNil(t, page.Summary.MeanResolution)
	assert.Equal(t, 108*time.Hour, *page.Summary.MeanResolution, "4,5 días")
	assert.Equal(t, 2, page.Summary.Decided)
}

func TestCadaCasoTraeSuAseguradoLeidoUnaVezPorPersona(t *testing.T) {
	e := nuevaCola(t, nil)
	e.radicarCon(t, "a1", domain.IncidentTheft, 1, time.Hour)
	e.radicarCon(t, "a2", domain.IncidentTheft, 2, time.Hour) // el mismo asegurado: sub-a
	e.radicarCon(t, "b2", domain.IncidentTheft, 2, time.Hour)

	page, err := e.svc.PriorityQueue(context.Background(), 0, "", evaluation.QueueFilter{})

	require.NoError(t, err)
	require.Len(t, page.Items, 3)
	for _, it := range page.Items {
		require.NotNil(t, it.Holder, it.Claim.ID)
		assert.Equal(t, "Asegurado "+it.Claim.Subject, it.Holder.FullName)
	}
	assert.EqualValues(t, 2, e.lecturas.Load(), "una lectura por asegurado, no por caso")
}

func TestSiElNucleoNoRespondeLaColaSaleSinElAsegurado(t *testing.T) {
	e := nuevaCola(t, errors.New("se cayó"))
	e.radicarCon(t, "a1", domain.IncidentTheft, 1, time.Hour)

	page, err := e.svc.PriorityQueue(context.Background(), 0, "", evaluation.QueueFilter{})

	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Nil(t, page.Items[0].Holder)
}

func TestLaColaPorPrioridadRechazaLoQueNoEsValido(t *testing.T) {
	e := nuevaCola(t, nil)
	for name, tc := range map[string]struct {
		size   int
		token  string
		filtro evaluation.QueueFilter
	}{
		"página grande":        {size: 51},
		"página negativa":      {size: -1},
		"token roto":           {token: "%%%"},
		"token de otra cola":   {token: "MjAyNi0wOS0wM1QxOToyMDowMFp8aWQ"},
		"nivel fuera de rango": {filtro: evaluation.QueueFilter{Level: 5}},
		"estado de otra cola":  {filtro: evaluation.QueueFilter{Status: domain.StatusActive}},
		"ramo sin tipos":       {filtro: evaluation.QueueFilter{Line: "travel"}},
	} {
		_, err := e.svc.PriorityQueue(context.Background(), tc.size, tc.token, tc.filtro)
		assert.ErrorIs(t, err, evaluation.ErrInvalidQuery, name)
	}

	sinCola := evaluation.NewService(e.casos, e.casos, &triageFalso{}, firmasFalsas{}, nil, almacenFalso{})
	_, err := sinCola.PriorityQueue(context.Background(), 0, "", evaluation.QueueFilter{})
	assert.Error(t, err, "sin la cola por prioridad configurada")
}
