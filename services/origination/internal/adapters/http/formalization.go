package httpadapter

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/formalization"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/issuance"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// FormalizationService is the use case behind the purchase formalization
// routes (HU-W1.1-03).
type FormalizationService interface {
	RequestConsent(ctx context.Context, quoteID string) (formalization.ConsentRequest, error)
	Formalize(ctx context.Context, quoteID string, in formalization.Input) (formalization.Result, error)
	Get(ctx context.Context, quoteID string) (formalization.Result, error)
}

// WithFormalizations mounts the routes with which an advisor formalizes the
// purchase of a quote on behalf of the customer (HU-W1.1-03). They are human
// routes: the Cognito JWT is verified with pkg/security and only an advisor
// uses them.
func WithFormalizations(svc FormalizationService, verifier security.Verifier) Option {
	return func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(noStore, security.Authenticate(verifier), security.RequireRole(security.RoleAdvisor))
			r.Post("/v1/assisted-quotes/{quote_id}/consent", requestConsent(svc))
			r.Post("/v1/assisted-quotes/{quote_id}/formalization", formalizePurchase(svc))
			r.Get("/v1/assisted-quotes/{quote_id}/formalization", getFormalization(svc))
		})
	}
}

var confirmationCodePattern = regexp.MustCompile(`^[0-9]{6}$`)

type consentRequestBody struct {
	QuoteID   string    `json:"quote_id"`
	SentTo    string    `json:"sent_to"`
	ExpiresAt time.Time `json:"expires_at"`
}

type acceptanceRequest struct {
	Method     string `json:"method"`
	AcceptedAt string `json:"accepted_at"`
	Reference  string `json:"reference"`
}

type formalizeRequest struct {
	Acceptance acceptanceRequest `json:"acceptance"`
	// ConfirmationCode is needed only when the method is confirmation_code.
	ConfirmationCode string `json:"confirmation_code"`
}

type formalizationResponse struct {
	FormalizationID string `json:"formalization_id"`
	QuoteID         string `json:"quote_id"`
	OfferID         string `json:"offer_id"`
	AcceptanceID    string `json:"acceptance_id"`
	CustomerID      string `json:"customer_id"`
	AdvisorID       string `json:"advisor_id"`
	AdvisorEmail    string `json:"advisor_email"`
	ConsentMethod   string `json:"consent_method"`
	// AcceptedAt and ConsentReference say when and how the customer accepted (CA-5).
	AcceptedAt       time.Time `json:"accepted_at"`
	ConsentReference string    `json:"consent_reference"`
	FormalizedAt     time.Time `json:"formalized_at"`
	// Status is the status of the acceptance that started the issuance.
	Status string `json:"status"`
}

type quoteExpiredResponse struct {
	Error           string    `json:"error"`
	Message         string    `json:"message"`
	ExpiresAt       time.Time `json:"expires_at"`
	SuggestNewQuote bool      `json:"suggest_new_quote"`
}

func requestConsent(svc FormalizationService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := pathID(req, "quote_id")
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid_request", "quote_id (max 128 chars) is required")
			return
		}
		res, err := svc.RequestConsent(req.Context(), id)
		if err != nil {
			writeFormalizationError(w, req, "could not send the confirmation code", err)
			return
		}
		writeJSON(w, http.StatusOK, consentRequestBody{QuoteID: res.QuoteID, SentTo: res.SentTo, ExpiresAt: res.ExpiresAt.UTC()})
	}
}

func formalizePurchase(svc FormalizationService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := pathID(req, "quote_id")
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid_request", "quote_id (max 128 chars) is required")
			return
		}
		var in formalizeRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, maxBodyBytes)).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "the request body is not valid JSON")
			return
		}
		input, invalid := toFormalizeInput(in)
		if invalid != nil {
			writeValidationError(w, invalid)
			return
		}
		res, err := svc.Formalize(req.Context(), id, input)
		if err != nil {
			writeFormalizationError(w, req, "could not formalize the purchase", err)
			return
		}
		status := http.StatusOK
		if res.Created {
			w.Header().Set("Location", "/v1/assisted-quotes/"+id+"/formalization")
			status = http.StatusCreated
		}
		writeJSON(w, status, toFormalizationResponse(res))
	}
}

