package integration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/email"
	corepolicies "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/policies"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/mail"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/pdf"
	policiesadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/policies"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/assessors"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/evaluation"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/filing"
)

// Every acceptance criterion of HU-W2.3-01 (assignment of the nearest
// provider) over HTTP: the real router, use cases and worker, PostgreSQL in a
// container with the reference providers of 0009_provider_seed, the claim
// filed through the real web filing and approved through the real review of
// the advisor. Only the policy core, the token verification, the bucket and
// the e-mail provider are doubles; the e-mail provider fails twice before it
// accepts.

func init() {
	verifier["coordinadora"] = security.Claims{Subject: "sub-coordinadora", Email: "laura.mora@solventa.example",
		Name: "Laura Mora", Roles: []security.Role{security.RoleOperations}}
}

// incidentAt is where the water damage of Carolina happened (the mockup):
// 2.3 km from Servicios Hogar Norte, 3.8 km from Reparaciones Chapinero and
// 6.1 km from Técnicos Unidos.
var incidentAt = domain.Coordinates{Latitude: 4.6771, Longitude: -74.0483}

// flakyMailbox answers /messages like the e-mail stub, failing the first
// failures requests.
type flakyMailbox struct {
	srv *httptest.Server

	mu        sync.Mutex
	failures  int
	attempts  map[string]int
	delivered map[string]email.Message
}

