package httpadapter

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/orchestrator"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// EmbeddedOfferService is the use case behind POST /v1/offers (HU-W1.2-03).
type EmbeddedOfferService interface {
	Generate(ctx context.Context, req domain.EmbeddedOfferRequest) (domain.PersonalizedOffer, error)
	Now() time.Time
}

// OfferService is the use case behind the routes that read and renew an offer
// (HU-W1.2-03).
type OfferService interface {
	Get(ctx context.Context, offerID string) (orchestrator.OfferView, error)
	Renew(ctx context.Context, offerID string) (domain.PersonalizedOffer, bool, error)
	Now() time.Time
}

// WithEmbeddedOffers mounts POST /v1/offers. It is a partner-channel route:
// the API Gateway authenticates the partner with its API key, like
// /v1/risk-profiles, so the service does not read a Cognito token here.
func WithEmbeddedOffers(svc EmbeddedOfferService) Option {
	return func(r chi.Router) {
		r.With(noStore).Post("/v1/offers", generateOffer(svc))
	}
}

// WithOffers mounts the offer routes of the insured (HU-W1.2-03). They are
// human routes: the Cognito JWT is verified with pkg/security. The owner (the
// first insured to open the offer), an advisor or an operations analyst read
// it; only the owner renews it.
func WithOffers(svc OfferService, verifier security.Verifier) Option {
	return func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(noStore, security.Authenticate(verifier))
			r.With(security.RequireRole(security.RoleInsured, security.RoleAdvisor, security.RoleOperations)).
				Get("/v1/offers/{offer_id}", getOffer(svc))
			r.With(security.RequireRole(security.RoleInsured)).
				Post("/v1/offers/{offer_id}/renewal", renewOffer(svc))
		})
	}
}

type generateOfferRequest struct {
	CustomerID    string  `json:"customer_id"`
	ConsentID     string  `json:"consent_id"`
	PartnerID     string  `json:"partner_id"`
	ApplicationID string  `json:"application_id"`
	DocumentID    string  `json:"document_id"`
	FullName      string  `json:"full_name"`
	Email         string  `json:"email"`
	BirthDate     string  `json:"birth_date"`
	InsuredAmount float64 `json:"insured_amount"`
	TermMonths    int     `json:"term_months"`
}

type offerProfileBody struct {
	Category string `json:"category"`
	Source   string `json:"source"`
}

type offerTermsBody struct {
	InsuredAmount float64        `json:"insured_amount"`
	TermMonths    int            `json:"term_months"`
	Coverages     []coverageBody `json:"coverages"`
}

type offerApplicantBody struct {
	FullName string `json:"full_name"`
}

type offerCreditBody struct {
	PartnerID     string `json:"partner_id"`
	ApplicationID string `json:"application_id"`
}

// offerResponse is the offer as the customer sees it (screens E3, E3b, E3c). It
// never carries the score, the rules or the identity document. For the
// adjusted and demo offers of other stories only the first block is filled;
// the rest is null.
type offerResponse struct {
	OfferID   string    `json:"offer_id"`
	Kind      string    `json:"kind"`
	Status    string    `json:"status"`
	Premium   float64   `json:"premium"`
	Currency  string    `json:"currency"`
	Mode      string    `json:"mode"`
	ExpiresAt time.Time `json:"expires_at"`
	CanAccept bool      `json:"can_accept"`
	CanRenew  bool      `json:"can_renew"`

	Channel           *string             `json:"channel"`
	CreatedAt         *time.Time          `json:"created_at"`
	ProvisionalReason *string             `json:"provisional_reason"`
	ProfileDate       *time.Time          `json:"profile_date"`
	UnavailableSource *string             `json:"unavailable_source"`
	Profile           *offerProfileBody   `json:"profile"`
	Terms             *offerTermsBody     `json:"terms"`
	Conditions        []string            `json:"conditions"`
	RaterVersion      *string             `json:"rater_version"`
	Applicant         *offerApplicantBody `json:"applicant"`
	Credit            *offerCreditBody    `json:"credit"`
	SupersededBy      *string             `json:"superseded_by"`
	RenewedBy         *string             `json:"renewed_by"`
}

