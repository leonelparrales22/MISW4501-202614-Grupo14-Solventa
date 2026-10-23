// Package httpadapter exposes the claims service over HTTP. It holds the
// router, the shape of the error responses and nothing about the use cases.
package httpadapter

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/observability"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
)

// maxBodyBytes bounds the request body. The filing is text: the evidence
// travels on its own route (HU-M2.1-01).
const maxBodyBytes = 1 << 14

// Option adds routes to the router.
type Option func(chi.Router)

// NewRouter builds the service handler. /health answers without a session,
// which is what the load balancer checks.
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

func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
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

func writeValidationError(w http.ResponseWriter, e *domain.ValidationError) {
	out := validationErrorResponse{
		Error: "validation_failed", Message: "one or more fields are missing or invalid",
		Fields: make([]fieldErrorBody, 0, len(e.Fields)),
	}
	for _, f := range e.Fields {
		out.Fields = append(out.Fields, fieldErrorBody{Field: f.Field, Code: f.Code})
	}
	writeJSON(w, http.StatusUnprocessableEntity, out)
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
