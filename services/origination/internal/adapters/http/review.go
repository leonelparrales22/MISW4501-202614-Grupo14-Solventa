package httpadapter

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/underwriting"
)

// ReviewService is the use case behind the advisor inbox routes (HU-W1.1-02).
type ReviewService interface {
	List(ctx context.Context, f domain.ReviewFilter) ([]domain.ReviewCase, domain.ReviewCounts, error)
	Get(ctx context.Context, acceptanceID string) (underwriting.ReviewDetail, error)
	Decide(ctx context.Context, acceptanceID string, in domain.DecisionInput) (domain.ReviewCase, error)
}

// WithReviews mounts the advisor inbox routes (HU-W1.1-02). They are human
// routes: the Cognito JWT is verified with pkg/security and only an advisor
// lists, opens and decides the requests routed to a human decision
// (decision D-11 a).
func WithReviews(svc ReviewService, verifier security.Verifier) Option {
	return func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(noStore, security.Authenticate(verifier), security.RequireRole(security.RoleAdvisor))
			r.Get("/v1/review-requests", listReviewRequests(svc))
			r.Get("/v1/review-requests/{acceptance_id}", getReviewRequest(svc))
			r.Post("/v1/review-requests/{acceptance_id}/decision", decideReviewRequest(svc))
		})
	}
}

type applicantBody struct {
	FullName   string `json:"full_name"`
	DocumentID string `json:"document_id"`
	Email      string `json:"email"`
}

type reviewOfferBody struct {
	Premium  float64 `json:"premium"`
	Currency string  `json:"currency"`
}

// reviewOfferDetailBody adds the mode and the validity of the routed offer;
// both are null when the offer could not be read.
type reviewOfferDetailBody struct {
	Premium   float64    `json:"premium"`
	Currency  string     `json:"currency"`
	Mode      *string    `json:"mode"`
	ExpiresAt *time.Time `json:"expires_at"`
}

type advisorBody struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

type adjustedOfferBody struct {
	OfferID   string    `json:"offer_id"`
	Premium   float64   `json:"premium"`
	Currency  string    `json:"currency"`
	ExpiresAt time.Time `json:"expires_at"`
}

type reviewNotificationBody struct {
	Channel  string     `json:"channel"`
	Status   string     `json:"status"`
	Attempts int        `json:"attempts"`
	SentAt   *time.Time `json:"sent_at"`
}

type decisionBody struct {
	DecisionID           string                  `json:"decision_id"`
	Type                 string                  `json:"type"`
	Advisor              advisorBody             `json:"advisor"`
	DecidedAt            time.Time               `json:"decided_at"`
	Observations         string                  `json:"observations"`
	PremiumAdjustmentPct *float64                `json:"premium_adjustment_pct"`
	AdjustedOffer        *adjustedOfferBody      `json:"adjusted_offer"`
	Notification         *reviewNotificationBody `json:"notification"`
}

type reviewRequestBody struct {
	AcceptanceID string          `json:"acceptance_id"`
	OfferID      string          `json:"offer_id"`
	CustomerID   string          `json:"customer_id"`
	Applicant    applicantBody   `json:"applicant"`
	Offer        reviewOfferBody `json:"offer"`
	Reason       string          `json:"reason"`
	ReceivedAt   time.Time       `json:"received_at"`
	Status       string          `json:"status"`
	Decision     *decisionBody   `json:"decision"`
}

// reviewRequestDetailBody is a request with the detail of its offer: the outer
// "offer" field replaces the one of the embedded body.
type reviewRequestDetailBody struct {
	reviewRequestBody
	Offer reviewOfferDetailBody `json:"offer"`
}

type reviewCountsBody struct {
	Pending int `json:"pending"`
	Decided int `json:"decided"`
}

type reviewRequestListBody struct {
	Items  []reviewRequestBody `json:"items"`
	Counts reviewCountsBody    `json:"counts"`
}

type decideRequest struct {
	Type                 string   `json:"type"`
	Observations         string   `json:"observations"`
	PremiumAdjustmentPct *float64 `json:"premium_adjustment_pct"`
}

