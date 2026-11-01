package httpadapter

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/evidence"
)

// EvidenceService is the use case behind the upload route (HU-M2.1-01).
type EvidenceService interface {
	SignUpload(ctx context.Context, subject string, r domain.UploadRequest) (evidence.Upload, error)
}

// WithEvidence mounts the route that issues the signed upload of one
// evidence. It is a human route: only an insured uploads, and always under
// their own subject.
func WithEvidence(svc EvidenceService, verifier security.Verifier) Option {
	return func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(noStore, security.Authenticate(verifier))
			r.With(security.RequireRole(security.RoleInsured)).
				Post("/v1/claims/evidence/upload-url", signUpload(svc))
		})
	}
}

// uploadRequest is what the client declares about the file it is going to
// upload. The file itself never passes through here.
type uploadRequest struct {
	Kind        string `json:"kind"`
	ContentType string `json:"content_type"`
	ByteSize    int64  `json:"byte_size"`
	SHA256      string `json:"sha256"`
}

type uploadResponse struct {
	ObjectKey string `json:"object_key"`
	UploadURL string `json:"upload_url"`
	// UploadHeaders are the headers the PUT has to carry, exactly these: the
	// signature covers them.
	UploadHeaders map[string]string `json:"upload_headers"`
	ExpiresAt     string            `json:"expires_at"`
}

func signUpload(svc EvidenceService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var in uploadRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, maxBodyBytes)).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "the request body is not valid JSON")
			return
		}

		claims, _ := security.ClaimsFromContext(req.Context())
		upload, err := svc.SignUpload(req.Context(), claims.Subject, domain.UploadRequest{
			Kind: in.Kind, ContentType: in.ContentType, ByteSize: in.ByteSize, SHA256: in.SHA256,
		})

		var invalid *domain.ValidationError
		switch {
		case err == nil:
		case errors.As(err, &invalid):
			writeValidationError(w, invalid)
			return
		default:
			slog.ErrorContext(req.Context(), "could not sign the upload of the evidence", "error", err)
			writeError(w, http.StatusServiceUnavailable, "evidence_unavailable",
				"the upload could not be prepared; retry later")
			return
		}

		writeJSON(w, http.StatusCreated, uploadResponse{
			ObjectKey:     upload.ObjectKey,
			UploadURL:     upload.URL,
			UploadHeaders: upload.Headers,
			ExpiresAt:     upload.ExpiresAt.UTC().Format(time.RFC3339),
		})
	}
}
