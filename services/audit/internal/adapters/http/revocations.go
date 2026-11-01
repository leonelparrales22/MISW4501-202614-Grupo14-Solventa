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

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/ports"
)

// HolderRevocationService is the use case behind the holder's revocation
// routes (HU-M1.3-02).
type HolderRevocationService interface {
	RevokeAsHolder(ctx context.Context, document, subject, consentID, requestID string, req domain.RevokeRequest) (domain.ConsentView, bool, error)
	Receipt(ctx context.Context, document, consentID, lang string) ([]byte, string, error)
}

// idempotencyKeyPattern is a UUID, the key the app generates when the holder
// confirms the revocation.
var idempotencyKeyPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// WithHolderRevocations mounts the revocation routes of the insured
// (HU-M1.3-02) in the same human route family as WithHolderConsents and with
// the same guards: the Cognito JWT, the insured role and the holder resolved
// from the subject of the token, never from a parameter.
//
//	POST /v1/consents/{consent_id}/revocations       revoke (Idempotency-Key)
//	GET  /v1/consents/{consent_id}/revocation-receipt the receipt in PDF
func WithHolderRevocations(holders HolderConsentService, svc HolderRevocationService, verifier security.Verifier) Option {
	return func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(noStore, security.Authenticate(verifier), security.RequireRole(security.RoleInsured))
			r.Post("/v1/consents/{consent_id}/revocations", revokeHolderConsent(holders, svc))
			r.Get("/v1/consents/{consent_id}/revocation-receipt", getRevocationReceipt(holders, svc))
		})
	}
}

type revokeRequest struct {
	RequestedAt        string `json:"requested_at"`
	Device             string `json:"device"`
	ConfirmationMethod string `json:"confirmation_method"`
}

type revocationEntityBody struct {
	Code        string     `json:"code"`
	Name        string     `json:"name"`
	Status      string     `json:"status"`
	ConfirmedAt *time.Time `json:"confirmed_at"`
	DeadlineAt  *time.Time `json:"deadline_at"`
}

// revocationBody is the revocation of a consent as the holder sees it.
type revocationBody struct {
	ConsentID     string                 `json:"consent_id"`
	ReceiptNumber string                 `json:"receipt_number"`
	Status        string                 `json:"status"`
	RequestedAt   time.Time              `json:"requested_at"`
	EffectiveAt   time.Time              `json:"effective_at"`
	Channel       string                 `json:"channel"`
	Device        *string                `json:"device"`
	NoticeStatus  string                 `json:"notice_status"`
	Entities      []revocationEntityBody `json:"entities"`
}

func revocationFrom(consentID string, r *domain.RevocationView) *revocationBody {
	if r == nil {
		return nil
	}
	out := &revocationBody{
		ConsentID: consentID, ReceiptNumber: r.ReceiptNumber, Status: string(r.Status),
		RequestedAt: r.RequestedAt.UTC(), EffectiveAt: r.EffectiveAt.UTC(), Channel: r.Channel, Device: optional(r.Device),
		NoticeStatus: string(r.Summary), Entities: make([]revocationEntityBody, 0, len(r.Entities)),
	}
	for _, e := range r.Entities {
		out.Entities = append(out.Entities, revocationEntityBody{
			Code: e.Entity.Code, Name: e.Entity.Name, Status: string(e.Status),
			ConfirmedAt: optionalTime(e.ConfirmedAt), DeadlineAt: optionalTime(e.DeadlineAt),
		})
	}
	return out
}

