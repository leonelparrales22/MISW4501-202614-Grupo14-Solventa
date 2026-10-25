package httpadapter_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/ports"
)

// The revocation routes of the compliance portal (HU-W4.2-02).

// complianceFake is the use case of the portal. The consents answer by id:
// cns-c-active is revoked (201, then 200 with the same key), cns-c-expired is
// not active, cns-c-revoked is revoked with a notice in progress,
// cns-c-partial is partially revoked, cns-c-reused reused a key and
// cns-c-down fails; anything else is not found.
type complianceFake struct {
	mu                   sync.Mutex
	keys                 map[string]string
	stateCalls           int
	revokeCalls          int
	retryCalls           int
	gotID, gotSub, gotKy string
	gotReq               domain.ComplianceRevokeRequest
}

func newComplianceFake() *complianceFake { return &complianceFake{keys: map[string]string{}} }

var (
	portalRevokedAt = time.Date(2026, 10, 24, 20, 0, 1, 123456000, time.UTC)
	opsActorBody    = domain.Actor{Type: domain.ActorOperations, ID: "u-ops"}
)

func portalState(id string, withRevocation, partial bool) domain.ComplianceState {
	var events []domain.ConsentEvent
	var record *domain.Revocation
	if withRevocation {
		details, _ := json.Marshal(domain.RevokedDetails{Entities: []domain.EntityWindow{{Code: "aliado-banco-1", DeadlineAt: portalRevokedAt.Add(5 * time.Minute)}}})
		events = []domain.ConsentEvent{
			{EventID: "e1", ConsentID: id, Status: domain.HistoryRevocationRequested, OccurredAt: portalRevokedAt, RecordedAt: portalRevokedAt,
				Channel: "compliance_portal", ActorType: "operations", ActorID: "u-ops", Reason: "customer_request", Reference: "TCK-2026-0412"},
			{EventID: "e2", ConsentID: id, Status: domain.HistoryRevoked, OccurredAt: portalRevokedAt, RecordedAt: portalRevokedAt,
				ReceiptID: "REV-2026-0089", Channel: "compliance_portal", Details: details,
				ActorType: "operations", ActorID: "u-ops", Reason: "customer_request", Reference: "TCK-2026-0412"},
		}
		if partial {
			events = append(events, domain.ConsentEvent{EventID: "e3", ConsentID: id, EntityCode: "aliado-banco-1",
				Status: domain.HistoryRevocationNotConfirmed, OccurredAt: portalRevokedAt.Add(5 * time.Minute),
				RecordedAt: portalRevokedAt.Add(5 * time.Minute), ActorType: "system", Reason: "window_expired"})
		}
	}
	view := holderView(id, "1020304050", events, holderNow.AddDate(0, -1, 0))
	history := []domain.ConsentEvent{{ConsentID: id, Status: domain.HistoryGranted, OccurredAt: view.Consent.GrantedAt, Channel: "embedded"}}
	history = append(history, events...)
	if withRevocation {
		record = &domain.Revocation{
			ID: "rvk-1", ConsentID: id, ReceiptNumber: "REV-2026-0089", Channel: domain.RevocationChannelCompliancePortal,
			Actor: opsActorBody, Reason: "customer_request", Reference: "TCK-2026-0412", RequestID: "secret-idempotency-key",
		}
	}
	return domain.ComplianceState{View: view, Record: record, History: history}
}

func (f *complianceFake) lookup(id string) (domain.ComplianceState, error) {
	switch id {
	case "cns-c-active", "cns-c-reused":
		return portalState(id, false, false), nil
	case "cns-c-revoked":
		return portalState(id, true, false), nil
	case "cns-c-partial":
		return portalState(id, true, true), nil
	case "cns-c-expired":
		s := portalState(id, false, false)
		s.View.Status, s.View.Revocable = domain.StatusExpired, false
		return s, nil
	case "cns-c-down":
		return domain.ComplianceState{}, errors.New("read the consent: SQLSTATE 08006 for 1020304050 Ana María Pérez TCK-2026-0412")
	}
	return domain.ComplianceState{}, ports.ErrConsentNotFound
}

func (f *complianceFake) State(_ context.Context, id string) (domain.ComplianceState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stateCalls++
	return f.lookup(id)
}

