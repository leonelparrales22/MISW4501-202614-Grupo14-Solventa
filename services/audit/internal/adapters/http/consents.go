// Package httpadapter exposes the audit use cases over HTTP (chi).
package httpadapter

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/observability"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/ports"
)

const (
	maxBodyBytes = 1 << 14
	// maxConsentIDLength bounds the id of the evidence lookup: ids are
	// "cns-" plus 32 hex characters.
	maxConsentIDLength = 64
)

// ConsentService is the use case behind the partner-channel and internal
// consent routes (HU-W1.2-01).
type ConsentService interface {
	CurrentTerms() domain.Terms
	Grant(ctx context.Context, req domain.GrantRequest) (domain.Consent, bool, error)
	Verify(ctx context.Context, customerID string) (domain.Verification, error)
}

// ConsentReader is the use case behind the consent evidence lookup.
type ConsentReader interface {
	Get(ctx context.Context, id string) (domain.Consent, error)
}

// Option mounts optional routes on the service router.
type Option func(chi.Router)

// WithObservability adds the correlation id, the server span, the request
// metrics and the access log to every route. Pass it before any other option:
// chi only accepts middlewares before the routes are registered.
func WithObservability(service string) Option {
	return func(r chi.Router) {
		r.Use(observability.Middleware(service, func(req *http.Request) string {
			if rc := chi.RouteContext(req.Context()); rc != nil {
				return rc.RoutePattern()
			}
			return ""
		}))
	}
}

// NewRouter builds the service router: GET /health plus whatever the options
// mount.
func NewRouter(opts ...Option) http.Handler {
	r := chi.NewRouter()
	for _, opt := range opts {
		opt(r)
	}
	r.Get("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	return r
}

// WithConsents mounts the routes of the partner channel and the internal
// verification (HU-W1.2-01): the current terms, the grant and the verification
// origination asks before it queries any source.
//
// These routes carry no Cognito token: the partner channel is authenticated
// by the API key at the API Gateway and the verification is a call between
// services, so the verification route must be reachable only from inside the
// network (internal load balancer or security group), never published to the
// partners.
func WithConsents(svc ConsentService) Option {
	return func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(noStore)
			r.Get("/v1/audit/consent-terms/current", getTerms(svc))
			r.Post("/v1/audit/consents", grantConsent(svc))
			r.Get("/v1/audit/consent-verifications", verifyConsent(svc))
		})
	}
}

// WithConsentReads mounts the evidence lookup, a human route: the Cognito JWT
// is verified with pkg/security and only operations and advisors read it.
func WithConsentReads(svc ConsentReader, verifier security.Verifier) Option {
	return func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(noStore, security.Authenticate(verifier), security.RequireRole(security.RoleOperations, security.RoleAdvisor))
			r.Get("/v1/audit/consents/{consent_id}", getConsent(svc))
		})
	}
}

type sourceTermBody struct {
	Code           string   `json:"code"`
	DataCategories []string `json:"data_categories"`
	Validity       string   `json:"validity"`
}

type termsResponse struct {
	Version    string           `json:"version"`
	Purpose    string           `json:"purpose"`
	Controller string           `json:"controller"`
	PolicyRef  string           `json:"policy_ref"`
	Sources    []sourceTermBody `json:"sources"`
}

func getTerms(svc ConsentService) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		t := svc.CurrentTerms()
		writeJSON(w, http.StatusOK, termsResponse{
			Version: string(t.Version), Purpose: t.Purpose, Controller: t.Controller, PolicyRef: t.PolicyRef,
			Sources: sourceBodies(t.Sources),
		})
	}
}

type grantRequest struct {
	CustomerID         string   `json:"customer_id"`
	DocumentID         string   `json:"document_id"`
	FullName           string   `json:"full_name"`
	TermsVersion       string   `json:"terms_version"`
	AcceptedSources    []string `json:"accepted_sources"`
	AcceptedDataPolicy bool     `json:"accepted_data_policy"`
	Channel            string   `json:"channel"`
	PartnerID          string   `json:"partner_id"`
	ApplicationID      string   `json:"application_id"`
}

// grantResponse is what the partner channel gets back: the receipt of the
// consent, without the holder's document.
type grantResponse struct {
	ConsentID    string           `json:"consent_id"`
	CustomerID   string           `json:"customer_id"`
	TermsVersion string           `json:"terms_version"`
	Purpose      string           `json:"purpose"`
	Channel      string           `json:"channel"`
	Scope        []sourceTermBody `json:"scope"`
	GrantedAt    time.Time        `json:"granted_at"`
	ExpiresAt    time.Time        `json:"expires_at"`
}

