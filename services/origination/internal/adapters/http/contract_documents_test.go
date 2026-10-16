package httpadapter_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/issuance"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

var issuedAt = time.Date(2026, 10, 16, 15, 0, 0, 0, time.UTC)

type fakeContracts struct {
	view    issuance.ContractView
	doc     domain.ContractDocument
	content []byte
	err     error
	token   string
}

func (f *fakeContracts) Status(context.Context, string) (issuance.ContractView, error) {
	return f.view, f.err
}

func (f *fakeContracts) Document(context.Context, string) (domain.ContractDocument, []byte, error) {
	return f.doc, f.content, f.err
}

func (f *fakeContracts) DocumentByLink(_ context.Context, token string) (domain.ContractDocument, []byte, error) {
	f.token = token
	return f.doc, f.content, f.err
}

func completedView() issuance.ContractView {
	doc := &domain.ContractDocument{
		ID: "d1", PolicyID: "pol-1", Version: 1, ContentType: "application/pdf", SizeBytes: 5, Checksum: "abc", GeneratedAt: issuedAt.Add(2 * time.Second),
	}
	return issuance.ContractView{
		Issuance: domain.ContractIssuance{
			Event:  domain.CoverageActivated{PolicyID: "pol-1", ActivatedAt: issuedAt},
			Status: domain.ContractCompleted, Document: doc, AvailableAt: issuedAt.Add(2 * time.Second),
			NotificationStatus: domain.NotificationSent, NotificationChannel: domain.ChannelEmail, NotifiedAt: issuedAt.Add(2 * time.Second),
			Attempts: 1, LastFailureAt: issuedAt.Add(time.Second),
		},
		Audit: []domain.IssuanceAuditEntry{
			{PolicyID: "pol-1", Event: domain.AuditDocumentGenerated, At: issuedAt.Add(2 * time.Second), DocumentVersion: 1, Detail: "5 bytes"},
			{PolicyID: "pol-1", Event: domain.AuditNotificationSent, At: issuedAt.Add(2 * time.Second), DocumentVersion: 1,
				DeliveryResult: domain.NotificationSent, Channel: domain.ChannelEmail, Attempt: 2},
		},
	}
}

func contractRouter(q httpadapter.ContractQueries, roles ...security.Role) http.Handler {
	if len(roles) == 0 {
		roles = []security.Role{security.RoleInsured}
	}
	return httpadapter.NewRouter(&fakeResolver{}, httpadapter.WithContractDocuments(q, fixedVerifier{roles: roles}))
}

func TestIssuanceStatusShowsDocumentNotificationAndAudit(t *testing.T) {
	rec, body := callQuotes(contractRouter(&fakeContracts{view: completedView()}), http.MethodGet, "/v1/policies/pol-1/issuance-status", "good", "")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, "pol-1", body["policy_id"])
	assert.Equal(t, "completed", body["status"])
	assert.Equal(t, true, body["within_slo"])
	assert.Equal(t, "2026-10-16T15:00:02Z", body["available_at"])

	doc := body["document"].(map[string]any)
	assert.EqualValues(t, 1, doc["version"])
	assert.Equal(t, "abc", doc["checksum_sha256"])
	n := body["notification"].(map[string]any)
	assert.Equal(t, "sent", n["status"])
	assert.Equal(t, "email", n["channel"])
	assert.Equal(t, "2026-10-16T15:00:02Z", n["sent_at"])
	assert.EqualValues(t, 1, n["attempts"])

	audit := body["audit"].([]any)
	require.Len(t, audit, 2)
	first := audit[0].(map[string]any)
	assert.Equal(t, "document_generated", first["event"])
	assert.Nil(t, first["delivery_result"])
	assert.Nil(t, first["attempt"])
	second := audit[1].(map[string]any)
	assert.Equal(t, "sent", second["delivery_result"])
	assert.EqualValues(t, 1, second["document_version"])
}

