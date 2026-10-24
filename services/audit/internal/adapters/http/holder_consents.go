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
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/ports"
)

// HolderConsentService is the use case behind the holder's consent routes
// (HU-M1.3-01).
type HolderConsentService interface {
	Resolve(ctx context.Context, subject string) (string, error)
	List(ctx context.Context, document string) ([]domain.ConsentView, error)
	Get(ctx context.Context, document, id string) (domain.ConsentView, error)
}

// WithHolderConsents mounts the routes of the insured over their own consents
// (HU-M1.3-01), a human route family under one prefix (decision D-3 a): the
// Cognito JWT is verified with pkg/security, only the insured role reads them
// and the holder is always the account of the token, never a parameter
// (criterion 6). Only GET exists: the routes are read-only (criterion 4).
// The revocation of HU-M1.3-02 is mounted apart, by WithHolderRevocations.
func WithHolderConsents(svc HolderConsentService, verifier security.Verifier) Option {
	return func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(noStore, security.Authenticate(verifier), security.RequireRole(security.RoleInsured))
			r.Get("/v1/consents", listHolderConsents(svc))
			r.Get("/v1/consents/{consent_id}", getHolderConsent(svc))
		})
	}
}

type entityBody struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

type historyBody struct {
	Status     string    `json:"status"`
	OccurredAt time.Time `json:"occurred_at"`
	ReceiptID  *string   `json:"receipt_id"`
	Channel    *string   `json:"channel"`
	EntityCode *string   `json:"entity_code"`
}

// holderConsentBody is what the holder sees of a consent. It never repeats
// the holder's document nor name (data minimization).
type holderConsentBody struct {
	ConsentID           string           `json:"consent_id"`
	Status              string           `json:"status"`
	Current             bool             `json:"current"`
	Entities            []entityBody     `json:"entities"`
	Purpose             string           `json:"purpose"`
	Channel             string           `json:"channel"`
	TermsVersion        string           `json:"terms_version"`
	Scope               []sourceTermBody `json:"scope"`
	GrantedAt           time.Time        `json:"granted_at"`
	ExpiresAt           time.Time        `json:"expires_at"`
	EndedAt             *time.Time       `json:"ended_at"`
	RevocationReceiptID *string          `json:"revocation_receipt_id"`
	// Revocable tells the app whether to offer the revocation (HU-M1.3-02
	// criterion 6): only an active consent can be revoked.
	Revocable bool `json:"revocable"`
	// Revocation is the revocation that ended the consent, with the state of
	// the notice to each entity; null when it was not revoked.
	Revocation *revocationBody `json:"revocation"`
	History    []historyBody   `json:"history"`
}

type holderConsentList struct {
	Items       []holderConsentBody `json:"items"`
	GeneratedAt time.Time           `json:"generated_at"`
}

// resolveHolder returns the document of the account of the token, or writes
// the error and returns false.
func resolveHolder(w http.ResponseWriter, req *http.Request, svc HolderConsentService) (string, bool) {
	claims, ok := security.ClaimsFromContext(req.Context())
	if !ok || claims.Subject == "" {
		security.WriteUnauthorized(w)
		return "", false
	}
	doc, err := svc.Resolve(req.Context(), claims.Subject)
	switch {
	case errors.Is(err, ports.ErrHolderNotLinked):
		writeError(w, http.StatusForbidden, "holder_not_linked",
			"the account is not linked to its consents yet; try again later")
		return "", false
	case err != nil:
		slog.ErrorContext(req.Context(), "could not resolve the holder", "error", err)
		writeError(w, http.StatusServiceUnavailable, "service_unavailable", "the consents could not be read; try again")
		return "", false
	}
	return doc, true
}

func listHolderConsents(svc HolderConsentService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		doc, ok := resolveHolder(w, req, svc)
		if !ok {
			return
		}
		views, err := svc.List(req.Context(), doc)
		if err != nil {
			slog.ErrorContext(req.Context(), "could not list the consents of the holder", "error", err)
			writeError(w, http.StatusServiceUnavailable, "service_unavailable", "the consents could not be read; try again")
			return
		}
		out := holderConsentList{Items: make([]holderConsentBody, 0, len(views)), GeneratedAt: time.Now().UTC()}
		for _, v := range views {
			out.Items = append(out.Items, holderConsentFrom(v))
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func getHolderConsent(svc HolderConsentService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		doc, ok := resolveHolder(w, req, svc)
		if !ok {
			return
		}
		id := chi.URLParam(req, "consent_id")
		if len(id) > maxConsentIDLength || !consentIDPattern.MatchString(id) {
			writeError(w, http.StatusNotFound, "not_found", "there is no consent with that id")
			return
		}
		v, err := svc.Get(req.Context(), doc, id)
		switch {
		case errors.Is(err, ports.ErrConsentNotFound):
			writeError(w, http.StatusNotFound, "not_found", "there is no consent with that id")
		case err != nil:
			slog.ErrorContext(req.Context(), "could not read the consent of the holder", "error", err)
			writeError(w, http.StatusServiceUnavailable, "service_unavailable", "the consent could not be read; try again")
		default:
			writeJSON(w, http.StatusOK, holderConsentFrom(v))
		}
	}
}

func holderConsentFrom(v domain.ConsentView) holderConsentBody {
	c := v.Consent
	out := holderConsentBody{
		ConsentID: c.ID, Status: string(v.Status), Current: v.Current, Entities: make([]entityBody, 0, len(v.Entities)),
		Purpose: c.Purpose, Channel: string(c.Channel), TermsVersion: string(c.TermsVersion), Scope: sourceBodies(c.Scope),
		GrantedAt: c.GrantedAt.UTC(), ExpiresAt: c.ExpiresAt().UTC(), History: make([]historyBody, 0, len(v.History)),
	}
	for _, e := range v.Entities {
		out.Entities = append(out.Entities, entityBody{Code: e.Code, Name: e.Name})
	}
	if !v.EndedAt.IsZero() {
		ended := v.EndedAt.UTC()
		out.EndedAt = &ended
	}
	out.RevocationReceiptID = optional(v.RevocationReceiptID)
	out.Revocable, out.Revocation = v.Revocable, revocationFrom(c.ID, v.Revocation)
	for _, h := range v.History {
		out.History = append(out.History, historyBody{
			Status: h.Status, OccurredAt: h.OccurredAt.UTC(), ReceiptID: optional(h.ReceiptID),
			Channel: optional(h.Channel), EntityCode: optional(h.EntityCode),
		})
	}
	return out
}

// consentIDPattern is the shape of a consent id ("cns-" plus hex today); an id
// outside it cannot exist and never reaches the database (a NUL byte, for
// example, is not valid text for PostgreSQL).
var consentIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// optional turns an empty string into a JSON null.
func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
