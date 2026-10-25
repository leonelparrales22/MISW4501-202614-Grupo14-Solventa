// This file exercises each acceptance criterion of HU-W4.2-02 (the compliance
// portal revokes a consent of Open Finance) over HTTP against the real router
// and a real PostgreSQL (Testcontainers). The only doubles are what is outside
// the service: the entities that receive the notices and the tokens.
package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/adapters/pdf"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/consents"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/ports"
)

// entityDouble is the entity that receives the notices: it confirms, rejects
// or stays silent per entity, and counts the calls and the keys it got.
type entityDouble struct {
	mu    sync.Mutex
	mode  map[string]string
	calls map[string]int
	keys  map[string][]string
}

func newEntityDouble() *entityDouble {
	return &entityDouble{mode: map[string]string{}, calls: map[string]int{}, keys: map[string][]string{}}
}

func (d *entityDouble) set(entity, mode string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.mode[entity] = mode
}

func (d *entityDouble) count(entity string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls[entity]
}

func (d *entityDouble) keysOf(entity string) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.keys[entity]...)
}

func (d *entityDouble) Notify(_ context.Context, n ports.NoticeRecord) (idempotency.Outcome, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls[n.EntityCode]++
	d.keys[n.EntityCode] = append(d.keys[n.EntityCode], n.Key)
	switch d.mode[n.EntityCode] {
	case "reject":
		return idempotency.Outcome{}, fmt.Errorf("%w: the entity refused it", idempotency.ErrRejected)
	case "silent":
		return idempotency.Outcome{}, fmt.Errorf("%w: timeout", idempotency.ErrAmbiguous)
	}
	return idempotency.Outcome{Reference: "ent-" + n.EntityCode}, nil
}

type complianceEnv struct {
	*env
	router   http.Handler
	entities *entityDouble
	worker   *consents.NoticeWorker
}

// newComplianceEnv mounts the routes as cmd/audit does over the real stores:
// the partner channel and the verification, the evidence, the holder's routes
// (A, sub-a, is linked to the document 1020304050) and the portal's.
func newComplianceEnv(t *testing.T) *complianceEnv {
	t.Helper()
	e := newEnv(t)
	ctx := context.Background()
	consentStore, events := postgres.NewConsentStore(e.db), postgres.NewConsentEventStore(e.db)
	revocationStore, noticeStore, links := postgres.NewRevocationStore(e.db), postgres.NewNoticeStore(e.db), postgres.NewHolderLinkStore(e.db)
	_, _, err := links.Link(ctx, domain.HolderLink{
		Subject: "sub-a", DocumentID: "1020304050", CustomerID: "cus-a", LinkedAt: e.clock.Now(), EventID: "customer-linked-cus-a",
	})
	require.NoError(t, err)

	names := domain.EntityNames{"aliado-banco-1": "Banco Aliado"}
	svc := consents.NewService(consentStore, e.clock, consents.WithEvents(events))
	holders := consents.NewHolderService(consentStore, events, links, e.clock, names)
	revocations := consents.NewRevocationService(consents.RevocationDeps{
		Consents: consentStore, Events: events, Revocations: revocationStore, Notices: noticeStore,
		Receipts: pdf.NewUncompressed(), Clock: e.clock, Names: names,
	})
	compliance := consents.NewComplianceService(consents.ComplianceDeps{
		Revocations: revocations, Consents: consentStore, Events: events, Records: revocationStore, Clock: e.clock, Names: names,
	})
	entities := newEntityDouble()
	worker, err := consents.NewNoticeWorker(noticeStore, entities, consents.NoticeWorkerConfig{Now: e.clock.Now})
	require.NoError(t, err)

	verifier := tokenVerifier{
		"operations": {Subject: "u-ops", Roles: []security.Role{security.RoleOperations}},
		"advisor":    {Subject: "u-adv", Roles: []security.Role{security.RoleAdvisor}},
		"insured-a":  {Subject: "sub-a", Roles: []security.Role{security.RoleInsured}},
		"partner":    {Subject: "u-par", Roles: []security.Role{security.RolePartner}},
	}
	router := httpadapter.NewRouter(httpadapter.WithConsents(svc), httpadapter.WithConsentReads(svc, verifier),
		httpadapter.WithHolderConsents(holders, verifier), httpadapter.WithHolderRevocations(holders, revocations, verifier),
		httpadapter.WithComplianceRevocations(compliance, verifier))
	return &complianceEnv{env: e, router: router, entities: entities, worker: worker}
}

