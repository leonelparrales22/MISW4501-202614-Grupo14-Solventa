package httpadapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/ports"
)

// ComplianceRevocationService is the use case behind the revocation routes of
// the compliance portal (HU-W4.2-02).
type ComplianceRevocationService interface {
	State(ctx context.Context, consentID string) (domain.ComplianceState, error)
	Revoke(ctx context.Context, consentID, actorSub, requestID string, req domain.ComplianceRevokeRequest) (domain.ComplianceState, bool, error)
	Retry(ctx context.Context, consentID, actorSub string) ([]string, domain.ComplianceState, error)
}

// WithComplianceRevocations mounts the revocation routes of the compliance
// portal (HU-W4.2-02), human routes in the family of the consent evidence:
// the Cognito JWT is verified with pkg/security and only the operations role
// reaches them. Any other role is refused before anything is executed.
//
//	GET  /v1/audit/consents/{consent_id}/revocation          the state for the modal, the alert and the retry
//	POST /v1/audit/consents/{consent_id}/revocations         revoke (Idempotency-Key)
//	POST /v1/audit/consents/{consent_id}/revocation-retries  notify again the entities that did not confirm
func WithComplianceRevocations(svc ComplianceRevocationService, verifier security.Verifier) Option {
	return func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(noStore, security.Authenticate(verifier), security.RequireRole(security.RoleOperations))
			r.Get("/v1/audit/consents/{consent_id}/revocation", getComplianceState(svc))
			r.Post("/v1/audit/consents/{consent_id}/revocations", revokeFromPortal(svc))
			r.Post("/v1/audit/consents/{consent_id}/revocation-retries", retryFromPortal(svc))
		})
	}
}

type complianceRevokeRequest struct {
	Reason        string `json:"reason"`
	Reference     string `json:"reference"`
	RoleConfirmed bool   `json:"role_confirmed"`
}

type complianceHolderBody struct {
	FullName       string `json:"full_name"`
	DocumentMasked string `json:"document_masked"`
}

type complianceActorBody struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type complianceRevocationBody struct {
	ReceiptNumber string                 `json:"receipt_number"`
	RequestedAt   time.Time              `json:"requested_at"`
	EffectiveAt   time.Time              `json:"effective_at"`
	Channel       string                 `json:"channel"`
	NoticeStatus  string                 `json:"notice_status"`
	RequestedBy   complianceActorBody    `json:"requested_by"`
	Reason        *string                `json:"reason"`
	Reference     *string                `json:"reference"`
	Entities      []revocationEntityBody `json:"entities"`
}

type complianceHistoryBody struct {
	Status     string    `json:"status"`
	OccurredAt time.Time `json:"occurred_at"`
	EntityCode *string   `json:"entity_code"`
	ActorType  *string   `json:"actor_type"`
	Reason     *string   `json:"reason"`
	Reference  *string   `json:"reference"`
}

// complianceStateBody is the consent as the compliance portal sees it. The
// document is masked (the full one is only in the evidence) and the
// idempotency key of the revocation is never shown.
type complianceStateBody struct {
	ConsentID  string                    `json:"consent_id"`
	Status     string                    `json:"status"`
	Revocable  bool                      `json:"revocable"`
	Holder     complianceHolderBody      `json:"holder"`
	Scope      []sourceTermBody          `json:"scope"`
	Entities   []entityBody              `json:"entities"`
	Revocation *complianceRevocationBody `json:"revocation"`
	History    []complianceHistoryBody   `json:"history"`
}

type complianceRetryBody struct {
	RetriedEntities []string            `json:"retried_entities"`
	State           complianceStateBody `json:"state"`
}

