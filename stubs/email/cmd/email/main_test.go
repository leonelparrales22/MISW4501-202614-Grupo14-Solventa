package main

import (
	"bytes"
	"encoding/json"
	"log"
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

// captureLog sends the output of the standard logger to a buffer for the test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})
	return &buf
}

// policyMail is a multi-line message with the signed link of the contractual
// document, like the one origination sends (HU-W1.3-03).
const policyMail = `{"to":"ana@example.com","subject":"Tu póliza de Solventa fue emitida",` +
	`"body":"Hola Ana:\nDescarga tu documento en https://api.example/staging/v1/documents/tok.abc\nSolventa"}`

func TestANewMessageIsWrittenToTheLogOnOneLine(t *testing.T) {
	logs := captureLog(t)
	h := newHandler(0, 0)

	require.Equal(t, http.StatusCreated, post(t, h, "policy-issued:pol-1", policyMail).Code)

	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	require.Len(t, lines, 1, "one line per message, whatever its body")
	line := lines[0]
	assert.Contains(t, line, "message accepted id=msg-1 to=ana@example.com")
	assert.Contains(t, line, `subject="Tu póliza de Solventa fue emitida"`)
	assert.Contains(t, line, "idempotency_key=policy-issued:pol-1")
	assert.Contains(t, line, `body="Hola Ana:\nDescarga tu documento en https://api.example/staging/v1/documents/tok.abc\nSolventa"`,
		"the link of the e-mail can be read from the log (CloudWatch Logs in AWS, HA-D02)")
}

func TestADuplicateIsLoggedWithoutItsBody(t *testing.T) {
	h := newHandler(0, 0)
	require.Equal(t, http.StatusCreated, post(t, h, "policy-issued:pol-1", policyMail).Code)
	logs := captureLog(t)

	require.Equal(t, http.StatusOK, post(t, h, "policy-issued:pol-1", policyMail).Code)

	line := strings.TrimSpace(logs.String())
	assert.Equal(t, "duplicate message id=msg-1 to=ana@example.com idempotency_key=policy-issued:pol-1: already accepted, not stored again", line)
	assert.NotContains(t, line, "documents/tok.abc")
}

func TestAnInvalidMessageIsNotLogged(t *testing.T) {
	logs := captureLog(t)
	h := newHandler(0, 0)

	assert.Equal(t, http.StatusBadRequest, post(t, h, "k", `{"to":"a@b.co"}`).Code)
	assert.Equal(t, http.StatusBadRequest, post(t, h, "", policyMail).Code)
	assert.Empty(t, logs.String())
}
