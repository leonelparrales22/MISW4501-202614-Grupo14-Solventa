package httpadapter

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/issuance"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// ContractQueries is the use case behind the contractual document routes
// (HU-W1.3-03).
type ContractQueries interface {
	Status(ctx context.Context, policyID string) (issuance.ContractView, error)
	Document(ctx context.Context, policyID string) (domain.ContractDocument, []byte, error)
	DocumentByLink(ctx context.Context, token string) (domain.ContractDocument, []byte, error)
}

// WithContractDocuments mounts the contractual document routes (HU-W1.3-03).
// The policy routes are human routes: the Cognito JWT is verified with
// pkg/security, and the insured who owns the policy, an advisor or an
// operations analyst may read. The download by secure link needs no session:
// the signed, expiring token in the link is the authorization.
func WithContractDocuments(q ContractQueries, verifier security.Verifier) Option {
	return func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(noStore, security.Authenticate(verifier),
				security.RequireRole(security.RoleInsured, security.RoleAdvisor, security.RoleOperations))
			r.Get("/v1/policies/{policy_id}/contract-document", getContractDocument(q))
			r.Get("/v1/policies/{policy_id}/issuance-status", getIssuanceStatus(q))
		})
		r.With(noStore).Get("/v1/documents/{token}", downloadByLink(q))
	}
}

type documentBody struct {
	DocumentID     string    `json:"document_id"`
	Version        int       `json:"version"`
	ContentType    string    `json:"content_type"`
	SizeBytes      int       `json:"size_bytes"`
	ChecksumSHA256 string    `json:"checksum_sha256"`
	GeneratedAt    time.Time `json:"generated_at"`
}

type notificationBody struct {
	Channel       string     `json:"channel"`
	Status        string     `json:"status"`
	Attempts      int        `json:"attempts"`
	SentAt        *time.Time `json:"sent_at"`
	LastFailureAt *time.Time `json:"last_failure_at"`
}

type auditEntryBody struct {
	Event           string    `json:"event"`
	At              time.Time `json:"at"`
	DocumentVersion *int      `json:"document_version"`
	DeliveryResult  *string   `json:"delivery_result"`
	Channel         *string   `json:"channel"`
	Attempt         *int      `json:"attempt"`
	Detail          *string   `json:"detail"`
}

type issuanceStatusResponse struct {
	PolicyID     string           `json:"policy_id"`
	Status       string           `json:"status"`
	ActivatedAt  time.Time        `json:"activated_at"`
	AvailableAt  *time.Time       `json:"available_at"`
	WithinSLO    *bool            `json:"within_slo"`
	Document     *documentBody    `json:"document"`
	Notification notificationBody `json:"notification"`
	Audit        []auditEntryBody `json:"audit"`
}

func getIssuanceStatus(q ContractQueries) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		policyID, ok := pathID(req, "policy_id")
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid_request", "policy_id (max 128 chars) is required")
			return
		}
		v, err := q.Status(req.Context(), policyID)
		if err != nil {
			respondContractError(w, req, err)
			return
		}
		writeJSON(w, http.StatusOK, toIssuanceStatus(v))
	}
}

func getContractDocument(q ContractQueries) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		policyID, ok := pathID(req, "policy_id")
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid_request", "policy_id (max 128 chars) is required")
			return
		}
		doc, content, err := q.Document(req.Context(), policyID)
		if err != nil {
			respondContractError(w, req, err)
			return
		}
		writeDocument(w, doc, content)
	}
}

func downloadByLink(q ContractQueries) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		doc, content, err := q.DocumentByLink(req.Context(), chi.URLParam(req, "token"))
		if err != nil {
			respondContractError(w, req, err)
			return
		}
		writeDocument(w, doc, content)
	}
}

func respondContractError(w http.ResponseWriter, req *http.Request, err error) {
	switch {
	case isAuthError(err):
		writeAuthError(w, err)
	case errors.Is(err, ports.ErrContractNotFound):
		writeError(w, http.StatusNotFound, "policy_not_found", "no contractual issuance exists for the policy")
	case errors.Is(err, ports.ErrDocumentNotFound):
		writeError(w, http.StatusNotFound, "document_not_available", "the contractual document is not available yet; retry shortly")
	case errors.Is(err, ports.ErrInvalidLink):
		writeError(w, http.StatusForbidden, "invalid_link", "the link is invalid or expired")
	default:
		slog.ErrorContext(req.Context(), "could not serve the contractual document", "error", err)
		writeError(w, http.StatusServiceUnavailable, "document_unavailable", "the document could not be read; retry later")
	}
}

func writeDocument(w http.ResponseWriter, doc domain.ContractDocument, content []byte) {
	w.Header().Set("Content-Type", doc.ContentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="poliza-%s-v%d.pdf"`, doc.PolicyID, doc.Version))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Document-Checksum-SHA256", doc.Checksum)
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(content); err != nil {
		slog.Error("could not write the document", "policy_id", doc.PolicyID, "error", err)
	}
}

func toIssuanceStatus(v issuance.ContractView) issuanceStatusResponse {
	c := v.Issuance
	out := issuanceStatusResponse{
		PolicyID: c.Event.PolicyID, Status: string(c.Status), ActivatedAt: c.Event.ActivatedAt.UTC(),
		Notification: notificationBody{Channel: c.NotificationChannel, Status: string(c.NotificationStatus), Attempts: c.Attempts},
		Audit:        make([]auditEntryBody, 0, len(v.Audit)),
	}
	if d := c.Document; d != nil {
		out.Document = &documentBody{
			DocumentID: d.ID, Version: d.Version, ContentType: d.ContentType, SizeBytes: d.SizeBytes,
			ChecksumSHA256: d.Checksum, GeneratedAt: d.GeneratedAt.UTC(),
		}
		at, within := c.AvailableAt.UTC(), c.WithinSLO()
		out.AvailableAt, out.WithinSLO = &at, &within
	}
	if !c.NotifiedAt.IsZero() {
		t := c.NotifiedAt.UTC()
		out.Notification.SentAt = &t
	}
	if !c.LastFailureAt.IsZero() {
		t := c.LastFailureAt.UTC()
		out.Notification.LastFailureAt = &t
	}
	for _, e := range v.Audit {
		out.Audit = append(out.Audit, toAuditEntry(e))
	}
	return out
}

func toAuditEntry(e domain.IssuanceAuditEntry) auditEntryBody {
	b := auditEntryBody{Event: e.Event, At: e.At.UTC()}
	if e.DocumentVersion > 0 {
		b.DocumentVersion = &e.DocumentVersion
	}
	if e.DeliveryResult != "" {
		r := string(e.DeliveryResult)
		b.DeliveryResult = &r
	}
	if e.Channel != "" {
		b.Channel = &e.Channel
	}
	if e.Attempt > 0 {
		b.Attempt = &e.Attempt
	}
	if e.Detail != "" {
		b.Detail = &e.Detail
	}
	return b
}