// request sends a request; key and ua may be empty.
func (c *complianceEnv) request(method, target, token, key, body, ua string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	if ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	rec := httptest.NewRecorder()
	c.router.ServeHTTP(rec, req)
	return rec
}

const (
	portalKey  = "3c1d2e4f-5a6b-4c7d-8e9f-0a1b2c3d4e5f"
	portalJSON = `{"reason":"customer_request","reference":"TCK-2026-0412","role_confirmed":true}`
)

func (c *complianceEnv) state(t *testing.T, id string) map[string]any {
	t.Helper()
	rec := c.request(http.MethodGet, "/v1/audit/consents/"+id+"/revocation", "operations", "", "", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	return jsonObject(t, rec)
}

func jsonObject(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out), rec.Body.String())
	return out
}

func (c *complianceEnv) revoke(t *testing.T, id string) *httptest.ResponseRecorder {
	t.Helper()
	rec := c.request(http.MethodPost, "/v1/audit/consents/"+id+"/revocations", "operations", portalKey, portalJSON, "Mozilla/5.0 (Macintosh)")
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	return rec
}

// process runs the notice worker once, at the clock of the test.
func (c *complianceEnv) process(t *testing.T) int {
	t.Helper()
	n, err := c.worker.ProcessOnce(context.Background())
	require.NoError(t, err)
	return n
}

// seedPortalRevocation records a revocation of the portal that notifies the
// given entities (a consent authorizes one today, so only a seed has two).
func (c *complianceEnv) seedPortalRevocation(t *testing.T, id string, entities ...string) {
	t.Helper()
	rev, notices := revocationFor(t, c.env, id, "req-seed", entities...)
	rev.Channel, rev.Device, rev.ConfirmationMethod = domain.RevocationChannelCompliancePortal, "Mozilla/5.0", domain.ConfirmationRoleAttestation
	rev.Actor, rev.Reason, rev.Reference = domain.Actor{Type: domain.ActorOperations, ID: "u-ops"}, "regulatory_order", "ORD-77"
	rev.RequestedAt = rev.EffectiveAt
	_, created, err := postgres.NewRevocationStore(c.db).Record(context.Background(), rev, notices)
	require.NoError(t, err)
	require.True(t, created)
}

func (c *complianceEnv) rowsOfRevocation(t *testing.T) (revocations, events int) {
	t.Helper()
	return c.count(t, "SELECT count(*) FROM consent_revocation"), c.count(t, "SELECT count(*) FROM consent_event")
}

func entityStatuses(state map[string]any) map[string]string {
	out := map[string]string{}
	for _, raw := range state["revocation"].(map[string]any)["entities"].([]any) {
		e := raw.(map[string]any)
		out[e["code"].(string)] = e["status"].(string)
	}
	return out
}

