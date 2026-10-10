// Package httpadapter exposes the origination use cases over HTTP (chi).
package httpadapter

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

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

// ProfileResolver is the use case behind POST /v1/risk-profiles/resolve.
type ProfileResolver interface {
	Resolve(ctx context.Context, customerID string, consentActive bool) (domain.ProfileResolution, error)
}

// NewRouter builds the service router.
func NewRouter(resolver ProfileResolver) http.Handler {
	r := chi.NewRouter()
	r.Get("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	r.Post("/v1/risk-profiles/resolve", resolveProfile(resolver))
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
	CustomerID        string      `json:"customer_id"`
	Mode              string      `json:"mode"`
	Source            string      `json:"source"`
	Profile           profileBody `json:"profile"`
	ProfileDate       *time.Time  `json:"profile_date"`
	UnavailableSource *string     `json:"unavailable_source"`
	Notice            *string     `json:"notice"`
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

		res, err := resolver.Resolve(req.Context(), in.CustomerID, *in.ConsentActive)
		switch {
		case errors.Is(err, profiling.ErrConsentRequired):
			writeError(w, http.StatusUnprocessableEntity, "consent_required", "an active consent is required to profile the customer")
		case err != nil:
			slog.Error("could not resolve the risk profile", "error", err)
			writeError(w, http.StatusServiceUnavailable, "request_aborted", "the request was aborted before an offer could be produced")
		default:
			writeJSON(w, http.StatusOK, toResponse(in.CustomerID, res))
		}
	}
}

func toResponse(customerID string, res domain.ProfileResolution) resolveResponse {
	out := resolveResponse{
		CustomerID: customerID,
		Mode:       string(res.Mode),
		Source:     string(res.Source),
		Profile:    profileBody{Score: res.Profile.Score, Category: res.Profile.Category},
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
