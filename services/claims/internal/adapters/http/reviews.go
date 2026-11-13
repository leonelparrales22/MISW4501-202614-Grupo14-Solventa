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
	Approve(ctx context.Context, claimID string, advisor domain.Reviewer) (domain.Review, error)
	Return(ctx context.Context, claimID string, advisor domain.Reviewer, in domain.ReturnRequest) (domain.Review, error)
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

type verdictRequest struct {
	RequirementCode string  `json:"requirement_code"`
	Verdict         string  `json:"verdict"`
	Note            *string `json:"note"`
}

type returnRequest struct {
	ReasonCode     *string          `json:"reason_code"`
	Message        *string          `json:"message"`
	Supports       []verdictRequest `json:"supports"`
	FactsToCorrect []string         `json:"facts_to_correct"`
}

// Bounds of a return, as the contract states them. The lengths of the texts
// are checked again in characters by the domain.
const (
	maxVerdicts       = 12
	maxFactsToCorrect = 4
	maxSupportCodeLen = 40
)

type verdictBody struct {
	RequirementCode string  `json:"requirement_code"`
	Label           string  `json:"label"`
	Verdict         string  `json:"verdict"`
	Note            *string `json:"note"`
}

type reviewBody struct {
	Seq            int           `json:"seq"`
	Outcome        string        `json:"outcome"`
	ReviewedBy     string        `json:"reviewed_by"`
	ReviewerName   string        `json:"reviewer_name"`
	ReviewedAt     time.Time     `json:"reviewed_at"`
	ReasonCode     *string       `json:"reason_code"`
	Message        *string       `json:"message"`
	Supports       []verdictBody `json:"supports"`
	FactsToCorrect []string      `json:"facts_to_correct"`
}

type reviewOutcomeBody struct {
	ClaimID      string     `json:"claim_id"`
	FilingNumber string     `json:"filing_number"`
	Status       string     `json:"status"`
	Review       reviewBody `json:"review"`
}

type checklistBody struct {
	RequirementCode string `json:"requirement_code"`
	Label           string `json:"label"`
	Required        bool   `json:"required"`
	Files           int    `json:"files"`
}

type claimReviewBody struct {
	evaluationBody
	Checklist   []checklistBody    `json:"checklist"`
	PriorClaims []claimSummaryBody `json:"prior_claims"`
	Reviews     []reviewBody       `json:"reviews"`
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
	Reviews       []reviewBody       `json:"reviews"`
	LastReturn    *reviewBody        `json:"last_return"`
	// Resubmissions are the times the insured sent the claim again after a
	// return (HU-W2.1-04), oldest first.
	Resubmissions []resubmissionBody `json:"resubmissions"`
	// Requirements are the supports the claim can have with their rules
	// (HU-W2.1-04): what the insured uploads to correct it.
	Requirements []requirementBody `json:"requirements"`
	// Decision is the decision of operations and Provider the provider
	// assigned, as the insured reads them (HU-W2.1-02); null when there is
	// none yet.
	Decision *decisionViewBody `json:"decision"`
	Provider *providerViewBody `json:"provider"`
	// HA-L02: whether the history came from the read cache, and when it was
	// read.
	*freshnessBody
}

type decisionViewBody struct {
	Outcome      string     `json:"outcome"`
	DecidedAt    time.Time  `json:"decided_at"`
	AmountMinor  *int64     `json:"amount_minor"`
	Currency     *string    `json:"currency"`
	PayoutStatus *string    `json:"payout_status"`
	PaidAt       *time.Time `json:"paid_at"`
	Reason       *string    `json:"rejection_reason"`
}

type providerViewBody struct {
	Name       string    `json:"name"`
	DistanceKm float64   `json:"distance_km"`
	AssignedAt time.Time `json:"assigned_at"`
}

func decisionViewBodyOf(d *domain.DecisionView) *decisionViewBody {
	if d == nil {
		return nil
	}
	out := &decisionViewBody{Outcome: string(d.Outcome), DecidedAt: d.DecidedAt.UTC(), Currency: nullable(d.Currency),
		PayoutStatus: nullable(string(d.PayoutStatus)), PaidAt: d.PaidAt, Reason: nullable(d.Reason)}
	if d.AmountMinor > 0 {
		amount := d.AmountMinor
		out.AmountMinor = &amount
	}
	return out
}

