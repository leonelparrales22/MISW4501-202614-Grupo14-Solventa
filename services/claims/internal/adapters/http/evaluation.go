package httpadapter

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/evaluation"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// EvaluationReader is what the routes of the evaluation read (HU-W2.2-03).
type EvaluationReader interface {
	Queue(ctx context.Context, pageSize int, pageToken string) (evaluation.QueuePage, error)
	Detail(ctx context.Context, claimID string) (evaluation.Evaluation, error)
}

// WithEvaluation mounts the reads of the evaluation of a claim (HU-W2.2-03):
// the claims in evaluation and the detail of one, for operations only. They
// hang from /v1/claims, so the API Gateway already routes them through the
// Cognito authorizer of the claims routes.
func WithEvaluation(svc EvaluationReader, verifier security.Verifier) Option {
	return func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(noStore, security.Authenticate(verifier), security.RequireRole(security.RoleOperations))
			r.Get("/v1/claims/evaluations", evaluationQueue(svc))
			r.Get("/v1/claims/{claim_id}/evaluation", evaluationDetail(svc))
		})
	}
}

type evaluationItemBody struct {
	claimSummaryBody
	EstimatedAmountMinor *int64            `json:"estimated_amount_minor"`
	Triage               triageSummaryBody `json:"triage"`
}

type evaluationQueueBody struct {
	Items         []evaluationItemBody `json:"items"`
	NextPageToken *string              `json:"next_page_token"`
}

type coverageBody struct {
	Incident              string `json:"incident"`
	InsuredValueMinor     int64  `json:"insured_value_minor"`
	Currency              string `json:"currency"`
	DeductibleBasisPoints int    `json:"deductible_basis_points"`
}

type currentPolicyBody struct {
	PolicyID      string         `json:"policy_id"`
	Line          string         `json:"line"`
	Product       string         `json:"product"`
	InsuredObject string         `json:"insured_object"`
	Status        string         `json:"status"`
	ValidFrom     time.Time      `json:"valid_from"`
	ValidTo       time.Time      `json:"valid_to"`
	Coverages     []coverageBody `json:"coverages"`
}

// payoutAccountBody shows the account masked: the full number is needed to
// pay, not to evaluate.
type payoutAccountBody struct {
	BankCode     string `json:"bank_code"`
	BankName     string `json:"bank_name"`
	AccountType  string `json:"account_type"`
	MaskedNumber string `json:"masked_number"`
}

type holderBody struct {
	FullName      string            `json:"full_name"`
	DocumentType  string            `json:"document_type"`
	DocumentID    string            `json:"document_id"`
	Email         string            `json:"email"`
	Phone         string            `json:"phone"`
	PayoutAccount payoutAccountBody `json:"payout_account"`
}

type supportLinkBody struct {
	SupportID         string     `json:"support_id"`
	RequirementCode   string     `json:"requirement_code"`
	FileName          string     `json:"file_name"`
	ContentType       string     `json:"content_type"`
	ByteSize          int64      `json:"byte_size"`
	SHA256            string     `json:"sha256"`
	DownloadURL       *string    `json:"download_url"`
	DownloadExpiresAt *time.Time `json:"download_expires_at"`
}

type evaluationBody struct {
	Claim             claimDetailBody    `json:"claim"`
	SupportFiles      []supportLinkBody  `json:"support_files"`
	Triage            claimTriageBody    `json:"triage"`
	Policy            *currentPolicyBody `json:"policy"`
	Coverage          *coverageBody      `json:"coverage"`
	Holder            *holderBody        `json:"holder"`
	PolicyUnavailable bool               `json:"policy_unavailable"`
}

