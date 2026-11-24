package httpadapter

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/assistance"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// AssistanceService is the use case behind the routes of the request for
// on-site assistance (HU-M2.3-01).
type AssistanceService interface {
	Request(ctx context.Context, requester assistance.Requester, key string,
		in domain.AssistanceRequestInput) (domain.AssistanceRequest, bool, error)
	Get(ctx context.Context, subject, id string) (domain.AssistanceRequest, error)
}

// assistanceKeyPattern is the Idempotency-Key of a request: the same text as
// the one of the filing, kept apart so neither route changes the other.
var assistanceKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

// assistanceIDPattern is a request id: a UUID version 4 in lower case.
var assistanceIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// WithAssistance mounts the request for on-site assistance (HU-M2.3-01). It
// is a human route: the Cognito JWT is verified with pkg/security and only an
// insured requests, always on their own behalf, and reads their own requests.
// Nothing is cached: the location of a person is sensitive (assumption S11).
func WithAssistance(svc AssistanceService, verifier security.Verifier) Option {
	return func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(noStore, security.Authenticate(verifier), security.RequireRole(security.RoleInsured))
			r.Post("/v1/claims/assistance-requests", requestAssistance(svc))
			r.Get("/v1/claims/assistance-requests/{request_id}", getAssistance(svc))
		})
	}
}

// assistanceRequestBody is what the phone sends. The pointers tell a field
// that was not sent from a zero: latitude 0 is a place.
type assistanceRequestBody struct {
	PolicyID string                     `json:"policy_id"`
	Location *assistanceLocationRequest `json:"location"`
	Place    *string                    `json:"place"`
}

type assistanceLocationRequest struct {
	Status            string   `json:"status"`
	Latitude          *float64 `json:"latitude"`
	Longitude         *float64 `json:"longitude"`
	AccuracyM         *float64 `json:"accuracy_m"`
	CapturedAt        *string  `json:"captured_at"`
	UnavailableReason *string  `json:"unavailable_reason"`
}

type assistanceResponse struct {
	RequestID     string                     `json:"request_id"`
	RequestNumber string                     `json:"request_number"`
	Status        string                     `json:"status"`
	Policy        assistancePolicyResponse   `json:"policy"`
	Location      assistanceLocationResponse `json:"location"`
	Place         *string                    `json:"place"`
	RequestedAt   string                     `json:"requested_at"`
}

type assistancePolicyResponse struct {
	PolicyID      string `json:"policy_id"`
	Line          string `json:"line"`
	Product       string `json:"product"`
	InsuredObject string `json:"insured_object"`
}

// assistanceLocationResponse carries every field; the ones that do not apply
// to the status are null.
type assistanceLocationResponse struct {
	Status            string   `json:"status"`
	Latitude          *float64 `json:"latitude"`
	Longitude         *float64 `json:"longitude"`
	AccuracyM         *float64 `json:"accuracy_m"`
	CapturedAt        *string  `json:"captured_at"`
	UnavailableReason *string  `json:"unavailable_reason"`
}

var assistanceNotEligibleMessages = map[string]string{
	assistance.ReasonPolicyNotFound:  "the policy is not one of the insured's",
	assistance.ReasonPolicyNotActive: "the policy is not active",
}

