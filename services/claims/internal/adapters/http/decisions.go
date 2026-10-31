package httpadapter

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/decision"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// Decider is what the routes of the decision use (HU-W2.2-03).
type Decider interface {
	Approve(ctx context.Context, claimID, analyst string, approvedMinor int64) (domain.Decision, error)
	Reject(ctx context.Context, claimID, analyst, reason string) (domain.Decision, error)
	RetryPayout(ctx context.Context, claimID, analyst string, destination *domain.PayoutAccount) (domain.Decision, error)
}

// WithDecisions mounts the decision of a claim (HU-W2.2-03): approve it,
// which pays the indemnity, reject it with a reason, and retry the payout of
// a payment exception. Only operations decides (HA-S01): the insured and the
// advisor get 403.
func WithDecisions(svc Decider, verifier security.Verifier) Option {
	return func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(noStore, security.Authenticate(verifier), security.RequireRole(security.RoleOperations))
			r.Post("/v1/claims/{claim_id}/approval", approveClaim(svc))
			r.Post("/v1/claims/{claim_id}/rejection", rejectClaim(svc))
			r.Post("/v1/claims/{claim_id}/payout-retry", retryPayout(svc))
		})
	}
}

type approvalRequest struct {
	ApprovedAmountMinor *int64 `json:"approved_amount_minor"`
}

type rejectionRequest struct {
	Reason *string `json:"reason"`
}

type destinationRequest struct {
	BankCode      string `json:"bank_code"`
	BankName      string `json:"bank_name"`
	AccountType   string `json:"account_type"`
	AccountNumber string `json:"account_number"`
}

type payoutRetryRequest struct {
	Destination *destinationRequest `json:"destination"`
}

var (
	bankCodePattern      = regexp.MustCompile(`^[0-9]{3,6}$`)
	accountNumberPattern = regexp.MustCompile(`^[0-9][0-9-]{4,22}[0-9]$`)
)

// maxBankNameLength bounds the name of the bank of a corrected destination.
const maxBankNameLength = 80

// decodeStrict reads a JSON object with no unknown fields and nothing after
// it.
func decodeStrict(w http.ResponseWriter, req *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, req.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil || dec.More() {
		writeError(w, http.StatusBadRequest, "invalid_request", "the request body is not valid")
		return false
	}
	return true
}

// analystOf is who decided, as the history of the case shows it: the e-mail
// of the analyst, or their user name or subject when the token has none.
func analystOf(req *http.Request) string {
	c, _ := security.ClaimsFromContext(req.Context())
	for _, v := range []string{c.Email, c.Username, c.Subject} {
		if v != "" {
			return v
		}
	}
	return "unknown"
}

func approveClaim(svc Decider) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := claimIDOf(w, req)
		if !ok {
			return
		}
		var in approvalRequest
		if !decodeStrict(w, req, &in) {
			return
		}
		if in.ApprovedAmountMinor == nil || *in.ApprovedAmountMinor < 1 {
			writeError(w, http.StatusBadRequest, "invalid_request", "approved_amount_minor must be a positive whole number of cents")
			return
		}
		d, err := svc.Approve(req.Context(), id, analystOf(req), *in.ApprovedAmountMinor)
		if writeDecisionError(w, req, err) {
			return
		}
		writeJSON(w, http.StatusAccepted, decisionBodyOf(d))
	}
}

func rejectClaim(svc Decider) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := claimIDOf(w, req)
		if !ok {
			return
		}
		var in rejectionRequest
		if !decodeStrict(w, req, &in) {
			return
		}
		if in.Reason == nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "reason is required")
			return
		}
		d, err := svc.Reject(req.Context(), id, analystOf(req), *in.Reason)
		if writeDecisionError(w, req, err) {
			return
		}
		writeJSON(w, http.StatusOK, decisionBodyOf(d))
	}
}