func (f *complianceFake) Revoke(_ context.Context, id, sub, key string, req domain.ComplianceRevokeRequest) (domain.ComplianceState, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revokeCalls++
	f.gotID, f.gotSub, f.gotKy, f.gotReq = id, sub, key, req
	if err := req.Validate(); err != nil {
		return domain.ComplianceState{}, false, err
	}
	switch id {
	case "cns-c-active":
		if prev, ok := f.keys[id]; ok {
			if prev == key {
				return portalState(id, true, false), false, nil
			}
			return domain.ComplianceState{}, false, domain.ErrAlreadyRevoked
		}
		f.keys[id] = key
		return portalState(id, true, false), true, nil
	case "cns-c-expired":
		return domain.ComplianceState{}, false, domain.ErrConsentNotActive
	case "cns-c-revoked", "cns-c-partial":
		return domain.ComplianceState{}, false, domain.ErrAlreadyRevoked
	case "cns-c-reused":
		return domain.ComplianceState{}, false, domain.ErrIdempotencyKeyReused
	case "cns-c-down":
		return domain.ComplianceState{}, false, errors.New("record the revocation: SQLSTATE 23503")
	}
	return domain.ComplianceState{}, false, ports.ErrConsentNotFound
}

func (f *complianceFake) Retry(_ context.Context, id, sub string) ([]string, domain.ComplianceState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.retryCalls++
	f.gotID, f.gotSub = id, sub
	state, err := f.lookup(id)
	switch {
	case err != nil:
		return nil, domain.ComplianceState{}, err
	case id == "cns-c-partial":
		return []string{"aliado-banco-1"}, portalState(id, true, false), nil
	case id == "cns-c-revoked":
		return nil, state, domain.ErrNothingToRetry
	}
	return nil, state, domain.ErrConsentNotRevoked
}

func (f *complianceFake) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stateCalls + f.revokeCalls + f.retryCalls
}

func complianceVerifier() tokenVerifier {
	return tokenVerifier{
		"operations": {Subject: "u-ops", Roles: []security.Role{security.RoleOperations}},
		"advisor":    {Subject: "u-adv", Roles: []security.Role{security.RoleAdvisor}},
		"insured-a":  {Subject: "sub-a", Roles: []security.Role{security.RoleInsured}},
		"partner":    {Subject: "u-par", Roles: []security.Role{security.RolePartner}},
	}
}

func complianceRouter(svc httpadapter.ComplianceRevocationService) http.Handler {
	return httpadapter.NewRouter(httpadapter.WithComplianceRevocations(svc, complianceVerifier()))
}

// complianceDo sends a request as the operations analyst unless token says
// otherwise; header may be nil.
func complianceDo(h http.Handler, method, target, token, body string, header map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func withKey(key string) map[string]string { return map[string]string{"Idempotency-Key": key} }

const portalBody = `{"reason":"customer_request","reference":"TCK-2026-0412","role_confirmed":true}`

func decodeMap(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out), rec.Body.String())
	return out
}

func TestTheAnalystReadsTheStateOfAnActiveConsent(t *testing.T) {
	fake := newComplianceFake()
	h := complianceRouter(fake)

	rec := complianceDo(h, http.MethodGet, "/v1/audit/consents/cns-c-active/revocation", "operations", "", nil)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"), "the portal polls it: never cached")
	out := decodeMap(t, rec)
	assert.Equal(t, "cns-c-active", out["consent_id"])
	assert.Equal(t, "active", out["status"])
	assert.Equal(t, true, out["revocable"], "criterion 1: the modal can be confirmed")
	assert.Equal(t, map[string]any{"full_name": "Ana María Pérez", "document_masked": "******4050"}, out["holder"])
	assert.Len(t, out["scope"], 3)
	assert.Equal(t, []any{map[string]any{"code": "aliado-banco-1", "name": "Banco Aliado"}}, out["entities"])
	assert.Nil(t, out["revocation"])
	assert.NotContains(t, rec.Body.String(), "1020304050", "the full document is only in the evidence")
	history := out["history"].([]any)
	require.Len(t, history, 1)
	assert.Equal(t, "granted", history[0].(map[string]any)["status"])
}

