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

// The holder's revocation routes (HU-M1.3-02).

// revocationFake is the revocation use case. A's consents answer by id:
// cns-a-active is revoked (201, then 200 with the same key), cns-a-expired
// is not active, cns-a-revoked is already revoked, cns-a-reused reused a key
// and cns-a-down fails; anything else is not found.
type revocationFake struct {
	mu       sync.Mutex
	keys     map[string]string
	got      domain.RevokeRequest
	subject  string
	lang     string
	document string
}

func newRevocationFake() *revocationFake { return &revocationFake{keys: map[string]string{}} }

var revokedAt = time.Date(2026, 10, 24, 20, 0, 1, 0, time.UTC)

func revokedView(id string) domain.ConsentView {
	details, _ := json.Marshal(domain.RevokedDetails{Entities: []domain.EntityWindow{{Code: "aliado-banco-1", DeadlineAt: revokedAt.Add(5 * time.Minute)}}})
	return holderView(id, "1020304050", []domain.ConsentEvent{
		{EventID: "e1", ConsentID: id, Status: domain.HistoryRevocationRequested, OccurredAt: revokedAt.Add(-2 * time.Second),
			RecordedAt: revokedAt, Channel: "mobile_app", Device: "Google Pixel 8"},
		{EventID: "e2", ConsentID: id, Status: domain.HistoryRevoked, OccurredAt: revokedAt, RecordedAt: revokedAt,
			ReceiptID: "REV-2026-0088", Channel: "mobile_app", Device: "Google Pixel 8", Details: details},
	}, holderNow.AddDate(0, -1, 0))
}

func (f *revocationFake) RevokeAsHolder(_ context.Context, document, subject, id, key string, req domain.RevokeRequest) (domain.ConsentView, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got, f.subject, f.document = req, subject, document
	if document != "1020304050" {
		return domain.ConsentView{}, false, ports.ErrConsentNotFound
	}
	if req.Device == "" {
		return domain.ConsentView{}, false, &domain.ValidationError{Fields: []domain.FieldError{{Field: "device", Code: domain.CodeRequired}}}
	}
	switch id {
	case "cns-a-active":
		if prev, ok := f.keys[id]; ok {
			if prev == key {
				return revokedView(id), false, nil
			}
			return domain.ConsentView{}, false, domain.ErrAlreadyRevoked
		}
		f.keys[id] = key
		return revokedView(id), true, nil
	case "cns-a-expired":
		return domain.ConsentView{}, false, domain.ErrConsentNotActive
	case "cns-a-revoked":
		return domain.ConsentView{}, false, domain.ErrAlreadyRevoked
	case "cns-a-reused":
		return domain.ConsentView{}, false, domain.ErrIdempotencyKeyReused
	case "cns-a-down":
		return domain.ConsentView{}, false, errors.New("record the revocation: SQLSTATE 23503")
	}
	return domain.ConsentView{}, false, ports.ErrConsentNotFound
}

func (f *revocationFake) Receipt(_ context.Context, document, id, lang string) ([]byte, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lang = lang
	switch {
	case document != "1020304050":
		return nil, "", ports.ErrConsentNotFound
	case id == "cns-a-revoked":
		return []byte("%PDF-1.3 receipt"), "REV-2026-0088", nil
	case id == "cns-a-down":
		return nil, "", errors.New("pdf broken")
	}
	return nil, "", ports.ErrConsentNotFound
}

func revocationRouter(rev httpadapter.HolderRevocationService) http.Handler {
	verifier := tokenVerifier{
		"insured-a":    {Subject: "sub-a", Roles: []security.Role{security.RoleInsured}},
		"insured-b":    {Subject: "sub-b", Roles: []security.Role{security.RoleInsured}},
		"insured-none": {Subject: "sub-none", Roles: []security.Role{security.RoleInsured}},
		"operations":   {Subject: "u-ops", Roles: []security.Role{security.RoleOperations}},
	}
	holders := newHolderFake()
	return httpadapter.NewRouter(httpadapter.WithHolderConsents(holders, verifier), httpadapter.WithHolderRevocations(holders, rev, verifier))
}

const validKey = "0b7e3f5a-2c1d-4e8f-9a6b-1c2d3e4f5a6b"