func complianceStateFrom(s domain.ComplianceState) complianceStateBody {
	v := s.View
	out := complianceStateBody{
		ConsentID: v.Consent.ID, Status: string(v.Status), Revocable: v.Revocable,
		Holder:   complianceHolderBody{FullName: v.Consent.Holder.FullName, DocumentMasked: domain.MaskDocument(v.Consent.Holder.DocumentID)},
		Scope:    sourceBodies(v.Consent.Scope),
		Entities: make([]entityBody, 0, len(v.Entities)),
		History:  make([]complianceHistoryBody, 0, len(s.History)),
	}
	for _, e := range v.Entities {
		out.Entities = append(out.Entities, entityBody{Code: e.Code, Name: e.Name})
	}
	if r := v.Revocation; r != nil {
		rb := &complianceRevocationBody{
			ReceiptNumber: r.ReceiptNumber, RequestedAt: r.RequestedAt.UTC(), EffectiveAt: r.EffectiveAt.UTC(),
			Channel: r.Channel, NoticeStatus: string(r.Summary), RequestedBy: requestedBy(s),
			Entities: make([]revocationEntityBody, 0, len(r.Entities)),
		}
		if s.Record != nil {
			rb.Reason, rb.Reference = optional(s.Record.Reason), optional(s.Record.Reference)
		}
		for _, e := range r.Entities {
			rb.Entities = append(rb.Entities, revocationEntityBody{
				Code: e.Entity.Code, Name: e.Entity.Name, Status: string(e.Status),
				ConfirmedAt: optionalTime(e.ConfirmedAt), DeadlineAt: optionalTime(e.DeadlineAt),
			})
		}
		out.Revocation = rb
	}
	for _, e := range s.History {
		out.History = append(out.History, complianceHistoryBody{
			Status: e.Status, OccurredAt: e.OccurredAt.UTC(), EntityCode: optional(e.EntityCode), ActorType: optional(e.ActorType),
			Reason: optional(e.Reason), Reference: optional(e.Reference),
		})
	}
	return out
}

// requestedBy is who requested the revocation: the actor of its immutable
// record or, for a revoked event without record, the actor of the event.
func requestedBy(s domain.ComplianceState) complianceActorBody {
	if s.Record != nil {
		return complianceActorBody{Type: string(s.Record.Actor.Type), ID: s.Record.Actor.ID}
	}
	for _, e := range s.History {
		if e.Status == domain.HistoryRevoked {
			return complianceActorBody{Type: e.ActorType, ID: e.ActorID}
		}
	}
	return complianceActorBody{}
}

// complianceConsentID returns the consent id of the path, or answers 404
// consent_not_found when it cannot be an id: an impossible id is answered like
// an unknown one.
func complianceConsentID(w http.ResponseWriter, req *http.Request) (string, bool) {
	id := chi.URLParam(req, "consent_id")
	if id == "" || len(id) > maxConsentIDLength || !consentIDPattern.MatchString(id) {
		writeError(w, http.StatusNotFound, "consent_not_found", "there is no consent with that id")
		return "", false
	}
	return id, true
}

func getComplianceState(svc ComplianceRevocationService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := complianceConsentID(w, req)
		if !ok {
			return
		}
		state, err := svc.State(req.Context(), id)
		switch {
		case errors.Is(err, ports.ErrConsentNotFound):
			writeError(w, http.StatusNotFound, "consent_not_found", "there is no consent with that id")
		case err != nil:
			slog.ErrorContext(req.Context(), "could not read the revocation state", "consent_id", id, "error", err)
			writeError(w, http.StatusServiceUnavailable, "service_unavailable", "the consent could not be read; try again")
		default:
			writeJSON(w, http.StatusOK, complianceStateFrom(state))
		}
	}
}