func TestTheAnalystReadsThePartialRevocationWithItsAlert(t *testing.T) {
	h := complianceRouter(newComplianceFake())

	rec := complianceDo(h, http.MethodGet, "/v1/audit/consents/cns-c-partial/revocation", "operations", "", nil)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	out := decodeMap(t, rec)
	assert.Equal(t, "partially_revoked", out["status"])
	assert.Equal(t, false, out["revocable"], "criterion 4")
	revocation := out["revocation"].(map[string]any)
	assert.Equal(t, "REV-2026-0089", revocation["receipt_number"])
	assert.Equal(t, "compliance_portal", revocation["channel"])
	assert.Equal(t, "partial", revocation["notice_status"])
	assert.Equal(t, map[string]any{"type": "operations", "id": "u-ops"}, revocation["requested_by"], "criterion 5: who")
	assert.Equal(t, "customer_request", revocation["reason"])
	assert.Equal(t, "TCK-2026-0412", revocation["reference"])
	assert.Equal(t, "2026-10-24T20:00:01.123456Z", revocation["effective_at"])
	assert.Equal(t, []any{map[string]any{
		"code": "aliado-banco-1", "name": "Banco Aliado", "status": "not_confirmed", "confirmed_at": nil,
		"deadline_at": "2026-10-24T20:05:01.123456Z",
	}}, revocation["entities"], "criterion 3: who did not confirm and when its window ended")
	history := out["history"].([]any)
	require.Len(t, history, 4)
	revoked := history[2].(map[string]any)
	assert.Equal(t, "revoked", revoked["status"])
	assert.Equal(t, "operations", revoked["actor_type"])
	assert.Equal(t, "TCK-2026-0412", revoked["reference"])
	assert.Nil(t, revoked["entity_code"])
	notConfirmed := history[3].(map[string]any)
	assert.Equal(t, "aliado-banco-1", notConfirmed["entity_code"])
	assert.NotContains(t, rec.Body.String(), "secret-idempotency-key", "the idempotency key is never shown")
	assert.NotContains(t, rec.Body.String(), "1020304050")
}

func TestTheStateOfAnExpiredConsentIsNotRevocable(t *testing.T) {
	h := complianceRouter(newComplianceFake())

	rec := complianceDo(h, http.MethodGet, "/v1/audit/consents/cns-c-expired/revocation", "operations", "", nil)

	require.Equal(t, http.StatusOK, rec.Code)
	out := decodeMap(t, rec)
	assert.Equal(t, "expired", out["status"])
	assert.Equal(t, false, out["revocable"])
}

func TestTheStateAnswersEveryFailure(t *testing.T) {
	h := complianceRouter(newComplianceFake())
	cases := map[string]struct {
		id   string
		want int
		code string
	}{
		"unknown":       {"cns-missing", http.StatusNotFound, "consent_not_found"},
		"impossible id": {"cns%20a", http.StatusNotFound, "consent_not_found"},
		"dots":          {"..", http.StatusNotFound, "consent_not_found"},
		"too long id":   {strings.Repeat("x", 65), http.StatusNotFound, "consent_not_found"},
		"store down":    {"cns-c-down", http.StatusServiceUnavailable, "service_unavailable"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rec := complianceDo(h, http.MethodGet, "/v1/audit/consents/"+tc.id+"/revocation", "operations", "", nil)

			assert.Equal(t, tc.want, rec.Code)
			assert.Equal(t, tc.code, decodeMap(t, rec)["error"])
			assert.NotContains(t, rec.Body.String(), "SQLSTATE", "the cause stays in the log")
			assert.NotContains(t, rec.Body.String(), "1020304050")
		})
	}
}

