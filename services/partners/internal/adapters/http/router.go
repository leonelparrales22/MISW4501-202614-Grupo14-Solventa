// Package httpadapter exposes the partners use cases over HTTP (chi).
package httpadapter

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/observability"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/partners/internal/credentials"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/partners/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/partners/internal/onboarding"
)

const maxBodyBytes = 1 << 14

// Onboarding is the use case behind the list, the registration, the detail
// and the approval (HU-W3.1-01).
type Onboarding interface {
	Register(ctx context.Context, req domain.RegisterRequest, actor domain.Actor) (onboarding.View, error)
	Approve(ctx context.Context, partnerID string, actor domain.Actor) (onboarding.Approval, error)
	Get(ctx context.Context, id string) (onboarding.View, error)
	List(ctx context.Context) ([]onboarding.View, onboarding.Indicators, error)
}

// Rotations is the use case behind the rotation of the credentials.
type Rotations interface {
	Rotate(ctx context.Context, partnerID string, graceHours int, actor domain.Actor) (credentials.IssuedSecret, error)
	DefaultGracePeriodHours() int
}

// Option mounts optional routes on the service router.
type Option func(chi.Router)

// WithObservability adds the correlation id, the server span, the request
// metrics and the access log. Pass it before any other option.
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

// NewRouter builds the router: GET /health plus whatever the options mount.
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

// WithPartners mounts the routes of the operations portal (HU-W3.1-01):
// human routes, the Cognito JWT verified with pkg/security and only the
// operations role. environment is shown in the detail.
func WithPartners(onb Onboarding, rot Rotations, verifier security.Verifier, environment string) Option {
	h := handlers{onb: onb, rot: rot, environment: environment}
	return func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(noStore, security.Authenticate(verifier), security.RequireRole(security.RoleOperations))
			r.Get("/v1/partners", h.list)
			r.Post("/v1/partners", h.register)
			r.Get("/v1/partners/{partner_id}", h.get)
			r.Post("/v1/partners/{partner_id}/approval", h.approve)
			r.Post("/v1/partners/{partner_id}/credential-rotations", h.rotate)
		})
	}
}

type handlers struct {
	onb         Onboarding
	rot         Rotations
	environment string
}

// actorOf is the analyst of the request: the sub and, as a label, the e-mail
// or the user name of the token.
func actorOf(req *http.Request) domain.Actor {
	c, _ := security.ClaimsFromContext(req.Context())
	label := c.Email
	if label == "" {
		label = c.Username
	}
	if label == "" {
		label = c.Subject
	}
	return domain.Actor{Type: domain.ActorOperations, ID: c.Subject, Label: label}
}

func (h handlers) list(w http.ResponseWriter, req *http.Request) {
	views, ind, err := h.onb.List(req.Context())
	if err != nil {
		h.fail(w, req, err)
		return
	}
	items := make([]summaryBody, 0, len(views))
	for _, v := range views {
		items = append(items, summaryOf(v))
	}
	writeJSON(w, http.StatusOK, listBody{Items: items, Indicators: indicatorsBody{
		ActivePartners: ind.ActivePartners, AverageQuotaUsedPercent: ind.AverageQuotaUsedPercent,
		NearLimitPartners: len(ind.NearLimitNames), NearLimitNames: ind.NearLimitNames,
	}})
}

func (h handlers) register(w http.ResponseWriter, req *http.Request) {
	var in registerBody
	if !decode(w, req, &in, false) {
		return
	}
	v, err := h.onb.Register(req.Context(), in.request(), actorOf(req))
	if err != nil {
		h.fail(w, req, err)
		return
	}
	writeJSON(w, http.StatusCreated, h.detailOf(v))
}

func (h handlers) get(w http.ResponseWriter, req *http.Request) {
	v, err := h.onb.Get(req.Context(), chi.URLParam(req, "partner_id"))
	if err != nil {
		h.fail(w, req, err)
		return
	}
	writeJSON(w, http.StatusOK, h.detailOf(v))
}

