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

// RegistrationService is the use case behind the sign-up routes (HU-M1.1-01).
type RegistrationService interface {
	ReadDocument(ctx context.Context, front, back string) (domain.DocumentData, error)
	Register(ctx context.Context, r domain.RegistrationRequest) (domain.Customer, error)
}

// WithRegistrations mounts the sign-up routes of the insured (HU-M1.1-01): the
// read of the document, whose data the person confirms (criterion 1), and the
// sign-up itself.
//
// They carry no session: the account does not exist yet, so the caller has no
// token. The user pool only accepts accounts created from the server, so this
// service decides who gets one. The gateway throttles the routes.
func WithRegistrations(svc RegistrationService) Option {
	return func(r chi.Router) {
		r.With(noStore).Post("/v1/registrations/document-reads", readDocument(svc))
		r.With(noStore).Post("/v1/registrations", register(svc))
	}
}

type documentReadRequest struct {
	DocumentFront string `json:"document_front"`
	DocumentBack  string `json:"document_back"`
}

type documentReadResponse struct {
	DocumentType string `json:"document_type"`
	DocumentID   string `json:"document_id"`
	GivenNames   string `json:"given_names"`
	Surnames     string `json:"surnames"`
	BirthDate    string `json:"birth_date"`
}

func readDocument(svc RegistrationService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var in documentReadRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, maxRegistrationBodyBytes)).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "the request body is not valid JSON")
			return
		}
		data, err := svc.ReadDocument(req.Context(), in.DocumentFront, in.DocumentBack)
		var invalid *domain.ValidationError
		switch {
		case err == nil:
		case errors.As(err, &invalid):
			writeValidationError(w, invalid)
			return
		default:
			slog.ErrorContext(req.Context(), "could not read the identity document", "error", err)
			writeError(w, http.StatusServiceUnavailable, "document_read_unavailable",
				"the document could not be read; retry later")
			return
		}
		writeJSON(w, http.StatusOK, documentReadResponse{
			DocumentType: data.DocumentType, DocumentID: data.DocumentID,
			GivenNames: data.GivenNames, Surnames: data.Surnames, BirthDate: data.BirthDate,
		})
	}
}

// maxRegistrationBodyBytes bounds the sign-up body, which carries the two
// faces of the identity document. Each face is capped at
// domain.MaxIdentityDocumentBytes, and base64 adds a third; the rest of the
// body is a few hundred bytes.
const maxRegistrationBodyBytes = 4 << 20

// registrationRequest is the body of the sign-up. The password travels to the
// user pool and is not stored; the faces of the document go to the document
// store, and only their keys stay with the customer.
type registrationRequest struct {
	DocumentID string `json:"document_id"`
	FullName   string `json:"full_name"`
	Email      string `json:"email"`
	BirthDate  string `json:"birth_date"`
	Password   string `json:"password"`
	// DocumentFront and DocumentBack are base64 of the JPEG of each face.
	DocumentFront string `json:"document_front"`
	DocumentBack  string `json:"document_back"`
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
	// IdentityDocuments are the faces that were kept, front first. They let
	// the caller name the object of each one without reading it back.
	IdentityDocuments []storedIdentityDocument `json:"identity_documents"`
}

// storedIdentityDocument is one face of the document in the document store.
type storedIdentityDocument struct {
	Face      string `json:"face"`
	ObjectKey string `json:"object_key"`
}

func register(svc RegistrationService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var in registrationRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, maxRegistrationBodyBytes)).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "the request body is not valid JSON")
			return
		}

		c, err := svc.Register(req.Context(), domain.RegistrationRequest{
			DocumentID: in.DocumentID, FullName: in.FullName, Email: in.Email,
			BirthDate: in.BirthDate, Password: in.Password,
			DocumentFront: in.DocumentFront, DocumentBack: in.DocumentBack,
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
		case errors.Is(err, orchestrator.ErrIdentityNotVerified):
			// One answer for every reason the provider gives (criterion 3):
			// telling a restrictive list from a blurred photo would tell the
			// person which one they are.
			writeError(w, http.StatusUnprocessableEntity, "identity_not_verified",
				"the identity could not be verified")
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
			IdentityDocuments: []storedIdentityDocument{
				{Face: string(domain.FaceFront), ObjectKey: c.DocumentFrontKey},
				{Face: string(domain.FaceBack), ObjectKey: c.DocumentBackKey},
			},
		})
	}
}