type offerConflictResponse struct {
	Error        string     `json:"error"`
	Message      string     `json:"message"`
	OfferID      string     `json:"offer_id"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
	SupersededBy *string    `json:"superseded_by,omitempty"`
}

func generateOffer(svc EmbeddedOfferService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var in generateOfferRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, maxBodyBytes)).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "the request body is not valid JSON")
			return
		}
		o, err := svc.Generate(req.Context(), domain.EmbeddedOfferRequest{
			CustomerID: in.CustomerID, ConsentID: in.ConsentID, PartnerID: in.PartnerID, ApplicationID: in.ApplicationID,
			DocumentID: in.DocumentID, FullName: in.FullName, Email: in.Email, BirthDate: in.BirthDate,
			InsuredAmount: in.InsuredAmount, TermMonths: in.TermMonths,
		})
		if err != nil {
			writeOfferError(w, req, err, "could not generate the offer", http.StatusBadRequest)
			return
		}
		w.Header().Set("Location", "/v1/offers/"+url.PathEscape(o.ID))
		writeJSON(w, http.StatusCreated, personalizedResponseAt(o, svc.Now()))
	}
}

func getOffer(svc OfferService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := offerPathID(req)
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid_request", "offer_id (max 128 chars) is required")
			return
		}
		v, err := svc.Get(req.Context(), id)
		if err != nil {
			writeOfferError(w, req, err, "could not read the offer", http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, toOfferResponse(v))
	}
}

func renewOffer(svc OfferService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := offerPathID(req)
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid_request", "offer_id (max 128 chars) is required")
			return
		}
		o, created, err := svc.Renew(req.Context(), id)
		if err != nil {
			writeOfferError(w, req, err, "could not renew the offer", http.StatusUnprocessableEntity)
			return
		}
		w.Header().Set("Location", "/v1/offers/"+url.PathEscape(o.ID))
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		writeJSON(w, status, personalizedResponseAt(o, svc.Now()))
	}
}

// writeOfferError translates the errors of the offer use cases.
// invalidStatus is the status of a *domain.ValidationError: 400 for the
// partner's request, 422 for a renewal whose stored data is no longer insurable.
func writeOfferError(w http.ResponseWriter, req *http.Request, err error, what string, invalidStatus int) {
	var invalid *domain.ValidationError
	var stillValid *orchestrator.OfferStillValidError
	var superseded *orchestrator.OfferSupersededError
	switch {
	case errors.As(err, &invalid):
		writeFieldErrors(w, invalidStatus, invalid)
	case errors.Is(err, orchestrator.ErrConsentRequired):
		writeError(w, http.StatusUnprocessableEntity, "consent_required", "an active consent is required to profile the customer")
	case errors.Is(err, orchestrator.ErrConsentMismatch):
		writeError(w, http.StatusUnprocessableEntity, "consent_mismatch", "the consent does not belong to the customer")
	case errors.Is(err, orchestrator.ErrConsentUnverifiable):
		slog.ErrorContext(req.Context(), what, "error", err)
		writeError(w, http.StatusServiceUnavailable, "consent_unverifiable", "the consent could not be verified; retry later")
	case errors.As(err, &stillValid):
		expires := stillValid.ExpiresAt.UTC()
		writeJSON(w, http.StatusConflict, offerConflictResponse{
			Error: "offer_still_valid", Message: "the offer can still be accepted; renew it once its validity is over",
			OfferID: stillValid.OfferID, ExpiresAt: &expires,
		})
	case errors.As(err, &superseded):
		by := superseded.SupersededBy
		writeJSON(w, http.StatusConflict, offerConflictResponse{
			Error: "offer_superseded", Message: "a newer offer replaced this one; use it",
			OfferID: superseded.OfferID, SupersededBy: &by,
		})
	case errors.Is(err, ports.ErrOfferNotFound):
		writeError(w, http.StatusNotFound, "offer_not_found", "the offer does not exist")
	case isAuthError(err):
		writeAuthError(w, err)
	default:
		slog.ErrorContext(req.Context(), what, "error", err)
		writeError(w, http.StatusServiceUnavailable, "offer_unavailable", "the offer is unavailable; retry later")
	}
}

var offerIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// offerPathID returns the offer id of the path when it has the shape every
// offer id has (letters, digits, hyphen and underscore, at most 128). Anything
// else cannot name an offer, so it is a bad request instead of a lookup.
func offerPathID(req *http.Request) (string, bool) {
	id, ok := pathID(req, "offer_id")
	return id, ok && offerIDPattern.MatchString(id)
}

func writeFieldErrors(w http.ResponseWriter, status int, e *domain.ValidationError) {
	out := validationErrorResponse{
		Error: "validation_failed", Message: "one or more fields are missing or invalid",
		Fields: make([]fieldErrorBody, 0, len(e.Fields)),
	}
	for _, f := range e.Fields {
		out.Fields = append(out.Fields, fieldErrorBody{Field: f.Field, Code: f.Code})
	}
	writeJSON(w, status, out)
}

func toOfferResponse(v orchestrator.OfferView) offerResponse {
	if v.Personalized != nil {
		return toPersonalizedResponse(*v.Personalized, v.Status, v.CanAccept, v.CanRenew)
	}
	o := v.Reduced
	return offerResponse{
		OfferID: o.ID, Kind: string(v.Kind), Status: string(v.Status), Premium: o.Premium, Currency: o.Currency,
		Mode: string(o.Mode), ExpiresAt: o.ExpiresAt.UTC(), CanAccept: v.CanAccept, CanRenew: v.CanRenew,
	}
}

// personalizedResponseAt builds the body of an offer the service just created,
// with its status computed at now.
func personalizedResponseAt(o domain.PersonalizedOffer, now time.Time) offerResponse {
	return toPersonalizedResponse(o, o.Status(now), o.CanAccept(now), o.CanRenew(now))
}

// toPersonalizedResponse builds the full body of a personalized offer.
func toPersonalizedResponse(o domain.PersonalizedOffer, status domain.OfferStatus, canAccept, canRenew bool) offerResponse {
	channel, created, version := string(o.Channel), o.CreatedAt.UTC(), o.RaterVersion
	out := offerResponse{
		OfferID: o.ID, Kind: string(orchestrator.OfferKindPersonalized), Status: string(status),
		Premium: o.Premium, Currency: o.Currency, Mode: string(o.Mode), ExpiresAt: o.ExpiresAt.UTC(),
		CanAccept: canAccept, CanRenew: canRenew,
		Channel: &channel, CreatedAt: &created, RaterVersion: &version,
		Profile:    &offerProfileBody{Category: o.Profile.Category, Source: string(o.ProfileSource)},
		Terms:      &offerTermsBody{InsuredAmount: o.Terms.InsuredAmount, TermMonths: o.Terms.TermMonths, Coverages: make([]coverageBody, 0, len(o.Terms.Coverages))},
		Conditions: append([]string{}, o.Conditions...),
		Applicant:  &offerApplicantBody{FullName: o.Applicant.FullName},
		Credit:     &offerCreditBody{PartnerID: o.PartnerID, ApplicationID: o.ApplicationID},
	}
	for _, c := range o.Terms.Coverages {
		out.Terms.Coverages = append(out.Terms.Coverages, coverageBody{Code: c.Code, InsuredAmount: c.InsuredAmount})
	}
	if o.ProvisionalReason != "" {
		out.ProvisionalReason = &o.ProvisionalReason
	}
	if !o.ProfileDate.IsZero() {
		d := o.ProfileDate.UTC()
		out.ProfileDate = &d
	}
	if o.UnavailableSource != "" {
		out.UnavailableSource = &o.UnavailableSource
	}
	if o.SupersededBy != "" {
		out.SupersededBy = &o.SupersededBy
	}
	if o.RenewedBy != "" {
		out.RenewedBy = &o.RenewedBy
	}
	return out
}
