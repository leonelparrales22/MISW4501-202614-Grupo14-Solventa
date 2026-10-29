package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	frontSHA = "8f14e45fceea167a5a36dedd4bea2543b3a0f5a0e6c4b1e3c2a8f9d7e6b5c4a3"
	backSHA  = "c4ca4238a0b923820dcc509a6f75849bc4ca4238a0b923820dcc509a6f75849b"
)

func postJSON(t *testing.T, h http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
	return rec
}

func readBody(front, back string) string {
	return `{"document_front":"` + base64.StdEncoding.EncodeToString([]byte(front)) +
		`","document_back":"` + base64.StdEncoding.EncodeToString([]byte(back)) + `"}`
}

func verificationBody(documentID string) string {
	return `{"document_id":"` + documentID + `","full_name":"Carolina Pérez Ruiz","birth_date":"1992-04-18",
		"document_front_sha256":"` + frontSHA + `","document_back_sha256":"` + backSHA + `"}`
}

func TestAReadGivesTheDataOfTheDocument(t *testing.T) {
	rec := postJSON(t, newHandler(0), "/documents/reads", readBody("frente de la cedula", "reverso"))

	require.Equal(t, http.StatusOK, rec.Code)
	var got documentRead
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, "CC", got.DocumentType)
	assert.Regexp(t, `^\d{10}$`, got.DocumentID)
	assert.NotEmpty(t, got.GivenNames)
	assert.NotEmpty(t, got.Surnames)
	_, err := time.Parse("2006-01-02", got.BirthDate)
	assert.NoError(t, err)
	assert.NotEmpty(t, got.ReadID)
}

func TestTheSamePhotoReadsTheSame(t *testing.T) {
	a := readDocument([]byte("foto-1"))

	assert.Equal(t, a, readDocument([]byte("foto-1")))
	assert.NotEqual(t, a.DocumentID, readDocument([]byte("foto-2")).DocumentID)
}

func TestAReadNeverGivesARejectingNumber(t *testing.T) {
	for i := 0; i < 500; i++ {
		read := readDocument([]byte{byte(i), byte(i >> 8), 'x'})
		last := read.DocumentID[len(read.DocumentID)-1]
		assert.Truef(t, last >= '3', "%s ends in a rejecting digit", read.DocumentID)
		v := verify(verificationRequest{DocumentID: read.DocumentID}, time.Now())
		assert.Equal(t, outcomeApproved, v.Outcome)
	}
}

func TestAReadNeedsBothFaces(t *testing.T) {
	h := newHandler(0)
	for name, body := range map[string]string{
		"not json":   `{not json`,
		"no back":    `{"document_front":"` + base64.StdEncoding.EncodeToString([]byte("x")) + `"}`,
		"no front":   `{"document_back":"` + base64.StdEncoding.EncodeToString([]byte("x")) + `"}`,
		"not base64": `{"document_front":"%%%","document_back":"%%%"}`,
	} {
		assert.Equal(t, http.StatusBadRequest, postJSON(t, h, "/documents/reads", body).Code, name)
	}
}

func TestTheLastDigitDecidesTheVerification(t *testing.T) {
	h := newHandler(0)
	for document, want := range map[string]verification{
		"1020345670": {Outcome: outcomeRejected, Reason: reasonRestrictedList},
		"1020345671": {Outcome: outcomeRejected, Reason: reasonUnreadable},
		"1020345672": {Outcome: outcomeRejected, Reason: reasonUnverifiable},
		"1020345673": {Outcome: outcomeApproved},
		"1020345679": {Outcome: outcomeApproved},
	} {
		rec := postJSON(t, h, "/verifications", verificationBody(document))

		require.Equal(t, http.StatusOK, rec.Code, document)
		var got verification
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		assert.Equal(t, want.Outcome, got.Outcome, document)
		assert.Equal(t, want.Reason, got.Reason, document)
		assert.NotEmpty(t, got.VerificationID)
		assert.False(t, got.CheckedAt.IsZero())
	}
}

func TestAVerificationNeedsTheData(t *testing.T) {
	h := newHandler(0)
	for name, body := range map[string]string{
		"not json": `{not json`,
		"no name":  strings.Replace(verificationBody("1020345673"), "Carolina Pérez Ruiz", "", 1),
		"bad date": strings.Replace(verificationBody("1020345673"), "1992-04-18", "18/04/1992", 1),
		"bad sha":  strings.Replace(verificationBody("1020345673"), frontSHA, "abc", 1),
		"no doc":   verificationBody(""),
	} {
		assert.Equal(t, http.StatusBadRequest, postJSON(t, h, "/verifications", body).Code, name)
	}
}
