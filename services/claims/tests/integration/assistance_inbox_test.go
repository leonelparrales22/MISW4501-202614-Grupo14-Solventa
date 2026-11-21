package integration_test

import (
	"context"
	"net/http"
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
	policiesadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/policies"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/assessors"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/assistance"
)

// Every acceptance criterion of HU-W2.3-03 (the inbox of the requests for
// assistance) over HTTP: the real router, use cases and worker, PostgreSQL in
// a container with the reference providers of 0009 and 0015, and the requests
// sent through the real route of the phone (HU-M2.3-01). Only the policy
// core, the token verification and the e-mail provider are doubles; the
// e-mail provider fails twice before it accepts.

func init() {
	verifier["bandeja-andres"] = security.Claims{Subject: "sub-andres-ops", Email: "andres.ruiz@solventa.example",
		Name: "Andrés Ruiz", Roles: []security.Role{security.RoleOperations}}
	verifier["bandeja-laura"] = security.Claims{Subject: "sub-laura-ops", Email: "laura.gomez@solventa.example",
		Name: "Laura Gómez", Roles: []security.Role{security.RoleOperations}}
	verifier["bandeja-socio"] = security.Claims{Subject: "sub-partner", Roles: []security.Role{security.RolePartner}}
}

const inbox = "/v1/claims/assistance-inbox"

// startWithInbox is the claims service as cmd/claims composes it with
// DATABASE_URL, AUTH_*, POLICY_CORE_URL and EMAIL_URL: the request of the
// phone, the inbox of operations over the same requests, and the registry of
// providers, with the worker of the notices polling every 50 ms.
func startWithInbox(t *testing.T, url string, core *assistanceCore, m *flakyMailbox) service {
	t.Helper()
	ctx := context.Background()
	db, err := postgres.Open(ctx, url)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, postgres.Migrate(ctx, db))

	breaker := resilience.Config{ConsecutiveFailures: 100, OpenTimeout: time.Second, HalfOpenSuccesses: 1}
	records := postgres.NewClaimRecordStore(db)
	directory := policiesadapter.NewDirectory(corepolicies.NewClient(corepolicies.Config{
		BaseURL: core.srv.URL, Timeout: 500 * time.Millisecond, Breaker: breaker}))
	store, providers := postgres.AssistanceInboxStoreOf(records), postgres.ProviderStoreOf(records)
	worker := assistance.NewNoticeWorker(store,
		mail.NewMailer(email.NewClient(email.Config{BaseURL: m.srv.URL, Breaker: breaker})), clock{},
		assistance.NoticeConfig{Poll: 50 * time.Millisecond, Backoff: []time.Duration{50 * time.Millisecond}})
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

	return service{db: db, Handler: httpadapter.NewRouter(
		httpadapter.WithAssistance(assistance.NewService(postgres.AssistanceStoreOf(records), directory, clock{}),
			verifier),
		httpadapter.WithAssistanceInbox(assistance.NewInboxService(store, providers, clock{}, worker.Notify), verifier),
		httpadapter.WithProviders(assessors.NewProviderService(providers, clock{}), verifier),
	)}
}

// sendFromThePhone is the request of the app; it returns its id.
func sendFromThePhone(t *testing.T, svc service, key string, location map[string]any) string {
	t.Helper()
	rec, body := requestAssistance(t, svc, "asistencia-carolina", key, map[string]any{
		"policy_id": "AU-2025-118204", "location": location, "place": "Autopista Norte km 12, Bogotá"})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	return body["request_id"].(string)
}

func decideOn(t *testing.T, svc service, token, id, decision string, body any) map[string]any {
	t.Helper()
	rec, out := postJSON(t, svc, inbox+"/"+id+"/"+decision, token, body)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	return out
}

