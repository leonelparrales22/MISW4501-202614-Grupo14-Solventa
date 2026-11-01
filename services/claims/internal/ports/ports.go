// Package ports declares what the claims use cases need from the outside.
// The adapters of internal/adapters implement them.
package ports

import (
	"context"
	"errors"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
)

// ErrFilingKeyReused is returned when the same filing key arrives with a
// different filing.
var ErrFilingKeyReused = errors.New("claims: the filing key was used for another filing")

// ClaimRepository stores the filed cases.
type ClaimRepository interface {
	// Create stores the claim and assigns its filing number. When the subject
	// already filed with the same key it returns that case and created=false,
	// or ErrFilingKeyReused when the filing is a different one.
	Create(ctx context.Context, c domain.Claim) (stored domain.Claim, created bool, err error)
}

// ErrEvidenceNotFound is returned by an EvidenceStore that holds no object
// under the requested key.
var ErrEvidenceNotFound = errors.New("claims: the evidence is not in the store")

// StoredEvidence is what the store knows about an object that was uploaded.
type StoredEvidence struct {
	ByteSize int64
	// SHA256 is the digest the storage computed when it received the object,
	// in lowercase hexadecimal.
	SHA256 string
}

// EvidenceStore is the repository of the captured evidence (HU-M2.1-01). The
// file never crosses this service: the client uploads it with a signed URL and
// the service only issues that URL and checks what arrived.
type EvidenceStore interface {
	// SignUpload returns the URL that accepts exactly that object, the headers
	// the client has to send with it, and when the URL stops working.
	SignUpload(ctx context.Context, key string, u domain.Upload, ttl time.Duration) (SignedUpload, error)
	// Stat returns ErrEvidenceNotFound when the key holds no object.
	Stat(ctx context.Context, key string) (StoredEvidence, error)
}

// SignedUpload is the signed instruction to upload one evidence.
type SignedUpload struct {
	URL       string
	Headers   map[string]string
	ExpiresAt time.Time
}

// Clock is the time the service reads, so the tests can fix it.
type Clock interface {
	Now() time.Time
}