func providerViewBodyOf(p *domain.ProviderView) *providerViewBody {
	if p == nil {
		return nil
	}
	return &providerViewBody{Name: p.Name, DistanceKm: p.DistanceKm, AssignedAt: p.AssignedAt.UTC()}
}

// nullable is the text, or null when it is empty.
func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func reviewBodyOf(r domain.Review) reviewBody {
	out := reviewBody{Seq: r.Seq, Outcome: string(r.Outcome), ReviewedBy: r.ReviewedBy, ReviewerName: r.ReviewerName,
		ReviewedAt: r.ReviewedAt.UTC(), ReasonCode: nullable(string(r.ReasonCode)), Message: nullable(r.Message),
		Supports: make([]verdictBody, 0, len(r.Supports)), FactsToCorrect: make([]string, 0, len(r.FactsToCorrect))}
	for _, a := range r.Supports {
		out.Supports = append(out.Supports, verdictBody{RequirementCode: a.RequirementCode, Label: a.Label,
			Verdict: string(a.Verdict), Note: nullable(a.Note)})
	}
	for _, f := range r.FactsToCorrect {
		out.FactsToCorrect = append(out.FactsToCorrect, string(f))
	}
	return out
}

// reviewerOf is who reviews, from the token: the id the history keeps and
// the name the insured reads, or the id when the user has no name.
func reviewerOf(req *http.Request) domain.Reviewer {
	c, _ := security.ClaimsFromContext(req.Context())
	id := analystOf(req)
	name := c.Name
	if name == "" {
		name = id
	}
	return domain.Reviewer{ID: id, Name: name}
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
		out := claimReviewBody{evaluationBody: evaluationBodyOf(d.Evaluation),
			Checklist:   make([]checklistBody, 0, len(d.Checklist)),
			PriorClaims: make([]claimSummaryBody, 0, len(d.PriorClaims)),
			Reviews:     make([]reviewBody, 0, len(d.Reviews))}
		for _, item := range d.Checklist {
			out.Checklist = append(out.Checklist, checklistBody{RequirementCode: item.RequirementCode, Label: item.Label,
				Required: item.Required, Files: item.Files})
		}
		for _, c := range d.PriorClaims {
			out.PriorClaims = append(out.PriorClaims, summaryBody(c))
		}
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
		r, err := svc.Approve(req.Context(), id, reviewerOf(req))
		if writeReviewError(w, req, err) {
			return
		}
		slog.InfoContext(req.Context(), "filing approved by the advisor", "claim_id", r.ClaimID, "seq", r.Seq)
		writeJSON(w, http.StatusOK, reviewOutcomeBody{ClaimID: r.ClaimID, FilingNumber: r.FilingNumber,
			Status: string(r.StatusOf()), Review: reviewBodyOf(r)})
	}
}

// returnRequestOf checks the shape of a return; the domain checks the rest.
func returnRequestOf(in returnRequest) (domain.ReturnRequest, bool) {
	if in.ReasonCode == nil || in.Message == nil || len(in.Supports) > maxVerdicts ||
		len(in.FactsToCorrect) > maxFactsToCorrect || len(*in.Message) > 4*domain.MaxReturnMessageLength {
		return domain.ReturnRequest{}, false
	}
	out := domain.ReturnRequest{ReasonCode: domain.ReasonCode(*in.ReasonCode), Message: *in.Message,
		Supports:       make([]domain.SupportVerdict, 0, len(in.Supports)),
		FactsToCorrect: make([]domain.Fact, 0, len(in.FactsToCorrect))}
	if !out.ReasonCode.Valid() {
		return domain.ReturnRequest{}, false
	}
	codes := make([]string, 0, len(in.Supports))
	for _, v := range in.Supports {
		verdict := domain.Verdict(v.Verdict)
		if v.RequirementCode == "" || len(v.RequirementCode) > maxSupportCodeLen ||
			(verdict != domain.VerdictValid && verdict != domain.VerdictIllegible && verdict != domain.VerdictMissing) ||
			(v.Note != nil && len(*v.Note) > 4*domain.MaxVerdictNoteLength) {
			return domain.ReturnRequest{}, false
		}
		sv := domain.SupportVerdict{RequirementCode: v.RequirementCode, Verdict: verdict}
		if v.Note != nil {
			sv.Note = *v.Note
		}
		out.Supports = append(out.Supports, sv)
		codes = append(codes, v.RequirementCode)
	}
	for _, f := range in.FactsToCorrect {
		if !domain.Fact(f).Valid() {
			return domain.ReturnRequest{}, false
		}
		out.FactsToCorrect = append(out.FactsToCorrect, domain.Fact(f))
	}
	if hasDuplicates(codes) || hasDuplicates(in.FactsToCorrect) {
		return domain.ReturnRequest{}, false
	}
	return out, true
}