func listReviewRequests(svc ReviewService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		f, ok := reviewFilter(req)
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid_request",
				"status must be pending or decided, order asc or desc and reason one of the routing reasons")
			return
		}
		cases, counts, err := svc.List(req.Context(), f)
		if err != nil {
			writeReviewError(w, req, err)
			return
		}
		out := reviewRequestListBody{Items: make([]reviewRequestBody, 0, len(cases)),
			Counts: reviewCountsBody{Pending: counts.Pending, Decided: counts.Decided}}
		for _, c := range cases {
			out.Items = append(out.Items, toReviewRequestBody(c))
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// reviewFilter reads ?status=pending|decided (default pending),
// ?order=asc|desc (default asc: the oldest first) and ?reason=, validated
// against the routing reasons of the domain.
func reviewFilter(req *http.Request) (domain.ReviewFilter, bool) {
	q := req.URL.Query()
	f := domain.ReviewFilter{Status: domain.ReviewPending}
	if s := q.Get("status"); s != "" {
		status, ok := domain.ParseReviewStatus(s)
		if !ok {
			return f, false
		}
		f.Status = status
	}
	switch q.Get("order") {
	case "", "asc":
	case "desc":
		f.Descending = true
	default:
		return f, false
	}
	if reason := q.Get("reason"); reason != "" {
		if !domain.IsReviewReason(reason) {
			return f, false
		}
		f.Reason = reason
	}
	return f, true
}

func getReviewRequest(svc ReviewService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := pathID(req, "acceptance_id")
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid_request", "acceptance_id (max 128 chars) is required")
			return
		}
		d, err := svc.Get(req.Context(), id)
		if err != nil {
			writeReviewError(w, req, err)
			return
		}
		out := reviewRequestDetailBody{
			reviewRequestBody: toReviewRequestBody(d.Case),
			Offer:             reviewOfferDetailBody{Premium: d.Case.Request.Premium, Currency: d.Case.Request.Currency},
		}
		if d.OfferMode != "" {
			mode := string(d.OfferMode)
			out.Offer.Mode = &mode
		}
		if !d.OfferExpiresAt.IsZero() {
			t := d.OfferExpiresAt.UTC()
			out.Offer.ExpiresAt = &t
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func decideReviewRequest(svc ReviewService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := pathID(req, "acceptance_id")
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid_request", "acceptance_id (max 128 chars) is required")
			return
		}
		var in decideRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, maxBodyBytes)).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "the request body is not valid JSON")
			return
		}
		c, err := svc.Decide(req.Context(), id, domain.DecisionInput{
			Type: domain.DecisionType(in.Type), Observations: in.Observations, PremiumAdjustmentPct: in.PremiumAdjustmentPct,
		})
		if err != nil {
			writeReviewError(w, req, err)
			return
		}
		writeJSON(w, http.StatusCreated, toReviewRequestBody(c))
	}
}

func writeReviewError(w http.ResponseWriter, req *http.Request, err error) {
	switch {
	case isAuthError(err):
		writeAuthError(w, err)
	case errors.Is(err, domain.ErrObservationsRequired):
		writeError(w, http.StatusBadRequest, "observations_required", "observations are required to adjust or reject")
	case errors.Is(err, domain.ErrInvalidPremiumAdjustment):
		writeError(w, http.StatusBadRequest, "premium_adjustment_invalid", err.Error())
	case errors.Is(err, domain.ErrInvalidDecision):
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
	case errors.Is(err, ports.ErrReviewRequestNotFound):
		writeError(w, http.StatusNotFound, "review_request_not_found", "the review request does not exist")
	case errors.Is(err, underwriting.ErrContinuationPending):
		slog.WarnContext(req.Context(), "human decision recorded, continuation pending", "error", err)
		writeError(w, http.StatusServiceUnavailable, "decision_continuation_pending",
			"the decision is registered but the origination flow did not continue yet; retrying is safe")
	case errors.Is(err, ports.ErrReviewAlreadyDecided):
		writeError(w, http.StatusConflict, "review_request_already_decided", "the review request is already decided")
	case errors.Is(err, underwriting.ErrDecisionNotRegistered):
		slog.ErrorContext(req.Context(), "could not register the human decision", "error", err)
		writeError(w, http.StatusServiceUnavailable, "decision_not_registered",
			"the decision could not be registered; retrying is safe")
	default:
		slog.ErrorContext(req.Context(), "could not read the review requests", "error", err)
		writeError(w, http.StatusServiceUnavailable, "review_unavailable", "the review requests could not be read; retry later")
	}
}

func toReviewRequestBody(c domain.ReviewCase) reviewRequestBody {
	r := c.Request
	out := reviewRequestBody{
		AcceptanceID: r.AcceptanceID,
		OfferID:      r.OfferID,
		CustomerID:   r.CustomerID,
		Applicant:    applicantBody{FullName: r.Applicant.FullName, DocumentID: r.Applicant.DocumentID, Email: r.Applicant.Email},
		Offer:        reviewOfferBody{Premium: r.Premium, Currency: r.Currency},
		Reason:       r.Reason,
		ReceivedAt:   r.ReceivedAt.UTC(),
		Status:       string(c.Status),
	}
	if d := c.Decision; d != nil {
		out.Decision = &decisionBody{
			DecisionID:           d.ID,
			Type:                 string(d.Type),
			Advisor:              advisorBody{ID: d.Advisor.ID, Email: d.Advisor.Email},
			DecidedAt:            d.DecidedAt.UTC(),
			Observations:         d.Observations,
			PremiumAdjustmentPct: d.PremiumAdjustmentPct,
			AdjustedOffer:        toAdjustedOfferBody(d.AdjustedOffer),
		}
		if n := c.Notification; n != nil {
			out.Decision.Notification = &reviewNotificationBody{Channel: n.Channel, Status: string(n.Status), Attempts: n.Attempts}
			if !n.SentAt.IsZero() {
				t := n.SentAt.UTC()
				out.Decision.Notification.SentAt = &t
			}
		}
	}
	return out
}

func toAdjustedOfferBody(o *domain.AdjustedOffer) *adjustedOfferBody {
	if o == nil {
		return nil
	}
	return &adjustedOfferBody{OfferID: o.ID, Premium: o.Premium, Currency: o.Currency, ExpiresAt: o.ExpiresAt.UTC()}
}
