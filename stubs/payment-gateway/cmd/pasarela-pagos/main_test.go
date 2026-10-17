package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const body = `{"reference":"demo-active-001","customer_id":"cust-1","amount_minor":8950000,"currency":"COP"}`

func post(t *testing.T, h http.Handler, key, payload string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/charges", strings.NewReader(payload))
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

type debugBody struct {
	Requests            map[string]int `json:"requests"`
	RealCharges         map[string]int `json:"real_charges"`
	TotalRequests       int            `json:"total_requests"`
	TotalRealCharges    int            `json:"total_real_charges"`
	SupportsIdempotency bool           `json:"supports_idempotency"`
}

func debug(t *testing.T, h http.Handler) debugBody {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/debug/charges", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var out debugBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	return out
}

func TestAChargeIsMadeAndAnswered(t *testing.T) {
	h := newHandler(0, true)

	rec := post(t, h, "cobro-acc-1", body)

	require.Equal(t, http.StatusCreated, rec.Code)
	var ch charge
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ch))
	assert.Equal(t, "ch-1", ch.ID)
	assert.Equal(t, "succeeded", ch.Status)
	assert.Equal(t, "cobro-acc-1", ch.IdempotencyKey)
	assert.EqualValues(t, 8_950_000, ch.AmountMinor)
	assert.Equal(t, "COP", ch.Currency)
	d := debug(t, h)
	assert.Equal(t, map[string]int{"cobro-acc-1": 1}, d.RealCharges)
	assert.True(t, d.SupportsIdempotency)
}

func TestTheSameKeyGetsTheSameAnswerWithoutChargingAgain(t *testing.T) {
	h := newHandler(0, true)
	first := post(t, h, "cobro-acc-1", body)

	again := post(t, h, "cobro-acc-1", body)

	assert.Equal(t, http.StatusOK, again.Code, "replayed")
	assert.JSONEq(t, first.Body.String(), again.Body.String())
	d := debug(t, h)
	assert.Equal(t, 2, d.Requests["cobro-acc-1"], "two requests received")
	assert.Equal(t, 1, d.RealCharges["cobro-acc-1"], "one real charge")
	assert.Equal(t, 2, d.TotalRequests)
	assert.Equal(t, 1, d.TotalRealCharges)
}

func TestConcurrentRequestsWithTheSameKeyChargeOnce(t *testing.T) {
	h := newHandler(0, true)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			post(t, h, "cobro-acc-1", body)
		}()
	}
	wg.Wait()

	d := debug(t, h)
	assert.Equal(t, 50, d.Requests["cobro-acc-1"])
	assert.Equal(t, 1, d.RealCharges["cobro-acc-1"])
}

func TestWithoutIdempotencySupportEveryRequestCharges(t *testing.T) {
	h := newHandler(0, false)
	post(t, h, "cobro-acc-1", body)

	rec := post(t, h, "cobro-acc-1", body)

	assert.Equal(t, http.StatusCreated, rec.Code)
	d := debug(t, h)
	assert.Equal(t, 2, d.RealCharges["cobro-acc-1"], "the duplicate the counterfactual run shows")
	assert.False(t, d.SupportsIdempotency)
}

func TestAKeyReusedWithAnotherChargeIsRejected(t *testing.T) {
	h := newHandler(0, true)
	post(t, h, "cobro-acc-1", body)

	rec := post(t, h, "cobro-acc-1", strings.Replace(body, "8950000", "100", 1))

	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Contains(t, rec.Body.String(), "idempotency_key_reused")
	assert.Equal(t, 1, debug(t, h).RealCharges["cobro-acc-1"])
}

func TestADeclinedReferenceIsDeclinedEveryTime(t *testing.T) {
	h := newHandler(0, true)
	declined := strings.Replace(body, "demo-active-001", "demo-active-declined01", 1)

	for i := 0; i < 2; i++ {
		rec := post(t, h, "cobro-acc-2", declined)
		assert.Equal(t, http.StatusPaymentRequired, rec.Code)
		var e errorBody
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &e))
		assert.Equal(t, "payment_declined", e.Error)
		assert.Equal(t, "51", e.DeclineCode)
	}
	d := debug(t, h)
	assert.Equal(t, 2, d.Requests["cobro-acc-2"])
	assert.Zero(t, d.RealCharges["cobro-acc-2"], "a decline moves no money")
}

func TestInvalidRequestsAreRejected(t *testing.T) {
	h := newHandler(0, true)
	for name, tc := range map[string]struct{ key, body string }{
		"no key":       {"", body},
		"long key":     {strings.Repeat("k", 256), body},
		"not json":     {"k", "{"},
		"zero amount":  {"k", `{"reference":"r","customer_id":"c","amount_minor":0,"currency":"COP"}`},
		"no reference": {"k", `{"customer_id":"c","amount_minor":1,"currency":"COP"}`},
		"no customer":  {"k", `{"reference":"r","amount_minor":1,"currency":"COP"}`},
		"no currency":  {"k", `{"reference":"r","customer_id":"c","amount_minor":1}`},
	} {
		rec := post(t, h, tc.key, tc.body)
		assert.Equal(t, http.StatusBadRequest, rec.Code, name)
	}
	assert.Zero(t, debug(t, h).TotalRequests, "an invalid request is not a charge request")
}

func TestResetClearsTheCounters(t *testing.T) {
	h := newHandler(0, true)
	post(t, h, "cobro-acc-1", body)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/debug/reset", nil))

	assert.Equal(t, http.StatusNoContent, rec.Code)
	d := debug(t, h)
	assert.Zero(t, d.TotalRequests)
	assert.Empty(t, d.RealCharges)
	assert.Equal(t, "ch-1", decodeCharge(t, post(t, h, "cobro-acc-1", body)).ID, "the sequence starts again")
}

func TestTheDelayComesAfterTheChargeIsRecorded(t *testing.T) {
	h := newHandler(150*time.Millisecond, true)
	srv := httptest.NewServer(h)
	defer srv.Close()
	client := &http.Client{Timeout: 50 * time.Millisecond}
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/charges", strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Idempotency-Key", "cobro-acc-1")

	_, err = client.Do(req)

	require.Error(t, err, "the caller timed out: an ambiguous answer")
	assert.Equal(t, 1, debug(t, h).RealCharges["cobro-acc-1"], "yet the charge happened")
}

func TestHealth(t *testing.T) {
	rec := httptest.NewRecorder()
	newHandler(0, true).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestEnvHelpers(t *testing.T) {
	t.Setenv("STUB_TEST_INT", "25")
	assert.Equal(t, 25, envInt("STUB_TEST_INT"))
	t.Setenv("STUB_TEST_INT", "-3")
	assert.Zero(t, envInt("STUB_TEST_INT"))
	t.Setenv("STUB_TEST_INT", "x")
	assert.Zero(t, envInt("STUB_TEST_INT"))
	assert.Equal(t, "9040", orDefault("", "9040"))
	assert.Equal(t, "1", orDefault("1", "9040"))
}

func decodeCharge(t *testing.T, rec *httptest.ResponseRecorder) charge {
	t.Helper()
	var ch charge
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ch))
	return ch
}
