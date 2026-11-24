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
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/review"
)

// Resubmitter is what the resubmission of a returned filing uses
// (HU-W2.1-04).
type Resubmitter interface {
	Resubmit(ctx context.Context, claimID, subject string, in domain.ResubmissionRequest) (domain.Resubmission, bool, error)
}

// WithResubmission mounts the resubmission of a returned filing: the insured
// corrects it and sends it again, only their own (HA-S01).
func WithResubmission(svc Resubmitter, verifier security.Verifier) Option {
	return func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(noStore, security.Authenticate(verifier), security.RequireRole(security.RoleInsured))
			r.Post("/v1/claims/{claim_id}/resubmission", resubmitFiling(svc))
		})
	}
}

type resubmissionRequest struct {
	ReviewSeq   *int    `json:"review_seq"`
	OccurredAt  *string `json:"occurred_at"`
	Place       *string `json:"place"`
	Description *string `json:"description"`
	// EstimatedAmountMinor is kept raw, as in the filing: a text or a decimal
	// is a field error, not a body that is not JSON.
	EstimatedAmountMinor json.RawMessage    `json:"estimated_amount_minor"`
	Supports             []supportGroupBody `json:"supports"`
}

type factChangeBody struct {
	Fact   string  `json:"fact"`
	Before *string `json:"before"`
	After  *string `json:"after"`
}

type supportChangeBody struct {
	RequirementCode string   `json:"requirement_code"`
	Label           string   `json:"label"`
	Added           []string `json:"added"`
	Withdrawn       []string `json:"withdrawn"`
}

type resubmissionBody struct {
	Seq             int                 `json:"seq"`
	ReviewSeq       int                 `json:"review_seq"`
	ResubmittedBy   string              `json:"resubmitted_by"`
	ResubmittedAt   time.Time           `json:"resubmitted_at"`
	ChangedFacts    []factChangeBody    `json:"changed_facts"`
	ChangedSupports []supportChangeBody `json:"changed_supports"`
}

type resubmissionOutcomeBody struct {
	ClaimID      string           `json:"claim_id"`
	FilingNumber string           `json:"filing_number"`
	Status       string           `json:"status"`
	Resubmission resubmissionBody `json:"resubmission"`
}

func resubmissionBodyOf(r domain.Resubmission) resubmissionBody {
	out := resubmissionBody{Seq: r.Seq, ReviewSeq: r.ReviewSeq, ResubmittedBy: r.ResubmittedBy,
		ResubmittedAt: r.ResubmittedAt.UTC(), ChangedFacts: make([]factChangeBody, 0, len(r.Facts)),
		ChangedSupports: make([]supportChangeBody, 0, len(r.Supports))}
	for _, f := range r.Facts {
		out.ChangedFacts = append(out.ChangedFacts, factChangeBody{Fact: string(f.Fact), Before: nullable(f.Before),
			After: nullable(f.After)})
	}
	for _, c := range r.Supports {
		out.ChangedSupports = append(out.ChangedSupports, supportChangeBody{RequirementCode: c.RequirementCode,
			Label: c.Label, Added: nonNilStrings(c.Added), Withdrawn: nonNilStrings(c.Withdrawn)})
	}
	return out
}

func nonNilStrings(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}

func resubmitFiling(svc Resubmitter) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := claimIDOf(w, req)
		if !ok {
			return
		}
		var body resubmissionRequest
		if !decodeStrict(w, req, &body) {
			return
		}
		if body.ReviewSeq == nil || *body.ReviewSeq < 1 {
			writeError(w, http.StatusBadRequest, "invalid_request", "review_seq is required")
			return
		}
		in := domain.ResubmissionRequest{ReviewSeq: *body.ReviewSeq, OccurredAt: body.OccurredAt, Place: body.Place,
			Description: body.Description}
		if len(body.EstimatedAmountMinor) > 0 && string(body.EstimatedAmountMinor) != "null" {
			amount := rawAmount(body.EstimatedAmountMinor)
			in.EstimatedAmountMinor = &amount
		}
		for _, g := range body.Supports {
			in.Supports = append(in.Supports, domain.SupportGroup{RequirementCode: g.RequirementCode, SupportIDs: g.SupportIDs})
		}

		claims, _ := security.ClaimsFromContext(req.Context())
		r, created, err := svc.Resubmit(req.Context(), id, claims.Subject, in)
		var invalid *domain.ValidationError
		switch {
		case err == nil:
		case errors.As(err, &invalid):
			writeValidationError(w, invalid)
			return
		case errors.Is(err, ports.ErrClaimNotFound), errors.Is(err, review.ErrNotOwner):
			// Somebody else's claim reads as one that does not exist.
			writeError(w, http.StatusNotFound, "claim_not_found", "there is no claim with that id")
			return
		case errors.Is(err, domain.ErrNotReturned):
			writeError(w, http.StatusConflict, "claim_not_returned", "the claim is not returned to the insured")
			return
		case errors.Is(err, domain.ErrStaleReturn):
			writeError(w, http.StatusConflict, "stale_return",
				"the claim was returned again; read it and correct the last return")
			return
		default:
			slog.ErrorContext(req.Context(), "could not resubmit the filing", "error", err)
			writeError(w, http.StatusServiceUnavailable, "claims_unavailable", "the filing could not be sent; retry")
			return
		}
		if created {
			slog.InfoContext(req.Context(), "returned filing sent again", "claim_id", r.ClaimID, "seq", r.Seq,
				"review_seq", r.ReviewSeq, "facts", len(r.Facts), "supports", len(r.Supports))
		}
		status := r.Claim.Status
		if status == "" {
			status = domain.StatusActive
		}
		writeJSON(w, http.StatusOK, resubmissionOutcomeBody{ClaimID: r.ClaimID, FilingNumber: r.FilingNumber,
			Status: string(status), Resubmission: resubmissionBodyOf(r)})
	}
}
