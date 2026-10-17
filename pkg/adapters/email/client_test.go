package email_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/email"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
)

func message() email.Message {
	return email.Message{To: "ana@example.com", Subject: "Hola", Body: "Cuerpo", IdempotencyKey: "k-1"}
}

func breaker() resilience.Config {
	return resilience.Config{ConsecutiveFailures: 2, OpenTimeout: time.Minute, HalfOpenSuccesses: 1}
}

func TestSendPostsTheMessageWithItsIdempotencyKey(t *testing.T) {
	var got map[string]string
	var key, contentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/messages", r.URL.Path)
		key, contentType = r.Header.Get("Idempotency-Key"), r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	err := email.NewClient(email.Config{BaseURL: srv.URL + "/", Breaker: breaker()}).Send(context.Background(), message())

	require.NoError(t, err)
	assert.Equal(t, "k-1", key)
	assert.Equal(t, "application/json", contentType)
	assert.Equal(t, map[string]string{"to": "ana@example.com", "subject": "Hola", "body": "Cuerpo"}, got, "the key travels in the header only")
}

func TestSendTreatsADuplicateAsDelivered(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer srv.Close()

	assert.NoError(t, email.NewClient(email.Config{BaseURL: srv.URL, Breaker: breaker()}).Send(context.Background(), message()))
}

func TestSendRejectsAnIncompleteMessageWithoutCallingTheProvider(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()
	c := email.NewClient(email.Config{BaseURL: srv.URL, Breaker: breaker()})

	for _, mutate := range []func(*email.Message){
		func(m *email.Message) { m.To = "" }, func(m *email.Message) { m.Subject = "" },
		func(m *email.Message) { m.Body = "" }, func(m *email.Message) { m.IdempotencyKey = "" },
	} {
		m := message()
		mutate(&m)
		assert.ErrorIs(t, c.Send(context.Background(), m), email.ErrInvalidMessage)
	}
	assert.Zero(t, hits.Load())
	assert.Equal(t, "closed", c.BreakerState(), "caller errors never open the circuit")
}

func TestSendReportsProviderFailuresAsUnavailableAndOpensTheCircuit(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	c := email.NewClient(email.Config{BaseURL: srv.URL, Breaker: breaker()})

	assert.ErrorIs(t, c.Send(context.Background(), message()), email.ErrUnavailable)
	assert.ErrorIs(t, c.Send(context.Background(), message()), email.ErrUnavailable)
	assert.Equal(t, "open", c.BreakerState())

	assert.ErrorIs(t, c.Send(context.Background(), message()), email.ErrUnavailable)
	assert.EqualValues(t, 2, hits.Load(), "the open circuit rejects without calling the provider")
}

func TestSendIsCutAtTheTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer srv.Close()
	defer close(release) // runs first: lets the stuck handler return so Close does not wait
	c := email.NewClient(email.Config{BaseURL: srv.URL, Timeout: 50 * time.Millisecond, Breaker: breaker()})

	start := time.Now()
	err := c.Send(context.Background(), message())

	assert.ErrorIs(t, err, email.ErrUnavailable)
	assert.Less(t, time.Since(start), time.Second)
}

func TestSendToAnUnreachableProvider(t *testing.T) {
	c := email.NewClient(email.Config{BaseURL: "http://127.0.0.1:1", Breaker: breaker()})
	assert.ErrorIs(t, c.Send(context.Background(), message()), email.ErrUnavailable)
}