func TestThePortalRevokesAConsent(t *testing.T) {
	fake := newComplianceFake()
	h := complianceRouter(fake)
	header := map[string]string{"Idempotency-Key": strings.ToUpper(validKey), "User-Agent": "Mozilla/5.0 (Macintosh)"}

	rec := complianceDo(h, http.MethodPost, "/v1/audit/consents/cns-c-active/revocations", "operations", portalBody, header)

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, "cns-c-active", fake.gotID)
	assert.Equal(t, "u-ops", fake.gotSub, "the actor is the subject of the token")
	assert.Equal(t, validKey, fake.gotKy, "the key is kept in lower case")
	assert.Equal(t, domain.ComplianceRevokeRequest{
		Reason: "customer_request", Reference: "TCK-2026-0412", RoleConfirmed: true, UserAgent: "Mozilla/5.0 (Macintosh)",
	}, fake.gotReq, "the device is the user agent of the request")
	out := decodeMap(t, rec)
	assert.Equal(t, "revoked", out["status"])
	assert.Equal(t, false, out["revocable"])
	assert.Equal(t, "REV-2026-0089", out["revocation"].(map[string]any)["receipt_number"])
	assert.NotContains(t, rec.Body.String(), "1020304050")

	again := complianceDo(h, http.MethodPost, "/v1/audit/consents/cns-c-active/revocations", "operations", portalBody, withKey(validKey))
	assert.Equal(t, http.StatusOK, again.Code, "the same key gets the stored revocation")
	other := complianceDo(h, http.MethodPost, "/v1/audit/consents/cns-c-active/revocations", "operations", portalBody,
		withKey("1b7e3f5a-2c1d-4e8f-9a6b-1c2d3e4f5a6b"))
	assert.Equal(t, http.StatusConflict, other.Code)
	assert.JSONEq(t, `{"error":"consent_already_revoked","message":"the consent is already revoked"}`, other.Body.String())
}

func TestTheUserAgentOfTenKilobytesReachesTheServiceAsIs(t *testing.T) {
	fake := newComplianceFake()
	h := complianceRouter(fake)
	ua := strings.Repeat("a", 10*1024)

	rec := complianceDo(h, http.MethodPost, "/v1/audit/consents/cns-c-active/revocations", "operations", portalBody,
		map[string]string{"Idempotency-Key": validKey, "User-Agent": ua})

	require.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, ua, fake.gotReq.UserAgent, "the service trims it to 120 characters when it records it")
}

