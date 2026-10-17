package payments_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/payments"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
)

func request() payments.ChargeRequest {
	return payments.ChargeRequest{
		IdempotencyKey: "cobro-acc-1", Reference: "demo-active-001", CustomerID: "cust-1",
		AmountMinor: 8_950_000, Currency: "COP",
	}
}

func breaker() resilience.Config {
	return resilience.Config{ConsecutiveFailures: 2, OpenTimeout: time.Minute, HalfOpenSuccesses: 1}
}

func client(url string) *payments.Client {
	return payments.NewClient(payments.Config{BaseURL: url, Timeout: 200 * time.Millisecond, Breaker: breaker()})
}

func TestChargeSendsTheIdempotencyKeyAndReturnsTheCharge(t *testing.T) {
	var got map[string]any
	var key, contentType, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, key, contentType = r.URL.Path, r.Header.Get("Idempotency-Key"), r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"charge_id":"ch-1","status":"succeeded","idempotency_key":"cobro-acc-1","amount_minor":8950000,"currency":"COP"}`))
	}))
	defer srv.Close()

	ch, err := payments.NewClient(payments.Config{BaseURL: srv.URL + "/", Breaker: breaker()}).Charge(context.Background(), request())

	require.NoError(t, err)
	assert.Equal(t, payments.Charge{ID: "ch-1", Status: "succeeded", IdempotencyKey: "cobro-acc-1", AmountMinor: 8_950_000, Currency: "COP"}, ch)
	assert.Equal(t, "/charges", path)
	assert.Equal(t, "cobro-acc-1", key)
	assert.Equal(t, "application/json", contentType)
	assert.Equal(t, map[string]any{"reference": "demo-active-001", "customer_id": "cust-1", "amount_minor": 8.95e6, "currency": "COP"}, got,
		"the key travels in the header only")
}

func TestARepeatedKeyAnsweredWith200IsASuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"charge_id":"ch-1","status":"succeeded"}`))
	}))
	defer srv.Close()

	ch, err := client(srv.URL).Charge(context.Background(), request())

	require.NoError(t, err)
	assert.Equal(t, "ch-1", ch.ID)
}

func TestADeclineIsAFinalRejectionWithItsCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte(`{"error":"payment_declined","message":"declined by the issuer","decline_code":"51"}`))
	}))
	defer srv.Close()

	_, err := client(srv.URL).Charge(context.Background(), request())

	require.Error(t, err)
	assert.ErrorIs(t, err, payments.ErrRejected)
	assert.ErrorIs(t, err, idempotency.ErrRejected)
	assert.NotErrorIs(t, err, idempotency.ErrAmbiguous)
	var rej *payments.RejectedError
	require.ErrorAs(t, err, &rej)
	assert.Equal(t, http.StatusPaymentRequired, rej.StatusCode)
	assert.Equal(t, "payment_declined", rej.Code)
	assert.Equal(t, "51", rej.DeclineCode)
	assert.Contains(t, err.Error(), "decline code 51")
}

func TestOtherClientErrorsAreRejectionsAndNeverOpenTheCircuit(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"error":"idempotency_key_reused"}`))
	}))
	defer srv.Close()
	c := client(srv.URL)

	for i := 0; i < 5; i++ {
		_, err := c.Charge(context.Background(), request())
		assert.ErrorIs(t, err, payments.ErrRejected)
	}

	assert.EqualValues(t, 5, calls.Load(), "a rejection is an answer of a healthy gateway")
	assert.Equal(t, "closed", c.BreakerState())
}

func TestServerErrorsAndNotNowAnswersAreAmbiguous(t *testing.T) {
	for _, status := range []int{
		http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable,
		http.StatusRequestTimeout, http.StatusConflict, http.StatusTooEarly, http.StatusTooManyRequests,
		http.StatusAccepted, http.StatusFound,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Location", "/elsewhere")
			w.WriteHeader(status)
		}))
		_, err := client(srv.URL).Charge(context.Background(), request())
		assert.ErrorIs(t, err, payments.ErrAmbiguous, "status %d", status)
		assert.ErrorIs(t, err, idempotency.ErrAmbiguous, "status %d", status)
		srv.Close()
	}
}

func TestATimeoutIsAmbiguous(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	defer close(release)

	start := time.Now()
	_, err := client(srv.URL).Charge(context.Background(), request())

	assert.ErrorIs(t, err, payments.ErrAmbiguous)
	assert.Less(t, time.Since(start), 2*time.Second, "cut at the timeout")
}

func TestARefusedConnectionIsAmbiguous(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())

	_, err = client("http://"+addr).Charge(context.Background(), request())

	assert.ErrorIs(t, err, payments.ErrAmbiguous)
}

func TestAConnectionCutInTheMiddleOfTheAnswerIsAmbiguous(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		conn, buf, err := w.(http.Hijacker).Hijack()
		if !assert.NoError(t, err) {
			return
		}
		_, _ = buf.WriteString("HTTP/1.1 201 Created\r\nContent-Type: application/json\r\nContent-Length: 200\r\n\r\n{\"charge_id\":")
		_ = buf.Flush()
		_ = conn.Close()
	}))
	defer srv.Close()

	_, err := client(srv.URL).Charge(context.Background(), request())

	assert.ErrorIs(t, err, payments.ErrAmbiguous, "the charge may have happened: retry with the same key")
}

func TestAnOpenCircuitDoesNotSendTheCharge(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	c := client(srv.URL)
	_, _ = c.Charge(context.Background(), request())
	_, _ = c.Charge(context.Background(), request())
	require.Equal(t, "open", c.BreakerState())

	_, err := c.Charge(context.Background(), request())

	assert.ErrorIs(t, err, payments.ErrUnavailable)
	assert.ErrorIs(t, err, idempotency.ErrNotSent)
	assert.EqualValues(t, 2, calls.Load(), "the third charge never left")
}

func TestAnInvalidChargeNeverReachesTheGateway(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1) }))
	defer srv.Close()
	c := client(srv.URL)

	for name, mutate := range map[string]func(*payments.ChargeRequest){
		"no key":       func(r *payments.ChargeRequest) { r.IdempotencyKey = "" },
		"no reference": func(r *payments.ChargeRequest) { r.Reference = "" },
		"no customer":  func(r *payments.ChargeRequest) { r.CustomerID = "" },
		"zero amount":  func(r *payments.ChargeRequest) { r.AmountMinor = 0 },
		"no currency":  func(r *payments.ChargeRequest) { r.Currency = "" },
	} {
		req := request()
		mutate(&req)
		_, err := c.Charge(context.Background(), req)
		assert.True(t, errors.Is(err, payments.ErrInvalidCharge), name)
	}
	assert.Zero(t, calls.Load())
}

func TestRejectedErrorWithoutDetails(t *testing.T) {
	err := &payments.RejectedError{StatusCode: http.StatusBadRequest}
	assert.Equal(t, "payment gateway rejected the charge (status 400)", err.Error())
	assert.NotErrorIs(t, err, payments.ErrAmbiguous)
}
