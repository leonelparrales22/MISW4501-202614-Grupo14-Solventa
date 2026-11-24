package payments_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/payments"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
)

// HU-W1.6-01: the refund of the unused part of a premium when the insured
// cancels their policy.

func refundRequest() payments.RefundRequest {
	return payments.RefundRequest{IdempotencyKey: "reembolso-VH-2026-000001", ChargeID: "ch-7",
		Reference: "REF-2026-00001", AmountMinor: 13_755_600, Currency: "COP"}
}

func TestRefundSendsTheKeyAndTheChargeAndARepeatIsASuccess(t *testing.T) {
	var got map[string]any
	var key, path string
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, path = r.Header.Get("Idempotency-Key"), r.URL.Path
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		status := http.StatusCreated
		if calls.Add(1) > 1 {
			status = http.StatusOK // already refunded with this key
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"refund_id":"re-1","status":"succeeded","idempotency_key":"reembolso-VH-2026-000001",
			"charge_id":"ch-7","amount_minor":13755600,"currency":"COP"}`))
	}))
	defer srv.Close()
	c := client(srv.URL)

	first, err := c.Refund(context.Background(), refundRequest())
	require.NoError(t, err)
	again, err := c.Refund(context.Background(), refundRequest())
	require.NoError(t, err)

	want := payments.Refund{ID: "re-1", Status: "succeeded", IdempotencyKey: "reembolso-VH-2026-000001", ChargeID: "ch-7",
		AmountMinor: 13_755_600, Currency: "COP"}
	assert.Equal(t, want, first)
	assert.Equal(t, want, again, "a 200 answers a refund already made")
	assert.Equal(t, "/refunds", path)
	assert.Equal(t, "reembolso-VH-2026-000001", key)
	assert.Equal(t, map[string]any{"charge_id": "ch-7", "reference": "REF-2026-00001", "amount_minor": float64(13_755_600),
		"currency": "COP"}, got, "the key travels in the header only")
}

func TestARejectedRefundIsFinal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"error":"idempotency_key_reused"}`))
	}))
	defer srv.Close()
	c := client(srv.URL)

	_, err := c.Refund(context.Background(), refundRequest())

	var rejected *payments.RejectedError
	require.True(t, errors.As(err, &rejected))
	assert.Equal(t, "idempotency_key_reused", rejected.Code)
	assert.ErrorIs(t, err, idempotency.ErrRejected)
	assert.Equal(t, "payment gateway rejected the refund (status 422, code idempotency_key_reused)", err.Error())
	assert.Equal(t, "closed", c.BreakerState(), "a rejection is an answer, not a failure")
}

func TestARefundWithAnUnknownOutcomeIsAmbiguousAndAnIsolatedGatewayIsNotCalled(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"5xx": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) },
		"cut body": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"refund_id":`))
		},
	} {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				handler(w, r)
			}))
			defer srv.Close()
			c := client(srv.URL)

			_, err := c.Refund(context.Background(), refundRequest())
			assert.ErrorIs(t, err, payments.ErrAmbiguous)
			assert.ErrorIs(t, err, idempotency.ErrAmbiguous)

			_, _ = c.Refund(context.Background(), refundRequest())
			require.Equal(t, "open", c.BreakerState())
			_, err = c.Refund(context.Background(), refundRequest())
			assert.ErrorIs(t, err, payments.ErrUnavailable)
			assert.ErrorIs(t, err, idempotency.ErrNotSent)
			assert.Equal(t, int32(2), calls.Load(), "the open circuit does not send the refund")
		})
	}
}

func TestAnInvalidRefundNeverReachesTheGateway(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer srv.Close()
	c := client(srv.URL)

	for name, mutate := range map[string]func(*payments.RefundRequest){
		"no key":       func(r *payments.RefundRequest) { r.IdempotencyKey = "" },
		"no charge":    func(r *payments.RefundRequest) { r.ChargeID = "" },
		"no reference": func(r *payments.RefundRequest) { r.Reference = "" },
		"zero amount":  func(r *payments.RefundRequest) { r.AmountMinor = 0 },
		"no currency":  func(r *payments.RefundRequest) { r.Currency = "" },
	} {
		req := refundRequest()
		mutate(&req)
		_, err := c.Refund(context.Background(), req)
		assert.ErrorIs(t, err, payments.ErrInvalidRefund, name)
	}
	assert.Zero(t, calls.Load())
}
