package httpadapter

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/orchestrator"
)

// RegistrationService is the use case behind the sign-up route (HU-M1.1-01).
type RegistrationService interface {
	Register(ctx context.Context, r domain.RegistrationRequest) (domain.Customer, error)
}

// WithRegistrations mounts the sign-up route of the insured (HU-M1.1-01).
//
// It carries no session: the account does not exist yet, so the caller has no
// token. The user pool only accepts accounts created from the server, so this
// service decides who gets one. The gateway throttles the route.
func WithRegistrations(svc RegistrationService) Option {
	return func(r chi.Router) {
		r.With(noStore).Post("/v1/registrations", register(svc))
	}
}

// registrationRequest is the body of the sign-up. The password travels to the
// user pool and is not stored.
type registrationRequest struct {
	DocumentID string `json:"document_id"`
	FullName   string `json:"full_name"`
	Email      string `json:"email"`
	BirthDate  string `json:"birth_date"`
	Password   string `json:"password"`
}

type registrationResponse struct {
	CustomerID string `json:"customer_id"`
	DocumentID string `json:"document_id"`
	FullName   string `json:"full_name"`
	Email      string `json:"email"`
	BirthDate  string `json:"birth_date"`
	// Subject of the account in the user pool: the same value the token
	// carries once the person signs in.
	Subject      string `json:"subject"`
	RegisteredAt string `json:"registered_at"`
}

func register(svc RegistrationService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var in registrationRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, maxBodyBytes)).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "the request body is not valid JSON")
			return
		}

		c, err := svc.Register(req.Context(), domain.RegistrationRequest{
			DocumentID: in.DocumentID, FullName: in.FullName, Email: in.Email,
			BirthDate: in.BirthDate, Password: in.Password,
		})

		var invalid *domain.ValidationError
		switch {
		case err == nil:
		case errors.As(err, &invalid):
			writeValidationError(w, invalid)
			return
		case errors.Is(err, orchestrator.ErrAlreadyRegistered):
			writeError(w, http.StatusConflict, "already_registered",
				"the document or the email already has an account")
			return
		default:
			// The reason never reaches the caller: the route is public and
			// the detail would describe the inside of the platform.
			slog.ErrorContext(req.Context(), "could not register the insured", "error", err)
			writeError(w, http.StatusServiceUnavailable, "registration_unavailable",
				"the registration could not be completed; retry later")
			return
		}

		writeJSON(w, http.StatusCreated, registrationResponse{
			CustomerID:   c.ID,
			DocumentID:   c.DocumentID,
			FullName:     c.FullName,
			Email:        c.Email,
			BirthDate:    c.BirthDate.Format("2006-01-02"),
			Subject:      c.CognitoSub,
			RegisteredAt: c.CreatedAt.UTC().Format(time.RFC3339Nano),
		})
	}
}