func revokeFromPortal(svc ComplianceRevocationService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := complianceConsentID(w, req)
		if !ok {
			return
		}
		key := strings.TrimSpace(req.Header.Get("Idempotency-Key"))
		switch {
		case key == "":
			writeValidation(w, &domain.ValidationError{Fields: []domain.FieldError{{Field: "Idempotency-Key", Code: domain.CodeRequired}}})
			return
		case !idempotencyKeyPattern.MatchString(key):
			writeValidation(w, &domain.ValidationError{Fields: []domain.FieldError{{Field: "Idempotency-Key", Code: domain.CodeInvalidFormat}}})
			return
		}
		var in complianceRevokeRequest
		dec := json.NewDecoder(http.MaxBytesReader(w, req.Body, maxBodyBytes))
		dec.DisallowUnknownFields()
		// A second JSON value after the first one is not a valid body either.
		if err := dec.Decode(&in); err != nil || dec.Decode(&struct{}{}) != io.EOF {
			writeError(w, http.StatusBadRequest, "invalid_request", "the request body is not valid JSON or has an unknown field")
			return
		}
		claims, _ := security.ClaimsFromContext(req.Context())
		state, created, err := svc.Revoke(req.Context(), id, claims.Subject, strings.ToLower(key), domain.ComplianceRevokeRequest{
			Reason: in.Reason, Reference: in.Reference, RoleConfirmed: in.RoleConfirmed,
			// The device is the browser's, never a field of the body.
			UserAgent: req.UserAgent(),
		})
		var verr *domain.ValidationError
		switch {
		case errors.As(err, &verr):
			writeValidation(w, verr)
		case errors.Is(err, ports.ErrConsentNotFound):
			writeError(w, http.StatusNotFound, "consent_not_found", "there is no consent with that id")
		case errors.Is(err, domain.ErrConsentNotActive):
			writeError(w, http.StatusConflict, "consent_not_active", "the consent is not active; there is nothing to revoke")
		case errors.Is(err, domain.ErrAlreadyRevoked):
			writeError(w, http.StatusConflict, "consent_already_revoked", "the consent is already revoked")
		case errors.Is(err, domain.ErrIdempotencyKeyReused):
			writeError(w, http.StatusConflict, "idempotency_key_reused", "the Idempotency-Key was already used for another consent")
		case err != nil:
			slog.ErrorContext(req.Context(), "could not revoke the consent from the portal", "consent_id", id, "error", err)
			writeError(w, http.StatusServiceUnavailable, "service_unavailable", "the consent could not be revoked; try again")
		default:
			status := http.StatusOK
			if created {
				status = http.StatusCreated
			}
			writeJSON(w, status, complianceStateFrom(state))
		}
	}
}

func retryFromPortal(svc ComplianceRevocationService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := complianceConsentID(w, req)
		if !ok {
			return
		}
		claims, _ := security.ClaimsFromContext(req.Context())
		retried, state, err := svc.Retry(req.Context(), id, claims.Subject)
		switch {
		case errors.Is(err, ports.ErrConsentNotFound):
			writeError(w, http.StatusNotFound, "consent_not_found", "there is no consent with that id")
		case errors.Is(err, domain.ErrConsentNotRevoked):
			writeError(w, http.StatusConflict, "consent_not_revoked", "the consent is not revoked; there is no notice to retry")
		case errors.Is(err, domain.ErrNothingToRetry):
			writeError(w, http.StatusConflict, "nothing_to_retry", nothingToRetryMessage(state))
		case err != nil:
			slog.ErrorContext(req.Context(), "could not retry the revocation notices", "consent_id", id, "error", err)
			writeError(w, http.StatusServiceUnavailable, "service_unavailable", "the notices could not be retried; try again")
		default:
			if retried == nil {
				retried = []string{}
			}
			writeJSON(w, http.StatusAccepted, complianceRetryBody{RetriedEntities: retried, State: complianceStateFrom(state)})
		}
	}
}

// nothingToRetryMessage says why: it carries the notice_status of the
// revocation.
func nothingToRetryMessage(s domain.ComplianceState) string {
	if r := s.View.Revocation; r != nil {
		return fmt.Sprintf("there is nothing to retry: no entity is waiting for a retry (notice_status: %s)", r.Summary)
	}
	return "there is nothing to retry"
}
