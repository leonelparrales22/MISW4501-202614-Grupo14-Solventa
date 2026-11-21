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
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/cancellation"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// HU-W1.6-01: the insured cancels one of their policies with the refund of
// the part of the current month not used (screens A5 and A5b).

// CancellationService is the use case behind the cancellation.
type CancellationService interface {
	Quote(ctx context.Context, policyID string) (domain.CancellationQuote, error)
	Cancel(ctx context.Context, policyID string, req domain.CancellationRequest) (domain.PolicyCancellation, error)
}

// WithCancellations mounts the quote and the confirmation of the cancellation
// of a policy. They are human routes for the insured who owns the policy, and
// their answers are never cached: the refund changes from one day to the next.
// Register it after WithObservability.
func WithCancellations(svc CancellationService, verifier security.Verifier) Option {
	return func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(noStore, security.Authenticate(verifier), security.RequireRole(security.RoleInsured))
			r.Get("/v1/policies/{policy_id}/cancellation/quote", getCancellationQuote(svc))
			r.Post("/v1/policies/{policy_id}/cancellation", cancelPolicy(svc))
		})
	}
}

// cancellationQuoteBody is what screen A5 shows before confirming (CA-1).
type cancellationQuoteBody struct {
	PolicyID string `json:"policy_id"`
	// EffectiveAt is when the coverage ends: the end of the day.
	EffectiveAt    time.Time         `json:"effective_at"`
	PaidPeriod     paidPeriodBody    `json:"paid_period"`
	UnusedDays     int               `json:"unused_days"`
	PeriodDays     int               `json:"period_days"`
	ProratePercent float64           `json:"prorate_percent"`
	RefundAmount   float64           `json:"refund_amount"`
	Currency       string            `json:"currency"`
	RefundMethod   *refundMethodBody `json:"refund_method"`
	// PaymentStatus is up_to_date or payment_pending (an instalment in its
	// grace period: nothing paid to give back).
	PaymentStatus string `json:"payment_status"`
}

func toCancellationQuoteBody(q domain.CancellationQuote) cancellationQuoteBody {
	return cancellationQuoteBody{
		PolicyID: q.PolicyID, EffectiveAt: q.EffectiveAt.UTC(),
		PaidPeriod: paidPeriodBody{Month: q.Paid.Seq, Kind: string(q.Paid.Kind), Start: q.PeriodStart.UTC(),
			End: q.PeriodEnd.UTC(), PaidAmount: domain.FromMinorUnits(q.Paid.AmountMinor), Currency: q.Currency},
		UnusedDays: q.UnusedDays, PeriodDays: q.PeriodDays, ProratePercent: q.ProratePercent(),
		RefundAmount: domain.FromMinorUnits(q.RefundAmountMinor), Currency: q.Currency,
		RefundMethod: toRefundMethodBody(q.Method), PaymentStatus: string(q.PaymentStatus),
	}
}

func getCancellationQuote(svc CancellationService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := pathID(req, "policy_id")
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid_request", "policy_id (max 128 chars) is required")
			return
		}
		q, err := svc.Quote(req.Context(), id)
		if err != nil {
			respondCancellationError(w, req, err)
			return
		}
		writeJSON(w, http.StatusOK, toCancellationQuoteBody(q))
	}
}

// cancellationInput is the confirmation of screen A5 (CA-2).
type cancellationInput struct {
	Reason   string  `json:"reason"`
	Comments *string `json:"comments"`
	// RefundAmount is the refund the insured saw: the one registered.
	RefundAmount *float64 `json:"refund_amount"`
	AcceptTerms  *bool    `json:"accept_terms"`
}

// cancelledPolicyBody is the policy just cancelled.
type cancelledPolicyBody struct {
	PolicyID     string            `json:"policy_id"`
	Status       string            `json:"status"`
	Cancellation *cancellationBody `json:"cancellation"`
}