func requestAssistance(svc AssistanceService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		key := req.Header.Get(headerFilingKey)
		if !assistanceKeyPattern.MatchString(key) {
			writeError(w, http.StatusBadRequest, "invalid_request",
				"the Idempotency-Key header is required (up to 128 characters)")
			return
		}
		var body assistanceRequestBody
		if !decodeStrict(w, req, &body) {
			return
		}
		in := domain.AssistanceRequestInput{PolicyID: body.PolicyID, Place: body.Place}
		if l := body.Location; l != nil {
			in.Location = &domain.AssistanceLocationInput{Status: l.Status, Latitude: l.Latitude,
				Longitude: l.Longitude, AccuracyM: l.AccuracyM, CapturedAt: l.CapturedAt,
				UnavailableReason: l.UnavailableReason}
		}

		claims, _ := security.ClaimsFromContext(req.Context())
		r, created, err := svc.Request(req.Context(),
			assistance.Requester{Subject: claims.Subject, Name: claims.Name}, key, in)

		var (
			invalid     *domain.ValidationError
			notEligible *assistance.PolicyNotEligibleError
		)
		switch {
		case err == nil:
		case errors.As(err, &invalid):
			writeValidationError(w, invalid)
			return
		case errors.As(err, &notEligible):
			out := policyNotEligibleResponse{Error: "policy_not_eligible", Reason: notEligible.Reason,
				Message: assistanceNotEligibleMessages[notEligible.Reason]}
			if p := notEligible.Policy; p != nil {
				from, to := p.ValidFrom.UTC(), p.ValidTo.UTC()
				out.ValidFrom, out.ValidTo = &from, &to
			}
			writeJSON(w, http.StatusUnprocessableEntity, out)
			return
		case errors.Is(err, ports.ErrAssistanceKeyReused):
			writeError(w, http.StatusUnprocessableEntity, "idempotency_key_reused",
				"that key was used for another request for assistance")
			return
		case errors.Is(err, ports.ErrPolicyDirectoryUnavailable):
			// The error of the core names the URL, and the URL the subject:
			// it does not go to the log (assumption S11).
			slog.WarnContext(req.Context(), "the policy core did not answer a request for assistance")
			writeError(w, http.StatusServiceUnavailable, "policy_directory_unavailable",
				"the policies could not be read; nothing was recorded, retry with the same key")
			return
		default:
			slog.ErrorContext(req.Context(), "could not record the request for assistance", "error", err)
			writeError(w, http.StatusServiceUnavailable, "assistance_unavailable",
				"the request could not be recorded; retry with the same key")
			return
		}

		status := http.StatusOK
		if created {
			status = http.StatusCreated
			// Only what identifies the request: never the coordinates, the
			// place, the name nor the subject (assumption S11).
			slog.InfoContext(req.Context(), "request for assistance recorded",
				"request_id", r.ID, "request_number", r.Number, "location_status", string(r.Location.Status))
		}
		writeJSON(w, status, assistanceBodyOf(r))
	}
}

func getAssistance(svc AssistanceService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id := chi.URLParam(req, "request_id")
		if !assistanceIDPattern.MatchString(id) {
			writeAssistanceNotFound(w)
			return
		}
		claims, _ := security.ClaimsFromContext(req.Context())
		r, err := svc.Get(req.Context(), claims.Subject, id)
		switch {
		case err == nil:
		case errors.Is(err, ports.ErrAssistanceRequestNotFound):
			writeAssistanceNotFound(w)
			return
		default:
			slog.ErrorContext(req.Context(), "could not read the request for assistance", "request_id", id, "error", err)
			writeError(w, http.StatusServiceUnavailable, "assistance_unavailable",
				"the request could not be read; retry")
			return
		}
		writeJSON(w, http.StatusOK, assistanceBodyOf(r))
	}
}

// writeAssistanceNotFound answers the same for a request that does not exist,
// one of another insured and an id that is not one (assumption S9).
func writeAssistanceNotFound(w http.ResponseWriter) {
	writeError(w, http.StatusNotFound, "assistance_request_not_found", "there is no such request for assistance")
}

func assistanceBodyOf(r domain.AssistanceRequest) assistanceResponse {
	l := r.Location
	location := assistanceLocationResponse{Status: string(l.Status), AccuracyM: l.AccuracyM}
	if c := l.Coordinates; c != nil {
		latitude, longitude := c.Latitude, c.Longitude
		location.Latitude, location.Longitude = &latitude, &longitude
	}
	if at := l.CapturedAt; at != nil {
		text := at.UTC().Format(time.RFC3339Nano)
		location.CapturedAt = &text
	}
	if l.UnavailableReason != "" {
		reason := string(l.UnavailableReason)
		location.UnavailableReason = &reason
	}
	return assistanceResponse{
		RequestID: r.ID, RequestNumber: r.Number, Status: string(r.Status),
		Policy: assistancePolicyResponse{PolicyID: r.Policy.ID, Line: string(r.Policy.Line),
			Product: r.Policy.Product, InsuredObject: r.Policy.InsuredObject},
		Location:    location,
		Place:       r.Place,
		RequestedAt: r.RequestedAt.UTC().Format(time.RFC3339Nano),
	}
}
