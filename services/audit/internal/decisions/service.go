// Package decisions is the module of the audit service that keeps the
// immutable record of every underwriting decision (HU-W4.1-01): which data and
// which rating rules set the price and the outcome. origination records it and
// an operations analyst reads it.
package decisions

import (
	"context"
	"fmt"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/ports"
)

// Service records and reads underwriting decisions.
type Service struct {
	store ports.DecisionStore
	clock ports.Clock
}

// NewService builds the service over a decision store and the clock.
func NewService(store ports.DecisionStore, clock ports.Clock) *Service {
	return &Service{store: store, clock: clock}
}

// Record validates and stores the decision. Sending the same offer again
// returns the stored record with created=false: the first one is never
// replaced. recorded_at is the server time.
func (s *Service) Record(ctx context.Context, r domain.DecisionRecord) (domain.DecisionRecord, bool, error) {
	if err := r.Validate(); err != nil {
		return domain.DecisionRecord{}, false, err
	}
	// PostgreSQL keeps microseconds: truncating makes the value the caller
	// gets equal to the stored one.
	r.RequestedAt = r.RequestedAt.UTC().Truncate(time.Microsecond)
	r.RecordedAt = s.clock.Now().UTC().Truncate(time.Microsecond)
	stored, created, err := s.store.Create(ctx, r)
	if err != nil {
		return domain.DecisionRecord{}, false, fmt.Errorf("record the decision: %w", err)
	}
	return stored, created, nil
}

// Get returns the decision of the offer or domain.ErrDecisionNotFound.
func (s *Service) Get(ctx context.Context, offerID string) (domain.DecisionRecord, error) {
	r, err := s.store.Get(ctx, offerID)
	if err != nil {
		return domain.DecisionRecord{}, fmt.Errorf("read the decision: %w", err)
	}
	return r, nil
}