func retryPayout(svc Decider) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := claimIDOf(w, req)
		if !ok {
			return
		}
		var in payoutRetryRequest
		if !decodeStrict(w, req, &in) {
			return
		}
		var destination *domain.PayoutAccount
		if dst := in.Destination; dst != nil {
			if !bankCodePattern.MatchString(dst.BankCode) || !accountNumberPattern.MatchString(dst.AccountNumber) ||
				(dst.AccountType != "savings" && dst.AccountType != "checking") || len([]rune(dst.BankName)) > maxBankNameLength {
				writeError(w, http.StatusBadRequest, "invalid_request",
					"the destination needs a bank_code of 3 to 6 digits, an account_type savings or checking and an account_number of 6 to 24 digits and dashes")
				return
			}
			destination = &domain.PayoutAccount{BankCode: dst.BankCode, BankName: strings.TrimSpace(dst.BankName),
				AccountType: dst.AccountType, AccountNumber: dst.AccountNumber}
		}
		d, err := svc.RetryPayout(req.Context(), id, analystOf(req), destination)
		if writeDecisionError(w, req, err) {
			return
		}
		writeJSON(w, http.StatusAccepted, decisionBodyOf(d))
	}
}

// claimIDOf reads the claim id of the path; an id that cannot exist is a 404.
func claimIDOf(w http.ResponseWriter, req *http.Request) (string, bool) {
	id := chi.URLParam(req, "claim_id")
	if !claimIDPattern.MatchString(id) {
		writeError(w, http.StatusNotFound, "claim_not_found", "there is no claim with that id")
		return "", false
	}
	return id, true
}

// writeDecisionError answers the error of a decision, and tells whether there
// was one.
func writeDecisionError(w http.ResponseWriter, req *http.Request, err error) bool {
	type answer struct {
		status        int
		code, message string
	}
	answers := []struct {
		err error
		answer
	}{
		{ports.ErrClaimNotFound, answer{http.StatusNotFound, "claim_not_found", "there is no claim with that id"}},
		{ports.ErrNotDecidable, answer{http.StatusConflict, "claim_not_decidable", "the claim is not in evaluation: it was already decided"}},
		{ports.ErrPayoutNotRetryable, answer{http.StatusConflict, "payout_not_retryable", "the payout of the claim is not a payment exception"}},
		{domain.ErrAmountOutOfRange, answer{http.StatusUnprocessableEntity, "amount_out_of_range", "the approved amount must be positive"}},
		{domain.ErrAmountExceedsInsuredValue, answer{http.StatusUnprocessableEntity, "amount_exceeds_insured_value", "the approved amount exceeds the insured value of the coverage"}},
		{domain.ErrNothingToPay, answer{http.StatusUnprocessableEntity, "nothing_to_pay", "after the deductible there is nothing to pay"}},
		{domain.ErrNoPayoutAccount, answer{http.StatusUnprocessableEntity, "no_payout_account", "the insured has no account to pay the indemnity to"}},
		{domain.ErrReasonRequired, answer{http.StatusUnprocessableEntity, "reason_required", "the reason of the rejection is required"}},
		{domain.ErrReasonTooLong, answer{http.StatusUnprocessableEntity, "reason_too_long", "the reason of the rejection is too long"}},
		{domain.ErrNoContact, answer{http.StatusUnprocessableEntity, "no_contact", "the insured has no e-mail to notify the rejection to"}},
		{decision.ErrNotCovered, answer{http.StatusUnprocessableEntity, "not_covered", "the policy does not cover the incident of the claim"}},
		{decision.ErrPolicyUnavailable, answer{http.StatusServiceUnavailable, "policy_unavailable", "the terms of the policy could not be read; retry"}},
	}
	if err == nil {
		return false
	}
	for _, a := range answers {
		if errors.Is(err, a.err) {
			writeError(w, a.status, a.code, a.message)
			return true
		}
	}
	slog.ErrorContext(req.Context(), "could not decide the claim", "error", err)
	writeError(w, http.StatusServiceUnavailable, "claims_unavailable", "the decision could not be recorded; retry")
	return true
}

