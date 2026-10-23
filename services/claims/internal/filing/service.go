// Package filing holds the use case that creates the case of a claim
// (HU-M2.2-01).
package filing

import (
	"context"
	"crypto/rand"
	"fmt"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// Service files the claim of an insured: it validates what the assistant
// collected and stores the case with its filing number.
type Service struct {
	claims ports.ClaimRepository
	clock  ports.Clock
	newID  func() (string, error)
}

// NewService builds the service.
func NewService(claims ports.ClaimRepository, clock ports.Clock) *Service {
	return &Service{claims: claims, clock: clock, newID: newID}
}

// File stores the case for the given subject and filing key, and returns it
// with its filing number. created tells whether this call is the one that
// created it: a retry with the same key returns the same case with false.
func (s *Service) File(ctx context.Context, subject, key string,
	r domain.FilingRequest) (domain.Claim, bool, error) {
	c, err := r.Validate(s.clock.Now().UTC())
	if err != nil {
		return domain.Claim{}, false, err
	}

	c.Subject = subject
	c.FilingKey = key
	c.FiledAt = s.clock.Now().UTC()
	if c.ID, err = s.newID(); err != nil {
		return domain.Claim{}, false, fmt.Errorf("could not generate the claim id: %w", err)
	}

	stored, created, err := s.claims.Create(ctx, c)
	if err != nil {
		return domain.Claim{}, false, err
	}
	return stored, created, nil
}

// newID returns a random UUID (version 4).
func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("could not generate an id: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