func post(h http.Handler, target, token, key, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

const revokeBody = `{"requested_at":"2026-10-24T14:59:59-05:00","device":"Google Pixel 8","confirmation_method":"biometric"}`

func TestTheHolderRevokesAConsentAndGetsTheRevocation(t *testing.T) {
	fake := newRevocationFake()
	h := revocationRouter(fake)

	rec := post(h, "/v1/consents/cns-a-active/revocations", "insured-a", validKey, revokeBody)

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, "sub-a", fake.subject, "the actor is the subject of the token")
	assert.Equal(t, "1020304050", fake.document)
	assert.True(t, time.Date(2026, 10, 24, 19, 59, 59, 0, time.UTC).Equal(fake.got.RequestedAt), "the device time is kept")
	assert.Equal(t, "biometric", fake.got.ConfirmationMethod)
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.Equal(t, "cns-a-active", out["consent_id"])
	assert.Equal(t, "REV-2026-0088", out["receipt_number"])
	assert.Equal(t, "revoked", out["status"])
	assert.Equal(t, "2026-10-24T19:59:59Z", out["requested_at"])
	assert.Equal(t, "2026-10-24T20:00:01Z", out["effective_at"])
	assert.Equal(t, "mobile_app", out["channel"])
	assert.Equal(t, "Google Pixel 8", out["device"])
	assert.Equal(t, "in_progress", out["notice_status"])
	assert.Equal(t, []any{map[string]any{"code": "aliado-banco-1", "name": "Banco Aliado", "status": "pending",
		"confirmed_at": nil, "deadline_at": "2026-10-24T20:05:01Z"}}, out["entities"])
	assert.NotContains(t, rec.Body.String(), "1020304050", "never the document")

	again := post(h, "/v1/consents/cns-a-active/revocations", "insured-a", strings.ToUpper(validKey), revokeBody)
	assert.Equal(t, http.StatusOK, again.Code, "the same key (in any case) gets the same revocation")
	other := post(h, "/v1/consents/cns-a-active/revocations", "insured-a", "1b7e3f5a-2c1d-4e8f-9a6b-1c2d3e4f5a6b", revokeBody)
	assert.Equal(t, http.StatusConflict, other.Code)
	assert.JSONEq(t, `{"error":"consent_already_revoked","message":"the consent is already revoked"}`, other.Body.String())
}

func TestRevokeValidatesTheKeyAndTheBody(t *testing.T) {
	h := revocationRouter(newRevocationFake())
	cases := []struct {
		name, key, body string
		want            int
		field, code     string
	}{
		{name: "without key", body: revokeBody, want: http.StatusBadRequest, field: "Idempotency-Key", code: "required"},
		{name: "key that is not a UUID", key: "abc", body: revokeBody, want: http.StatusBadRequest, field: "Idempotency-Key", code: "invalid_format"},
		{name: "not JSON", key: validKey, body: "{", want: http.StatusBadRequest},
		{name: "requested_at not a date", key: validKey, body: `{"requested_at":"yesterday","device":"x","confirmation_method":"biometric"}`,
			want: http.StatusBadRequest, field: "requested_at", code: "invalid_format"},
		{name: "field reported by the service", key: validKey, body: `{"requested_at":"2026-10-24T20:00:00Z","confirmation_method":"biometric"}`,
			want: http.StatusBadRequest, field: "device", code: "required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := post(h, "/v1/consents/cns-a-active/revocations", "insured-a", tc.key, tc.body)
			assert.Equal(t, tc.want, rec.Code)
			var out map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
			if tc.field == "" {
				assert.Equal(t, "invalid_request", out["error"])
				return
			}
			assert.Equal(t, "validation_failed", out["error"])
			assert.Equal(t, []any{map[string]any{"field": tc.field, "code": tc.code}}, out["fields"])
		})
	}
}

func TestRevokeAnswersEveryRefusal(t *testing.T) {
	h := revocationRouter(newRevocationFake())
	cases := map[string]struct {
		target, token string
		want          int
		code          string
	}{
		"expired":            {"/v1/consents/cns-a-expired/revocations", "insured-a", http.StatusConflict, "consent_not_active"},
		"already revoked":    {"/v1/consents/cns-a-revoked/revocations", "insured-a", http.StatusConflict, "consent_already_revoked"},
		"key reused":         {"/v1/consents/cns-a-reused/revocations", "insured-a", http.StatusConflict, "idempotency_key_reused"},
		"store down":         {"/v1/consents/cns-a-down/revocations", "insured-a", http.StatusServiceUnavailable, "service_unavailable"},
		"another holder":     {"/v1/consents/cns-a-active/revocations", "insured-b", http.StatusNotFound, "not_found"},
		"unknown":            {"/v1/consents/cns-missing/revocations", "insured-a", http.StatusNotFound, "not_found"},
		"impossible id":      {"/v1/consents/cns%20a/revocations", "insured-a", http.StatusNotFound, "not_found"},
		"too long id":        {"/v1/consents/" + strings.Repeat("x", 65) + "/revocations", "insured-a", http.StatusNotFound, "not_found"},
		"account not linked": {"/v1/consents/cns-a-active/revocations", "insured-none", http.StatusForbidden, "holder_not_linked"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rec := post(h, tc.target, tc.token, validKey, revokeBody)
			assert.Equal(t, tc.want, rec.Code)
			var out map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
			assert.Equal(t, tc.code, out["error"])
		})
	}
}

