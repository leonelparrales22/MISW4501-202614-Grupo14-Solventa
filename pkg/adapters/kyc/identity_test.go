package kyc_test

import (
	"context"
	"encoding/base64"
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

func verificationRequest() kyc.VerificationRequest {
	return kyc.VerificationRequest{DocumentID: "1020345678", FullName: "Carolina Pérez Ruiz", BirthDate: "1992-04-18",
		DocumentFrontSHA256: "aa", DocumentBackSHA256: "bb"}
}

func TestReadDocumentSendsBothFacesAndReturnsTheData(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/documents/reads", r.URL.Path)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		_, _ = w.Write([]byte(`{"read_id":"read-1","document_type":"CC","document_id":"1020345678",
			"given_names":"Carolina","surnames":"Pérez Ruiz","birth_date":"1992-04-18"}`))
	}))
	defer srv.Close()

	read, err := kyc.NewClient(testConfig(srv.URL, time.Second)).ReadDocument(context.Background(), []byte("front"), []byte("back"))

	require.NoError(t, err)
	assert.Equal(t, kyc.DocumentRead{ReadID: "read-1", DocumentType: "CC", DocumentID: "1020345678",
		GivenNames: "Carolina", Surnames: "Pérez Ruiz", BirthDate: "1992-04-18"}, read)
	assert.Equal(t, base64.StdEncoding.EncodeToString([]byte("front")), got["document_front"])
	assert.Equal(t, base64.StdEncoding.EncodeToString([]byte("back")), got["document_back"])
}

func TestVerifyTellsAnApprovalFromARejection(t *testing.T) {
	for body, approved := range map[string]bool{
		`{"verification_id":"kyc-1","outcome":"approved","checked_at":"2026-10-29T10:00:00Z"}`:                            true,
		`{"verification_id":"kyc-2","outcome":"rejected","reason":"restricted_list","checked_at":"2026-10-29T10:00:00Z"}`: false,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "/verifications", r.URL.Path)
			_, _ = w.Write([]byte(body))
		}))
		c := kyc.NewClient(testConfig(srv.URL, time.Second))

		v, err := c.Verify(context.Background(), verificationRequest())

		require.NoError(t, err, "a rejection is an answer, not a failure")
		assert.Equal(t, approved, v.Approved())
		assert.Equal(t, "closed", c.BreakerState())
		srv.Close()
	}
}

func TestVerifyReportsTheFailuresOfTheProvider(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"5xx":             func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) },
		"bad body":        func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{not json`)) },
		"unknown outcome": func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"outcome":"maybe"}`)) },
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(handler)
			defer srv.Close()

			_, err := kyc.NewClient(testConfig(srv.URL, time.Second)).Verify(context.Background(), verificationRequest())

			assert.ErrorIs(t, err, kyc.ErrSourceUnavailable)
		})
	}
}

func TestReadDocumentWithoutADocumentIDIsAFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"read_id":"read-1"}`))
	}))
	defer srv.Close()

	_, err := kyc.NewClient(testConfig(srv.URL, time.Second)).ReadDocument(context.Background(), []byte("f"), []byte("b"))

	assert.ErrorIs(t, err, kyc.ErrSourceUnavailable)
}

func TestTheIdentityCallsAreCutAtTheirTimeout(t *testing.T) {
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
	_, err := kyc.NewClient(cfg).Verify(context.Background(), verificationRequest())

	assert.ErrorIs(t, err, kyc.ErrSourceUnavailable)
	assert.Less(t, time.Since(start), 500*time.Millisecond)
}

func TestInvalidIdentityRequestsNeverReachTheProvider(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer srv.Close()
	c := kyc.NewClient(testConfig(srv.URL, time.Second))

	_, err := c.ReadDocument(context.Background(), nil, []byte("b"))
	assert.ErrorIs(t, err, kyc.ErrInvalidIdentityRequest)
	_, err = c.ReadDocument(context.Background(), []byte("f"), nil)
	assert.ErrorIs(t, err, kyc.ErrInvalidIdentityRequest)
	for _, mutate := range []func(*kyc.VerificationRequest){
		func(r *kyc.VerificationRequest) { r.DocumentID = "" },
		func(r *kyc.VerificationRequest) { r.FullName = "" },
		func(r *kyc.VerificationRequest) { r.BirthDate = "" },
		func(r *kyc.VerificationRequest) { r.DocumentFrontSHA256 = "" },
		func(r *kyc.VerificationRequest) { r.DocumentBackSHA256 = "" },
	} {
		req := verificationRequest()
		mutate(&req)
		_, err := c.Verify(context.Background(), req)
		assert.ErrorIs(t, err, kyc.ErrInvalidIdentityRequest)
	}
	assert.Zero(t, calls.Load())
}