func revokeHolderConsent(holders HolderConsentService, svc HolderRevocationService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		doc, ok := resolveHolder(w, req, holders)
		if !ok {
			return
		}
		id, ok := consentIDParam(w, req)
		if !ok {
			return
		}
		key := strings.TrimSpace(req.Header.Get("Idempotency-Key"))
		switch {
		case key == "":
			writeValidation(w, &domain.ValidationError{Fields: []domain.FieldError{{Field: "Idempotency-Key", Code: domain.CodeRequired}}})
			return
		case !idempotencyKeyPattern.MatchString(key):
			writeValidation(w, &domain.ValidationError{Fields: []domain.FieldError{{Field: "Idempotency-Key", Code: domain.CodeInvalidFormat}}})
			return
		}
		var in revokeRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, maxBodyBytes)).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "the request body is not valid JSON")
			return
		}
		r := domain.RevokeRequest{Device: in.Device, ConfirmationMethod: in.ConfirmationMethod}
		if in.RequestedAt != "" {
			at, err := time.Parse(time.RFC3339Nano, in.RequestedAt)
			if err != nil {
				writeValidation(w, &domain.ValidationError{Fields: []domain.FieldError{{Field: "requested_at", Code: domain.CodeInvalidFormat}}})
				return
			}
			r.RequestedAt = at
		}
		claims, _ := security.ClaimsFromContext(req.Context())
		v, created, err := svc.RevokeAsHolder(req.Context(), doc, claims.Subject, id, strings.ToLower(key), r)
		var verr *domain.ValidationError
		switch {
		case errors.As(err, &verr):
			writeValidation(w, verr)
		case errors.Is(err, ports.ErrConsentNotFound):
			writeError(w, http.StatusNotFound, "not_found", "there is no consent with that id")
		case errors.Is(err, domain.ErrConsentNotActive):
			writeError(w, http.StatusConflict, "consent_not_active", "the consent is not active; there is nothing to revoke")
		case errors.Is(err, domain.ErrAlreadyRevoked):
			writeError(w, http.StatusConflict, "consent_already_revoked", "the consent is already revoked")
		case errors.Is(err, domain.ErrIdempotencyKeyReused):
			writeError(w, http.StatusConflict, "idempotency_key_reused", "the Idempotency-Key was already used for another consent")
		case err != nil:
			slog.ErrorContext(req.Context(), "could not revoke the consent", "error", err)
			writeError(w, http.StatusServiceUnavailable, "service_unavailable", "the consent could not be revoked; try again")
		default:
			status := http.StatusOK
			if created {
				status = http.StatusCreated
			}
			writeJSON(w, status, revocationFrom(v.Consent.ID, v.Revocation))
		}
	}
}

func getRevocationReceipt(holders HolderConsentService, svc HolderRevocationService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		doc, ok := resolveHolder(w, req, holders)
		if !ok {
			return
		}
		id, ok := consentIDParam(w, req)
		if !ok {
			return
		}
		lang := "es"
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(req.Header.Get("Accept-Language"))), "en") {
			lang = "en"
		}
		pdf, number, err := svc.Receipt(req.Context(), doc, id, lang)
		switch {
		case errors.Is(err, ports.ErrConsentNotFound):
			writeError(w, http.StatusNotFound, "not_found", "there is no revocation of a consent with that id")
		case err != nil:
			slog.ErrorContext(req.Context(), "could not render the revocation receipt", "error", err)
			writeError(w, http.StatusServiceUnavailable, "service_unavailable", "the receipt could not be generated; try again")
		default:
			w.Header().Set("Content-Type", "application/pdf")
			w.Header().Set("Content-Disposition", `attachment; filename="`+number+`.pdf"`)
			w.WriteHeader(http.StatusOK)
			if _, err := w.Write(pdf); err != nil {
				slog.ErrorContext(req.Context(), "could not write the receipt", "error", err)
			}
		}
	}
}

// consentIDParam returns the consent id of the path, or answers 404 when it
// cannot be an id.
func consentIDParam(w http.ResponseWriter, req *http.Request) (string, bool) {
	id := chi.URLParam(req, "consent_id")
	if len(id) > maxConsentIDLength || !consentIDPattern.MatchString(id) {
		writeError(w, http.StatusNotFound, "not_found", "there is no consent with that id")
		return "", false
	}
	return id, true
}

// optionalTime turns the zero time into a JSON null.
func optionalTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}