func newFlakyMailbox(t *testing.T, failures int) *flakyMailbox {
	t.Helper()
	m := &flakyMailbox{failures: failures, attempts: map[string]int{}, delivered: map[string]email.Message{}}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in email.Message
		_ = json.NewDecoder(r.Body).Decode(&in)
		key := r.Header.Get("Idempotency-Key")
		m.mu.Lock()
		defer m.mu.Unlock()
		m.attempts[key]++
		if m.failures > 0 {
			m.failures--
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		m.delivered[key] = in
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *flakyMailbox) delivery(key string) (email.Message, int, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	msg, ok := m.delivered[key]
	return msg, m.attempts[key], ok
}

// startWithAssignment is the claims service as cmd/claims composes it with
// DATABASE_URL, AUTH_*, POLICY_CORE_URL and EMAIL_URL, with the worker of the
// assignment searching again every 200 ms instead of every 5 minutes.
func startWithAssignment(t *testing.T, url string, core *policyCore, m *flakyMailbox) service {
	t.Helper()
	ctx := context.Background()
	db, err := postgres.Open(ctx, url)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, postgres.Migrate(ctx, db))

	breaker := resilience.Config{ConsecutiveFailures: 100, OpenTimeout: time.Second, HalfOpenSuccesses: 1}
	records, supports, objects := postgres.NewClaimRecordStore(db), postgres.NewSupportStore(db), newBucket()
	directory := policiesadapter.NewDirectory(corepolicies.NewClient(corepolicies.Config{
		BaseURL: core.srv.URL, Timeout: 500 * time.Millisecond, Breaker: breaker}))
	priorities := startSweep(t, db)

	store, providers := postgres.AssignmentStoreOf(records), postgres.ProviderStoreOf(records)
	var worker *assessors.Worker
	assignment := assessors.NewService(store, providers, clock{}, func() { worker.Notify() })
	worker = assessors.NewWorker(assignment, store,
		mail.NewMailer(email.NewClient(email.Config{BaseURL: m.srv.URL, Breaker: breaker})), clock{},
		assessors.WorkerConfig{RetryInterval: 200 * time.Millisecond, NoticePoll: 50 * time.Millisecond,
			NoticeBackoff: []time.Duration{50 * time.Millisecond}})
	wctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		worker.Run(wctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	return service{db: db, bucket: objects, Handler: httpadapter.NewRouter(
		httpadapter.WithClaimReads(filing.NewQueryService(records, pdf.NewReceiptRenderer()).WithObjects(objects), verifier),
		httpadapter.WithWebFilings(filing.NewWebService(records, supports, directory, clock{}),
			filing.NewSupportService(supports, objects, clock{}), verifier),
		withReviews(records, evaluation.NewService(records, records, priorities, nil, nil, objects)),
		httpadapter.WithAssignment(assignment, assessors.DefaultRetryInterval, verifier),
		httpadapter.WithProviders(assessors.NewProviderService(providers, clock{}), verifier),
	)}
}

// fileFromThePhone stores a claim of water damage filed from the app with a
// trusted capture at the place of the incident, approved by the advisor.
func fileFromThePhone(t *testing.T, svc service, id string) string {
	t.Helper()
	_, _, err := postgres.NewClaimRecordStore(svc.db).Create(context.Background(), domain.Claim{
		ID: id, Subject: "sub-carolina", FilingKey: "draft-" + id, PolicyID: "HG-2025-880231",
		IncidentType: domain.IncidentWaterDamage, OccurredAt: now.Add(-2 * time.Hour), Place: "Carrera 15 # 93-40",
		Description: "Filtración desde el apartamento superior.", Details: map[string]string{},
		Status: domain.StatusInEvaluation, FiledAt: now.Add(-time.Hour),
		Evidence: []domain.Evidence{
			{ObjectKey: "evidence/sub-carolina/" + id + "-1.jpg", Kind: domain.KindPhoto, CapturedAt: now.Add(-90 * time.Minute),
				Latitude: incidentAt.Latitude, Longitude: incidentAt.Longitude, AccuracyM: 8, LocationTrusted: true,
				SHA256: strings.Repeat("a", 64), ByteSize: 312044},
			{ObjectKey: "evidence/sub-carolina/" + id + "-2.jpg", Kind: domain.KindPhoto, CapturedAt: now.Add(-89 * time.Minute),
				Latitude: 4.70, Longitude: -74.05, AccuracyM: 40, LocationTrusted: true,
				SHA256: strings.Repeat("b", 64), ByteSize: 312044},
		},
	})
	require.NoError(t, err)
	return id
}

func assignmentOf(t *testing.T, svc service, id string) map[string]any {
	t.Helper()
	rec, body := call(t, svc, http.MethodGet, "/v1/claims/"+id+"/assignment", "coordinadora", nil, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	return body
}

func searchFor(t *testing.T, svc service, id string, body any) map[string]any {
	t.Helper()
	rec, out := postJSON(t, svc, "/v1/claims/"+id+"/provider-searches", "coordinadora", body)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	return out
}

func setAvailability(t *testing.T, svc service, providerID string, body map[string]any) map[string]any {
	t.Helper()
	rec, out := postJSON(t, svc, "/v1/claims/providers/"+providerID+"/availability", "coordinadora", body)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	return out
}

func lastSearch(body map[string]any) map[string]any { return body["last_search"].(map[string]any) }

func candidateIDs(search map[string]any) map[string]string {
	out := map[string]string{}
	for _, c := range search["candidates"].([]any) {
		candidate := c.(map[string]any)
		out[candidate["provider_id"].(string)] = candidate["eligibility"].(string)
	}
	return out
}

func attemptsOf(search map[string]any) [][2]float64 {
	var out [][2]float64
	for _, a := range search["attempts"].([]any) {
		attempt := a.(map[string]any)
		out = append(out, [2]float64{attempt["radius_km"].(float64), attempt["eligible_count"].(float64)})
	}
	return out
}

func TestProviderAssignmentAcceptanceCriteria(t *testing.T) {
	requireDocker(t)
	mailbox := newFlakyMailbox(t, 2)
	svc := startWithAssignment(t, newDatabase(t), newPolicyCore(t), mailbox)
	web := admit(t, svc, fileClaim(t, svc, "form-asignacion", waterDamageBody(t, svc, "carolina", cents(4_800_000))))

	t.Run("only operations assigns and keeps the registry", func(t *testing.T) {
		for _, token := range []string{"carolina", "advisor", "partner"} {
			for _, path := range []string{"/v1/claims/" + web + "/assignment", "/v1/claims/providers",
				"/v1/claims/assignment-policy"} {
				rec, _ := call(t, svc, http.MethodGet, path, token, nil, nil)
				assert.Equal(t, http.StatusForbidden, rec.Code, "%s as %s", path, token)
			}
			rec, _ := postJSON(t, svc, "/v1/claims/"+web+"/provider-searches", token, map[string]any{})
			assert.Equal(t, http.StatusForbidden, rec.Code, token)
		}
		rec, _ := call(t, svc, http.MethodGet, "/v1/claims/"+web+"/assignment", "", nil, nil)
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})

	t.Run("the policy in force and the reference providers", func(t *testing.T) {
		rec, policy := call(t, svc, http.MethodGet, "/v1/claims/assignment-policy", "coordinadora", nil, nil)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "assignment-policy-v1", policy["version"])
		assert.Equal(t, []any{5.0, 10.0, 20.0}, policy["radii_km"])
		assert.Equal(t, 300.0, policy["retry_interval_seconds"])

		rec, list := call(t, svc, http.MethodGet, "/v1/claims/providers?specialty=plumbing", "coordinadora", nil, nil)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Len(t, list["items"], 3)
	})

	t.Run("CA-1 a web claim without coordinates waits for the address and the pin of the analyst", func(t *testing.T) {
		body := assignmentOf(t, svc, web)
		assert.Equal(t, "in_evaluation", body["claim_status"], "approved through the real review of the advisor")
		assert.Equal(t, "location_missing", body["state"])
		assert.Equal(t, "plumbing", body["specialty"])
		assert.Nil(t, body["location"])

		rec, out := postJSON(t, svc, "/v1/claims/"+web+"/provider-searches", "coordinadora", map[string]any{})
		assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
		assert.Equal(t, "location_missing", out["error"])

		rec, out = postJSON(t, svc, "/v1/claims/"+web+"/incident-location", "coordinadora",
			map[string]any{"address": "Carrera 15 # 93-40, apto 402, Bogotá", "latitude": 91})
		assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
		assert.Len(t, out["fields"], 2, "every invalid field at once")

		rec, out = postJSON(t, svc, "/v1/claims/"+web+"/incident-location", "coordinadora",
			map[string]any{"address": "Carrera 15 # 93-40, apto 402, Bogotá", "latitude": incidentAt.Latitude,
				"longitude": incidentAt.Longitude})
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
		assert.Equal(t, "ready", out["state"])
		location := out["location"].(map[string]any)
		assert.Equal(t, "analyst", location["source"])
		assert.Equal(t, "Carrera 15 # 93-40, apto 402, Bogotá", location["address"])
		assert.Equal(t, map[string]any{"email": "laura.mora@solventa.example", "name": "Laura Mora"},
			location["recorded_by"])
	})

	t.Run("CA-1 a claim of the phone is searched from its most precise trusted capture", func(t *testing.T) {
		phone := fileFromThePhone(t, svc, "11111111-1111-4111-8111-111111111111")
		body := assignmentOf(t, svc, phone)
		assert.Equal(t, "ready", body["state"])
		location := body["location"].(map[string]any)
		assert.Equal(t, "app", location["source"])
		assert.Equal(t, 8.0, location["accuracy_m"])
		assert.Equal(t, incidentAt.Latitude, location["latitude"])

		rec, out := postJSON(t, svc, "/v1/claims/"+phone+"/incident-location", "coordinadora",
			map[string]any{"address": "Otra dirección, Bogotá", "latitude": 4.7, "longitude": -74.05})
		assert.Equal(t, http.StatusConflict, rec.Code)
		assert.Equal(t, "location_from_app", out["error"])
	})

	t.Run("CA-2 and CA-3 the nearest available provider with the specialty is proposed", func(t *testing.T) {
		body := searchFor(t, svc, web, nil)
		assert.Equal(t, "proposed", body["state"])
		search := lastSearch(body)
		assert.Equal(t, "found", search["outcome"])
		assert.Equal(t, 5.0, search["radius_km"])
		assert.Equal(t, map[string]any{"email": "laura.mora@solventa.example", "name": "Laura Mora"}, search["searched_by"])
		assert.Equal(t, map[string]string{"prv-servicios-hogar-norte": "eligible",
			"prv-reparaciones-chapinero": "eligible", "prv-tecnicos-unidos": "out_of_radius"}, candidateIDs(search),
			"only the providers of plumbing")
		first := search["candidates"].([]any)[0].(map[string]any)
		assert.Equal(t, "prv-servicios-hogar-norte", first["provider_id"])
		assert.Equal(t, 2.3, first["distance_km"])
		assert.Equal(t, true, first["suggested"])
	})

	t.Run("CA-4 the radius widens and every attempt is recorded", func(t *testing.T) {
		tomorrow := now.In(domain.ColombiaTime).AddDate(0, 0, 1).Format(time.DateOnly)
		setAvailability(t, svc, "prv-servicios-hogar-norte", map[string]any{"availability": "unavailable"})
		out := setAvailability(t, svc, "prv-reparaciones-chapinero",
			map[string]any{"availability": "unavailable", "unavailable_until": tomorrow})
		assert.Equal(t, "laura.mora@solventa.example", out["updated_by"])

		search := lastSearch(searchFor(t, svc, web, nil))
		assert.Equal(t, [][2]float64{{5, 0}, {10, 1}}, attemptsOf(search))
		assert.Equal(t, 10.0, search["radius_km"])
		assert.Equal(t, map[string]string{"prv-servicios-hogar-norte": "unavailable",
			"prv-reparaciones-chapinero": "unavailable", "prv-tecnicos-unidos": "eligible"}, candidateIDs(search),
			"available tomorrow is not available")
		assert.Equal(t, tomorrow, search["candidates"].([]any)[1].(map[string]any)["unavailable_until"])

		search = lastSearch(searchFor(t, svc, web, map[string]any{"from_radius_km": 20}))
		assert.Equal(t, [][2]float64{{20, 1}}, attemptsOf(search), "«Ampliar radio» starts from the radius asked")
		rec, out := postJSON(t, svc, "/v1/claims/"+web+"/provider-searches", "coordinadora",
			map[string]any{"from_radius_km": 7.5})
		assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
		assert.Equal(t, "validation_failed", out["error"])
	})

	t.Run("CA-6 without eligible providers the claim is pending assignment and nobody is assigned", func(t *testing.T) {
		setAvailability(t, svc, "prv-tecnicos-unidos", map[string]any{"availability": "unavailable"})
		body := searchFor(t, svc, web, nil)
		assert.Equal(t, "pending_assignment", body["state"])
		search := lastSearch(body)
		assert.Equal(t, "exhausted", search["outcome"])
		assert.Equal(t, "no_eligible_provider_within_max_radius", search["reason"])
		assert.Equal(t, [][2]float64{{5, 0}, {10, 0}, {20, 0}}, attemptsOf(search))

		rec, out := postJSON(t, svc, "/v1/claims/"+web+"/assignment", "coordinadora",
			map[string]any{"provider_id": "prv-tecnicos-unidos", "search_seq": search["seq"]})
		assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
		assert.Equal(t, "provider_not_eligible", out["error"])

		rec, page := call(t, svc, http.MethodGet, "/v1/claims/assignments?claim_id="+web, "coordinadora", nil, nil)
		require.Equal(t, http.StatusOK, rec.Code)
		item := page["items"].([]any)[0].(map[string]any)
		assert.Equal(t, "pending_assignment", item["state"])
		assert.Equal(t, "no_eligible_provider_within_max_radius", item["reason"])
	})

	t.Run("CA-6 the search repeats on its own and proposes, never assigns", func(t *testing.T) {
		require.Eventually(t, func() bool {
			return lastSearch(assignmentOf(t, svc, web))["trigger"] == "automatic_retry"
		}, 10*time.Second, 50*time.Millisecond, "the worker searches the pending claim again")

		setAvailability(t, svc, "prv-tecnicos-unidos", map[string]any{"availability": "available"})
		require.Eventually(t, func() bool { return assignmentOf(t, svc, web)["state"] == "proposed" },
			10*time.Second, 50*time.Millisecond)
		body := assignmentOf(t, svc, web)
		assert.Equal(t, "automatic_retry", lastSearch(body)["trigger"])
		assert.Nil(t, lastSearch(body)["searched_by"])
		assert.Nil(t, body["assignment"], "the analyst confirms")
	})

	t.Run("CA-5 the assignment is recorded and the provider receives the location once", func(t *testing.T) {
		seq := lastSearch(assignmentOf(t, svc, web))["seq"]
		rec, body := postJSON(t, svc, "/v1/claims/"+web+"/assignment", "coordinadora",
			map[string]any{"provider_id": "prv-tecnicos-unidos", "search_seq": seq})
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
		assert.Equal(t, "assigned", body["state"])
		a := body["assignment"].(map[string]any)
		assert.Equal(t, "prv-tecnicos-unidos", a["provider"].(map[string]any)["id"])
		assert.Equal(t, map[string]any{"email": "laura.mora@solventa.example", "name": "Laura Mora"}, a["assigned_by"])
		assert.Equal(t, 6.1, a["distance_km"])
		assert.NotEmpty(t, a["assigned_at"])
		assert.Equal(t, "Carrera 15 # 93-40, apto 402, Bogotá", a["location_shared"].(map[string]any)["address"])

		rec, provider := call(t, svc, http.MethodGet, "/v1/claims/providers/prv-tecnicos-unidos", "coordinadora", nil, nil)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "busy", provider["availability"])
		assert.Equal(t, web, provider["busy_claim_id"])

		key := "asignacion-" + web
		require.Eventually(t, func() bool {
			return assignmentOf(t, svc, web)["assignment"].(map[string]any)["notice"].(map[string]any)["status"] == "sent"
		}, 10*time.Second, 50*time.Millisecond, "the notice is retried until the provider accepts it")
		msg, attempts, delivered := mailbox.delivery(key)
		require.True(t, delivered)
		assert.Equal(t, 3, attempts, "two failures and one delivery, always with the same key")
		assert.Equal(t, "operaciones@tecnicosunidos.example.com", msg.To)
		assert.Contains(t, msg.Subject, "Asignación de siniestro SIN-")
		assert.Contains(t, msg.Body, "Ubicación: 4.677100, -74.048300")
		assert.Contains(t, msg.Body, "Dirección: Carrera 15 # 93-40, apto 402, Bogotá")
		assert.NotContains(t, msg.Body, "Carolina")
		notice := assignmentOf(t, svc, web)["assignment"].(map[string]any)["notice"].(map[string]any)
		assert.Equal(t, 3.0, notice["attempts"])

		assert.Equal(t, "in_evaluation", statusFor(t, svc, "carolina", web), "the status of the claim does not change")
		rec, out := postJSON(t, svc, "/v1/claims/"+web+"/assignment", "coordinadora",
			map[string]any{"provider_id": "prv-tecnicos-unidos", "search_seq": seq})
		assert.Equal(t, http.StatusConflict, rec.Code)
		assert.Equal(t, "assignment_exists", out["error"])
	})

	t.Run("of two analysts and of two claims racing, one wins", func(t *testing.T) {
		setAvailability(t, svc, "prv-servicios-hogar-norte", map[string]any{"availability": "available"})
		setAvailability(t, svc, "prv-reparaciones-chapinero", map[string]any{"availability": "available"})
		one := "11111111-1111-4111-8111-111111111111"
		seq := lastSearch(searchFor(t, svc, one, nil))["seq"]
		codes := race(func() int {
			rec, _ := postJSON(t, svc, "/v1/claims/"+one+"/assignment", "coordinadora",
				map[string]any{"provider_id": "prv-servicios-hogar-norte", "search_seq": seq})
			return rec.Code
		}, func() int {
			rec, _ := postJSON(t, svc, "/v1/claims/"+one+"/assignment", "analista",
				map[string]any{"provider_id": "prv-reparaciones-chapinero", "search_seq": seq})
			return rec.Code
		})
		assert.ElementsMatch(t, []int{http.StatusCreated, http.StatusConflict}, codes)
		assert.Equal(t, 1, countRows(t, svc.db, `SELECT count(*) FROM provider WHERE busy_claim_id = $1`, one))

		var remaining string
		require.NoError(t, svc.db.QueryRow(`SELECT id FROM provider WHERE availability = 'available'
			AND id IN ('prv-servicios-hogar-norte', 'prv-reparaciones-chapinero')`).Scan(&remaining))
		two := fileFromThePhone(t, svc, "22222222-2222-4222-8222-222222222222")
		three := fileFromThePhone(t, svc, "33333333-3333-4333-8333-333333333333")
		seqs := map[string]any{two: lastSearch(searchFor(t, svc, two, nil))["seq"],
			three: lastSearch(searchFor(t, svc, three, nil))["seq"]}
		assign := func(id string) func() int {
			return func() int {
				rec, _ := postJSON(t, svc, "/v1/claims/"+id+"/assignment", "coordinadora",
					map[string]any{"provider_id": remaining, "search_seq": seqs[id]})
				return rec.Code
			}
		}
		codes = race(assign(two), assign(three))
		assert.ElementsMatch(t, []int{http.StatusCreated, http.StatusConflict}, codes)
		assert.Equal(t, 1, countRows(t, svc.db,
			`SELECT count(*) FROM claim_assignment WHERE claim_id IN ($1, $2)`, two, three))
		assert.Equal(t, 1, countRows(t, svc.db,
			`SELECT count(*) FROM provider_notice WHERE claim_id IN ($1, $2)`, two, three), "nothing half written")
	})

	t.Run("the assignment is never changed nor deleted", func(t *testing.T) {
		for _, stmt := range []string{`UPDATE claim_assignment SET provider_name = 'Otro'`, `DELETE FROM claim_assignment`,
			`UPDATE claim_provider_search SET reason = NULL`, `DELETE FROM claim_incident_location`} {
			_, err := svc.db.Exec(stmt)
			require.Error(t, err, stmt)
			assert.Contains(t, err.Error(), "append-only", stmt)
		}
	})
}

// race runs the calls at once and returns their status codes.
func race(calls ...func() int) []int {
	var wg sync.WaitGroup
	codes := make([]int, len(calls))
	ready := make(chan struct{})
	for i, call := range calls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-ready
			codes[i] = call()
		}()
	}
	close(ready)
	wg.Wait()
	return codes
}
