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
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/orchestrator"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// Reasons a quote cannot be formalized.
const blockedQuoteExpired = "quote_expired"

// AssistedQuoteService is the use case behind the advisor quote routes
// (HU-W1.1-01).
type AssistedQuoteService interface {
	Create(ctx context.Context, advisorID string, req domain.QuoteRequest) (domain.Quote, error)
	Get(ctx context.Context, quoteID string) (domain.Quote, error)
	CustomerByDocument(ctx context.Context, documentID string) (domain.Customer, error)
	Now() time.Time
}

// WithAssistedQuotes mounts the advisor quote routes (HU-W1.1-01). They are
// human routes: the Cognito JWT is verified with pkg/security and only an
// advisor quotes, reads a quote or looks a customer up.
func WithAssistedQuotes(svc AssistedQuoteService, verifier security.Verifier) Option {
	return func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(noStore, security.Authenticate(verifier), security.RequireRole(security.RoleAdvisor))
			r.Post("/v1/assisted-quotes", createAssistedQuote(svc))
			r.Get("/v1/assisted-quotes/{quote_id}", getAssistedQuote(svc))
			r.Get("/v1/customers", getCustomerByDocument(svc))
		})
	}
}

type createQuoteRequest struct {
	DocumentID    string  `json:"document_id"`
	FullName      string  `json:"full_name"`
	Email         string  `json:"email"`
	BirthDate     string  `json:"birth_date"`
	InsuredAmount float64 `json:"insured_amount"`
	TermMonths    int     `json:"term_months"`
	ConsentActive *bool   `json:"consent_active"`
}

type customerBody struct {
	DocumentID string `json:"document_id"`
	FullName   string `json:"full_name"`
	Email      string `json:"email"`
}

type coverageBody struct {
	Code          string  `json:"code"`
	InsuredAmount float64 `json:"insured_amount"`
}

type quoteResponse struct {
	QuoteID       string         `json:"quote_id"`
	CustomerID    string         `json:"customer_id"`
	Customer      customerBody   `json:"customer"`
	InsuredAmount float64        `json:"insured_amount"`
	TermMonths    int            `json:"term_months"`
	Premium       float64        `json:"premium"`
	Currency      string         `json:"currency"`
	Coverages     []coverageBody `json:"coverages"`
	RatingVersion string         `json:"rating_version"`

	Mode              string      `json:"mode"`
	Source            string      `json:"source"`
	Profile           profileBody `json:"profile"`
	ProfileDate       *time.Time  `json:"profile_date"`
	UnavailableSource *string     `json:"unavailable_source"`
	Notice            *string     `json:"notice"`

	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	// Status is valid until expires_at and expired afterwards.
	Status string `json:"status"`
	// CanFormalize is false once the quote expired; BlockedReason says why.
	CanFormalize  bool    `json:"can_formalize"`
	BlockedReason *string `json:"blocked_reason"`
}

type fieldErrorBody struct {
	Field string `json:"field"`
	Code  string `json:"code"`
}

type validationErrorResponse struct {
	Error   string           `json:"error"`
	Message string           `json:"message"`
	Fields  []fieldErrorBody `json:"fields"`
}

type customerResponse struct {
	CustomerID string `json:"customer_id"`
	DocumentID string `json:"document_id"`
	FullName   string `json:"full_name"`
	Email      string `json:"email"`
	BirthDate  string `json:"birth_date"`
}

func createAssistedQuote(svc AssistedQuoteService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var in createQuoteRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, maxBodyBytes)).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "the request body is not valid JSON")
			return
		}
		claims, ok := security.ClaimsFromContext(req.Context())
		if !ok {
			security.WriteUnauthorized(w)
			return
		}

		q, err := svc.Create(req.Context(), claims.Subject, domain.QuoteRequest{
			DocumentID: in.DocumentID, FullName: in.FullName, Email: in.Email, BirthDate: in.BirthDate,
			InsuredAmount: in.InsuredAmount, TermMonths: in.TermMonths, ConsentActive: in.ConsentActive,
		})
		var invalid *domain.ValidationError
		switch {
		case err == nil:
			w.Header().Set("Location", "/v1/assisted-quotes/"+url.PathEscape(q.ID))
			writeJSON(w, http.StatusCreated, toQuoteResponse(q, svc.Now()))
		case errors.As(err, &invalid):
			writeValidationError(w, invalid)
		case errors.Is(err, orchestrator.ErrConsentRequired):
			writeError(w, http.StatusUnprocessableEntity, "consent_required", "an active consent is required to profile the customer")
		default:
			slog.ErrorContext(req.Context(), "could not generate the assisted quote", "error", err)
			writeError(w, http.StatusServiceUnavailable, "quote_unavailable", "the quote could not be generated; retry later")
		}
	}
}

