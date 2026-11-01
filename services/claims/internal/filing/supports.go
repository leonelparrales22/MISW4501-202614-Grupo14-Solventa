package filing

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// SupportUploadTTL is how long a signed upload of a support is good for.
const SupportUploadTTL = 10 * time.Minute

// signatureBytes is how much of a stored file is read to tell its format: the
// longest signature is the 8 bytes of a PNG.
const signatureBytes = 8

// SupportService receives the supports of a web filing, one file at a time
// (HU-W2.1-01 criterion 5): a refused file never affects the ones already
// uploaded. The file goes from the browser to the object store with a signed
// URL; the service declares it, signs it and, once uploaded, checks it.
type SupportService struct {
	store   ports.SupportStore
	objects ports.ObjectStore
	clock   ports.Clock
	newID   func() (string, error)
}

// NewSupportService builds the service.
func NewSupportService(store ports.SupportStore, objects ports.ObjectStore, clock ports.Clock) *SupportService {
	return &SupportService{store: store, objects: objects, clock: clock, newID: newSupportID}
}

// SupportUpload is the answer to a declaration: the support and how to put its
// file in the store.
type SupportUpload struct {
	File      domain.SupportFile
	URL       string
	Headers   map[string]string
	ExpiresAt time.Time
}

// Declare registers one support of the subject and signs the upload of its
// file. The size and the type are checked here, from the declaration; the
// content is checked by Confirm.
//
// Errors: domain.ErrTooManyPendingSupports, domain.ErrFileTooLarge,
// domain.ErrEmptyFile, domain.ErrUnsupportedFileType and *domain.ValidationError.
func (s *SupportService) Declare(ctx context.Context, subject string, r domain.SupportUploadRequest) (SupportUpload, error) {
	u, err := r.Validate()
	if err != nil {
		return SupportUpload{}, err
	}
	pending, err := s.store.CountPending(ctx, subject)
	if err != nil {
		return SupportUpload{}, fmt.Errorf("count the pending supports: %w", err)
	}
	if pending >= domain.MaxPendingSupports {
		return SupportUpload{}, domain.ErrTooManyPendingSupports
	}

	id, err := s.newID()
	if err != nil {
		return SupportUpload{}, err
	}
	f := domain.SupportFile{
		ID:           id,
		OwnerSubject: subject,
		FileName:     u.FileName,
		Format:       u.Format,
		ByteSize:     u.ByteSize,
		SHA256:       u.SHA256,
		// PostgreSQL keeps microseconds: truncating here makes the value the
		// caller gets equal to the stored one.
		UploadedAt: s.clock.Now().UTC().Truncate(time.Microsecond),
	}
	signed, err := s.objects.SignUpload(ctx, f.ObjectKey(), domain.Upload{
		ContentType: f.ContentType(), ByteSize: f.ByteSize, SHA256: f.SHA256,
	}, SupportUploadTTL)
	if err != nil {
		return SupportUpload{}, fmt.Errorf("sign the upload of the support: %w", err)
	}
	if err := s.store.Save(ctx, f); err != nil {
		return SupportUpload{}, fmt.Errorf("store the support: %w", err)
	}
	return SupportUpload{File: f, URL: signed.URL, Headers: signed.Headers, ExpiresAt: signed.ExpiresAt}, nil
}

// Confirm checks the file the browser uploaded against its declaration: it is
// in the store, has the declared size and digest, and its first bytes are the
// format that was declared (the extension and the media type are never
// trusted: a renamed executable is not a PDF). Confirming twice is harmless.
//
// Errors: domain.ErrSupportNotFound (it does not exist or is not the
// subject's), domain.ErrSupportNotUploaded, domain.ErrSupportMismatch and
// domain.ErrUnsupportedFileType.
func (s *SupportService) Confirm(ctx context.Context, subject, id string) (domain.SupportFile, error) {
	found, err := s.store.Find(ctx, []string{id})
	if err != nil {
		return domain.SupportFile{}, fmt.Errorf("read the support: %w", err)
	}
	f, ok := found[id]
	if !ok || f.OwnerSubject != subject {
		return domain.SupportFile{}, domain.ErrSupportNotFound
	}
	if f.Confirmed() {
		return f, nil
	}

	stored, err := s.objects.Stat(ctx, f.ObjectKey())
	switch {
	case errors.Is(err, ports.ErrEvidenceNotFound):
		return domain.SupportFile{}, domain.ErrSupportNotUploaded
	case err != nil:
		return domain.SupportFile{}, fmt.Errorf("read the file of the support: %w", err)
	// The digest is compared only when the storage computes its own.
	case stored.ByteSize != f.ByteSize, stored.SHA256 != "" && stored.SHA256 != f.SHA256:
		return domain.SupportFile{}, domain.ErrSupportMismatch
	}

	head, err := s.objects.Head(ctx, f.ObjectKey(), signatureBytes)
	switch {
	case errors.Is(err, ports.ErrEvidenceNotFound):
		return domain.SupportFile{}, domain.ErrSupportNotUploaded
	case err != nil:
		return domain.SupportFile{}, fmt.Errorf("read the start of the file of the support: %w", err)
	}
	if format, ok := domain.DetectFormat(head); !ok || format != f.Format {
		return domain.SupportFile{}, domain.ErrUnsupportedFileType
	}

	confirmed, err := s.store.Confirm(ctx, id, s.clock.Now().UTC().Truncate(time.Microsecond))
	if err != nil {
		return domain.SupportFile{}, fmt.Errorf("confirm the support: %w", err)
	}
	return confirmed, nil
}

// newSupportID returns "sup-" and 128 random bits in hexadecimal.
func newSupportID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("could not generate the support id: %w", err)
	}
	return domain.SupportIDPrefix + hex.EncodeToString(b[:]), nil
}