func TestComplianceRevocationAcceptanceCriteria(t *testing.T) {
	t.Run("CA-1_confirmation_shows_the_holder_and_asks_for_reason_reference_and_role", func(t *testing.T) {
		c := newComplianceEnv(t)
		id := c.grantFor(t, "cust-a", "app-a", "1020304050")

		state := c.state(t, id)

		assert.Equal(t, "active", state["status"])
		assert.Equal(t, true, state["revocable"])
		assert.Equal(t, map[string]any{"full_name": "Ana María Pérez", "document_masked": "******4050"}, state["holder"])
		assert.Len(t, state["scope"], 3)
		assert.Equal(t, []any{map[string]any{"code": "aliado-banco-1", "name": "Banco Aliado"}}, state["entities"])
		assert.Nil(t, state["revocation"])

		for name, body := range map[string]string{
			"nothing":              `{}`,
			"no role confirmation": `{"reason":"customer_request","reference":"TCK-2026-0412"}`,
			"role false":           `{"reason":"customer_request","reference":"TCK-2026-0412","role_confirmed":false}`,
			"unknown reason":       `{"reason":"because","reference":"TCK-2026-0412","role_confirmed":true}`,
			"reference of 2":       `{"reason":"customer_request","reference":"ab","role_confirmed":true}`,
			"reference of 65":      `{"reason":"customer_request","reference":"` + strings.Repeat("a", 65) + `","role_confirmed":true}`,
		} {
			rec := c.request(http.MethodPost, "/v1/audit/consents/"+id+"/revocations", "operations", portalKey, body, "")
			assert.Equal(t, http.StatusBadRequest, rec.Code, name)
		}
		rec := c.request(http.MethodPost, "/v1/audit/consents/"+id+"/revocations", "operations", portalKey, `{}`, "")
		assert.JSONEq(t, `{"error":"validation_failed","message":"one or more fields are invalid","fields":[
			{"field":"reason","code":"required"},{"field":"reference","code":"required"},{"field":"role_confirmed","code":"must_be_true"}]}`,
			rec.Body.String(), "every missing field at once")
		revocations, events := c.rowsOfRevocation(t)
		assert.Zero(t, revocations, "nothing is recorded before the explicit confirmation")
		assert.Zero(t, events)
		status, verification := c.do(http.MethodGet, "/v1/audit/consent-verifications?customer_id=cust-a", "", "")
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, true, verification["active"], "the consent is untouched")
	})

	t.Run("CA-2_blocks_at_once_notifies_every_entity_and_is_revoked_within_5_minutes", func(t *testing.T) {
		c := newComplianceEnv(t)
		id := c.grantFor(t, "cust-a", "app-a", "1020304050")
		status, before := c.do(http.MethodGet, "/v1/audit/consent-verifications?customer_id=cust-a", "", "")
		require.Equal(t, http.StatusOK, status)
		require.Equal(t, true, before["active"])

		rec := c.revoke(t, id)

		// Right after the 201, before any entity answers: origination would
		// stop querying the sources (fail-closed).
		status, after := c.do(http.MethodGet, "/v1/audit/consent-verifications?customer_id=cust-a", "", "")
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, false, after["active"], "blocked from the instant it is recorded")
		assert.Equal(t, []any{}, after["sources"])
		out := jsonObject(t, rec)
		assert.Equal(t, "revoked", out["status"])
		revocation := out["revocation"].(map[string]any)
		assert.Equal(t, "in_progress", revocation["notice_status"])
		assert.Equal(t, map[string]string{"aliado-banco-1": "pending"}, entityStatuses(out))
		assert.Zero(t, c.entities.count("aliado-banco-1"), "the entity has not been told yet")

		assert.Equal(t, 1, c.process(t))

		assert.Equal(t, 1, c.entities.count("aliado-banco-1"))
		assert.Equal(t, []string{"revocation-" + id + "-aliado-banco-1"}, c.entities.keysOf("aliado-banco-1"), "the notice carries its idempotency key")
		done := c.state(t, id)
		assert.Equal(t, "revoked", done["status"])
		done0 := done["revocation"].(map[string]any)
		assert.Equal(t, "completed", done0["notice_status"])
		entity := done0["entities"].([]any)[0].(map[string]any)
		assert.Equal(t, "confirmed", entity["status"])
		effective, err := time.Parse(time.RFC3339Nano, done0["effective_at"].(string))
		require.NoError(t, err)
		confirmed, err := time.Parse(time.RFC3339Nano, entity["confirmed_at"].(string))
		require.NoError(t, err)
		assert.LessOrEqual(t, confirmed.Sub(effective), domain.RevocationNoticeDeadline, "confirmed within the 5 minutes of RN-02")
		assert.Zero(t, c.process(t), "nothing else is due: the entity is not notified again")
		assert.Equal(t, 1, c.entities.count("aliado-banco-1"))
	})

	t.Run("CA-3_partial_revocation_alert_and_retry_of_only_the_pending_entities", func(t *testing.T) {
		c := newComplianceEnv(t)
		id := c.grantFor(t, "cust-a", "app-a", "1020304050")
		// Two entities: a consent authorizes one today, so the revocation is
		// seeded with two. One confirms, the other rejects the notice.
		c.seedPortalRevocation(t, id, "aliado-a", "aliado-b")
		c.entities.set("aliado-b", "reject")

		assert.Equal(t, 2, c.process(t))

		partial := c.state(t, id)
		assert.Equal(t, "partially_revoked", partial["status"])
		assert.Equal(t, false, partial["revocable"])
		revocation := partial["revocation"].(map[string]any)
		assert.Equal(t, "partial", revocation["notice_status"])
		assert.Equal(t, map[string]string{"aliado-a": "confirmed", "aliado-b": "not_confirmed"}, entityStatuses(partial))
		for _, raw := range revocation["entities"].([]any) {
			e := raw.(map[string]any)
			assert.NotNil(t, e["deadline_at"], "the time its window ended: %v", e["code"])
		}
		assert.Equal(t, "regulatory_order", revocation["reason"])
		assert.Equal(t, "ORD-77", revocation["reference"])

		// The first retry: only the entity that did not confirm.
		c.entities.set("aliado-b", "confirm")
		rec := c.request(http.MethodPost, "/v1/audit/consents/"+id+"/revocation-retries", "operations", "", "", "")
		require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
		out := jsonObject(t, rec)
		assert.Equal(t, []any{"aliado-b"}, out["retried_entities"])
		retrying := out["state"].(map[string]any)
		assert.Equal(t, "partially_revoked", retrying["status"], "a retry keeps it partial until the entity confirms")
		assert.Equal(t, map[string]string{"aliado-a": "confirmed", "aliado-b": "pending"}, entityStatuses(retrying))

		again := c.request(http.MethodPost, "/v1/audit/consents/"+id+"/revocation-retries", "operations", "", "", "")
		assert.Equal(t, http.StatusConflict, again.Code, "a second retry with the window open")
		assert.Contains(t, again.Body.String(), "nothing_to_retry")
		assert.Equal(t, 1, c.process(t), "only the retried entity is called")

		assert.Equal(t, 1, c.entities.count("aliado-a"), "the entity that confirmed is never notified again")
		assert.Equal(t, 2, c.entities.count("aliado-b"))
		keys := c.entities.keysOf("aliado-b")
		assert.Equal(t, keys[0], keys[1], "the same idempotency key on the retry")
		done := c.state(t, id)
		assert.Equal(t, "revoked", done["status"], "revoked when every entity confirms")
		assert.Equal(t, "completed", done["revocation"].(map[string]any)["notice_status"])
		assert.Equal(t, 1, c.count(t, `SELECT count(*) FROM consent_event WHERE consent_id = $1 AND status = 'revocation_retry_requested'
			AND entity_code = 'aliado-b' AND actor_type = 'operations' AND actor_id = 'u-ops'`, id), "the retry is recorded with its actor")
		last := c.request(http.MethodPost, "/v1/audit/consents/"+id+"/revocation-retries", "operations", "", "", "")
		assert.Equal(t, http.StatusConflict, last.Code, "completed: nothing to retry")
	})

	t.Run("CA-3_the_window_ends_without_the_worker_and_the_portal_still_sees_and_retries_it", func(t *testing.T) {
		c := newComplianceEnv(t)
		id := c.grantFor(t, "cust-a", "app-a", "1020304050")
		c.revoke(t, id)
		c.entities.set("aliado-banco-1", "silent")
		c.clock.set(c.clock.Now().Add(domain.RevocationNoticeDeadline + time.Second))

		partial := c.state(t, id)

		assert.Equal(t, "partially_revoked", partial["status"], "derived by date, like the expiry")
		assert.Equal(t, map[string]string{"aliado-banco-1": "not_confirmed"}, entityStatuses(partial))
		rec := c.request(http.MethodPost, "/v1/audit/consents/"+id+"/revocation-retries", "operations", "", "", "")
		require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
		assert.Equal(t, []any{"aliado-banco-1"}, jsonObject(t, rec)["retried_entities"])
	})

	t.Run("CA-3_only_a_partial_revocation_is_retried", func(t *testing.T) {
		c := newComplianceEnv(t)
		id := c.grantFor(t, "cust-a", "app-a", "1020304050")

		rec := c.request(http.MethodPost, "/v1/audit/consents/"+id+"/revocation-retries", "operations", "", "", "")
		assert.Equal(t, http.StatusConflict, rec.Code)
		assert.Contains(t, rec.Body.String(), "consent_not_revoked")

		c.revoke(t, id)
		rec = c.request(http.MethodPost, "/v1/audit/consents/"+id+"/revocation-retries", "operations", "", "", "")
		assert.Equal(t, http.StatusConflict, rec.Code, "in progress")
		assert.Contains(t, rec.Body.String(), "nothing_to_retry")
		assert.Contains(t, rec.Body.String(), "in_progress")
		assert.Equal(t, 0, c.count(t, "SELECT count(*) FROM consent_event WHERE status = 'revocation_retry_requested'"))

		rec = c.request(http.MethodPost, "/v1/audit/consents/cns-missing/revocation-retries", "operations", "", "", "")
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("CA-4_a_revoked_or_expired_consent_is_refused_with_a_readable_answer", func(t *testing.T) {
		c := newComplianceEnv(t)
		id := c.grantFor(t, "cust-a", "app-a", "1020304050")
		first := c.revoke(t, id)

		state := c.state(t, id)
		assert.Equal(t, false, state["revocable"], "the action is not available")

		other := c.request(http.MethodPost, "/v1/audit/consents/"+id+"/revocations", "operations", "1b7e3f5a-2c1d-4e8f-9a6b-1c2d3e4f5a6b", portalJSON, "")
		assert.Equal(t, http.StatusConflict, other.Code)
		assert.JSONEq(t, `{"error":"consent_already_revoked","message":"the consent is already revoked"}`, other.Body.String())
		same := c.request(http.MethodPost, "/v1/audit/consents/"+id+"/revocations", "operations", portalKey, portalJSON, "")
		assert.Equal(t, http.StatusOK, same.Code, "the same key returns the stored revocation")
		assert.JSONEq(t, first.Body.String(), same.Body.String())
		revocations, _ := c.rowsOfRevocation(t)
		assert.Equal(t, 1, revocations)

		// Revoked by the holder from the app: the portal is refused as well.
		mobile := c.grantFor(t, "cust-m", "app-m", "1020304050")
		holderBody := `{"requested_at":"` + c.clock.Now().Format(time.RFC3339) + `","device":"Google Pixel 8","confirmation_method":"biometric"}`
		rec := c.request(http.MethodPost, "/v1/consents/"+mobile+"/revocations", "insured-a", "4b7e3f5a-2c1d-4e8f-9a6b-1c2d3e4f5a6b", holderBody, "")
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
		fromPortal := c.request(http.MethodPost, "/v1/audit/consents/"+mobile+"/revocations", "operations", "5b7e3f5a-2c1d-4e8f-9a6b-1c2d3e4f5a6b", portalJSON, "")
		assert.Equal(t, http.StatusConflict, fromPortal.Code)
		assert.Contains(t, fromPortal.Body.String(), "consent_already_revoked")
		seen := c.state(t, mobile)
		assert.Equal(t, "mobile_app", seen["revocation"].(map[string]any)["channel"], "the portal sees who revoked it and from where")
		assert.Equal(t, "holder", seen["revocation"].(map[string]any)["requested_by"].(map[string]any)["type"])
		assert.Nil(t, seen["revocation"].(map[string]any)["reason"])

		// Expired.
		c.clock.set(c.clock.Now().AddDate(1, 0, 1))
		old := c.grantFor(t, "cust-o", "app-o", "1020304050")
		c.clock.set(c.clock.Now().AddDate(1, 0, 1))
		expired := c.state(t, old)
		assert.Equal(t, "expired", expired["status"])
		assert.Equal(t, false, expired["revocable"])
		before, _ := c.rowsOfRevocation(t)
		gone := c.request(http.MethodPost, "/v1/audit/consents/"+old+"/revocations", "operations", "6b7e3f5a-2c1d-4e8f-9a6b-1c2d3e4f5a6b", portalJSON, "")
		assert.Equal(t, http.StatusConflict, gone.Code)
		assert.Contains(t, gone.Body.String(), "consent_not_active")
		after, _ := c.rowsOfRevocation(t)
		assert.Equal(t, before, after)
	})

	t.Run("CA-5_the_record_is_immutable_exists_before_the_answer_and_the_holder_sees_it", func(t *testing.T) {
		c := newComplianceEnv(t)
		ctx := context.Background()
		id := c.grantFor(t, "cust-a", "app-a", "1020304050")

		rec := c.request(http.MethodPost, "/v1/audit/consents/"+id+"/revocations", "operations", portalKey,
			`{"reason":"regulatory_order","reference":"  OF   12/2026 ","role_confirmed":true}`, "Mozilla/5.0 (Macintosh)")
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

		// The row exists when the 201 arrives, before any entity answered.
		assert.Equal(t, 1, c.count(t, `SELECT count(*) FROM consent_revocation WHERE consent_id = $1 AND actor_type = 'operations'
			AND actor_id = 'u-ops' AND channel = 'compliance_portal' AND reason = 'regulatory_order' AND reference = 'OF 12/2026'
			AND device = 'Mozilla/5.0 (Macintosh)' AND confirmation_method = 'role_attestation' AND request_id = $2
			AND jsonb_array_length(scope) = 3 AND requested_at = effective_at`, id, portalKey), "who, why, reference, scope and times")
		for _, status := range []string{domain.HistoryRevocationRequested, domain.HistoryRevoked} {
			assert.Equal(t, 1, c.count(t, `SELECT count(*) FROM consent_event WHERE consent_id = $1 AND status = $2 AND actor_type = 'operations'
				AND actor_id = 'u-ops' AND channel = 'compliance_portal' AND reason = 'regulatory_order' AND reference = 'OF 12/2026'`, id, status), status)
		}
		assert.Equal(t, 1, c.count(t, "SELECT count(*) FROM revocation_notice_step WHERE consent_id = $1", id))

		c.process(t)
		assert.Equal(t, 1, c.count(t, "SELECT count(*) FROM consent_event WHERE consent_id = $1 AND status = 'revocation_confirmed' AND entity_code = 'aliado-banco-1'", id),
			"the answer of the entity with its time")
		state := c.state(t, id)
		history := state["history"].([]any)
		var statuses []string
		for _, raw := range history {
			h := raw.(map[string]any)
			statuses = append(statuses, h["status"].(string))
			assert.NotEmpty(t, h["occurred_at"])
		}
		assert.Equal(t, []string{"granted", "revocation_requested", "revoked", "revocation_confirmed"}, statuses)
		revocation := state["revocation"].(map[string]any)
		assert.Equal(t, map[string]any{"type": "operations", "id": "u-ops"}, revocation["requested_by"])
		assert.Equal(t, "OF 12/2026", revocation["reference"])

		// Immutable: the rows of the portal cannot be changed, deleted or truncated.
		for _, stmt := range []string{
			`UPDATE consent_revocation SET reason = 'x' WHERE actor_type = 'operations'`,
			`DELETE FROM consent_revocation WHERE actor_type = 'operations'`, `TRUNCATE consent_revocation`,
			`UPDATE consent_event SET reference = 'x' WHERE actor_type = 'operations'`,
			`DELETE FROM consent_event WHERE actor_type = 'operations'`, `TRUNCATE consent_event`,
		} {
			_, err := c.db.ExecContext(ctx, stmt)
			assert.Equal(t, "23001", pgCode(err), "%s: %v", stmt, err)
		}

		// The holder sees it in the app: the status, the channel and the receipt.
		detail := c.request(http.MethodGet, "/v1/consents/"+id, "insured-a", "", "", "")
		require.Equal(t, http.StatusOK, detail.Code)
		consent := jsonObject(t, detail)
		assert.Equal(t, "revoked", consent["status"])
		assert.Equal(t, "compliance_portal", consent["revocation"].(map[string]any)["channel"])
		receipt := c.request(http.MethodGet, "/v1/consents/"+id+"/revocation-receipt", "insured-a", "", "", "")
		require.Equal(t, http.StatusOK, receipt.Code)
		assert.Contains(t, receipt.Body.String(), consent["revocation_receipt_id"].(string))
		assert.Contains(t, receipt.Body.String(), "Portal de cumplimiento de Solventa")
		assert.NotContains(t, receipt.Body.String(), "compliance_portal")
		assert.NotContains(t, receipt.Body.String(), "role_attestation")
		assert.Contains(t, receipt.Body.String(), "******4050")
		assert.NotContains(t, receipt.Body.String(), "OF 12/2026", "the reference is not part of the receipt of the holder")
	})

	t.Run("CA-6_only_operations_and_nothing_is_executed_for_the_rest", func(t *testing.T) {
		c := newComplianceEnv(t)
		id := c.grantFor(t, "cust-a", "app-a", "1020304050")
		c.seedPortalRevocation(t, c.grantFor(t, "cust-p", "app-p", "9988776655"), "aliado-a")
		revocations0, events0 := c.rowsOfRevocation(t)
		notices0 := c.count(t, "SELECT count(*) FROM revocation_notice_step")

		for _, caller := range []string{"", "forged", "insured-a", "advisor", "partner"} {
			want := http.StatusForbidden
			if caller == "" || caller == "forged" {
				want = http.StatusUnauthorized
			}
			for _, rt := range []struct{ method, path, body, key string }{
				{http.MethodGet, "/v1/audit/consents/" + id + "/revocation", "", ""},
				{http.MethodPost, "/v1/audit/consents/" + id + "/revocations", portalJSON, portalKey},
				{http.MethodPost, "/v1/audit/consents/" + id + "/revocation-retries", "", ""},
			} {
				rec := c.request(rt.method, rt.path, caller, rt.key, rt.body, "")
				assert.Equal(t, want, rec.Code, "%q %s %s", caller, rt.method, rt.path)
			}
		}

		revocations1, events1 := c.rowsOfRevocation(t)
		assert.Equal(t, revocations0, revocations1, "no revocation recorded by a refused caller")
		assert.Equal(t, events0, events1, "no event either")
		assert.Equal(t, notices0, c.count(t, "SELECT count(*) FROM revocation_notice_step"))
		status, verification := c.do(http.MethodGet, "/v1/audit/consent-verifications?customer_id=cust-a", "", "")
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, true, verification["active"], "the consent was not touched")
		assert.Equal(t, http.StatusOK, c.request(http.MethodGet, "/v1/audit/consents/"+id+"/revocation", "operations", "", "", "").Code)
	})
}

