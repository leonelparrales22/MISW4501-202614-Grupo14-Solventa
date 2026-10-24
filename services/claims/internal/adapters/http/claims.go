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
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// FilingService is the use case behind the filing route (HU-M2.2-01).
type FilingService interface {
	File(ctx context.Context, subject, key string, r domain.FilingRequest) (domain.Claim, bool, error)
}

// headerFilingKey identifies the filing. The mobile app sends the identifier
// of its draft, so a retry after a lost response finds the case that already
// exists instead of filing a second one.
const headerFilingKey = "Idempotency-Key"

var filingKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

// WithFilings mounts the filing route of the claim (HU-M2.2-01). It is a
// human route: the Cognito JWT is verified with pkg/security and only an
// insured files, always on their own behalf.
func WithFilings(svc FilingService, verifier security.Verifier) Option {
	return func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(noStore, security.Authenticate(verifier))
			r.With(security.RequireRole(security.RoleInsured)).Post("/v1/claims", fileClaim(svc))
		})
	}
}

// filingRequest is the body the guided assistant sends when the insured
// confirms the declaration.
type filingRequest struct {
	PolicyID            string            `json:"policy_id"`
	IncidentType        string            `json:"incident_type"`
	OccurredAt          string            `json:"occurred_at"`
	Place               string            `json:"place"`
	Description         string            `json:"description"`
	Details             map[string]string `json:"details"`
	DeclarationAccepted bool              `json:"declaration_accepted"`
}

type filingResponse struct {
	ClaimID      string `json:"claim_id"`
	FilingNumber string `json:"filing_number"`
	Status       string `json:"status"`
	PolicyID     string `json:"policy_id"`
	IncidentType string `json:"incident_type"`
	OccurredAt   string `json:"occurred_at"`
	FiledAt      string `json:"filed_at"`
}

func fileClaim(svc FilingService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		key := req.Header.Get(headerFilingKey)
		if !filingKeyPattern.MatchString(key) {
			writeError(w, http.StatusBadRequest, "invalid_request",
				"the Idempotency-Key header is required (up to 128 characters)")
			return
		}

		var in filingRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, maxBodyBytes)).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "the request body is not valid JSON")
			return
		}

		claims, _ := security.ClaimsFromContext(req.Context())
		c, created, err := svc.File(req.Context(), claims.Subject, key, domain.FilingRequest{
			PolicyID: in.PolicyID, IncidentType: in.IncidentType, OccurredAt: in.OccurredAt,
			Place: in.Place, Description: in.Description, Details: in.Details,
			DeclarationAccepted: in.DeclarationAccepted,
		})

		var invalid *domain.ValidationError
		switch {
		case err == nil:
		case errors.As(err, &invalid):
			writeValidationError(w, invalid)
			return
		case errors.Is(err, ports.ErrFilingKeyReused):
			writeError(w, http.StatusUnprocessableEntity, "idempotency_key_reused",
				"that key was used for another filing")
			return
		default:
			slog.ErrorContext(req.Context(), "could not file the claim", "error", err)
			writeError(w, http.StatusServiceUnavailable, "filing_unavailable",
				"the filing could not be completed; retry with the same key")
			return
		}

		status := http.StatusOK
		if created {
			status = http.StatusCreated
			slog.InfoContext(req.Context(), "claim filed",
				"claim_id", c.ID, "filing_number", c.FilingNumber)
		}
		writeJSON(w, status, filingResponse{
			ClaimID:      c.ID,
			FilingNumber: c.FilingNumber,
			Status:       string(c.Status),
			PolicyID:     c.PolicyID,
			IncidentType: string(c.IncidentType),
			OccurredAt:   c.OccurredAt.UTC().Format(time.RFC3339),
			FiledAt:      c.FiledAt.UTC().Format(time.RFC3339Nano),
		})
	}
}
