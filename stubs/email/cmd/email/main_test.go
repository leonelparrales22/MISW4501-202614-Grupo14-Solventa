package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func post(t *testing.T, h http.Handler, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/messages", strings.NewReader(body))
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

const mail = `{"to":"ana@example.com","subject":"Hola","body":"Cuerpo"}`

func listed(t *testing.T, h http.Handler, query string) []message {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/messages"+query, nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var out struct {
		Messages []message `json:"messages"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	return out.Messages
}

func TestAcceptsAndListsMessages(t *testing.T) {
	h := newHandler(0, 0)

	rec := post(t, h, "k-1", mail)

	require.Equal(t, http.StatusCreated, rec.Code)
	msgs := listed(t, h, "")
	require.Len(t, msgs, 1)
	assert.Equal(t, "ana@example.com", msgs[0].To)
	assert.Equal(t, "k-1", msgs[0].IdempotencyKey)
	assert.Equal(t, "msg-1", msgs[0].ID)
}

func TestAMessageWithTheSameKeyIsStoredOnce(t *testing.T) {
	h := newHandler(0, 0)
	post(t, h, "k-1", mail)

	rec := post(t, h, "k-1", mail)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Len(t, listed(t, h, ""), 1)
}

func TestListFiltersByRecipient(t *testing.T) {
	h := newHandler(0, 0)
	post(t, h, "k-1", mail)
	post(t, h, "k-2", `{"to":"luis@example.com","subject":"Hola","body":"Cuerpo"}`)

	assert.Len(t, listed(t, h, "?to=luis@example.com"), 1)
	assert.Len(t, listed(t, h, ""), 2)
	assert.Empty(t, listed(t, h, "?to=nadie@example.com"))
}

func TestRejectsInvalidRequests(t *testing.T) {
	h := newHandler(0, 0)
	assert.Equal(t, http.StatusBadRequest, post(t, h, "k", `{`).Code)
	assert.Equal(t, http.StatusBadRequest, post(t, h, "", mail).Code, "the idempotency key is required")
	assert.Equal(t, http.StatusBadRequest, post(t, h, "k", `{"to":"a@b.co"}`).Code)
	assert.Empty(t, listed(t, h, ""))
}

func TestFailFirstInjectsFailuresThenRecovers(t *testing.T) {
	h := newHandler(0, 2)

	assert.Equal(t, http.StatusServiceUnavailable, post(t, h, "k-1", mail).Code)
	assert.Equal(t, http.StatusServiceUnavailable, post(t, h, "k-1", mail).Code)
	assert.Equal(t, http.StatusCreated, post(t, h, "k-1", mail).Code)
	assert.Len(t, listed(t, h, ""), 1)
}

func TestDelayAndHealth(t *testing.T) {
	h := newHandler(20*time.Millisecond, 0)
	start := time.Now()
	post(t, h, "k-1", mail)
	assert.GreaterOrEqual(t, time.Since(start), 20*time.Millisecond)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestEnvHelpers(t *testing.T) {
	t.Setenv("X_NUM", "15")
	t.Setenv("X_BAD", "nope")
	assert.Equal(t, 15, envInt("X_NUM"))
	assert.Equal(t, 0, envInt("X_BAD"))
	assert.Equal(t, 0, envInt("X_MISSING"))
	assert.Equal(t, "d", orDefault("", "d"))
	assert.Equal(t, "v", orDefault("v", "d"))
}
