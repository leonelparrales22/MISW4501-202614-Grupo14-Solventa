package claims_test

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
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/claims"
)

func config(url string, timeout time.Duration) claims.Config {
	return claims.Config{BaseURL: url, Timeout: timeout,
		Breaker: resilience.Config{ConsecutiveFailures: 3, OpenTimeout: 5 * time.Second, HalfOpenSuccesses: 3}}
}

func TestTheOpenClaimsOfThePolicyAreAskedInsideTheVPC(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/internal/policies/pol-acc-1/open-claims", r.URL.Path)
		assert.Empty(t, r.Header.Get("Authorization"), "the internal route carries no token")
		_, _ = w.Write([]byte(`{"policy_id":"pol-acc-1","open_claims":2}`))
	}))
	defer srv.Close()

	n, err := claims.NewOpenClaims(config(srv.URL+"/", time.Second)).OpenClaims(context.Background(), "pol-acc-1")

	require.NoError(t, err)
	assert.Equal(t, 2, n)
}

func TestEveryFailureOfClaimsIsUnavailable(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"5xx":      func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) },
		"4xx":      func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadRequest) },
		"bad body": func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{not json`)) },
		"no count": func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"policy_id":"pol-acc-1"}`)) },
		"other policy": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"policy_id":"x","open_claims":0}`))
		},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(handler)
			defer srv.Close()

			_, err := claims.NewOpenClaims(config(srv.URL, time.Second)).OpenClaims(context.Background(), "pol-acc-1")

			assert.ErrorIs(t, err, claims.ErrUnavailable)
		})
	}
}

func TestTheQueryIsCutAtItsTimeoutAndTheCircuitOpens(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		select {
		case <-time.After(time.Second):
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	cfg := config(srv.URL, 50*time.Millisecond)
	cfg.HTTPClient = &http.Client{}
	c := claims.NewOpenClaims(cfg)

	start := time.Now()
	for i := 0; i < 5; i++ {
		_, err := c.OpenClaims(context.Background(), "pol-acc-1")
		assert.ErrorIs(t, err, claims.ErrUnavailable)
	}

	assert.Less(t, time.Since(start), 900*time.Millisecond, "three cuts of 50 ms, then the open circuit answers at once")
	assert.Equal(t, "open", c.BreakerState())
	assert.EqualValues(t, 3, calls.Load())
}