func inboxCase(t *testing.T, svc service, id string) map[string]any {
	t.Helper()
	rec, body := call(t, svc, http.MethodGet, inbox+"/"+id, "bandeja-andres", nil, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	return body
}

func suggested(body map[string]any) (string, map[string]any) {
	for _, c := range body["suggestion"].(map[string]any)["candidates"].([]any) {
		candidate := c.(map[string]any)
		if candidate["suggested"] == true {
			return candidate["provider_id"].(string), candidate
		}
	}
	return "", nil
}

func actionsOf(body map[string]any) []string {
	var out []string
	for _, h := range body["history"].([]any) {
		out = append(out, h.(map[string]any)["action"].(string))
	}
	return out
}

func TestAssistanceInboxAcceptanceCriteria(t *testing.T) {
	requireDocker(t)
	mailbox := newFlakyMailbox(t, 2)
	svc := startWithInbox(t, newDatabase(t), newAssistanceCore(t), mailbox)
	var withFix, withoutFix string

	t.Run("CA-1 a request of the phone is in the inbox at once with its policy, time and location", func(t *testing.T) {
		sent := time.Now()
		withFix = sendFromThePhone(t, svc, "ca1-fix", fixAt(time.Now().Add(-5*time.Second)))
		withoutFix = sendFromThePhone(t, svc, "ca1-nofix", noFix("no_fix"))

		rec, body := call(t, svc, http.MethodGet, inbox, "bandeja-andres", nil, nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Less(t, time.Since(sent), 60*time.Second, "within 60 seconds: at once")
		assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))

		items := map[string]map[string]any{}
		for _, it := range body["items"].([]any) {
			item := it.(map[string]any)
			items[item["request_id"].(string)] = item
		}
		require.Contains(t, items, withFix)
		require.Contains(t, items, withoutFix)
		fix := items[withFix]
		assert.Equal(t, "new", fix["status"])
		assert.Equal(t, "Carolina Ruiz", fix["requester_name"])
		assert.Equal(t, map[string]any{"policy_id": "AU-2025-118204", "line": "auto", "product": "Autos",
			"insured_object": "KJT-482"}, fix["policy"])
		instant(t, fix["requested_at"])
		location := fix["location"].(map[string]any)
		assert.Equal(t, "sent", location["status"])
		assert.Equal(t, []any{4.7712, -74.0452, 8.0}, []any{location["latitude"], location["longitude"],
			location["accuracy_m"]}, "the coordinates for the map")
		noGPS := items[withoutFix]["location"].(map[string]any)
		assert.Equal(t, "unavailable", noGPS["status"])
		assert.Equal(t, "no_fix", noGPS["unavailable_reason"])
		assert.Nil(t, noGPS["latitude"], "no map without coordinates")
		assert.Nil(t, noGPS["voice"])

		summary := body["summary"].(map[string]any)
		assert.Equal(t, 2.0, summary["new_count"])
		assert.Equal(t, 300.0, summary["dispatch_target_seconds"])
		assert.Nil(t, summary["mean_dispatch_seconds_today"])

		rec, body = call(t, svc, http.MethodGet, inbox+"?status=in_call", "bandeja-andres", nil, nil)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Empty(t, body["items"])
	})

	t.Run("CA-2 the analyst sees the request and the nearest eligible provider", func(t *testing.T) {
		body := inboxCase(t, svc, withFix)
		assert.Equal(t, "new", body["status"], "opening the detail does not change it")
		assert.Equal(t, "location_missing", inboxCase(t, svc, withoutFix)["suggestion"].(map[string]any)["state"])

		decideOn(t, svc, "bandeja-andres", withFix, "attend", map[string]any{})
		body = decideOn(t, svc, "bandeja-andres", withFix, "need", map[string]any{"need": "towing"})
		s := body["suggestion"].(map[string]any)
		assert.Equal(t, "found", s["state"])
		assert.Equal(t, "towing", s["specialty"])
		assert.Equal(t, "app", s["location_source"])
		id, candidate := suggested(body)
		assert.Equal(t, "prv-gruas-autopista-norte", id, "the nearest tow truck available today")
		assert.Equal(t, 0.9, candidate["distance_km"])
		assert.Equal(t, 12.0, candidate["eta_minutes"])

		// Without GPS: the address given by voice and the pin of the map.
		decideOn(t, svc, "bandeja-laura", withoutFix, "attend", nil)
		decideOn(t, svc, "bandeja-laura", withoutFix, "need", map[string]any{"need": "towing"})
		// A text PostgreSQL cannot store is refused before it reaches it.
		rec, out := postJSON(t, svc, inbox+"/"+withoutFix+"/voice-location", "bandeja-laura",
			map[string]any{"address": "Vía Cali\x00Palmira", "latitude": 3.4921, "longitude": -76.4203})
		assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
		assert.Equal(t, "validation_failed", out["error"])
		assert.Zero(t, countRows(t, svc.db, `SELECT count(*) FROM assistance_voice_location WHERE request_id = $1`,
			withoutFix))
		body = decideOn(t, svc, "bandeja-laura", withoutFix, "voice-location",
			map[string]any{"address": "Vía Cali–Palmira km 4", "latitude": 3.4921, "longitude": -76.4203})
		assert.Equal(t, "voice", body["suggestion"].(map[string]any)["location_source"])
		id, _ = suggested(body)
		assert.Equal(t, "prv-gruas-del-valle", id)
		assert.Equal(t, "Vía Cali–Palmira km 4", body["location"].(map[string]any)["voice"].(map[string]any)["address"])
	})

	t.Run("CA-3 the dispatch is recorded and the provider receives the location once", func(t *testing.T) {
		body := decideOn(t, svc, "bandeja-andres", withFix, "dispatch",
			map[string]any{"provider_id": "prv-gruas-autopista-norte"})
		assert.Equal(t, "dispatched", body["status"])
		instant(t, body["dispatched_at"])
		d := body["dispatch"].(map[string]any)
		assert.Equal(t, "prv-gruas-autopista-norte", d["provider"].(map[string]any)["id"])
		assert.Equal(t, map[string]any{"source": "app", "latitude": 4.7712, "longitude": -74.0452, "accuracy_m": 8.0,
			"address": nil, "place": "Autopista Norte km 12, Bogotá"}, d["location_shared"])

		key := "asistencia-" + withFix
		assert.Eventually(t, func() bool {
			_, _, ok := mailbox.delivery(key)
			return ok
		}, 10*time.Second, 50*time.Millisecond, "the provider receives the request after two failures")
		msg, attempts, _ := mailbox.delivery(key)
		assert.Equal(t, 3, attempts, "two failures and one delivery, with the same key")
		assert.Equal(t, "despachos@gruasautopistanorte.example.com", msg.To)
		assert.Contains(t, msg.Body, "4.7712, -74.0452")
		assert.Contains(t, msg.Body, "Grúa")
		assert.NotContains(t, msg.Body, "Carolina", "nothing of the insured")
		assert.Eventually(t, func() bool {
			return inboxCase(t, svc, withFix)["dispatch"].(map[string]any)["notice"].(map[string]any)["status"] == "sent"
		}, 5*time.Second, 50*time.Millisecond)

		rec, mine := readAssistance(t, svc, "asistencia-carolina", withFix)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "dispatched", mine["status"], "the insured sees the new status")
		assert.NotContains(t, rec.Body.String(), "prv-gruas", "and nothing of the provider")

		rec, summary := call(t, svc, http.MethodGet, inbox, "bandeja-andres", nil, nil)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, 1.0, summary["summary"].(map[string]any)["dispatched_today"])
		assert.NotNil(t, summary["summary"].(map[string]any)["mean_dispatch_seconds_today"])
	})

	t.Run("CA-4 a request resolved another way is closed with its reason and nobody is dispatched", func(t *testing.T) {
		rec, out := postJSON(t, svc, inbox+"/"+withoutFix+"/close", "bandeja-laura", map[string]any{"reason": "other"})
		assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, "the reason other needs its note")
		assert.Equal(t, "validation_failed", out["error"])
		rec, _ = postJSON(t, svc, inbox+"/"+withoutFix+"/close", "bandeja-laura", map[string]any{})
		assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, "the reason is required")
		rec, _ = postJSON(t, svc, inbox+"/"+withoutFix+"/close", "bandeja-laura",
			map[string]any{"reason": "other", "note": "Le pasaron\x00corriente"})
		assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, "a note PostgreSQL cannot store")

		body := decideOn(t, svc, "bandeja-laura", withoutFix, "close", map[string]any{"reason": "resolved_by_phone"})
		assert.Equal(t, "closed", body["status"])
		assert.Equal(t, "resolved_by_phone", body["closure"].(map[string]any)["reason"])
		assert.Nil(t, body["dispatch"])

		rec, out = postJSON(t, svc, inbox+"/"+withoutFix+"/dispatch", "bandeja-laura",
			map[string]any{"provider_id": "prv-gruas-del-valle"})
		assert.Equal(t, http.StatusConflict, rec.Code)
		assert.Equal(t, "assistance_not_open", out["error"])
		assert.Zero(t, countRows(t, svc.db, `SELECT count(*) FROM assistance_dispatch WHERE request_id = $1`,
			withoutFix))
		_, attempts, _ := mailbox.delivery("asistencia-" + withoutFix)
		assert.Zero(t, attempts, "no provider was notified")
	})

	t.Run("CA-5 every decision keeps its analyst and its time", func(t *testing.T) {
		body := inboxCase(t, svc, withFix)
		assert.Equal(t, []string{"attended", "need_recorded", "dispatched"}, actionsOf(body))
		for _, h := range body["history"].([]any) {
			entry := h.(map[string]any)
			assert.Equal(t, map[string]any{"email": "andres.ruiz@solventa.example", "name": "Andrés Ruiz"}, entry["by"])
			instant(t, entry["at"])
		}
		assert.Equal(t, "andres.ruiz@solventa.example",
			body["dispatch"].(map[string]any)["dispatched_by"].(map[string]any)["email"])

		body = inboxCase(t, svc, withoutFix)
		assert.Equal(t, []string{"attended", "need_recorded", "voice_location_recorded", "closed"}, actionsOf(body))
		assert.Equal(t, "laura.gomez@solventa.example", body["handled_by"].(map[string]any)["email"])

		other := sendFromThePhone(t, svc, "ca5-other", fixAt(time.Now()))
		decideOn(t, svc, "bandeja-andres", other, "attend", nil)
		body = decideOn(t, svc, "bandeja-laura", other, "attend", nil)
		history := body["history"].([]any)
		require.Len(t, history, 2)
		assert.Equal(t, map[string]any{"from": "andres.ruiz@solventa.example"}, history[1].(map[string]any)["detail"],
			"who took it over from whom")
	})

	t.Run("of two analysts deciding on the same request at once, one wins", func(t *testing.T) {
		id := sendFromThePhone(t, svc, "race", fixAt(time.Now()))
		decideOn(t, svc, "bandeja-andres", id, "attend", nil)
		decideOn(t, svc, "bandeja-andres", id, "need", map[string]any{"need": "battery"})

		var wg sync.WaitGroup
		codes := make([]int, 2)
		for i, decision := range []struct{ token, path string }{{"bandeja-andres", "/dispatch"},
			{"bandeja-laura", "/close"}} {
			wg.Add(1)
			go func(i int, token, path string) {
				defer wg.Done()
				body := map[string]any{"reason": "duplicate"}
				if path == "/dispatch" {
					body = map[string]any{"provider_id": "prv-auxilio-vial-norte"}
				}
				rec, _ := postJSON(t, svc, inbox+"/"+id+path, token, body)
				codes[i] = rec.Code
			}(i, decision.token, decision.path)
		}
		wg.Wait()
		assert.ElementsMatch(t, []int{http.StatusOK, http.StatusConflict}, codes)
		assert.Equal(t, 1, countRows(t, svc.db, `SELECT (SELECT count(*) FROM assistance_dispatch WHERE request_id = $1)
			+ (SELECT count(*) FROM assistance_closure WHERE request_id = $1)`, id), "dispatched or closed, never both")
	})

	t.Run("only operations attends the inbox", func(t *testing.T) {
		for _, token := range []string{"asistencia-carolina", "advisor", "bandeja-socio"} {
			rec, _ := call(t, svc, http.MethodGet, inbox, token, nil, nil)
			assert.Equal(t, http.StatusForbidden, rec.Code, token)
			rec, _ = postJSON(t, svc, inbox+"/"+withFix+"/attend", token, map[string]any{})
			assert.Equal(t, http.StatusForbidden, rec.Code, token)
		}
		rec, _ := call(t, svc, http.MethodGet, inbox, "", nil, nil)
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})

	t.Run("the decisions are never changed nor deleted", func(t *testing.T) {
		for _, stmt := range []string{
			`UPDATE assistance_dispatch SET provider_id = 'prv-gruas-calle-80'`,
			`DELETE FROM assistance_action`,
			`UPDATE assistance_closure SET reason = 'duplicate'`,
			`TRUNCATE assistance_need`,
		} {
			_, err := svc.db.Exec(stmt)
			assert.ErrorContains(t, err, "append-only", stmt)
		}
	})
}
