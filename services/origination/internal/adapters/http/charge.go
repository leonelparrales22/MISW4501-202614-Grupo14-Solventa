package httpadapter

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/billing"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// ChargeService is the use case behind the premium charge routes (HA-D02).
type ChargeService interface {
	Get(ctx context.Context, acceptanceID string) (domain.Charge, error)
	ListExceptions(ctx context.Context, limit int) ([]domain.Charge, error)
	Resume(ctx context.Context, acceptanceID, note string) (domain.Charge, error)
	AttachPaymentMethod(ctx context.Context, acceptanceID string, m domain.PaymentMethod) (domain.Charge, error)
}

// WithCharges mounts the premium charge routes (HA-D02). They are human
// routes: the Cognito JWT is verified with pkg/security. The insured who owns
// the acceptance, an advisor or an operations analyst read the charge (screen
// E4b); only operations list the issuance exceptions and resume them (the
// monitoring screen O1/O1b is HU-W1.3-04). Register it after
// WithObservability.
func WithCharges(svc ChargeService, verifier security.Verifier) Option {
	return func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(noStore, security.Authenticate(verifier))
			r.With(security.RequireRole(security.RoleInsured, security.RoleAdvisor, security.RoleOperations)).
				Get("/v1/acceptances/{acceptance_id}/charge", getCharge(svc))
			operations := security.RequireRole(security.RoleOperations)
			r.With(operations).Get("/v1/issuance-exceptions", listIssuanceExceptions(svc))
			r.With(operations).Post("/v1/acceptances/{acceptance_id}/charge/resume", resumeCharge(svc))
			// Screen E4: only the insured pays, so no advisor or analyst
			// role here (HU-W1.3-02).
			r.With(security.RequireRole(security.RoleInsured)).
				Post("/v1/acceptances/{acceptance_id}/charge/payment-method", attachPaymentMethod(svc))
		})
	}
}

type paymentMethodBody struct {
	Method       string    `json:"method"`
	Brand        *string   `json:"brand"`
	Last4        *string   `json:"last4"`
	Installments int       `json:"installments"`
	BankCode     *string   `json:"bank_code"`
	CapturedAt   time.Time `json:"captured_at"`
}

type retryWindowBody struct {
	StartedAt time.Time  `json:"started_at"`
	EndsAt    *time.Time `json:"ends_at"`
}

type chargeResponse struct {
	AcceptanceID          string          `json:"acceptance_id"`
	OfferID               string          `json:"offer_id"`
	CustomerID            string          `json:"customer_id"`
	AcceptedAt            time.Time       `json:"accepted_at"`
	Status                string          `json:"status"`
	IdempotencyKey        string          `json:"idempotency_key"`
	Amount                float64         `json:"amount"`
	Currency              string          `json:"currency"`
	Attempts              int             `json:"attempts"`
	LastAttemptAt         *time.Time      `json:"last_attempt_at"`
	LastFailureAt         *time.Time      `json:"last_failure_at"`
	NextAttemptAt         *time.Time      `json:"next_attempt_at"`
	RetryWindow           retryWindowBody `json:"retry_window"`
	FailureReason         *string         `json:"failure_reason"`
	ConfirmedAt           *time.Time      `json:"confirmed_at"`
	PolicyID              *string         `json:"policy_id"`
	CoverageActivatedAt   *time.Time      `json:"coverage_activated_at"`
	ActivationPublishedAt *time.Time      `json:"activation_published_at"`
	ResumeCount           int             `json:"resume_count"`
	// PaymentMethod is null while there is none; PaymentAttempt counts how
	// many the customer handed over, which is not Attempts (HU-W1.3-02).
	PaymentMethod  *paymentMethodBody `json:"payment_method"`
	PaymentAttempt int                `json:"payment_attempt"`
	UpdatedAt      time.Time          `json:"updated_at"`
}