func returnFiling(svc Reviewer) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := claimIDOf(w, req)
		if !ok {
			return
		}
		var body returnRequest
		if !decodeStrict(w, req, &body) {
			return
		}
		in, ok := returnRequestOf(body)
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid_request", "the request body is not valid")
			return
		}
		r, err := svc.Return(req.Context(), id, reviewerOf(req), in)
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

// reviewErrors are the errors of a review the advisor corrects: 422 with the
// code of the contract.
var reviewErrors = []struct {
	err     error
	code    string
	message string
}{
	{domain.ErrSupportsIncomplete, "supports_incomplete", "a required support is missing: return the filing instead"},
	{domain.ErrReasonCodeInvalid, "reason_code_invalid", "the reason is not one of the list"},
	{domain.ErrMessageRequired, "message_required", "the message to the insured is required"},
	{domain.ErrMessageTooLong, "message_too_long", "the message to the insured is too long"},
	{domain.ErrVerdictNoteTooLong, "note_too_long", "the note of a support is too long"},
	{domain.ErrNothingRequested, "nothing_requested", "mark a support as illegible or missing, or ask for a fact"},
	{domain.ErrSupportNotApplicable, "support_not_applicable", "a support is not one of the claim type"},
	{domain.ErrVerdictNotApplicable, "verdict_not_applicable",
		"an attached support is valid or illegible; one that is not attached can only be missing"},
}

// writeReviewError answers the error of a review, and tells whether there
// was one.
func writeReviewError(w http.ResponseWriter, req *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, ports.ErrClaimNotFound):
		writeError(w, http.StatusNotFound, "claim_not_found", "there is no claim with that id")
		return true
	case errors.Is(err, domain.ErrNotReviewable):
		writeError(w, http.StatusConflict, "claim_not_reviewable", "the claim is not waiting for review")
		return true
	}
	for _, e := range reviewErrors {
		if errors.Is(err, e.err) {
			writeError(w, http.StatusUnprocessableEntity, e.code, e.message)
			return true
		}
	}
	slog.ErrorContext(req.Context(), "could not record the review of the claim", "error", err)
	writeError(w, http.StatusServiceUnavailable, "claims_unavailable", "the review could not be recorded; retry")
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
		req, fresh := trackFreshness(req)
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
		out.Requirements = requirementBodiesOf(h.Requirements)
		out.Decision, out.Provider = decisionViewBodyOf(h.Decision), providerViewBodyOf(h.Provider)
		out.Resubmissions = make([]resubmissionBody, 0, len(h.Resubmissions))
		for _, r := range h.Resubmissions {
			out.Resubmissions = append(out.Resubmissions, resubmissionBodyOf(r))
		}
		out.Reviews = make([]reviewBody, 0, len(h.Reviews))
		for _, r := range h.Reviews {
			out.Reviews = append(out.Reviews, reviewBodyOf(r))
		}
		if r := h.LastReturn; r != nil {
			body := reviewBodyOf(*r)
			out.LastReturn = &body
		}
		out.freshnessBody = freshnessOf(w, fresh)
		writeJSON(w, http.StatusOK, out)
	}
}
