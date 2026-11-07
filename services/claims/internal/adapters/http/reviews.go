package httpadapter

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/evaluation"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/review"
)

// Reviewer is what the routes of the review of the advisor use (HU-W2.1-03).
type Reviewer interface {
	Queue(ctx context.Context, pageSize int, pageToken string) (evaluation.QueuePage, error)
	Detail(ctx context.Context, claimID string) (review.Detail, error)
	Approve(ctx context.Context, claimID, advisor string) (domain.Review, error)
	Return(ctx context.Context, claimID, advisor string, in domain.ReturnRequest) (domain.Review, error)
	History(ctx context.Context, claimID, owner string) (review.History, error)
}

// WithReviews mounts the review of a filing by the advisor (HU-W2.1-03): the
// filings waiting for review, the detail of one, its approval and its
// return. Only the advisor reviews (HA-S01). It also mounts the history of
// the status of a claim, which the insured reads of their own claims and the
// advisor and operations of any.
func WithReviews(svc Reviewer, verifier security.Verifier) Option {
	return func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(noStore, security.Authenticate(verifier), security.RequireRole(security.RoleAdvisor))
			r.Get("/v1/claims/reviews", evaluationQueue(svc.Queue))
			r.Get("/v1/claims/{claim_id}/review", reviewDetail(svc))
			r.Post("/v1/claims/{claim_id}/review-approval", approveFiling(svc))
			r.Post("/v1/claims/{claim_id}/review-return", returnFiling(svc))
		})
		r.Group(func(r chi.Router) {
			r.Use(noStore, security.Authenticate(verifier),
				security.RequireRole(security.RoleInsured, security.RoleAdvisor, security.RoleOperations))
			r.Get("/v1/claims/{claim_id}/history", claimHistory(svc))
		})
	}
}

type returnRequest struct {
	Reason          *string  `json:"reason"`
	MissingSupports []string `json:"missing_supports"`
	FactsToCorrect  []string `json:"facts_to_correct"`
}

// Bounds of a return, as the contract states them.
const (
	maxMissingSupports = 10
	maxFactsToCorrect  = 4
	maxSupportCodeLen  = 40
)

type reviewBody struct {
	Seq             int       `json:"seq"`
	Outcome         string    `json:"outcome"`
	ReviewedBy      string    `json:"reviewed_by"`
	ReviewedAt      time.Time `json:"reviewed_at"`
	Reason          *string   `json:"reason"`
	MissingSupports []string  `json:"missing_supports"`
	FactsToCorrect  []string  `json:"facts_to_correct"`
}

type reviewOutcomeBody struct {
	ClaimID      string     `json:"claim_id"`
	FilingNumber string     `json:"filing_number"`
	Status       string     `json:"status"`
	Review       reviewBody `json:"review"`
}

type claimReviewBody struct {
	evaluationBody
	Reviews []reviewBody `json:"reviews"`
}

type statusChangeBody struct {
	ID             int64     `json:"id"`
	Status         string    `json:"status"`
	PreviousStatus *string   `json:"previous_status"`
	ChangedAt      time.Time `json:"changed_at"`
}

type claimHistoryBody struct {
	ClaimID       string             `json:"claim_id"`
	FilingNumber  string             `json:"filing_number"`
	Status        string             `json:"status"`
	LastChangedAt time.Time          `json:"last_changed_at"`
	Changes       []statusChangeBody `json:"changes"`
	LastReturn    *reviewBody        `json:"last_return"`
}

func reviewBodyOf(r domain.Review) reviewBody {
	out := reviewBody{Seq: r.Seq, Outcome: string(r.Outcome), ReviewedBy: r.ReviewedBy, ReviewedAt: r.ReviewedAt.UTC(),
		MissingSupports: append([]string{}, r.MissingSupports...), FactsToCorrect: make([]string, 0, len(r.FactsToCorrect))}
	if r.Reason != "" {
		reason := r.Reason
		out.Reason = &reason
	}
	for _, f := range r.FactsToCorrect {
		out.FactsToCorrect = append(out.FactsToCorrect, string(f))
	}
	return out
}

func reviewDetail(svc Reviewer) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := claimIDOf(w, req)
		if !ok {
			return
		}
		d, err := svc.Detail(req.Context(), id)
		switch {
		case errors.Is(err, ports.ErrClaimNotFound):
			writeError(w, http.StatusNotFound, "claim_not_found", "there is no claim with that id")
			return
		case err != nil:
			slog.ErrorContext(req.Context(), "could not read the filing to review", "error", err)
			writeError(w, http.StatusServiceUnavailable, "claims_unavailable", "the claim could not be read; retry")
			return
		}
		out := claimReviewBody{evaluationBody: evaluationBodyOf(d.Evaluation), Reviews: make([]reviewBody, 0, len(d.Reviews))}
		for _, r := range d.Reviews {
			out.Reviews = append(out.Reviews, reviewBodyOf(r))
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func approveFiling(svc Reviewer) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := claimIDOf(w, req)
		if !ok {
			return
		}
		r, err := svc.Approve(req.Context(), id, analystOf(req))
		if writeReviewError(w, req, err) {
			return
		}
		slog.InfoContext(req.Context(), "filing approved by the advisor", "claim_id", r.ClaimID, "seq", r.Seq)
		writeJSON(w, http.StatusOK, reviewOutcomeBody{ClaimID: r.ClaimID, FilingNumber: r.FilingNumber,
			Status: string(r.StatusOf()), Review: reviewBodyOf(r)})
	}
}