// toFormalizeInput reads the request: the acceptance and, for the confirmation
// code method, the code. Every field that cannot be read is reported at once.
func toFormalizeInput(in formalizeRequest) (formalization.Input, *domain.ValidationError) {
	var fields []domain.FieldError
	bad := func(field, code string) { fields = append(fields, domain.FieldError{Field: field, Code: code}) }

	var acceptedAt time.Time
	// An empty date is left for the domain to report as required.
	if in.Acceptance.AcceptedAt != "" {
		t, err := time.Parse(time.RFC3339, in.Acceptance.AcceptedAt)
		if err != nil {
			bad("acceptance.accepted_at", domain.CodeInvalidFormat)
		}
		acceptedAt = t.UTC()
	}
	method := domain.ConsentMethod(in.Acceptance.Method)
	if method == domain.ConsentConfirmationCode && !confirmationCodePattern.MatchString(in.ConfirmationCode) {
		bad("confirmation_code", domain.CodeInvalidFormat)
	}
	if len(fields) > 0 {
		return formalization.Input{}, &domain.ValidationError{Fields: fields}
	}
	return formalization.Input{
		Acceptance: domain.ConsentAcceptance{Method: method, AcceptedAt: acceptedAt, Reference: in.Acceptance.Reference},
		Code:       in.ConfirmationCode,
	}, nil
}

func getFormalization(svc FormalizationService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := pathID(req, "quote_id")
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid_request", "quote_id (max 128 chars) is required")
			return
		}
		res, err := svc.Get(req.Context(), id)
		if err != nil {
			writeFormalizationError(w, req, "could not read the formalization", err)
			return
		}
		writeJSON(w, http.StatusOK, toFormalizationResponse(res))
	}
}

func toFormalizationResponse(r formalization.Result) formalizationResponse {
	f := r.Formalization
	return formalizationResponse{
		FormalizationID: f.ID, QuoteID: f.QuoteID, OfferID: f.OfferID, AcceptanceID: r.Acceptance.ID,
		CustomerID: f.CustomerID, AdvisorID: f.AdvisorID, AdvisorEmail: f.AdvisorEmail,
		ConsentMethod: string(f.Consent.Method), AcceptedAt: f.Consent.AcceptedAt.UTC(), ConsentReference: f.Consent.Reference,
		FormalizedAt: f.FormalizedAt.UTC(), Status: string(r.Acceptance.Status),
	}
}

// writeFormalizationError answers the errors of the three routes.
func writeFormalizationError(w http.ResponseWriter, req *http.Request, what string, err error) {
	var expired *formalization.QuoteExpiredError
	var offerExpired *issuance.OfferExpiredError
	var invalid *domain.ValidationError
	switch {
	case isAuthError(err):
		writeAuthError(w, err)
	case errors.As(err, &invalid):
		writeValidationError(w, invalid)
	case errors.Is(err, ports.ErrQuoteNotFound):
		writeError(w, http.StatusNotFound, "quote_not_found", "the quote does not exist")
	case errors.Is(err, ports.ErrFormalizationNotFound):
		writeError(w, http.StatusNotFound, "formalization_not_found", "the quote was not formalized")
	case errors.As(err, &expired):
		writeQuoteExpired(w, expired.ExpiresAt)
	case errors.As(err, &offerExpired):
		writeQuoteExpired(w, offerExpired.ExpiresAt)
	case errors.Is(err, formalization.ErrAlreadyFormalized):
		writeError(w, http.StatusConflict, "already_formalized", "another advisor already formalized the purchase of this quote")
	case errors.Is(err, formalization.ErrConsentCodeRequired):
		writeError(w, http.StatusUnprocessableEntity, "consent_code_required", "ask for the confirmation code before formalizing")
	case errors.Is(err, formalization.ErrConsentCodeInvalid):
		writeError(w, http.StatusUnprocessableEntity, "invalid_confirmation_code", "the confirmation code is not the one sent to the customer")
	case errors.Is(err, formalization.ErrConsentCodeExpired):
		writeError(w, http.StatusUnprocessableEntity, "confirmation_code_expired", "the confirmation code expired; ask for a new one")
	case errors.Is(err, formalization.ErrConsentCodeLocked):
		writeError(w, http.StatusTooManyRequests, "too_many_attempts", "too many confirmation code attempts for this quote")
	case errors.Is(err, formalization.ErrConsentCodeLimit):
		writeError(w, http.StatusTooManyRequests, "too_many_codes", "too many confirmation codes were sent for this quote")
	case errors.Is(err, formalization.ErrCodeDeliveryUnavailable):
		writeError(w, http.StatusServiceUnavailable, "code_delivery_unavailable", "the confirmation code cannot be delivered; retry later")
	default:
		slog.ErrorContext(req.Context(), what, "error", err)
		writeError(w, http.StatusServiceUnavailable, "formalization_unavailable", "the purchase could not be formalized; retry later")
	}
}

func writeQuoteExpired(w http.ResponseWriter, expiresAt time.Time) {
	writeJSON(w, http.StatusUnprocessableEntity, quoteExpiredResponse{
		Error: blockedQuoteExpired, Message: "the quote expired and cannot be formalized; generate a new one",
		ExpiresAt: expiresAt.UTC(), SuggestNewQuote: true,
	})
}
