// Package httpadapter exposes the origination use cases over HTTP (chi).
package httpadapter

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/observability"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/profiling"
)

const (
	maxBodyBytes      = 1 << 14
	maxCustomerIDSize = 128
)

// Notice codes tell the client which provisional-mode message to show. They
// are stable identifiers; the text is translated by each client.
const (
	NoticeLastProfileInUse           = "last_profile_in_use"
	NoticePersonalizationUnavailable = "personalization_unavailable"
)

// ProfileResolver is the use case behind POST /v1/risk-profiles/resolve and
// GET /v1/risk-profiles/{customer_id}.
type ProfileResolver interface {
	Resolve(ctx context.Context, customerID string, consentActive bool) (domain.ProfileResolution, error)
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

// NewRouter builds the service router.
func NewRouter(resolver ProfileResolver, opts ...Option) http.Handler {
	r := chi.NewRouter()
	for _, opt := range opts {
		opt(r)
	}
	r.Get("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	r.Post("/v1/risk-profiles/resolve", resolveProfile(resolver))
	// Same answer as the POST, as a read the API Gateway can cache per partner
	// (HA-L01 CA-4, decision D-6): the cache key is built from the path, the
	// query and the partner API key, never from a body.
	r.Get("/v1/risk-profiles/{customer_id}", getProfile(resolver))
	return r
}

type resolveRequest struct {
	CustomerID    string `json:"customer_id"`
	ConsentActive *bool  `json:"consent_active"`
}

type profileBody struct {
	Score    float64 `json:"score"`
	Category string  `json:"category"`
}

type resolveResponse struct {
	CustomerID        string       `json:"customer_id"`
	Mode              string       `json:"mode"`
	Source            string       `json:"source"`
	Profile           profileBody  `json:"profile"`
	ProfileDate       *time.Time   `json:"profile_date"`
	UnavailableSource *string      `json:"unavailable_source"`
	Notice            *string      `json:"notice"`
	Partial           bool         `json:"partial"`
	Sources           []sourceBody `json:"sources"`
}

// sourceBody is the state of one external source, shown by screen E2.
type sourceBody struct {
	Name      string `json:"name"`
	Status    string `json:"status"`
	LatencyMs int64  `json:"latency_ms"`
}

type errorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

func resolveProfile(resolver ProfileResolver) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var in resolveRequest
		body := http.MaxBytesReader(w, req.Body, maxBodyBytes)
		if err := json.NewDecoder(body).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "the request body is not valid JSON")
			return
		}
		if in.CustomerID == "" || len(in.CustomerID) > maxCustomerIDSize || in.ConsentActive == nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "customer_id (max 128 chars) and consent_active are required")
			return
		}

		respond(w, req, resolver, in.CustomerID, *in.ConsentActive)
	}
}

func getProfile(resolver ProfileResolver) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		customerID, err := url.PathUnescape(chi.URLParam(req, "customer_id"))
		if err != nil || customerID == "" || len(customerID) > maxCustomerIDSize {
			writeError(w, http.StatusBadRequest, "invalid_request", "customer_id (max 128 chars) is required")
			return
		}
		var consentActive bool
		switch req.URL.Query().Get("consent_active") {
		case "true":
			consentActive = true
		case "false":
			consentActive = false
		default:
			writeError(w, http.StatusBadRequest, "invalid_request", "consent_active (true or false) is required")
			return
		}
		respond(w, req, resolver, customerID, consentActive)
	}
}

func respond(w http.ResponseWriter, req *http.Request, resolver ProfileResolver, customerID string, consentActive bool) {
	res, err := resolver.Resolve(req.Context(), customerID, consentActive)
	switch {
	case errors.Is(err, profiling.ErrConsentRequired):
		writeError(w, http.StatusUnprocessableEntity, "consent_required", "an active consent is required to profile the customer")
	case err != nil:
		slog.Error("could not resolve the risk profile", "error", err)
		writeError(w, http.StatusServiceUnavailable, "request_aborted", "the request was aborted before an offer could be produced")
	default:
		writeJSON(w, http.StatusOK, toResponse(customerID, res))
	}
}

func toResponse(customerID string, res domain.ProfileResolution) resolveResponse {
	out := resolveResponse{
		CustomerID: customerID,
		Mode:       string(res.Mode),
		Source:     string(res.Source),
		Profile:    profileBody{Score: res.Profile.Score, Category: res.Profile.Category},
		// partial describes an online calculation; a provisional answer is
		// already flagged by mode and source.
		Partial: res.Mode == domain.ModeNormal && res.Profile.Partial,
		Sources: make([]sourceBody, 0, len(res.Sources)),
	}
	for _, o := range res.Sources {
		out.Sources = append(out.Sources, sourceBody{Name: o.Source, Status: string(o.Status), LatencyMs: o.Latency.Milliseconds()})
	}
	if !res.Profile.CalculatedAt.IsZero() {
		d := res.Profile.CalculatedAt.UTC()
		out.ProfileDate = &d
	}
	if res.Mode == domain.ModeProvisional {
		unavailable := res.UnavailableSource
		out.UnavailableSource = &unavailable
		notice := NoticeLastProfileInUse
		if res.Source == domain.SourceBaseActuarial {
			notice = NoticePersonalizationUnavailable
		}
		out.Notice = &notice
	}
	return out
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