func TestIssuanceStatusWhileTheDocumentIsPending(t *testing.T) {
	view := issuance.ContractView{Issuance: domain.ContractIssuance{
		Event: domain.CoverageActivated{PolicyID: "pol-1", ActivatedAt: issuedAt}, Status: domain.ContractPending,
		NotificationStatus: domain.NotificationPending, NotificationChannel: domain.ChannelEmail,
	}}

	_, body := callQuotes(contractRouter(&fakeContracts{view: view}), http.MethodGet, "/v1/policies/pol-1/issuance-status", "good", "")

	assert.Equal(t, "pending", body["status"])
	assert.Nil(t, body["document"])
	assert.Nil(t, body["available_at"])
	assert.Nil(t, body["within_slo"])
	assert.Nil(t, body["notification"].(map[string]any)["sent_at"])
	assert.Empty(t, body["audit"])
}

func TestContractDocumentDownload(t *testing.T) {
	doc := completedView().Issuance.Document
	q := &fakeContracts{doc: *doc, content: []byte("%PDF-1")}

	rec, _ := callQuotes(contractRouter(q), http.MethodGet, "/v1/policies/pol-1/contract-document", "good", "")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/pdf", rec.Header().Get("Content-Type"))
	assert.Equal(t, `attachment; filename="poliza-pol-1-v1.pdf"`, rec.Header().Get("Content-Disposition"))
	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "abc", rec.Header().Get("X-Document-Checksum-SHA256"))
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, "%PDF-1", rec.Body.String())
}

func TestDownloadByLinkNeedsNoSession(t *testing.T) {
	doc := completedView().Issuance.Document
	q := &fakeContracts{doc: *doc, content: []byte("%PDF-1")}

	rec, _ := callQuotes(contractRouter(q), http.MethodGet, "/v1/documents/the-token.sig", "", "")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "the-token.sig", q.token)
	assert.Equal(t, "%PDF-1", rec.Body.String())
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
}

func TestContractErrors(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"unknown policy", ports.ErrContractNotFound, http.StatusNotFound, "policy_not_found"},
		{"document not ready", ports.ErrDocumentNotFound, http.StatusNotFound, "document_not_available"},
		{"other customer", security.ErrForbidden, http.StatusForbidden, "forbidden"},
		{"no session", security.ErrUnauthenticated, http.StatusUnauthorized, "unauthorized"},
		{"infrastructure", errors.New("disk down"), http.StatusServiceUnavailable, "document_unavailable"},
	}
	for _, tc := range cases {
		for _, path := range []string{"/v1/policies/pol-1/contract-document", "/v1/policies/pol-1/issuance-status"} {
			rec, body := callQuotes(contractRouter(&fakeContracts{err: tc.err}), http.MethodGet, path, "good", "")
			assert.Equal(t, tc.status, rec.Code, "%s %s", tc.name, path)
			assert.Equal(t, tc.code, body["error"], "%s %s", tc.name, path)
		}
	}
}

func TestInvalidOrExpiredLinkIsForbidden(t *testing.T) {
	rec, body := callQuotes(contractRouter(&fakeContracts{err: ports.ErrInvalidLink}), http.MethodGet, "/v1/documents/forged", "", "")

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, "invalid_link", body["error"])
}

func TestPolicyIDLengthIsValidated(t *testing.T) {
	long := "/v1/policies/" + strings.Repeat("p", 129)
	rec, body := callQuotes(contractRouter(&fakeContracts{}), http.MethodGet, long+"/issuance-status", "good", "")
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "invalid_request", body["error"])

	rec, _ = callQuotes(contractRouter(&fakeContracts{}), http.MethodGet, long+"/contract-document", "good", "")
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestPolicyRoutesRequireAHumanSessionWithAnAllowedRole(t *testing.T) {
	for _, path := range []string{"/v1/policies/pol-1/contract-document", "/v1/policies/pol-1/issuance-status"} {
		q := &fakeContracts{view: completedView(), doc: *completedView().Issuance.Document, content: []byte("x")}
		rec, _ := callQuotes(contractRouter(q), http.MethodGet, path, "", "")
		assert.Equal(t, http.StatusUnauthorized, rec.Code, path)
		rec, _ = callQuotes(contractRouter(q, security.RolePartner), http.MethodGet, path, "good", "")
		assert.Equal(t, http.StatusForbidden, rec.Code, "%s as a partner", path)
		for _, role := range []security.Role{security.RoleInsured, security.RoleAdvisor, security.RoleOperations} {
			rec, _ = callQuotes(contractRouter(q, role), http.MethodGet, path, "good", "")
			assert.Equal(t, http.StatusOK, rec.Code, "%s as %s", path, role)
		}
	}
}
