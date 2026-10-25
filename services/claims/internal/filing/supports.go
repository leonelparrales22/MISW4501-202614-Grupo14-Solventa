package filing

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// SupportService receives the supports of a web filing, one file at a time
// (HU-W2.1-01 criterion 5): a refused file never affects the ones already
// uploaded.
//
// Provisional (HU-W2.1-01 §9): the file goes through the service. With the
// evidence store of HU-M2.1-01 the client will upload it to S3 with a signed
// URL and this service will only check what arrived.
type SupportService struct {
	store ports.SupportStore
	clock ports.Clock
	newID func() (string, error)
}

// NewSupportService builds the service.
func NewSupportService(store ports.SupportStore, clock ports.Clock) *SupportService {
	return &SupportService{store: store, clock: clock, newID: newSupportID}
}

// Upload stores one support of the subject. The format is told by the content;
// it reads at most one byte more than the limit, so a larger body is cut, not
// loaded.
//
// Errors: domain.ErrTooManyPendingSupports, domain.ErrFileTooLarge,
// domain.ErrEmptyFile and domain.ErrUnsupportedFileType.
func (s *SupportService) Upload(ctx context.Context, subject, fileName string, body io.Reader) (domain.SupportFile, error) {
	pending, err := s.store.CountPending(ctx, subject)
	if err != nil {
		return domain.SupportFile{}, fmt.Errorf("count the pending supports: %w", err)
	}
	if pending >= domain.MaxPendingSupports {
		return domain.SupportFile{}, domain.ErrTooManyPendingSupports
	}

	content, err := io.ReadAll(io.LimitReader(body, domain.MaxSupportBytes+1))
	if err != nil {
		return domain.SupportFile{}, fmt.Errorf("read the file: %w", err)
	}
	switch {
	case len(content) > domain.MaxSupportBytes:
		return domain.SupportFile{}, domain.ErrFileTooLarge
	case len(content) == 0:
		return domain.SupportFile{}, domain.ErrEmptyFile
	}
	format, ok := domain.DetectFormat(content)
	if !ok {
		return domain.SupportFile{}, domain.ErrUnsupportedFileType
	}

	id, err := s.newID()
	if err != nil {
		return domain.SupportFile{}, err
	}
	sum := sha256.Sum256(content)
	f := domain.SupportFile{
		ID:           id,
		OwnerSubject: subject,
		FileName:     domain.SanitizeFileName(fileName),
		Format:       format,
		ByteSize:     int64(len(content)),
		SHA256:       hex.EncodeToString(sum[:]),
		// PostgreSQL keeps microseconds: truncating here makes the value the
		// caller gets equal to the stored one.
		UploadedAt: s.clock.Now().UTC().Truncate(time.Microsecond),
	}
	if err := s.store.Save(ctx, f, content); err != nil {
		return domain.SupportFile{}, fmt.Errorf("store the support: %w", err)
	}
	return f, nil
}

// newSupportID returns "sup-" and 128 random bits in hexadecimal.
func newSupportID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("could not generate the support id: %w", err)
	}
	return domain.SupportIDPrefix + hex.EncodeToString(b[:]), nil
}
