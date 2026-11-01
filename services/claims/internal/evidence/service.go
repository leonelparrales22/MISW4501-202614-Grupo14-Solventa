// Package evidence holds the use case that lets the insured upload what they
// captured in the place of the incident (HU-M2.1-01).
package evidence

import (
	"context"
	"fmt"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// UploadTTL is how long a signed upload is good for. It covers a capture
// uploaded over a bad mobile network and leaves the URL useless soon after.
const UploadTTL = 10 * time.Minute

// Service issues the signed uploads of the evidence. The file never crosses
// the service: it goes from the phone to the store.
type Service struct {
	store ports.EvidenceStore
	clock ports.Clock
}

// NewService builds the service.
func NewService(store ports.EvidenceStore, clock ports.Clock) *Service {
	return &Service{store: store, clock: clock}
}

// Upload is the answer to the insured: where the evidence will live and how
// to put it there.
type Upload struct {
	ObjectKey string
	URL       string
	Headers   map[string]string
	ExpiresAt time.Time
}

// SignUpload validates the declaration and returns the signed upload. The key
// hangs from the subject of the token, so one insured cannot write in the
// directory of another.
func (s *Service) SignUpload(ctx context.Context, subject string,
	r domain.UploadRequest) (Upload, error) {
	u, err := r.Validate()
	if err != nil {
		return Upload{}, err
	}
	key := u.Key(subject)
	signed, err := s.store.SignUpload(ctx, key, u, UploadTTL)
	if err != nil {
		return Upload{}, fmt.Errorf("sign the upload of the evidence: %w", err)
	}
	return Upload{
		ObjectKey: key,
		URL:       signed.URL,
		Headers:   signed.Headers,
		ExpiresAt: signed.ExpiresAt,
	}, nil
}