type issuanceExceptionBody struct {
	AcceptanceID   string     `json:"acceptance_id"`
	OfferID        string     `json:"offer_id"`
	CustomerID     string     `json:"customer_id"`
	Step           string     `json:"step"`
	Reason         string     `json:"reason"`
	Attempts       int        `json:"attempts"`
	IdempotencyKey string     `json:"idempotency_key"`
	Amount         float64    `json:"amount"`
	Currency       string     `json:"currency"`
	AcceptedAt     time.Time  `json:"accepted_at"`
	FailedAt       time.Time  `json:"failed_at"`
	ResumeCount    int        `json:"resume_count"`
	LastResumedAt  *time.Time `json:"last_resumed_at"`
	LastResumedBy  *string    `json:"last_resumed_by"`
}

type issuanceExceptionsResponse struct {
	Items []issuanceExceptionBody `json:"items"`
}

type resumeRequest struct {
	Note string `json:"note"`
}

// paymentMethodRequest is the body of screen E4. The card number is never
// here: the browser tokenises it against the gateway and only the token
// travels, which keeps the platform out of PCI-DSS scope.
type paymentMethodRequest struct {
	Method       string `json:"method"`
	PaymentToken string `json:"payment_token"`
	Brand        string `json:"brand"`
	Last4        string `json:"last4"`
	Installments *int   `json:"installments"`
	BankCode     string `json:"bank_code"`
}

func attachPaymentMethod(svc ChargeService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := pathID(req, "acceptance_id")
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid_request", "acceptance_id (max 128 chars) is required")
			return
		}
		var in paymentMethodRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, maxBodyBytes)).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "the request body is not valid JSON")
			return
		}
		installments := 1
		if in.Installments != nil {
			installments = *in.Installments
		}
		c, err := svc.AttachPaymentMethod(req.Context(), id, domain.PaymentMethod{
			Kind: domain.PaymentMethodKind(in.Method), Token: in.PaymentToken,
			Brand: in.Brand, Last4: in.Last4, Installments: installments, BankCode: in.BankCode,
		})
		respondCharge(w, req, http.StatusAccepted, c, err)
	}
}

func getCharge(svc ChargeService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := pathID(req, "acceptance_id")
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid_request", "acceptance_id (max 128 chars) is required")
			return
		}
		c, err := svc.Get(req.Context(), id)
		respondCharge(w, req, http.StatusOK, c, err)
	}
}

func resumeCharge(svc ChargeService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := pathID(req, "acceptance_id")
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid_request", "acceptance_id (max 128 chars) is required")
			return
		}
		var in resumeRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, maxBodyBytes)).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "the request body is not valid JSON")
			return
		}
		c, err := svc.Resume(req.Context(), id, in.Note)
		respondCharge(w, req, http.StatusAccepted, c, err)
	}
}

func respondCharge(w http.ResponseWriter, req *http.Request, status int, c domain.Charge, err error) {
	switch {
	case err == nil:
		writeJSON(w, status, toChargeResponse(c))
	case errors.Is(err, billing.ErrInvalidNote):
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
	case errors.Is(err, ports.ErrChargeNotFound):
		writeError(w, http.StatusNotFound, "charge_not_found",
			"the acceptance has no premium charge (underwriting has not approved it, it is in review or it does not exist)")
	case errors.Is(err, billing.ErrInvalidPaymentMethod):
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
	case errors.Is(err, ports.ErrChargeNotResumable):
		writeError(w, http.StatusConflict, "charge_not_resumable", "only a failed charge can be resumed")
	case errors.Is(err, ports.ErrChargeNotPayable):
		writeError(w, http.StatusConflict, "charge_not_payable",
			"the charge does not take a payment method: it is being charged, it is confirmed, or its result is ambiguous and only operations can resume it")
	case isAuthError(err):
		writeAuthError(w, err)
	default:
		slog.ErrorContext(req.Context(), "could not read or resume the premium charge", "error", err)
		writeError(w, http.StatusServiceUnavailable, "charge_unavailable", "the premium charge could not be read; retry later")
	}
}

