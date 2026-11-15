package main

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func documentCheckBody(documentType, number string) string {
	return `{"document_type":"` + documentType + `","document_number":"` + number + `","full_name":"Martín López Gómez"}`
}

func TestADocumentEndingIn000IsNotInTheRegistry(t *testing.T) {
	h := newHandler(0)
	for number, want := range map[string]string{
		"1020304000": documentNotFound,
		"1020304001": documentValid,
		"1020300010": documentValid,
		"AB123000":   documentNotFound,
	} {
		rec := postJSON(t, h, "/document-checks", documentCheckBody("CC", number))

		require.Equal(t, http.StatusOK, rec.Code, number)
		var got documentCheck
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		assert.Equal(t, want, got.Status, number)
		assert.NotEmpty(t, got.CheckID)
		assert.False(t, got.CheckedAt.IsZero())
	}
}

func TestTheSameDocumentGetsTheSameCheck(t *testing.T) {
	a := checkDocument(documentCheckRequest{DocumentType: "CC", DocumentNumber: "1020304050"}, time.Time{})

	assert.Equal(t, a.CheckID, checkDocument(documentCheckRequest{DocumentType: "CC", DocumentNumber: " 1020304050 "}, time.Time{}).CheckID)
	assert.NotEqual(t, a.CheckID, checkDocument(documentCheckRequest{DocumentType: "TI", DocumentNumber: "1020304050"}, time.Time{}).CheckID)
}

func TestADocumentCheckNeedsTheTypeTheNumberAndTheName(t *testing.T) {
	h := newHandler(0)
	for name, body := range map[string]string{
		"not json":      `{not json`,
		"unknown type":  documentCheckBody("NIT", "1020304050"),
		"no number":     documentCheckBody("CC", " "),
		"long number":   documentCheckBody("CC", "123456789012345678901234567890123"),
		"no name":       `{"document_type":"CC","document_number":"1020304050","full_name":"  "}`,
		"missing field": `{"document_type":"CC"}`,
	} {
		assert.Equal(t, http.StatusBadRequest, postJSON(t, h, "/document-checks", body).Code, name)
	}
}
