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

// HU-W1.6-01: the refund of the unused part of a premium when the insured
// cancels their policy.

func refundBody(amount string) string {
	return `{"charge_id":"ch-7","reference":"REF-2026-00001","amount_minor":` + amount + `,"currency":"COP"}`
}

func postRefund(t *testing.T, h http.Handler, key, payload string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/refunds", strings.NewReader(payload))
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

type refundDebug struct {
	RefundRequests   map[string]int `json:"refund_requests"`
	RealRefunds      map[string]int `json:"real_refunds"`
	TotalRealRefunds int            `json:"total_real_refunds"`
	TotalRealCharges int            `json:"total_real_charges"`
}

func refundCounters(t *testing.T, h http.Handler) refundDebug {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/debug/charges", nil))
	var out refundDebug
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	return out
}

func TestARefundOfAnUnknownChargeIsMadeAndCounted(t *testing.T) {
	h := newHandler(0, true)

	rec := postRefund(t, h, "reembolso-pol-1", refundBody("13755600"))

	require.Equal(t, http.StatusCreated, rec.Code, "the stub forgets its charges on a restart: the refund does not need it")
	var got refund
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, statusSuccess, got.Status)
	assert.Equal(t, "reembolso-pol-1", got.IdempotencyKey)
	assert.Equal(t, "ch-7", got.ChargeID)
	assert.Equal(t, int64(13_755_600), got.AmountMinor)
	assert.True(t, strings.HasPrefix(got.ID, "re-"))
	c := refundCounters(t, h)
	assert.Equal(t, 1, c.TotalRealRefunds)
	assert.Zero(t, c.TotalRealCharges, "refunds are counted apart from charges")
}

func TestTheSameKeyRefundsOnceEvenAtTheSameTime(t *testing.T) {
	h := newHandler(0, true)
	first := postRefund(t, h, "reembolso-pol-1", refundBody("13755600"))
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			postRefund(t, h, "reembolso-pol-1", refundBody("13755600"))
		}()
	}
	wg.Wait()
	again := postRefund(t, h, "reembolso-pol-1", refundBody("13755600"))

	assert.Equal(t, http.StatusCreated, first.Code)
	assert.Equal(t, http.StatusOK, again.Code, "a retry gets the refund already made")
	assert.JSONEq(t, first.Body.String(), again.Body.String())
	c := refundCounters(t, h)
	assert.Equal(t, 22, c.RefundRequests["reembolso-pol-1"])
	assert.Equal(t, 1, c.RealRefunds["reembolso-pol-1"])
}

func TestARefundKeyReusedWithAnotherAmountIsRejected(t *testing.T) {
	h := newHandler(0, true)
	require.Equal(t, http.StatusCreated, postRefund(t, h, "reembolso-pol-1", refundBody("13755600")).Code)

	rec := postRefund(t, h, "reembolso-pol-1", refundBody("6640600"))

	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Contains(t, rec.Body.String(), "idempotency_key_reused")
	assert.Equal(t, 1, refundCounters(t, h).TotalRealRefunds)
}

func TestARefundOfNothingIsRejected(t *testing.T) {
	h := newHandler(0, true)
	for _, amount := range []string{"0", "-100"} {
		rec := postRefund(t, h, "reembolso-pol-1", refundBody(amount))

		assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, amount)
		assert.Contains(t, rec.Body.String(), "invalid_amount")
	}
	for name, tc := range map[string]struct{ key, payload string }{
		"no key":       {"", refundBody("100")},
		"not json":     {"k", `{not json`},
		"no charge id": {"k", strings.Replace(refundBody("100"), `"ch-7"`, `""`, 1)},
	} {
		assert.Equal(t, http.StatusBadRequest, postRefund(t, h, tc.key, tc.payload).Code, name)
	}
	assert.Zero(t, refundCounters(t, h).TotalRealRefunds)
}