type approvalBody struct {
	ApprovedAmountMinor   int64  `json:"approved_amount_minor"`
	InsuredValueMinor     int64  `json:"insured_value_minor"`
	DeductibleBasisPoints int    `json:"deductible_basis_points"`
	DeductibleMinor       int64  `json:"deductible_minor"`
	PayoutAmountMinor     int64  `json:"payout_amount_minor"`
	Currency              string `json:"currency"`
}

type payoutBody struct {
	PaymentReference     string            `json:"payment_reference"`
	Status               string            `json:"status"`
	Destination          payoutAccountBody `json:"destination"`
	AutomaticAttempts    int               `json:"automatic_attempts"`
	MaxAutomaticAttempts int               `json:"max_automatic_attempts"`
	Failure              *string           `json:"failure"`
	GatewayCode          *string           `json:"gateway_code"`
	LastFailureAt        *time.Time        `json:"last_failure_at"`
	GatewayReference     *string           `json:"gateway_reference"`
	PaidAt               *time.Time        `json:"paid_at"`
	ManualRetries        int               `json:"manual_retries"`
	LastRetriedBy        *string           `json:"last_retried_by"`
	LastRetriedAt        *time.Time        `json:"last_retried_at"`
}

type rejectionBody struct {
	Reason     string     `json:"reason"`
	NotifiedAt *time.Time `json:"notified_at"`
}

type decisionBody struct {
	ClaimID      string         `json:"claim_id"`
	FilingNumber string         `json:"filing_number"`
	Status       string         `json:"status"`
	Outcome      string         `json:"outcome"`
	DecidedBy    string         `json:"decided_by"`
	DecidedAt    time.Time      `json:"decided_at"`
	Approval     *approvalBody  `json:"approval"`
	Payout       *payoutBody    `json:"payout"`
	Rejection    *rejectionBody `json:"rejection"`
}

// payoutStatuses name the states of the payout for the portal.
var payoutStatuses = map[idempotency.Status]string{
	idempotency.StatusPending: "pending", idempotency.StatusInProgress: "in_progress",
	idempotency.StatusConfirmed: "paid", idempotency.StatusFailed: "failed",
}

func optionalString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func optionalTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	t = t.UTC()
	return &t
}

func decisionBodyOf(d domain.Decision) decisionBody {
	out := decisionBody{ClaimID: d.ClaimID, FilingNumber: d.FilingNumber, Status: string(d.StatusOf()),
		Outcome: string(d.Outcome), DecidedBy: d.DecidedBy, DecidedAt: d.DecidedAt.UTC()}
	if p := d.Payout; p != nil {
		out.Approval = &approvalBody{ApprovedAmountMinor: d.ApprovedAmountMinor, InsuredValueMinor: d.InsuredValueMinor,
			DeductibleBasisPoints: d.DeductibleBasisPoints, DeductibleMinor: d.DeductibleMinor(),
			PayoutAmountMinor: p.AmountMinor, Currency: d.Currency}
		a := p.Destination
		out.Payout = &payoutBody{
			PaymentReference: p.Key, Status: payoutStatuses[p.Status],
			Destination: payoutAccountBody{BankCode: a.BankCode, BankName: a.BankName, AccountType: a.AccountType,
				MaskedNumber: a.MaskedNumber()},
			AutomaticAttempts: p.Attempts, MaxAutomaticAttempts: domain.MaxAutomaticPayoutAttempts,
			Failure: optionalString(string(p.Failure)), GatewayCode: optionalString(p.GatewayCode),
			LastFailureAt: optionalTime(p.LastFailureAt), GatewayReference: optionalString(p.Reference),
			PaidAt: optionalTime(p.ConfirmedAt), ManualRetries: p.ManualRetries,
			LastRetriedBy: optionalString(p.LastRetriedBy), LastRetriedAt: optionalTime(p.LastRetriedAt),
		}
	}
	if d.Outcome == domain.OutcomeRejected {
		out.Rejection = &rejectionBody{Reason: d.Reason}
		if d.Notice != nil {
			out.Rejection.NotifiedAt = optionalTime(d.Notice.SentAt)
		}
	}
	return out
}