func cancelPolicy(svc CancellationService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := pathID(req, "policy_id")
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid_request", "policy_id (max 128 chars) is required")
			return
		}
		var in cancellationInput
		dec := json.NewDecoder(http.MaxBytesReader(w, req.Body, maxBodyBytes))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "the request body is not a valid cancellation")
			return
		}
		var missing []domain.FieldError
		if in.RefundAmount == nil {
			missing = append(missing, domain.FieldError{Field: "refund_amount", Code: "required"})
		}
		if in.AcceptTerms == nil {
			missing = append(missing, domain.FieldError{Field: "accept_terms", Code: "required"})
		}
		if len(missing) > 0 {
			writeInvalidCancellation(w, &domain.ValidationError{Fields: missing})
			return
		}
		r := domain.CancellationRequest{Reason: domain.InsuredCancellationReason(in.Reason),
			RefundAmountMinor: domain.ToMinorUnits(*in.RefundAmount), AcceptTerms: *in.AcceptTerms}
		if in.Comments != nil {
			r.Comments = *in.Comments
		}
		c, err := svc.Cancel(req.Context(), id, r)
		if err != nil {
			respondCancellationError(w, req, err)
			return
		}
		w.Header().Set("Location", "/v1/policies/"+url.PathEscape(id))
		writeJSON(w, http.StatusCreated, cancelledPolicyBody{PolicyID: id, Status: string(domain.PolicyCancelled),
			Cancellation: cancellationBodyOf(c)})
	}
}

// writeInvalidCancellation is the 422 of a confirmation with invalid fields.
func writeInvalidCancellation(w http.ResponseWriter, e *domain.ValidationError) {
	out := validationErrorResponse{Error: "invalid_request", Message: "one or more fields are missing or invalid",
		Fields: make([]fieldErrorBody, 0, len(e.Fields))}
	for _, f := range e.Fields {
		out.Fields = append(out.Fields, fieldErrorBody{Field: f.Field, Code: f.Code})
	}
	writeJSON(w, http.StatusUnprocessableEntity, out)
}

// refundChangedBody is the 409 of a confirmation whose refund changed, with
// the new quote to show.
type refundChangedBody struct {
	Error   string                `json:"error"`
	Message string                `json:"message"`
	Quote   cancellationQuoteBody `json:"quote"`
}

func respondCancellationError(w http.ResponseWriter, req *http.Request, err error) {
	var (
		invalid *domain.ValidationError
		changed *cancellation.RefundChangedError
	)
	switch {
	case errors.As(err, &invalid):
		writeInvalidCancellation(w, invalid)
	case errors.As(err, &changed):
		writeJSON(w, http.StatusConflict, refundChangedBody{Error: "refund_changed",
			Message: "the refund changed since it was quoted; confirm the new amount", Quote: toCancellationQuoteBody(changed.Quote)})
	case errors.Is(err, ports.ErrPolicyNotFound):
		writeError(w, http.StatusNotFound, "policy_not_found", "the policy does not exist")
	case errors.Is(err, domain.ErrPolicyNotInForce):
		writeError(w, http.StatusConflict, "policy_not_in_force", "the policy is not in force")
	case errors.Is(err, ports.ErrPolicyAlreadyCancelled):
		writeError(w, http.StatusConflict, "policy_already_cancelled", "the policy is already cancelled")
	case errors.Is(err, ports.ErrRenewalInProgress):
		writeError(w, http.StatusConflict, "renewal_in_progress", "the renewal of the policy is in progress")
	case errors.Is(err, ports.ErrPaymentInProgress):
		writeError(w, http.StatusConflict, "payment_in_progress", "an instalment of the policy is being charged; retry in a few minutes")
	case isAuthError(err):
		writeAuthError(w, err)
	default:
		slog.ErrorContext(req.Context(), "could not process the cancellation", "error", err)
		writeError(w, http.StatusServiceUnavailable, "cancellation_unavailable", "the cancellation could not be processed; retry later")
	}
}