func (h handlers) approve(w http.ResponseWriter, req *http.Request) {
	id := chi.URLParam(req, "partner_id")
	if len(id) > domain.MaxPartnerIDLen {
		h.fail(w, req, domain.ErrPartnerNotFound)
		return
	}
	a, err := h.onb.Approve(req.Context(), id, actorOf(req))
	if err != nil {
		h.fail(w, req, err)
		return
	}
	writeJSON(w, http.StatusCreated, issuedBody{Partner: h.detailOf(a.View), Secret: secretOf(a.Secret)})
}

func (h handlers) rotate(w http.ResponseWriter, req *http.Request) {
	id := chi.URLParam(req, "partner_id")
	if len(id) > domain.MaxPartnerIDLen {
		h.fail(w, req, domain.ErrPartnerNotFound)
		return
	}
	var in rotationBody
	if !decode(w, req, &in, true) {
		return
	}
	grace := 0
	if in.GracePeriodHours != nil {
		grace = *in.GracePeriodHours
		if grace == 0 {
			grace = -1 // an explicit 0 is out of range, not the default
		}
	}
	secret, err := h.rot.Rotate(req.Context(), id, grace, actorOf(req))
	if err != nil {
		h.fail(w, req, err)
		return
	}
	v, err := h.onb.Get(req.Context(), id)
	if err != nil {
		h.fail(w, req, err)
		return
	}
	writeJSON(w, http.StatusCreated, issuedBody{Partner: h.detailOf(v), Secret: secretOf(secret)})
}

// decode reads a JSON body; an empty body is accepted when optional.
func decode(w http.ResponseWriter, req *http.Request, v any, optional bool) bool {
	err := json.NewDecoder(http.MaxBytesReader(w, req.Body, maxBodyBytes)).Decode(v)
	if err == nil || (optional && errors.Is(err, io.EOF)) {
		return true
	}
	writeError(w, http.StatusBadRequest, "invalid_request", "the request body is not valid JSON")
	return false
}

func (h handlers) fail(w http.ResponseWriter, req *http.Request, err error) {
	var verr *domain.ValidationError
	switch {
	case errors.As(err, &verr):
		fields := make([]fieldErrorBody, 0, len(verr.Fields))
		for _, f := range verr.Fields {
			fields = append(fields, fieldErrorBody{Field: f.Field, Code: f.Code})
		}
		writeJSON(w, http.StatusBadRequest, errorBody{Error: "validation_failed", Message: "one or more fields are invalid", Fields: fields})
	case errors.Is(err, domain.ErrPartnerNotFound):
		writeError(w, http.StatusNotFound, "partner_not_found", "no partner was found with that identifier")
	case errors.Is(err, domain.ErrCodeTaken):
		writeError(w, http.StatusConflict, "partner_code_taken", "another partner already uses that code")
	case errors.Is(err, domain.ErrNotPending):
		writeError(w, http.StatusConflict, "partner_not_pending", "the partner is not pending approval; its secret is never shown again")
	case errors.Is(err, domain.ErrNotActive):
		writeError(w, http.StatusConflict, "partner_not_active", "only an active partner can rotate its credentials")
	case errors.Is(err, domain.ErrRotationPending):
		writeError(w, http.StatusConflict, "rotation_in_progress", "a previous credential is still in its transition period")
	case errors.Is(err, domain.ErrGatewayUnavailable):
		slog.ErrorContext(req.Context(), "the api gateway did not answer", "error", err)
		writeError(w, http.StatusServiceUnavailable, "gateway_unavailable", "the credentials could not be managed now; nothing changed, try again")
	default:
		slog.ErrorContext(req.Context(), "could not serve the partners request", "error", err)
		writeError(w, http.StatusServiceUnavailable, "service_unavailable", "the request could not be completed; try again")
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorBody{Error: code, Message: message})
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

func utc(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}
