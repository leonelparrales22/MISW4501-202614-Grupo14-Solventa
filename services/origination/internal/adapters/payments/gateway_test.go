package payments_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sharedpayments "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/payments"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/payments"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

func charge() domain.Charge {
	return domain.Charge{AcceptanceID: "acc-1", IdempotencyKey: "cobro-acc-1", OfferID: "demo-active-001",
		CustomerID: "cust-1", AmountMinor: 8_950_000, Currency: "COP"}
}

func gateway(h http.HandlerFunc) (*payments.Gateway, func()) {
	srv := httptest.NewServer(h)
	client := sharedpayments.NewClient(sharedpayments.Config{BaseURL: srv.URL, Timeout: 200 * time.Millisecond,
		Breaker: resilience.Config{ConsecutiveFailures: 5, OpenTimeout: time.Minute, HalfOpenSuccesses: 1}})
	return payments.NewGateway(client), srv.Close
}

func TestChargeSendsTheSnapshotWithItsKeyAndReturnsTheChargeID(t *testing.T) {
	var key, body string
	g, done := gateway(func(w http.ResponseWriter, r *http.Request) {
		key = r.Header.Get("Idempotency-Key")
		buf := make([]byte, 512)
		n, _ := r.Body.Read(buf)
		body = string(buf[:n])
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"charge_id":"ch-9","status":"succeeded"}`))
	})
	defer done()

	ref, err := g.Charge(context.Background(), charge())

	require.NoError(t, err)
	assert.Equal(t, "ch-9", ref)
	assert.Equal(t, "cobro-acc-1", key)
	assert.JSONEq(t, `{"reference":"demo-active-001","customer_id":"cust-1","amount_minor":8950000,"currency":"COP"}`, body)
}

func TestChargeKeepsTheClassificationOfTheClient(t *testing.T) {
	declined, done := gateway(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte(`{"error":"payment_declined","decline_code":"51"}`))
	})
	_, err := declined.Charge(context.Background(), charge())
	done()
	assert.ErrorIs(t, err, idempotency.ErrRejected)

	down, done := gateway(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) })
	_, err = down.Charge(context.Background(), charge())
	done()
	assert.ErrorIs(t, err, idempotency.ErrAmbiguous)
}

// HU-W1.6-01: the refund of a cancellation goes back from the charge that
// paid the month, with its deterministic key.
func TestRefundSendsTheChargeOfTheMonthWithItsKey(t *testing.T) {
	var key, body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key = r.Header.Get("Idempotency-Key")
		buf := make([]byte, 512)
		n, _ := r.Body.Read(buf)
		body = string(buf[:n])
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"refund_id":"re-4","status":"succeeded"}`))
	}))
	defer srv.Close()
	g := payments.NewRefundGateway(sharedpayments.NewClient(sharedpayments.Config{BaseURL: srv.URL}))
	r := domain.Refund{PolicyID: "VH-2026-000101", Reference: "REF-2026-00001", AmountMinor: 13_755_600, Currency: "COP",
		IdempotencyKey: "reembolso-VH-2026-000101", Paid: domain.PaidMonth{Seq: 1, ChargeReference: "ch-1"}}

	ref, err := g.Refund(context.Background(), r)

	require.NoError(t, err)
	assert.Equal(t, "re-4", ref)
	assert.Equal(t, "reembolso-VH-2026-000101", key)
	assert.JSONEq(t, `{"charge_id":"ch-1","reference":"REF-2026-00001","amount_minor":13755600,"currency":"COP"}`, body)

	r.Paid.ChargeReference = ""
	_, err = g.Refund(context.Background(), r)
	assert.ErrorIs(t, err, idempotency.ErrRejected, "without its charge it can never be sent: final, not retried")
}
