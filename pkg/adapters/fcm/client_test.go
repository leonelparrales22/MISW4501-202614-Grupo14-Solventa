package fcm_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/fcm"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
)

// FCM simulated with httptest: what the client sends and how it reads each answer.

type fakeTokens struct{ err error }

func (f fakeTokens) Token() (*oauth2.Token, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &oauth2.Token{AccessToken: "token-de-prueba", Expiry: time.Now().Add(time.Hour)}, nil
}

func client(t *testing.T, url string, tokens oauth2.TokenSource) *fcm.Client {
	t.Helper()
	c, err := fcm.NewClient(fcm.Config{ProjectID: "solventa-94126", TokenSource: tokens, BaseURL: url,
		Breaker: resilience.Config{ConsecutiveFailures: 100}})
	require.NoError(t, err)
	return c
}

func msg() fcm.Message {
	return fcm.Message{Token: "tok-1", Data: map[string]string{"notification_id": "ntf-stage-42"}, CollapseKey: "ntf-stage-42"}
}

func TestADataMessageWithHighPriorityGoesToTheProject(t *testing.T) {
	var got map[string]any
	var path, auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, auth = r.URL.Path, r.Header.Get("Authorization")
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		_, _ = w.Write([]byte(`{"name":"projects/solventa-94126/messages/0:1"}`))
	}))
	defer srv.Close()

	r, err := client(t, srv.URL, fakeTokens{}).Send(context.Background(), msg())

	require.NoError(t, err)
	assert.Equal(t, "projects/solventa-94126/messages/0:1", r.ID)
	assert.Equal(t, "/v1/projects/solventa-94126/messages:send", path)
	assert.Equal(t, "Bearer token-de-prueba", auth)
	assert.Equal(t, map[string]any{"message": map[string]any{
		"token":   "tok-1",
		"data":    map[string]any{"notification_id": "ntf-stage-42"},
		"android": map[string]any{"priority": "HIGH", "ttl": "86400s", "collapse_key": "ntf-stage-42"},
	}}, got, "data only: the app writes the notification")
}

func TestEveryAnswerIsClassified(t *testing.T) {
	for name, tc := range map[string]struct {
		code int
		body string
		want error
	}{
		"an uninstalled app":     {http.StatusNotFound, `{"error":{"status":"UNREGISTERED"}}`, idempotency.ErrRejected},
		"an invalid message":     {http.StatusBadRequest, `{"error":{"status":"INVALID_ARGUMENT"}}`, idempotency.ErrRejected},
		"a quota":                {http.StatusTooManyRequests, `{}`, idempotency.ErrAmbiguous},
		"a failure of FCM":       {http.StatusInternalServerError, `{}`, idempotency.ErrAmbiguous},
		"refused credentials":    {http.StatusUnauthorized, `{}`, idempotency.ErrAmbiguous},
		"an unreadable 200 body": {http.StatusOK, `no es json`, idempotency.ErrAmbiguous},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.code)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			_, err := client(t, srv.URL, fakeTokens{}).Send(context.Background(), msg())

			assert.ErrorIs(t, err, tc.want)
		})
	}
}

func TestWithoutAnAccessTokenNothingIsSent(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer srv.Close()

	_, err := client(t, srv.URL, fakeTokens{err: errors.New("no network")}).Send(context.Background(), msg())

	assert.ErrorIs(t, err, idempotency.ErrNotSent)
	assert.Zero(t, calls)
}

func TestAMessageWithoutTokenOrDataIsTheCallersError(t *testing.T) {
	c := client(t, "http://127.0.0.1:1", fakeTokens{})
	_, err := c.Send(context.Background(), fcm.Message{Data: map[string]string{"a": "b"}})
	assert.ErrorIs(t, err, fcm.ErrInvalidMessage)
	_, err = c.Send(context.Background(), fcm.Message{Token: "tok"})
	assert.ErrorIs(t, err, fcm.ErrInvalidMessage)
}

func TestAnOpenCircuitDoesNotCallFCM(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	c, err := fcm.NewClient(fcm.Config{ProjectID: "p", TokenSource: fakeTokens{}, BaseURL: srv.URL,
		Breaker: resilience.Config{ConsecutiveFailures: 1, OpenTimeout: time.Hour}})
	require.NoError(t, err)

	_, _ = c.Send(context.Background(), msg())
	_, err = c.Send(context.Background(), msg())

	assert.ErrorIs(t, err, idempotency.ErrNotSent)
	assert.Equal(t, "open", c.BreakerState())
}

func TestTheClientNeedsAProjectAndAServiceAccount(t *testing.T) {
	_, err := fcm.NewClient(fcm.Config{TokenSource: fakeTokens{}})
	assert.Error(t, err)
	_, err = fcm.NewClient(fcm.Config{ProjectID: "p", Credentials: []byte(`{"type":"user"}`)})
	assert.Error(t, err)
	c, err := fcm.NewClient(fcm.Config{ProjectID: "p", TokenSource: fakeTokens{}})
	require.NoError(t, err)
	assert.Equal(t, "closed", c.BreakerState())
}