func evaluationQueue(svc EvaluationReader) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		query := req.URL.Query()
		size := 0
		if raw := query.Get("page_size"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 {
				writeError(w, http.StatusBadRequest, "invalid_request", "page_size must be between 1 and 50")
				return
			}
			size = n
		}
		token := query.Get("page_token")
		if len(token) > 512 {
			writeError(w, http.StatusBadRequest, "invalid_request", "the page token is not valid")
			return
		}
		page, err := svc.Queue(req.Context(), size, token)
		switch {
		case errors.Is(err, evaluation.ErrInvalidQuery):
			writeError(w, http.StatusBadRequest, "invalid_request", "page_size must be between 1 and 50 and the page token valid")
			return
		case err != nil:
			slog.ErrorContext(req.Context(), "could not list the claims in evaluation", "error", err)
			writeError(w, http.StatusServiceUnavailable, "claims_unavailable", "the claims could not be read; retry")
			return
		}
		out := evaluationQueueBody{Items: make([]evaluationItemBody, 0, len(page.Items))}
		for _, item := range page.Items {
			out.Items = append(out.Items, evaluationItemBody{
				claimSummaryBody:     summaryBody(item.Claim),
				EstimatedAmountMinor: item.Claim.EstimatedAmountMinor,
				Triage:               summaryOfTriage(item.Claim.ID, item.Triage.State, item.Triage.Current),
			})
		}
		if page.NextPageToken != "" {
			out.NextPageToken = &page.NextPageToken
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func evaluationDetail(svc EvaluationReader) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id := chi.URLParam(req, "claim_id")
		if !claimIDPattern.MatchString(id) {
			writeError(w, http.StatusNotFound, "claim_not_found", "there is no claim with that id")
			return
		}
		e, err := svc.Detail(req.Context(), id)
		switch {
		case errors.Is(err, ports.ErrClaimNotFound):
			writeError(w, http.StatusNotFound, "claim_not_found", "there is no claim with that id")
			return
		case err != nil:
			slog.ErrorContext(req.Context(), "could not read the evaluation of the claim", "error", err)
			writeError(w, http.StatusServiceUnavailable, "claims_unavailable", "the claim could not be read; retry")
			return
		}
		writeJSON(w, http.StatusOK, evaluationBodyOf(e))
	}
}

func evaluationBodyOf(e evaluation.Evaluation) evaluationBody {
	out := evaluationBody{
		Claim:             detailBody(e.Claim, e.Evidence),
		SupportFiles:      make([]supportLinkBody, 0, len(e.Supports)),
		Triage:            claimTriageBody{View: "operations", ClaimID: e.Triage.ClaimID, State: string(e.Triage.State)},
		PolicyUnavailable: e.PolicyUnavailable,
	}
	out.Triage.History = make([]triageHistoryBody, 0, len(e.Triage.History))
	if e.Triage.Current != nil {
		out.Triage.Current = assessmentBody(*e.Triage.Current)
	}
	for _, r := range e.Triage.History {
		out.Triage.History = append(out.Triage.History, historyBody(r))
	}
	for _, s := range e.Supports {
		body := supportLinkBody{
			SupportID: s.Support.File.ID, RequirementCode: s.Support.RequirementCode, FileName: s.Support.File.FileName,
			ContentType: s.Support.File.ContentType(), ByteSize: s.Support.File.ByteSize, SHA256: s.Support.File.SHA256,
		}
		if s.URL != "" {
			url, expires := s.URL, s.ExpiresAt.UTC()
			body.DownloadURL, body.DownloadExpiresAt = &url, &expires
		}
		out.SupportFiles = append(out.SupportFiles, body)
	}
	if t := e.Terms; t != nil {
		p := t.Policy
		out.Policy = &currentPolicyBody{
			PolicyID: p.ID, Line: string(p.Line), Product: p.Product, InsuredObject: p.InsuredObject,
			Status: string(p.Status), ValidFrom: p.ValidFrom.UTC(), ValidTo: p.ValidTo.UTC(),
			Coverages: make([]coverageBody, 0, len(t.Coverages)),
		}
		for _, c := range t.Coverages {
			out.Policy.Coverages = append(out.Policy.Coverages, coverageBodyOf(c))
		}
	}
	if c := e.Coverage; c != nil {
		body := coverageBodyOf(*c)
		out.Coverage = &body
	}
	if h := e.Holder; h != nil {
		a := h.PayoutAccount
		out.Holder = &holderBody{
			FullName: h.FullName, DocumentType: h.DocumentType, DocumentID: h.DocumentID, Email: h.Email, Phone: h.Phone,
			PayoutAccount: payoutAccountBody{BankCode: a.BankCode, BankName: a.BankName, AccountType: a.AccountType,
				MaskedNumber: a.MaskedNumber()},
		}
	}
	return out
}

func coverageBodyOf(c domain.Coverage) coverageBody {
	return coverageBody{Incident: string(c.Incident), InsuredValueMinor: c.InsuredValueMinor, Currency: c.Currency,
		DeductibleBasisPoints: c.DeductibleBasisPoints}
}

// summaryOfTriage is the priority of a claim as the batch read of the triage
// gives it.
func summaryOfTriage(claimID string, state domain.TriageState, current *domain.TriageRecord) triageSummaryBody {
	body := triageSummaryBody{ClaimID: claimID, State: string(state)}
	if c := current; c != nil {
		a := c.Assessment
		level, severity, band, target, at := int(a.Level), string(a.Severity), string(a.AmountBand),
			targetBody(a.Target), c.AssessedAt.UTC()
		body.Level, body.Severity, body.AmountBand, body.Target, body.AssessedAt = &level, &severity, &band, &target, &at
	}
	return body
}