// The portal and the holder's app revoke the same consent at once: one
// revocation is recorded and the other is told it is already revoked.
func TestThePortalAndTheAppRevokingAtOnceRecordOneRevocation(t *testing.T) {
	c := newComplianceEnv(t)
	id := c.grantFor(t, "cust-a", "app-a", "1020304050")
	holderBody := `{"requested_at":"` + c.clock.Now().Format(time.RFC3339) + `","device":"Google Pixel 8","confirmation_method":"biometric"}`

	codes := make(chan int, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, call := range []func() int{
		func() int {
			return c.request(http.MethodPost, "/v1/audit/consents/"+id+"/revocations", "operations", portalKey, portalJSON, "").Code
		},
		func() int {
			return c.request(http.MethodPost, "/v1/consents/"+id+"/revocations", "insured-a", "7b7e3f5a-2c1d-4e8f-9a6b-1c2d3e4f5a6b", holderBody, "").Code
		},
	} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			codes <- call()
		}()
	}
	close(start)
	wg.Wait()
	close(codes)

	var got []int
	for code := range codes {
		got = append(got, code)
	}
	assert.ElementsMatch(t, []int{http.StatusCreated, http.StatusConflict}, got)
	revocations, _ := c.rowsOfRevocation(t)
	assert.Equal(t, 1, revocations, "a single revocation")
	assert.Equal(t, 1, c.count(t, "SELECT count(*) FROM revocation_notice_step"), "a single notice")
}

