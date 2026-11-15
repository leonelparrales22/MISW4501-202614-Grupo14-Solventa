package kyc_test

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

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/kyc"
)

func documentCheckRequest() kyc.DocumentCheckRequest {
	return kyc.DocumentCheckRequest{DocumentType: "CC", DocumentNumber: "1020304050", FullName: "Martín López Gómez"}
}

func TestCheckDocumentTellsAValidDocumentFromOneNotFound(t *testing.T) {
	for status, valid := range map[string]bool{kyc.DocumentValid: true, kyc.DocumentNotFound: false} {
		var got kyc.DocumentCheckRequest
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, "/document-checks", r.URL.Path)
			require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
			_, _ = w.Write([]byte(`{"check_id":"doc-1","status":"` + status + `","checked_at":"2026-11-15T10:00:00Z"}`))
		}))
		c := kyc.NewClient(testConfig(srv.URL, time.Second))

		check, err := c.CheckDocument(context.Background(), documentCheckRequest())

		require.NoError(t, err, "a document not found is an answer, not a failure")
		assert.Equal(t, valid, check.Valid(), status)
		assert.Equal(t, "doc-1", check.CheckID)
		assert.Equal(t, documentCheckRequest(), got)
		assert.Equal(t, "closed", c.BreakerState())
		srv.Close()
	}
}

func TestCheckDocumentReportsTheFailuresOfTheProvider(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"4xx":            func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadRequest) },
		"5xx":            func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) },
		"bad body":       func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{not json`)) },
		"unknown status": func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"status":"maybe"}`)) },
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(handler)
			defer srv.Close()

			_, err := kyc.NewClient(testConfig(srv.URL, time.Second)).CheckDocument(context.Background(), documentCheckRequest())

			assert.ErrorIs(t, err, kyc.ErrDocumentCheckUnavailable)
		})
	}
}

func TestCheckDocumentIsCutAtItsTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(time.Second):
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	cfg := testConfig(srv.URL, 50*time.Millisecond)
	cfg.HTTPClient = &http.Client{}

	start := time.Now()
	_, err := kyc.NewClient(cfg).CheckDocument(context.Background(), documentCheckRequest())

	assert.ErrorIs(t, err, kyc.ErrDocumentCheckUnavailable)
	assert.Less(t, time.Since(start), 500*time.Millisecond)
}

func TestAnOpenCircuitStopsCheckingDocuments(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	c := kyc.NewClient(testConfig(srv.URL, time.Second))

	for i := 0; i < 5; i++ {
		_, err := c.CheckDocument(context.Background(), documentCheckRequest())
		assert.ErrorIs(t, err, kyc.ErrDocumentCheckUnavailable)
	}

	assert.Equal(t, "open", c.BreakerState())
	assert.EqualValues(t, 3, calls.Load(), "after 3 failures the circuit answers without calling the provider")
}

func TestAnIncompleteDocumentCheckNeverReachesTheProvider(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer srv.Close()
	c := kyc.NewClient(testConfig(srv.URL, time.Second))

	for _, mutate := range []func(*kyc.DocumentCheckRequest){
		func(r *kyc.DocumentCheckRequest) { r.DocumentType = " " },
		func(r *kyc.DocumentCheckRequest) { r.DocumentNumber = "" },
		func(r *kyc.DocumentCheckRequest) { r.FullName = "" },
	} {
		req := documentCheckRequest()
		mutate(&req)
		_, err := c.CheckDocument(context.Background(), req)
		assert.ErrorIs(t, err, kyc.ErrInvalidDocumentCheck)
	}
	assert.Zero(t, calls.Load())
	assert.Equal(t, "closed", c.BreakerState())
}
