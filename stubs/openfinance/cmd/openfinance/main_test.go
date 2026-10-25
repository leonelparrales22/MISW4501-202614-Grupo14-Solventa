package main

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildProfileIsDeterministicPerCustomer(t *testing.T) {
	now := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)

	a, b := buildProfile("c1", now), buildProfile("c1", now)

	assert.Equal(t, a, b)
	assert.GreaterOrEqual(t, a.Score, 0.0)
	assert.Less(t, a.Score, 1.0)
	assert.NotEmpty(t, a.Category)
}

func TestBuildProfileCategoriesFollowTheScore(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 500; i++ {
		p := buildProfile("customer-"+string(rune('a'+i%26))+string(rune('a'+i/26%26)), time.Now())
		seen[p.Category] = true
		switch {
		case p.Score > 0.8:
			assert.Equal(t, "high_risk", p.Category)
		case p.Score < 0.2:
			assert.Equal(t, "preferred", p.Category)
		default:
			assert.Equal(t, "standard", p.Category)
		}
	}
	assert.Len(t, seen, 3)
}

func TestEnvInt(t *testing.T) {
	t.Setenv("DELAY", "250")
	assert.Equal(t, 250, envInt("DELAY"))
	t.Setenv("DELAY", "-5")
	assert.Equal(t, 0, envInt("DELAY"))
	t.Setenv("DELAY", "abc")
	assert.Equal(t, 0, envInt("DELAY"))
	assert.Equal(t, 0, envInt("UNSET_VAR_FOR_TEST"))
}

func TestOrDefault(t *testing.T) {
	assert.Equal(t, "x", orDefault("", "x"))
	assert.Equal(t, "y", orDefault("y", "x"))
}

func TestHandlerServesProfilesAndHealth(t *testing.T) {
	h := newHandler(0)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/profiles/c1", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
	var p profile
	assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &p))
	assert.Equal(t, "c1", p.CustomerID)

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	assert.Equal(t, http.StatusOK, rec.Code)

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/profiles/%20", nil))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandlerAppliesTheArtificialDelay(t *testing.T) {
	h := newHandler(30 * time.Millisecond)
	start := time.Now()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/profiles/c1", nil))
	assert.GreaterOrEqual(t, time.Since(start), 30*time.Millisecond)
}

// HU-M1.3-02: the notices of revoked consents.

func revoke(h http.Handler, key, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/consent-revocations", strings.NewReader(body))
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	return &buf
}

const noticeBody = `{"consent_id":"cns-1","entity_code":"aliado-banco-1","customer_id":"cust-1","effective_at":"2026-10-24T20:00:01Z"}`

func TestRevocationIsRecordedOncePerKey(t *testing.T) {
	logs := captureLog(t)
	h := newServer(options{}).routes()

	first := revoke(h, "revocation-cns-1-aliado-banco-1", noticeBody)
	again := revoke(h, "revocation-cns-1-aliado-banco-1", noticeBody)

	require.Equal(t, http.StatusCreated, first.Code)
	require.Equal(t, http.StatusOK, again.Code)
	var a, b revocation
	require.NoError(t, json.Unmarshal(first.Body.Bytes(), &a))
	require.NoError(t, json.Unmarshal(again.Body.Bytes(), &b))
	assert.Equal(t, a, b, "the same key gets the same result")
	assert.Equal(t, "confirmed", a.Status)
	assert.Equal(t, "of-rev-1", a.Reference)
	assert.False(t, a.ConfirmedAt.IsZero())
	assert.Contains(t, logs.String(), "consent revocation key=revocation-cns-1-aliado-banco-1 entity=aliado-banco-1 consent=cns-1 new=true")
	assert.Contains(t, logs.String(), "consent revocation key=revocation-cns-1-aliado-banco-1 entity=aliado-banco-1 consent=cns-1 new=false")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/debug/consent-revocations", nil))
	var dbg struct {
		Requests int          `json:"requests_received"`
		Injected int          `json:"failures_injected"`
		Unique   int          `json:"unique_revocations"`
		Items    []revocation `json:"revocations"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &dbg))
	assert.Equal(t, 2, dbg.Requests)
	assert.Equal(t, 0, dbg.Injected)
	assert.Equal(t, 1, dbg.Unique)
	assert.Equal(t, "cns-1", dbg.Items[0].ConsentID)
}

func TestRevocationNeedsTheKeyAndAValidBody(t *testing.T) {
	h := newServer(options{}).routes()

	assert.Equal(t, http.StatusBadRequest, revoke(h, "", noticeBody).Code, "no Idempotency-Key")
	assert.Equal(t, http.StatusBadRequest, revoke(h, "k", "{").Code, "not JSON")
	assert.Equal(t, http.StatusBadRequest, revoke(h, "k", `{"consent_id":"cns-1"}`).Code, "no entity")
}

func TestRevocationFailFirstAnswers503ThenRecords(t *testing.T) {
	logs := captureLog(t)
	h := newServer(options{failFirst: 2}).routes()

	assert.Equal(t, http.StatusServiceUnavailable, revoke(h, "k1", noticeBody).Code)
	assert.Equal(t, http.StatusServiceUnavailable, revoke(h, "k1", noticeBody).Code)
	assert.Equal(t, http.StatusCreated, revoke(h, "k1", noticeBody).Code, "the third attempt with the same key is recorded")
	assert.Contains(t, logs.String(), "injected=fail_first")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/debug/consent-revocations", nil))
	assert.Contains(t, rec.Body.String(), `"failures_injected":2`)
	assert.Contains(t, rec.Body.String(), `"unique_revocations":1`)
}

func TestRevocationOfAnUnresponsiveEntityAlwaysAnswers503(t *testing.T) {
	logs := captureLog(t)
	h := newServer(options{unresponsive: map[string]bool{"aliado-banco-1": true}}).routes()
	other := `{"consent_id":"cns-1","entity_code":"aliado-banco-2","customer_id":"cust-1","effective_at":"2026-10-24T20:00:01Z"}`

	for i := 0; i < 3; i++ {
		assert.Equal(t, http.StatusServiceUnavailable, revoke(h, "k1", noticeBody).Code)
	}
	assert.Equal(t, http.StatusCreated, revoke(h, "k2", other).Code, "another entity answers")
	assert.Contains(t, logs.String(), "injected=unresponsive")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/debug/consent-revocations", nil))
	assert.Contains(t, rec.Body.String(), `"unresponsive_entities":["aliado-banco-1"]`)
}

func TestEveryProfileRequestIsLogged(t *testing.T) {
	logs := captureLog(t)
	h := newHandler(0)

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/profiles/cust-9", nil))

	assert.Contains(t, logs.String(), "profile request customer=cust-9")
}

func TestEnvSet(t *testing.T) {
	t.Setenv("SET", " a, b ,,a ")
	assert.Equal(t, map[string]bool{"a": true, "b": true}, envSet("SET"))
	assert.Empty(t, envSet("UNSET_SET_FOR_TEST"))
	assert.Equal(t, []string{"a", "b"}, sortedKeys(map[string]bool{"b": true, "a": true}))
}