// Two analysts retry the same partial revocation at once: the entity gets one
// new window and one new notice.
func TestTwoRetriesAtOnceStartOneWindowInPostgreSQL(t *testing.T) {
	c := newComplianceEnv(t)
	id := c.grantFor(t, "cust-a", "app-a", "1020304050")
	c.seedPortalRevocation(t, id, "aliado-a", "aliado-b")
	c.entities.set("aliado-b", "reject")
	c.process(t)
	c.entities.set("aliado-b", "confirm")

	codes := make(chan int, 6)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			codes <- c.request(http.MethodPost, "/v1/audit/consents/"+id+"/revocation-retries", "operations", "", "", "").Code
		}()
	}
	close(start)
	wg.Wait()
	close(codes)

	accepted := 0
	for code := range codes {
		switch code {
		case http.StatusAccepted:
			accepted++
		case http.StatusConflict:
		default:
			t.Errorf("unexpected status %d", code)
		}
	}
	assert.Equal(t, 1, accepted, "one retry wins")
	assert.Equal(t, 1, c.count(t, "SELECT count(*) FROM consent_event WHERE consent_id = $1 AND status = 'revocation_retry_requested'", id))
	c.process(t)
	assert.Equal(t, 2, c.entities.count("aliado-b"), "the first attempt and one retry")
	assert.Equal(t, 1, c.entities.count("aliado-a"))
}