func getAssistedQuote(svc AssistedQuoteService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := pathID(req, "quote_id")
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid_request", "quote_id (max 128 chars) is required")
			return
		}
		q, err := svc.Get(req.Context(), id)
		switch {
		case err == nil:
			writeJSON(w, http.StatusOK, toQuoteResponse(q, svc.Now()))
		case errors.Is(err, ports.ErrQuoteNotFound):
			writeError(w, http.StatusNotFound, "quote_not_found", "the quote does not exist")
		default:
			slog.ErrorContext(req.Context(), "could not read the assisted quote", "error", err)
			writeError(w, http.StatusServiceUnavailable, "quote_unavailable", "the quote could not be read; retry later")
		}
	}
}

func getCustomerByDocument(svc AssistedQuoteService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		doc := req.URL.Query().Get("document_id")
		if doc == "" {
			writeError(w, http.StatusBadRequest, "invalid_request", "document_id is required")
			return
		}
		c, err := svc.CustomerByDocument(req.Context(), doc)
		var invalid *domain.ValidationError
		switch {
		case err == nil:
			writeJSON(w, http.StatusOK, customerResponse{
				CustomerID: c.ID, DocumentID: c.DocumentID, FullName: c.FullName, Email: c.Email,
				BirthDate: c.BirthDate.Format("2006-01-02"),
			})
		case errors.As(err, &invalid):
			writeValidationError(w, invalid)
		case errors.Is(err, ports.ErrCustomerNotFound):
			writeError(w, http.StatusNotFound, "customer_not_found", "no customer has that document")
		default:
			slog.ErrorContext(req.Context(), "could not look the customer up", "error", err)
			writeError(w, http.StatusServiceUnavailable, "customer_unavailable", "the customer could not be read; retry later")
		}
	}
}

func writeValidationError(w http.ResponseWriter, e *domain.ValidationError) {
	out := validationErrorResponse{
		Error: "validation_failed", Message: "one or more fields are missing or invalid",
		Fields: make([]fieldErrorBody, 0, len(e.Fields)),
	}
	for _, f := range e.Fields {
		out.Fields = append(out.Fields, fieldErrorBody{Field: f.Field, Code: f.Code})
	}
	writeJSON(w, http.StatusUnprocessableEntity, out)
}

func toQuoteResponse(q domain.Quote, now time.Time) quoteResponse {
	out := quoteResponse{
		QuoteID: q.ID, CustomerID: q.CustomerID,
		Customer:      customerBody{DocumentID: q.Applicant.DocumentID, FullName: q.Applicant.FullName, Email: q.Applicant.Email},
		InsuredAmount: q.InsuredAmount, TermMonths: q.TermMonths, Premium: q.Premium, Currency: q.Currency,
		Coverages: make([]coverageBody, 0, len(q.Coverages)), RatingVersion: q.RatingVersion,
		Mode: string(q.Mode), Source: string(q.ProfileSource),
		Profile:   profileBody{Score: q.ProfileScore, Category: q.ProfileCategory},
		CreatedAt: q.CreatedAt.UTC(), ExpiresAt: q.ExpiresAt.UTC(),
		Status: string(q.Status(now)), CanFormalize: q.CanFormalize(now),
	}
	for _, c := range q.Coverages {
		out.Coverages = append(out.Coverages, coverageBody{Code: c.Code, InsuredAmount: c.InsuredAmount})
	}
	if !q.ProfileDate.IsZero() {
		d := q.ProfileDate.UTC()
		out.ProfileDate = &d
	}
	if q.Mode == domain.ModeProvisional {
		unavailable := q.UnavailableSource
		out.UnavailableSource = &unavailable
		notice := NoticeLastProfileInUse
		if q.ProfileSource == domain.SourceBaseActuarial {
			notice = NoticePersonalizationUnavailable
		}
		out.Notice = &notice
	}
	if !out.CanFormalize {
		reason := blockedQuoteExpired
		out.BlockedReason = &reason
	}
	return out
}