func listIssuanceExceptions(svc ChargeService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		limit := billing.DefaultExceptionsLimit
		if raw := req.URL.Query().Get("limit"); raw != "" {
			v, err := strconv.Atoi(raw)
			if err != nil || v < 1 || v > billing.MaxExceptionsLimit {
				writeError(w, http.StatusBadRequest, "invalid_request", "limit must be an integer between 1 and 200")
				return
			}
			limit = v
		}
		items, err := svc.ListExceptions(req.Context(), limit)
		switch {
		case err == nil:
		case errors.Is(err, billing.ErrInvalidLimit):
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		case isAuthError(err):
			writeAuthError(w, err)
			return
		default:
			slog.ErrorContext(req.Context(), "could not list the issuance exceptions", "error", err)
			writeError(w, http.StatusServiceUnavailable, "charge_unavailable", "the issuance exceptions could not be read; retry later")
			return
		}
		out := issuanceExceptionsResponse{Items: make([]issuanceExceptionBody, 0, len(items))}
		for _, c := range items {
			out.Items = append(out.Items, toIssuanceException(c))
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func optionalTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

func optionalString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func toChargeResponse(c domain.Charge) chargeResponse {
	return chargeResponse{
		AcceptanceID:   c.AcceptanceID,
		OfferID:        c.OfferID,
		CustomerID:     c.CustomerID,
		AcceptedAt:     c.AcceptedAt.UTC(),
		Status:         string(c.Status),
		IdempotencyKey: c.IdempotencyKey,
		Amount:         domain.FromMinorUnits(c.AmountMinor),
		Currency:       c.Currency,
		Attempts:       c.Attempts,
		LastAttemptAt:  optionalTime(c.LastAttemptAt),
		LastFailureAt:  optionalTime(c.LastFailureAt),
		NextAttemptAt:  optionalTime(c.NextAttemptAt),
		RetryWindow: retryWindowBody{
			StartedAt: c.WindowStartedAt.UTC(),
			EndsAt:    optionalTime(c.WindowEndsAt),
		},
		FailureReason:         optionalString(string(c.FailureReason)),
		ConfirmedAt:           optionalTime(c.ConfirmedAt),
		PolicyID:              optionalString(c.PolicyID),
		CoverageActivatedAt:   optionalTime(c.ActivatedAt),
		ActivationPublishedAt: optionalTime(c.ActivationPublishedAt),
		ResumeCount:           c.ResumeCount,
		PaymentMethod:         toPaymentMethodBody(c.PaymentMethod),
		PaymentAttempt:        c.PaymentAttempt,
		UpdatedAt:             c.UpdatedAt.UTC(),
	}
}

// toPaymentMethodBody returns nil while the customer has not handed over a
// payment method. It is the field a client reads to tell "waiting for the
// card" (E4) from "the gateway is failing" (E4b): both are status pending.
func toPaymentMethodBody(m domain.PaymentMethod) *paymentMethodBody {
	if !m.Captured() {
		return nil
	}
	return &paymentMethodBody{
		Method:       string(m.Kind),
		Brand:        optionalString(m.Brand),
		Last4:        optionalString(m.Last4),
		Installments: m.Installments,
		BankCode:     optionalString(m.BankCode),
		CapturedAt:   m.CapturedAt.UTC(),
	}
}

func toIssuanceException(c domain.Charge) issuanceExceptionBody {
	return issuanceExceptionBody{
		AcceptanceID:   c.AcceptanceID,
		OfferID:        c.OfferID,
		CustomerID:     c.CustomerID,
		Step:           domain.IssuanceStepPremiumCharge,
		Reason:         string(c.FailureReason),
		Attempts:       c.Attempts,
		IdempotencyKey: c.IdempotencyKey,
		Amount:         domain.FromMinorUnits(c.AmountMinor),
		Currency:       c.Currency,
		AcceptedAt:     c.AcceptedAt.UTC(),
		FailedAt:       c.LastFailureAt.UTC(),
		ResumeCount:    c.ResumeCount,
		LastResumedAt:  optionalTime(c.LastResumedAt),
		LastResumedBy:  optionalString(c.LastResumedBy),
	}
}
