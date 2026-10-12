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

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/issuance"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

const maxIDSize = 128

// AcceptanceService is the use case behind the offer acceptance routes
// (HU-W1.3-01).
type AcceptanceService interface {
	Accept(ctx context.Context, offerID string, channel domain.Channel) (domain.Acceptance, bool, error)
	Get(ctx context.Context, acceptanceID string) (domain.Acceptance, error)
	GetByOffer(ctx context.Context, offerID string) (domain.Acceptance, error)
}

// WithAcceptances mounts the offer acceptance routes (HU-W1.3-01). They are
// human routes: the Cognito JWT is verified with pkg/security, only an insured
// accepts (their own offers) and the owner, an advisor or an operations
// analyst read.
func WithAcceptances(svc AcceptanceService, verifier security.Verifier) Option {
	return func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(noStore, security.Authenticate(verifier))
			r.With(security.RequireRole(security.RoleInsured)).
				Post("/v1/offers/{offer_id}/acceptance", acceptOffer(svc))
			readers := security.RequireRole(security.RoleInsured, security.RoleAdvisor, security.RoleOperations)
			r.With(readers).Get("/v1/offers/{offer_id}/acceptance", getOfferAcceptance(svc))
			r.With(readers).Get("/v1/acceptances/{acceptance_id}", getAcceptance(svc))
		})
	}
}

type acceptRequest struct {
	Channel string `json:"channel"`
}

type acceptanceResponse struct {
	AcceptanceID string            `json:"acceptance_id"`
	OfferID      string            `json:"offer_id"`
	CustomerID   string            `json:"customer_id"`
	Channel      string            `json:"channel"`
	AcceptedAt   time.Time         `json:"accepted_at"`
	Status       string            `json:"status"`
	Underwriting *underwritingBody `json:"underwriting"`
	Processing   processingBody    `json:"processing"`
	UpdatedAt    time.Time         `json:"updated_at"`
}

type underwritingBody struct {
	Outcome   string    `json:"outcome"`
	Reason    *string   `json:"reason"`
	DecidedAt time.Time `json:"decided_at"`
}

type processingBody struct {
	Attempts      int        `json:"attempts"`
	LastFailureAt *time.Time `json:"last_failure_at"`
	NextAttemptAt *time.Time `json:"next_attempt_at"`
}

type offerExpiredResponse struct {
	Error     string    `json:"error"`
	Message   string    `json:"message"`
	OfferID   string    `json:"offer_id"`
	ExpiresAt time.Time `json:"expires_at"`
}

func acceptOffer(svc AcceptanceService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		offerID, ok := pathID(req, "offer_id")
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid_request", "offer_id (max 128 chars) is required")
			return
		}
		var in acceptRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, maxBodyBytes)).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "the request body is not valid JSON")
			return
		}
		channel, ok := domain.ParseChannel(in.Channel)
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid_request", "channel must be web or mobile")
			return
		}

		a, created, err := svc.Accept(req.Context(), offerID, channel)
		var expired *issuance.OfferExpiredError
		switch {
		case err == nil:
			w.Header().Set("Location", "/v1/acceptances/"+url.PathEscape(a.ID))
			status := http.StatusOK
			if created {
				status = http.StatusAccepted
			}
			writeAcceptance(w, status, a)
		case errors.As(err, &expired):
			writeJSON(w, http.StatusUnprocessableEntity, offerExpiredResponse{
				Error: "offer_expired", Message: "the offer validity is over; request a new offer",
				OfferID: expired.OfferID, ExpiresAt: expired.ExpiresAt.UTC(),
			})
		case errors.Is(err, ports.ErrOfferNotFound):
			writeError(w, http.StatusNotFound, "offer_not_found", "the offer does not exist")
		case isAuthError(err):
			writeAuthError(w, err)
		default:
			slog.Error("could not register the acceptance", "offer_id", offerID, "error", err)
			writeError(w, http.StatusServiceUnavailable, "acceptance_not_registered",
				"the acceptance could not be registered; retrying is safe")
		}
	}
}

func getOfferAcceptance(svc AcceptanceService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		offerID, ok := pathID(req, "offer_id")
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid_request", "offer_id (max 128 chars) is required")
			return
		}
		a, err := svc.GetByOffer(req.Context(), offerID)
		respondAcceptance(w, a, err)
	}
}

func getAcceptance(svc AcceptanceService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := pathID(req, "acceptance_id")
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid_request", "acceptance_id (max 128 chars) is required")
			return
		}
		a, err := svc.Get(req.Context(), id)
		respondAcceptance(w, a, err)
	}
}

func respondAcceptance(w http.ResponseWriter, a domain.Acceptance, err error) {
	switch {
	case err == nil:
		writeAcceptance(w, http.StatusOK, a)
	case errors.Is(err, ports.ErrAcceptanceNotFound):
		writeError(w, http.StatusNotFound, "acceptance_not_found", "the acceptance does not exist")
	case isAuthError(err):
		writeAuthError(w, err)
	default:
		slog.Error("could not read the acceptance", "error", err)
		writeError(w, http.StatusServiceUnavailable, "acceptance_unavailable", "the acceptance could not be read; retry later")
	}
}

func pathID(req *http.Request, name string) (string, bool) {
	id, err := url.PathUnescape(chi.URLParam(req, name))
	return id, err == nil && id != "" && len(id) <= maxIDSize
}

func isAuthError(err error) bool {
	return errors.Is(err, security.ErrForbidden) || errors.Is(err, security.ErrUnauthenticated)
}

func writeAuthError(w http.ResponseWriter, err error) {
	if errors.Is(err, security.ErrUnauthenticated) {
		security.WriteUnauthorized(w)
		return
	}
	security.WriteForbidden(w)
}

// noStore keeps the API Gateway and the browser from caching any answer of
// the acceptance routes: an acceptance changes state while it is processed.
func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func writeAcceptance(w http.ResponseWriter, status int, a domain.Acceptance) {
	writeJSON(w, status, toAcceptanceResponse(a))
}

func toAcceptanceResponse(a domain.Acceptance) acceptanceResponse {
	out := acceptanceResponse{
		AcceptanceID: a.ID,
		OfferID:      a.OfferID,
		CustomerID:   a.CustomerID,
		Channel:      string(a.Channel),
		AcceptedAt:   a.AcceptedAt.UTC(),
		Status:       string(a.Status),
		Processing:   processingBody{Attempts: a.Attempts},
		UpdatedAt:    a.UpdatedAt.UTC(),
	}
	if d := a.Decision; d != nil {
		out.Underwriting = &underwritingBody{Outcome: string(d.Outcome), DecidedAt: d.DecidedAt.UTC()}
		if d.Reason != "" {
			reason := d.Reason
			out.Underwriting.Reason = &reason
		}
	}
	if !a.LastFailureAt.IsZero() {
		t := a.LastFailureAt.UTC()
		out.Processing.LastFailureAt = &t
	}
	if !a.NextAttemptAt.IsZero() {
		t := a.NextAttemptAt.UTC()
		out.Processing.NextAttemptAt = &t
	}
	return out
}
