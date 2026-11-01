package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const pushBody = `{"token":"inst-1","title":"Pago automático recibido: $ 180.000 COP","body":"Tu vuelo AV-8421 se retrasó 215 min.",` +
	`"group":"parametric_payout","data":{"notification_id":"ntf-1"}}`

func send(t *testing.T, h http.Handler, body, key string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/messages", strings.NewReader(body))
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAMessageIsAcceptedOnceByKey(t *testing.T) {
	h := newHandler(0, 0)

	require.Equal(t, http.StatusCreated, send(t, h, pushBody, "k1").Code)
	require.Equal(t, http.StatusOK, send(t, h, pushBody, "k1").Code)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/messages?token=inst-1", nil))
	var out struct {
		Messages []message `json:"messages"`
		Requests int       `json:"requests"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Len(t, out.Messages, 1)
	assert.Equal(t, 2, out.Requests)
	assert.Equal(t, "parametric_payout", out.Messages[0].Group)
	assert.Equal(t, "ntf-1", out.Messages[0].Data["notification_id"])
}

func TestTheFirstSendsFailWhenAsked(t *testing.T) {
	h := newHandler(0, 2)

	assert.Equal(t, http.StatusServiceUnavailable, send(t, h, pushBody, "k1").Code)
	assert.Equal(t, http.StatusServiceUnavailable, send(t, h, pushBody, "k1").Code)
	assert.Equal(t, http.StatusCreated, send(t, h, pushBody, "k1").Code)
}

func TestAnInvalidTokenIsUnregistered(t *testing.T) {
	rec := send(t, newHandler(0, 0), strings.Replace(pushBody, "inst-1", "invalid-1", 1), "k1")

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), "unregistered")
}

func TestAMessageNeedsItsFieldsAndKey(t *testing.T) {
	h := newHandler(0, 0)

	assert.Equal(t, http.StatusBadRequest, send(t, h, pushBody, "").Code)
	assert.Equal(t, http.StatusBadRequest, send(t, h, `{"token":"inst-1"}`, "k1").Code)
	assert.Equal(t, http.StatusBadRequest, send(t, h, `nope`, "k1").Code)
}

func TestHealth(t *testing.T) {
	rec := httptest.NewRecorder()
	newHandler(0, 0).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
}