// Minimization: the answers never carry the full document, and the User-Agent
// of 10 KB is recorded trimmed.
func TestThePortalAnswersNeverCarryTheFullDocumentAndTheDeviceIsTrimmed(t *testing.T) {
	c := newComplianceEnv(t)
	id := c.grantFor(t, "cust-a", "app-a", "1020304050")

	rec := c.request(http.MethodPost, "/v1/audit/consents/"+id+"/revocations", "operations", portalKey, portalJSON, strings.Repeat("a", 10*1024))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	c.process(t)

	bodies := []string{rec.Body.String(), c.request(http.MethodGet, "/v1/audit/consents/"+id+"/revocation", "operations", "", "", "").Body.String()}
	retry := c.request(http.MethodPost, "/v1/audit/consents/"+id+"/revocation-retries", "operations", "", "", "")
	bodies = append(bodies, retry.Body.String())
	for _, body := range bodies {
		assert.NotContains(t, body, "1020304050")
		assert.NotContains(t, body, portalKey, "the idempotency key is never shown")
	}
	assert.Equal(t, domain.MaxDeviceLength, c.count(t, "SELECT length(device) FROM consent_revocation WHERE consent_id = $1", id))
	var storedKey bool
	require.NoError(t, c.db.QueryRow("SELECT request_id = $2 FROM consent_revocation WHERE consent_id = $1", id, portalKey).Scan(&storedKey))
	assert.True(t, storedKey, "kept in the record")
}