func grantConsent(svc ConsentService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var in grantRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, maxBodyBytes)).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "the request body is not valid JSON")
			return
		}
		c, created, err := svc.Grant(req.Context(), domain.GrantRequest{
			CustomerID: in.CustomerID, DocumentID: in.DocumentID, FullName: in.FullName,
			TermsVersion: in.TermsVersion, AcceptedSources: in.AcceptedSources, AcceptedDataPolicy: in.AcceptedDataPolicy,
			Channel: in.Channel, PartnerID: in.PartnerID, ApplicationID: in.ApplicationID,
			UserAgent: req.UserAgent(),
		})
		var verr *domain.ValidationError
		switch {
		case errors.As(err, &verr):
			writeValidation(w, verr)
		case errors.Is(err, domain.ErrTermsOutdated):
			writeError(w, http.StatusConflict, "terms_outdated", "the terms version shown is no longer the current one; show the current terms again")
		case err != nil:
			slog.ErrorContext(req.Context(), "could not record the consent", "error", err)
			writeError(w, http.StatusServiceUnavailable, "service_unavailable", "the consent could not be recorded; try again")
		default:
			status := http.StatusOK
			if created {
				status = http.StatusCreated
			}
			writeJSON(w, status, grantResponse{
				ConsentID: c.ID, CustomerID: c.CustomerID, TermsVersion: string(c.TermsVersion), Purpose: c.Purpose,
				Channel: string(c.Channel), Scope: sourceBodies(c.Scope), GrantedAt: c.GrantedAt, ExpiresAt: c.ExpiresAt(),
			})
		}
	}
}

type verificationResponse struct {
	Active    bool       `json:"active"`
	ConsentID *string    `json:"consent_id"`
	GrantedAt *time.Time `json:"granted_at"`
	ExpiresAt *time.Time `json:"expires_at"`
	Sources   []string   `json:"sources"`
}

func verifyConsent(svc ConsentService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		customerID := req.URL.Query().Get("customer_id")
		if customerID == "" || len(customerID) > domain.MaxIdentifierLength {
			writeError(w, http.StatusBadRequest, "invalid_request", "customer_id (max 128 chars) is required")
			return
		}
		v, err := svc.Verify(req.Context(), customerID)
		if err != nil {
			slog.ErrorContext(req.Context(), "could not verify the consent", "error", err)
			writeError(w, http.StatusServiceUnavailable, "service_unavailable", "the consent could not be verified; try again")
			return
		}
		out := verificationResponse{Active: v.Active, Sources: []string{}}
		if v.ConsentID != "" {
			granted, expires := v.GrantedAt, v.ExpiresAt
			out.ConsentID, out.GrantedAt, out.ExpiresAt = &v.ConsentID, &granted, &expires
			out.Sources = append(out.Sources, v.Sources...)
		}
		writeJSON(w, http.StatusOK, out)
	}
}

type holderBody struct {
	DocumentID string `json:"document_id"`
	FullName   string `json:"full_name"`
}

// evidenceResponse is the full record, for operations and advisors.
type evidenceResponse struct {
	ConsentID     string           `json:"consent_id"`
	CustomerID    string           `json:"customer_id"`
	Holder        holderBody       `json:"holder"`
	TermsVersion  string           `json:"terms_version"`
	Scope         []sourceTermBody `json:"scope"`
	Purpose       string           `json:"purpose"`
	Channel       string           `json:"channel"`
	PartnerID     string           `json:"partner_id"`
	ApplicationID string           `json:"application_id"`
	UserAgent     string           `json:"user_agent"`
	GrantedAt     time.Time        `json:"granted_at"`
	ExpiresAt     time.Time        `json:"expires_at"`
}

func getConsent(svc ConsentReader) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id := chi.URLParam(req, "consent_id")
		if id == "" || len(id) > maxConsentIDLength {
			writeError(w, http.StatusNotFound, "consent_not_found", "there is no consent with that id")
			return
		}
		c, err := svc.Get(req.Context(), id)
		switch {
		case errors.Is(err, ports.ErrConsentNotFound):
			writeError(w, http.StatusNotFound, "consent_not_found", "there is no consent with that id")
		case err != nil:
			slog.ErrorContext(req.Context(), "could not read the consent", "error", err)
			writeError(w, http.StatusServiceUnavailable, "service_unavailable", "the consent could not be read; try again")
		default:
			writeJSON(w, http.StatusOK, evidenceResponse{
				ConsentID: c.ID, CustomerID: c.CustomerID,
				Holder:       holderBody{DocumentID: c.Holder.DocumentID, FullName: c.Holder.FullName},
				TermsVersion: string(c.TermsVersion), Scope: sourceBodies(c.Scope), Purpose: c.Purpose,
				Channel: string(c.Channel), PartnerID: c.PartnerID, ApplicationID: c.ApplicationID,
				UserAgent: c.UserAgent, GrantedAt: c.GrantedAt, ExpiresAt: c.ExpiresAt(),
			})
		}
	}
}

func sourceBodies(sources []domain.SourceTerm) []sourceTermBody {
	out := make([]sourceTermBody, 0, len(sources))
	for _, s := range sources {
		cats := s.DataCategories
		if cats == nil {
			cats = []string{}
		}
		out = append(out, sourceTermBody{Code: s.Code, DataCategories: cats, Validity: string(s.Validity)})
	}
	return out
}

type errorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

type fieldErrorBody struct {
	Field string `json:"field"`
	Code  string `json:"code"`
}

type validationErrorResponse struct {
	Error   string           `json:"error"`
	Message string           `json:"message"`
	Fields  []fieldErrorBody `json:"fields"`
}

func writeValidation(w http.ResponseWriter, err *domain.ValidationError) {
	fields := make([]fieldErrorBody, 0, len(err.Fields))
	for _, f := range err.Fields {
		fields = append(fields, fieldErrorBody{Field: f.Field, Code: f.Code})
	}
	writeJSON(w, http.StatusBadRequest, validationErrorResponse{
		Error: "validation_failed", Message: "one or more fields are invalid", Fields: fields,
	})
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorResponse{Error: code, Message: message})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("could not write the response", "error", err)
	}
}

func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}