func returnFiling(svc Reviewer) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := claimIDOf(w, req)
		if !ok {
			return
		}
		var in returnRequest
		if !decodeStrict(w, req, &in) {
			return
		}
		if in.Reason == nil || len(in.MissingSupports) > maxMissingSupports || len(in.FactsToCorrect) > maxFactsToCorrect ||
			len(*in.Reason) > 4*domain.MaxReturnReasonLength {
			writeError(w, http.StatusBadRequest, "invalid_request", "the request body is not valid")
			return
		}
		for _, code := range in.MissingSupports {
			if len(code) > maxSupportCodeLen {
				writeError(w, http.StatusBadRequest, "invalid_request", "a missing support is not valid")
				return
			}
		}
		facts := make([]domain.Fact, 0, len(in.FactsToCorrect))
		for _, f := range in.FactsToCorrect {
			if !domain.Fact(f).Valid() {
				writeError(w, http.StatusBadRequest, "invalid_request", "a fact to correct is not valid")
				return
			}
			facts = append(facts, domain.Fact(f))
		}
		if hasDuplicates(in.MissingSupports) || hasDuplicates(in.FactsToCorrect) {
			writeError(w, http.StatusBadRequest, "invalid_request", "the missing supports and the facts cannot repeat")
			return
		}
		r, err := svc.Return(req.Context(), id, analystOf(req),
			domain.ReturnRequest{Reason: *in.Reason, MissingSupports: in.MissingSupports, FactsToCorrect: facts})
		if writeReviewError(w, req, err) {
			return
		}
		slog.InfoContext(req.Context(), "filing returned to the insured", "claim_id", r.ClaimID, "seq", r.Seq)
		writeJSON(w, http.StatusOK, reviewOutcomeBody{ClaimID: r.ClaimID, FilingNumber: r.FilingNumber,
			Status: string(r.StatusOf()), Review: reviewBodyOf(r)})
	}
}

func hasDuplicates(list []string) bool {
	seen := make(map[string]bool, len(list))
	for _, v := range list {
		if seen[v] {
			return true
		}
		seen[v] = true
	}
	return false
}

// writeReviewError answers the error of a review, and tells whether there
// was one.
func writeReviewError(w http.ResponseWriter, req *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, ports.ErrClaimNotFound):
		writeError(w, http.StatusNotFound, "claim_not_found", "there is no claim with that id")
	case errors.Is(err, domain.ErrNotReviewable):
		writeError(w, http.StatusConflict, "claim_not_reviewable", "the claim is not waiting for review")
	case errors.Is(err, domain.ErrReturnReasonRequired):
		writeError(w, http.StatusUnprocessableEntity, "reason_required", "the reason of the return is required")
	case errors.Is(err, domain.ErrReturnReasonTooLong):
		writeError(w, http.StatusUnprocessableEntity, "reason_too_long", "the reason of the return is too long")
	case errors.Is(err, domain.ErrNothingRequested):
		writeError(w, http.StatusUnprocessableEntity, "nothing_requested",
			"say which supports are missing or which facts to correct")
	case errors.Is(err, domain.ErrSupportNotApplicable):
		writeError(w, http.StatusUnprocessableEntity, "support_not_applicable",
			"a missing support is not one of the claim type")
	default:
		slog.ErrorContext(req.Context(), "could not record the review of the claim", "error", err)
		writeError(w, http.StatusServiceUnavailable, "claims_unavailable", "the review could not be recorded; retry")
	}
	return true
}

func claimHistory(svc Reviewer) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := claimIDOf(w, req)
		if !ok {
			return
		}
		// An insured reads only their own claims; the advisor and operations
		// read any.
		claims, _ := security.ClaimsFromContext(req.Context())
		owner := ""
		if !claims.HasAnyRole(security.RoleAdvisor, security.RoleOperations) {
			owner = claims.Subject
		}
		h, err := svc.History(req.Context(), id, owner)
		switch {
		case errors.Is(err, ports.ErrClaimNotFound), errors.Is(err, review.ErrNotOwner):
			writeError(w, http.StatusNotFound, "claim_not_found", "there is no claim with that id")
			return
		case err != nil:
			slog.ErrorContext(req.Context(), "could not read the history of the claim", "error", err)
			writeError(w, http.StatusServiceUnavailable, "claims_unavailable", "the claim could not be read; retry")
			return
		}
		out := claimHistoryBody{ClaimID: h.Claim.ID, FilingNumber: h.Claim.FilingNumber, Status: string(h.Claim.Status),
			LastChangedAt: h.Claim.FiledAt.UTC(), Changes: make([]statusChangeBody, 0, len(h.Changes))}
		for _, c := range h.Changes {
			body := statusChangeBody{ID: c.ID, Status: string(c.Status), ChangedAt: c.ChangedAt.UTC()}
			if c.PreviousStatus != "" {
				previous := string(c.PreviousStatus)
				body.PreviousStatus = &previous
			}
			out.Changes = append(out.Changes, body)
			out.LastChangedAt = c.ChangedAt.UTC()
		}
		if r := h.LastReturn; r != nil {
			body := reviewBodyOf(*r)
			out.LastReturn = &body
		}
		writeJSON(w, http.StatusOK, out)
	}
}
