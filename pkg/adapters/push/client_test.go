package push_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/push"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
)

func message() push.Message {
	return push.Message{Token: "inst-1", Title: "Pago automático recibido", Body: "Tu vuelo AV-8421 se retrasó 215 min.",
		Group: "parametric_payout", Data: map[string]string{"notification_id": "ntf-1"}, IdempotencyKey: "ntf-1:inst-1"}
}

func clientOver(t *testing.T, h http.HandlerFunc, breaker resilience.Config) *push.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return push.NewClient(push.Config{BaseURL: srv.URL + "/", Timeout: 200 * time.Millisecond, Breaker: breaker})
}

func TestSendDeliversWithTheKeyAndReturnsTheReceipt(t *testing.T) {
	c := clientOver(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/messages", r.URL.Path)
		assert.Equal(t, "ntf-1:inst-1", r.Header.Get("Idempotency-Key"))
		var in map[string]any
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&in))
		assert.Equal(t, "parametric_payout", in["group"])
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"push-1"}`))
	}, resilience.DefaultConfig())

	got, err := c.Send(context.Background(), message())

	require.NoError(t, err)
	assert.Equal(t, "push-1", got.ID)
	assert.Equal(t, "closed", c.BreakerState())
}

func TestSendClassifiesTheAnswers(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
		want   error
	}{
		"unregistered device": {http.StatusNotFound, `{"error":"unregistered"}`, idempotency.ErrRejected},
		"invalid message":     {http.StatusBadRequest, ``, idempotency.ErrRejected},
		"provider down":       {http.StatusServiceUnavailable, ``, idempotency.ErrAmbiguous},
		"too many requests":   {http.StatusTooManyRequests, ``, idempotency.ErrAmbiguous},
		"cut receipt":         {http.StatusCreated, `{"id":`, idempotency.ErrAmbiguous},
	} {
		t.Run(name, func(t *testing.T) {
			c := clientOver(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}, resilience.DefaultConfig())

			_, err := c.Send(context.Background(), message())

			require.ErrorIs(t, err, tc.want)
		})
	}
}

func TestSendIsNotSentWhileTheBreakerIsOpen(t *testing.T) {
	c := clientOver(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) },
		resilience.Config{ConsecutiveFailures: 1, OpenTimeout: time.Minute, HalfOpenSuccesses: 1})

	_, first := c.Send(context.Background(), message())
	_, second := c.Send(context.Background(), message())

	require.ErrorIs(t, first, push.ErrAmbiguous)
	require.ErrorIs(t, second, push.ErrUnavailable)
	require.ErrorIs(t, second, idempotency.ErrNotSent)
}

func TestSendRefusesAnIncompleteMessage(t *testing.T) {
	c := push.NewClient(push.Config{BaseURL: "http://127.0.0.1:1"})
	m := message()
	m.IdempotencyKey = ""

	_, err := c.Send(context.Background(), m)

	require.ErrorIs(t, err, push.ErrInvalidMessage)
}

func TestSendWithoutAnAnswerIsAmbiguous(t *testing.T) {
	c := push.NewClient(push.Config{BaseURL: "http://127.0.0.1:1", Timeout: 100 * time.Millisecond})

	_, err := c.Send(context.Background(), message())

	require.ErrorIs(t, err, idempotency.ErrAmbiguous)
}
