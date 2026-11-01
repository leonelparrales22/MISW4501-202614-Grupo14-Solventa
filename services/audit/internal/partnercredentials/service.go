// Package partnercredentials is the module of the audit service that keeps
// the audit record of the credentials of the commercial partners
// (HU-W3.1-01 criterion 3): partners publishes every issue, rotation and
// revocation, with its date and the analyst or process that issued it, and
// audit stores it append-only, part of the subscription audit record of
// HU-W4.1-01.
package partnercredentials

import (
	"context"
	"fmt"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/ports"
)

// Service records and reads the partner credential records.
type Service struct {
	store ports.PartnerCredentialStore
}

// NewService builds the service over its store.
func NewService(store ports.PartnerCredentialStore) *Service {
	return &Service{store: store}
}

// Record validates and stores one record. Recording the same event again
// writes nothing and reports created=false (the queue delivers at least
// once).
func (s *Service) Record(ctx context.Context, r domain.PartnerCredentialRecord) (bool, error) {
	if err := r.Validate(); err != nil {
		return false, err
	}
	// PostgreSQL keeps microseconds: truncating makes what is read equal to
	// what was stored.
	r.OccurredAt = r.OccurredAt.UTC().Truncate(time.Microsecond)
	if !r.PreviousExpiresAt.IsZero() {
		r.PreviousExpiresAt = r.PreviousExpiresAt.UTC().Truncate(time.Microsecond)
	}
	created, err := s.store.Append(ctx, r)
	if err != nil {
		return false, fmt.Errorf("record the partner credential event %s: %w", r.EventID, err)
	}
	return created, nil
}

// History returns the records of a partner, newest first.
func (s *Service) History(ctx context.Context, partnerID string) ([]domain.PartnerCredentialRecord, error) {
	records, err := s.store.ListByPartner(ctx, partnerID)
	if err != nil {
		return nil, fmt.Errorf("read the partner credential records: %w", err)
	}
	return records, nil
}