func TestRevokeFromThePortalValidatesTheKeyAndTheBody(t *testing.T) {
	cases := []struct {
		name, key, body string
		field, code     string
		viaService      bool
	}{
		{name: "without key", body: portalBody, field: "Idempotency-Key", code: "required"},
		{name: "key that is not a UUID", key: "abc", body: portalBody, field: "Idempotency-Key", code: "invalid_format"},
		{name: "not JSON", key: validKey, body: "{"},
		{name: "empty body", key: validKey, body: ""},
		{name: "unknown field", key: validKey, body: `{"reason":"customer_request","reference":"TCK-1","role_confirmed":true,"device":"x"}`},
		{name: "the device cannot come in the body", key: validKey, body: `{"reason":"customer_request","reference":"TCK-1","role_confirmed":true,"user_agent":"x"}`},
		{name: "role_confirmed is not a boolean", key: validKey, body: `{"reason":"customer_request","reference":"TCK-1","role_confirmed":"yes"}`},
		{name: "two JSON values", key: validKey, body: portalBody + portalBody},
		{name: "1 MB body", key: validKey, body: `{"reference":"` + strings.Repeat("a", 1<<20) + `"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := newComplianceFake()
			rec := complianceDo(complianceRouter(fake), http.MethodPost, "/v1/audit/consents/cns-c-active/revocations", "operations", tc.body, withKey(tc.key))

			assert.Equal(t, http.StatusBadRequest, rec.Code)
			out := decodeMap(t, rec)
			if tc.field == "" {
				assert.Equal(t, "invalid_request", out["error"])
			} else {
				assert.Equal(t, "validation_failed", out["error"])
				assert.Equal(t, []any{map[string]any{"field": tc.field, "code": tc.code}}, out["fields"])
			}
			assert.Zero(t, fake.calls(), "nothing is executed")
		})
	}
}

func TestRevokeFromThePortalNamesEveryMissingFieldAtOnce(t *testing.T) {
	fake := newComplianceFake()

	rec := complianceDo(complianceRouter(fake), http.MethodPost, "/v1/audit/consents/cns-c-active/revocations", "operations", `{}`, withKey(validKey))

	require.Equal(t, http.StatusBadRequest, rec.Code)
	out := decodeMap(t, rec)
	assert.Equal(t, "validation_failed", out["error"])
	assert.Equal(t, []any{
		map[string]any{"field": "reason", "code": "required"},
		map[string]any{"field": "reference", "code": "required"},
		map[string]any{"field": "role_confirmed", "code": "must_be_true"},
	}, out["fields"], "criterion 1")
	assert.Empty(t, fake.keys, "nothing is recorded")
}

func TestRevokeFromThePortalAnswersEveryRefusal(t *testing.T) {
	h := complianceRouter(newComplianceFake())
	cases := map[string]struct {
		id   string
		want int
		code string
	}{
		"expired":         {"cns-c-expired", http.StatusConflict, "consent_not_active"},
		"already revoked": {"cns-c-revoked", http.StatusConflict, "consent_already_revoked"},
		"partial":         {"cns-c-partial", http.StatusConflict, "consent_already_revoked"},
		"key reused":      {"cns-c-reused", http.StatusConflict, "idempotency_key_reused"},
		"store down":      {"cns-c-down", http.StatusServiceUnavailable, "service_unavailable"},
		"unknown":         {"cns-missing", http.StatusNotFound, "consent_not_found"},
		"impossible id":   {"cns%20a", http.StatusNotFound, "consent_not_found"},
		"too long id":     {strings.Repeat("x", 65), http.StatusNotFound, "consent_not_found"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rec := complianceDo(h, http.MethodPost, "/v1/audit/consents/"+tc.id+"/revocations", "operations", portalBody, withKey(validKey))

			assert.Equal(t, tc.want, rec.Code)
			assert.Equal(t, tc.code, decodeMap(t, rec)["error"])
			assert.NotContains(t, rec.Body.String(), "SQLSTATE")
		})
	}
}

func TestThePortalRetriesThePendingEntities(t *testing.T) {
	fake := newComplianceFake()
	h := complianceRouter(fake)

	rec := complianceDo(h, http.MethodPost, "/v1/audit/consents/cns-c-partial/revocation-retries", "operations", "", nil)

	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, "u-ops", fake.gotSub)
	out := decodeMap(t, rec)
	assert.Equal(t, []any{"aliado-banco-1"}, out["retried_entities"])
	state := out["state"].(map[string]any)
	assert.Equal(t, "cns-c-partial", state["consent_id"])
	assert.NotNil(t, state["revocation"])
	assert.NotContains(t, rec.Body.String(), "1020304050")
}

func TestTheRetryAnswersEveryRefusal(t *testing.T) {
	h := complianceRouter(newComplianceFake())
	cases := map[string]struct {
		id      string
		want    int
		code    string
		message string
	}{
		"not revoked":      {"cns-c-active", http.StatusConflict, "consent_not_revoked", ""},
		"nothing to retry": {"cns-c-revoked", http.StatusConflict, "nothing_to_retry", "notice_status: in_progress"},
		"unknown":          {"cns-missing", http.StatusNotFound, "consent_not_found", ""},
		"impossible id":    {"cns%20a", http.StatusNotFound, "consent_not_found", ""},
		"store down":       {"cns-c-down", http.StatusServiceUnavailable, "service_unavailable", ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rec := complianceDo(h, http.MethodPost, "/v1/audit/consents/"+tc.id+"/revocation-retries", "operations", "", nil)

			assert.Equal(t, tc.want, rec.Code)
			out := decodeMap(t, rec)
			assert.Equal(t, tc.code, out["error"])
			assert.Contains(t, out["message"], tc.message)
		})
	}
}

func TestTheComplianceRoutesOnlyAnswerTheirMethods(t *testing.T) {
	h := complianceRouter(newComplianceFake())

	assert.Equal(t, http.StatusMethodNotAllowed, complianceDo(h, http.MethodPost, "/v1/audit/consents/cns-c-active/revocation", "operations", "", nil).Code)
	assert.Equal(t, http.StatusMethodNotAllowed, complianceDo(h, http.MethodGet, "/v1/audit/consents/cns-c-active/revocations", "operations", "", nil).Code)
	assert.Equal(t, http.StatusMethodNotAllowed, complianceDo(h, http.MethodGet, "/v1/audit/consents/cns-c-active/revocation-retries", "operations", "", nil).Code)
	assert.Equal(t, http.StatusMethodNotAllowed, complianceDo(h, http.MethodDelete, "/v1/audit/consents/cns-c-active/revocations", "operations", "", nil).Code)
}
