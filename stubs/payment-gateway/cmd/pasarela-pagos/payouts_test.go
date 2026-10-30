package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	validAccount   = "0123-4567-12"
	invalidAccount = "0123-4567-89"
)

func payoutBody(account string) string {
	return `{"reference":"SIN-2026-01655","beneficiary_id":"sub-1","amount_minor":413250000,"currency":"COP",
		"destination":{"bank_code":"1007","account_type":"savings","account_number":"` + account + `"}}`
}

func postPayout(t *testing.T, h http.Handler, key, payload string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/payouts", strings.NewReader(payload))
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

type payoutDebug struct {
	PayoutRequests   map[string]int `json:"payout_requests"`
	RealPayouts      map[string]int `json:"real_payouts"`
	TotalRealPayouts int            `json:"total_real_payouts"`
	TotalRealCharges int            `json:"total_real_charges"`
}

func payoutCounters(t *testing.T, h http.Handler) payoutDebug {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/debug/charges", nil))
	var out payoutDebug
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	return out
}

func TestAPayoutIsPaidAndAnswered(t *testing.T) {
	h := newHandler(0, true)

	rec := postPayout(t, h, "indemnizacion-1", payoutBody(validAccount))

	require.Equal(t, http.StatusCreated, rec.Code)
	var got payout
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, statusPaid, got.Status)
	assert.Equal(t, "indemnizacion-1", got.IdempotencyKey)
	assert.Equal(t, int64(413_250_000), got.AmountMinor)
	assert.Equal(t, validAccount, got.Destination.AccountNumber)
	assert.True(t, strings.HasPrefix(got.ID, "po-"))
	assert.Equal(t, 1, payoutCounters(t, h).TotalRealPayouts)
}

func TestTheSameKeyPaysOnce(t *testing.T) {
	h := newHandler(0, true)

	first := postPayout(t, h, "indemnizacion-1", payoutBody(validAccount))
	again := postPayout(t, h, "indemnizacion-1", payoutBody(validAccount))

	assert.Equal(t, http.StatusCreated, first.Code)
	assert.Equal(t, http.StatusOK, again.Code, "a retry gets the payout already made")
	assert.JSONEq(t, first.Body.String(), again.Body.String())
	c := payoutCounters(t, h)
	assert.Equal(t, 2, c.PayoutRequests["indemnizacion-1"])
	assert.Equal(t, 1, c.RealPayouts["indemnizacion-1"])
}

func TestConcurrentPayoutsWithTheSameKeyPayOnce(t *testing.T) {
	h := newHandler(0, true)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			postPayout(t, h, "indemnizacion-1", payoutBody(validAccount))
		}()
	}
	wg.Wait()

	assert.Equal(t, 1, payoutCounters(t, h).TotalRealPayouts)
}

func TestAnInvalidAccountIsRejectedAndLeavesTheKeyFree(t *testing.T) {
	h := newHandler(0, true)

	rejected := postPayout(t, h, "indemnizacion-1", payoutBody(invalidAccount))
	require.Equal(t, http.StatusUnprocessableEntity, rejected.Code)
	var eb errorBody
	require.NoError(t, json.Unmarshal(rejected.Body.Bytes(), &eb))
	assert.Equal(t, "invalid_destination_account", eb.Error)
	assert.NotEmpty(t, eb.PayoutID)
	assert.Zero(t, payoutCounters(t, h).TotalRealPayouts, "a rejected payout moves no money")

	corrected := postPayout(t, h, "indemnizacion-1", payoutBody(validAccount))

	assert.Equal(t, http.StatusCreated, corrected.Code, "the corrected account is paid with the same key")
	assert.Equal(t, 1, payoutCounters(t, h).RealPayouts["indemnizacion-1"])
}

func TestAPaidKeyReusedWithAnotherAccountIsRejected(t *testing.T) {
	h := newHandler(0, true)
	require.Equal(t, http.StatusCreated, postPayout(t, h, "indemnizacion-1", payoutBody(validAccount)).Code)

	rec := postPayout(t, h, "indemnizacion-1", payoutBody("9876-5432-10"))

	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Contains(t, rec.Body.String(), "idempotency_key_reused")
	assert.Equal(t, 1, payoutCounters(t, h).TotalRealPayouts)
}

func TestWithoutIdempotencySupportEveryPayoutPays(t *testing.T) {
	h := newHandler(0, false)

	postPayout(t, h, "indemnizacion-1", payoutBody(validAccount))
	postPayout(t, h, "indemnizacion-1", payoutBody(validAccount))

	assert.Equal(t, 2, payoutCounters(t, h).TotalRealPayouts)
}

func TestInvalidPayoutsAreRejected(t *testing.T) {
	h := newHandler(0, true)
	for name, tc := range map[string]struct{ key, payload string }{
		"no key":         {"", payoutBody(validAccount)},
		"not json":       {"k", `{not json`},
		"no amount":      {"k", strings.Replace(payoutBody(validAccount), `413250000`, `0`, 1)},
		"no bank":        {"k", strings.Replace(payoutBody(validAccount), `"1007"`, `""`, 1)},
		"short account":  {"k", payoutBody("12345")},
		"no beneficiary": {"k", strings.Replace(payoutBody(validAccount), `"sub-1"`, `""`, 1)},
	} {
		rec := postPayout(t, h, tc.key, tc.payload)
		assert.Equal(t, http.StatusBadRequest, rec.Code, name)
	}
	assert.Zero(t, payoutCounters(t, h).TotalRealPayouts)
}

func TestPayoutsAreCountedApartFromCharges(t *testing.T) {
	h := newHandler(0, true)

	postPayout(t, h, "indemnizacion-1", payoutBody(validAccount))

	c := payoutCounters(t, h)
	assert.Equal(t, 1, c.TotalRealPayouts)
	assert.Zero(t, c.TotalRealCharges, "the charge experiment (EC-D02) counts charges only")
}

func TestResetClearsThePayouts(t *testing.T) {
	h := newHandler(0, true)
	postPayout(t, h, "indemnizacion-1", payoutBody(validAccount))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/debug/reset", nil))

	assert.Zero(t, payoutCounters(t, h).TotalRealPayouts)
	assert.Equal(t, http.StatusCreated, postPayout(t, h, "indemnizacion-1", payoutBody(validAccount)).Code)
}
