package httpadapter_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/evidence"
)

// The route that issues the signed upload of one evidence (HU-M2.1-01). The
// file never passes through here: only its declaration.

type fakeEvidence struct {
	upload evidence.Upload
	err    error

	gotSubject string
	gotRequest domain.UploadRequest
}

func (f *fakeEvidence) SignUpload(_ context.Context, subject string,
	r domain.UploadRequest) (evidence.Upload, error) {
	f.gotSubject, f.gotRequest = subject, r
	return f.upload, f.err
}

func evidenceRouter(svc httpadapter.EvidenceService, roles ...security.Role) http.Handler {
	if len(roles) == 0 {
		roles = []security.Role{security.RoleInsured}
	}
	return httpadapter.NewRouter(httpadapter.WithEvidence(svc, fixedVerifier{roles: roles}))
}

func callUpload(h http.Handler, token, body string) (*httptest.ResponseRecorder, map[string]any) {
	req := httptest.NewRequest(http.MethodPost, "/v1/claims/evidence/upload-url", strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	return rec, m
}

const cuerpoDeSubida = `{"kind":"photo","content_type":"image/jpeg","byte_size":312044,` +
	`"sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`

func firmada() evidence.Upload {
	return evidence.Upload{
		ObjectKey: "evidence/sub-carolina/aaaa.jpg",
		URL:       "https://bucket.s3.us-east-2.amazonaws.com/evidence/sub-carolina/aaaa.jpg?X-Amz-Signature=abc",
		Headers:   map[string]string{"Content-Type": "image/jpeg", "Content-Length": "312044"},
		ExpiresAt: filedAt.Add(10 * time.Minute),
	}
}

func TestTheUploadIsAnsweredWithItsSignedUrl(t *testing.T) {
	svc := &fakeEvidence{upload: firmada()}

	rec, body := callUpload(evidenceRouter(svc), "good", cuerpoDeSubida)

	require.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, "evidence/sub-carolina/aaaa.jpg", body["object_key"])
	assert.Contains(t, body["upload_url"], "X-Amz-Signature")
	assert.Equal(t, "2026-10-23T15:10:00Z", body["expires_at"])

	headers, _ := body["upload_headers"].(map[string]any)
	assert.Equal(t, "image/jpeg", headers["Content-Type"])

	// The declaration reaches the use case, and the subject comes from the
	// token and never from the body.
	assert.Equal(t, "sub-carolina", svc.gotSubject)
	assert.Equal(t, "photo", svc.gotRequest.Kind)
	assert.Equal(t, int64(312044), svc.gotRequest.ByteSize)
}

func TestTheUploadNeedsASession(t *testing.T) {
	rec, _ := callUpload(evidenceRouter(&fakeEvidence{upload: firmada()}), "", cuerpoDeSubida)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

// Only the insured uploads evidence: an advisor has no claim of their own.
func TestOnlyTheInsuredUploadsEvidence(t *testing.T) {
	rec, _ := callUpload(
		evidenceRouter(&fakeEvidence{upload: firmada()}, security.RoleAdvisor), "good", cuerpoDeSubida)

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestABodyThatIsNotJSON(t *testing.T) {
	rec, body := callUpload(evidenceRouter(&fakeEvidence{}), "good", "{")

	require.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "invalid_request", body["error"])
}

func TestADeclarationThatBreaksItsRules(t *testing.T) {
	svc := &fakeEvidence{err: &domain.ValidationError{Fields: []domain.FieldError{
		{Field: "byte_size", Code: domain.CodeOutOfRange},
	}}}

	rec, body := callUpload(evidenceRouter(svc), "good", cuerpoDeSubida)

	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Equal(t, "validation_failed", body["error"])
	campos, _ := body["fields"].([]any)
	require.Len(t, campos, 1)
}

// A store that does not answer is retryable, and its reason does not reach
// whoever is calling.
func TestAStoreThatCannotSign(t *testing.T) {
	svc := &fakeEvidence{err: errors.New("the bucket solventa-evidence is not reachable")}

	rec, body := callUpload(evidenceRouter(svc), "good", cuerpoDeSubida)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Equal(t, "evidence_unavailable", body["error"])
	assert.NotContains(t, rec.Body.String(), "solventa-evidence")
}