func TestTheRevocationRoutesNeedATokenAndTheInsuredRole(t *testing.T) {
	h := revocationRouter(newRevocationFake())

	assert.Equal(t, http.StatusUnauthorized, post(h, "/v1/consents/cns-a-active/revocations", "", validKey, revokeBody).Code)
	assert.Equal(t, http.StatusUnauthorized, post(h, "/v1/consents/cns-a-active/revocations", "forged", validKey, revokeBody).Code)
	assert.Equal(t, http.StatusForbidden, post(h, "/v1/consents/cns-a-active/revocations", "operations", validKey, revokeBody).Code)
	assert.Equal(t, http.StatusUnauthorized, call(h, http.MethodGet, "/v1/consents/cns-a-revoked/revocation-receipt", "").Code)
	assert.Equal(t, http.StatusForbidden, call(h, http.MethodGet, "/v1/consents/cns-a-revoked/revocation-receipt", "operations").Code)
	assert.Equal(t, http.StatusMethodNotAllowed, call(h, http.MethodPost, "/v1/consents/cns-a-active", "insured-a").Code,
		"the consent itself stays read-only")
	assert.Equal(t, http.StatusMethodNotAllowed, call(h, http.MethodGet, "/v1/consents/cns-a-active/revocations", "insured-a").Code)
}

func TestTheHolderDownloadsTheReceipt(t *testing.T) {
	fake := newRevocationFake()
	h := revocationRouter(fake)

	rec := call(h, http.MethodGet, "/v1/consents/cns-a-revoked/revocation-receipt", "insured-a")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/pdf", rec.Header().Get("Content-Type"))
	assert.Equal(t, `attachment; filename="REV-2026-0088.pdf"`, rec.Header().Get("Content-Disposition"))
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, "%PDF-1.3 receipt", rec.Body.String())
	assert.Equal(t, "es", fake.lang, "Spanish by default")

	req := httptest.NewRequest(http.MethodGet, "/v1/consents/cns-a-revoked/revocation-receipt", nil)
	req.Header.Set("Authorization", "Bearer insured-a")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	h.ServeHTTP(httptest.NewRecorder(), req)
	assert.Equal(t, "en", fake.lang)

	assert.Equal(t, http.StatusNotFound, call(h, http.MethodGet, "/v1/consents/cns-a-active/revocation-receipt", "insured-a").Code, "not revoked")
	assert.Equal(t, http.StatusNotFound, call(h, http.MethodGet, "/v1/consents/cns-a-revoked/revocation-receipt", "insured-b").Code, "another holder")
	assert.Equal(t, http.StatusNotFound, call(h, http.MethodGet, "/v1/consents/%00/revocation-receipt", "insured-a").Code)
	assert.Equal(t, http.StatusServiceUnavailable, call(h, http.MethodGet, "/v1/consents/cns-a-down/revocation-receipt", "insured-a").Code)
	assert.Equal(t, http.StatusForbidden, call(h, http.MethodGet, "/v1/consents/cns-a-revoked/revocation-receipt", "insured-none").Code)
}

func TestTheConsentSaysWhetherItIsRevocableAndCarriesItsRevocation(t *testing.T) {
	holders := newHolderFake()
	holders.views["1020304050"] = append(holders.views["1020304050"], revokedView("cns-a-new"))
	verifier := tokenVerifier{"insured-a": {Subject: "sub-a", Roles: []security.Role{security.RoleInsured}}}
	h := httpadapter.NewRouter(httpadapter.WithHolderConsents(holders, verifier))

	rec := call(h, http.MethodGet, "/v1/consents", "insured-a")

	require.Equal(t, http.StatusOK, rec.Code)
	var out struct {
		Items []map[string]any `json:"items"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	byID := map[string]map[string]any{}
	for _, item := range out.Items {
		byID[item["consent_id"].(string)] = item
	}
	assert.Equal(t, true, byID["cns-a-active"]["revocable"])
	assert.Nil(t, byID["cns-a-active"]["revocation"])
	assert.Equal(t, false, byID["cns-a-revoked"]["revocable"], "criterion 6")
	seeded := byID["cns-a-revoked"]["revocation"].(map[string]any)
	assert.Equal(t, "completed", seeded["notice_status"], "a revocation without entities has nothing to notify")
	assert.Nil(t, seeded["device"])
	revocation := byID["cns-a-new"]["revocation"].(map[string]any)
	assert.Equal(t, "REV-2026-0088", revocation["receipt_number"])
	assert.Equal(t, "cns-a-new", revocation["consent_id"])
	history := byID["cns-a-new"]["history"].([]any)
	assert.Equal(t, "revocation_requested", history[1].(map[string]any)["status"])
	assert.Equal(t, "revoked", history[2].(map[string]any)["status"])
}
