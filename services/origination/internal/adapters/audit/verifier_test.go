package audit_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
	adapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/audit"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

func breaker() resilience.Config {
	return resilience.Config{ConsecutiveFailures: 3, OpenTimeout: 80 * time.Millisecond, HalfOpenSuccesses: 2}
}

func newVerifier(url string, timeout time.Duration) *adapter.Verifier {
	return adapter.NewVerifier(adapter.Config{BaseURL: url, Timeout: timeout, Breaker: breaker()})
}

func serve(t *testing.T, h http.HandlerFunc) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestVerifyAnActiveConsent(t *testing.T) {
	srv, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/audit/consent-verifications", r.URL.Path)
		assert.Equal(t, "cust 1&x=2", r.URL.Query().Get("customer_id"), "the id travels escaped in the query")
		_, _ = w.Write([]byte(`{"active":true,"consent_id":"cns-1","granted_at":"2026-10-17T12:00:00Z","expires_at":"2027-10-17T12:00:00Z","sources":["open_finance"]}`))
	})

	got, err := newVerifier(srv.URL+"/", time.Second).Verify(context.Background(), "cust 1&x=2")

	require.NoError(t, err)
	assert.True(t, got.Active)
	assert.Equal(t, "cns-1", got.ConsentID)
	assert.Equal(t, time.Date(2027, 10, 17, 12, 0, 0, 0, time.UTC), got.ExpiresAt)
}

func TestVerifyACustomerWithoutConsentIsInactiveNotAnError(t *testing.T) {
	srv, _ := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"active":false,"consent_id":null,"granted_at":null,"expires_at":null,"sources":[]}`))
	})

	got, err := newVerifier(srv.URL, time.Second).Verify(context.Background(), "c1")

	require.NoError(t, err)
	assert.False(t, got.Active)
	assert.Empty(t, got.ConsentID)
	assert.True(t, got.ExpiresAt.IsZero())
}

func TestEveryFailureIsUnverifiable(t *testing.T) {
	tests := map[string]http.HandlerFunc{
		"500":            func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) },
		"503":            func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) },
		"400":            func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadRequest) },
		"404":            func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) },
		"malformed body": func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"active":`)) },
		"not JSON":       func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`<html>`)) },
		"empty body":     func(http.ResponseWriter, *http.Request) {},
		"slow answer": func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(300 * time.Millisecond)
			_, _ = w.Write([]byte(`{"active":true}`))
		},
		"connection reset": func(w http.ResponseWriter, _ *http.Request) { panic(http.ErrAbortHandler) },
	}
	for name, h := range tests {
		t.Run(name, func(t *testing.T) {
			srv, _ := serve(t, h)

			got, err := newVerifier(srv.URL, 100*time.Millisecond).Verify(context.Background(), "c1")

			require.ErrorIs(t, err, ports.ErrConsentUnverifiable)
			assert.False(t, got.Active, "an unverifiable consent is never active")
		})
	}
}

func TestVerifyWithAnUnreachableAudit(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()

	_, err := newVerifier(url, 100*time.Millisecond).Verify(context.Background(), "c1")

	assert.ErrorIs(t, err, ports.ErrConsentUnverifiable)
}

func TestVerifyHonorsTheCallerContext(t *testing.T) {
	srv, _ := serve(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"active":true}`)) })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := newVerifier(srv.URL, time.Second).Verify(ctx, "c1")

	assert.ErrorIs(t, err, ports.ErrConsentUnverifiable)
}

func TestTheDefaultTimeoutIs300Milliseconds(t *testing.T) {
	assert.Equal(t, 300*time.Millisecond, adapter.DefaultTimeout)
	srv, _ := serve(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"active":true}`)) })

	got, err := adapter.NewVerifier(adapter.Config{BaseURL: srv.URL, Breaker: breaker()}).Verify(context.Background(), "c1")

	require.NoError(t, err)
	assert.True(t, got.Active)
}

func TestTheCircuitOpensAfterRepeatedFailuresAndAuditIsNotCalled(t *testing.T) {
	srv, hits := serve(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) })
	v := newVerifier(srv.URL, time.Second)
	for i := 0; i < 3; i++ {
		_, err := v.Verify(context.Background(), "c1")
		require.ErrorIs(t, err, ports.ErrConsentUnverifiable)
	}
	require.Equal(t, "open", v.BreakerState())
	before := hits.Load()

	_, err := v.Verify(context.Background(), "c1")

	require.ErrorIs(t, err, ports.ErrConsentUnverifiable)
	assert.ErrorIs(t, err, resilience.ErrOpen)
	assert.Equal(t, before, hits.Load(), "an open circuit rejects without calling audit")
}

func TestTheCircuitClosesWhenAuditRecovers(t *testing.T) {
	var healthy atomic.Bool
	srv, _ := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		if !healthy.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"active":true,"consent_id":"cns-1"}`))
	})
	v := newVerifier(srv.URL, time.Second)
	for i := 0; i < 3; i++ {
		_, _ = v.Verify(context.Background(), "c1")
	}
	require.Equal(t, "open", v.BreakerState())
	healthy.Store(true)
	time.Sleep(120 * time.Millisecond)

	for i := 0; i < 2; i++ {
		got, err := v.Verify(context.Background(), "c1")
		require.NoError(t, err)
		assert.True(t, got.Active)
	}
	assert.Equal(t, "closed", v.BreakerState())
}
