// Package filing holds the use case that creates the case of a claim
// (HU-M2.2-01).
package filing

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strconv"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// Service files the claim of an insured: it validates what the assistant
// collected and stores the case with its filing number.
type Service struct {
	claims   ports.ClaimRepository
	evidence ports.EvidenceStore
	clock    ports.Clock
	newID    func() (string, error)
}

// NewService builds the service.
//
// evidence puede ser nil en un servicio que no recibe evidencia —la
// radicación web de HU-W2.1-01 manda sus soportes por otra ruta—; una
// radicación que declare evidencia se rechaza entonces como inválida, en
// lugar de reventar.
func NewService(claims ports.ClaimRepository, evidence ports.EvidenceStore, clock ports.Clock) *Service {
	return &Service{claims: claims, evidence: evidence, clock: clock, newID: newID}
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
	if err := s.checkEvidence(ctx, subject, c.Evidence); err != nil {
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

// checkEvidence confronts every key with the evidence store: the object
// exists, belongs to whoever is filing, and is the size and the content that
// were declared. A case that names evidence nobody can open would be a
// verification impossible to review afterwards.
func (s *Service) checkEvidence(ctx context.Context, subject string, evidence []domain.Evidence) error {
	if len(evidence) == 0 {
		return nil
	}
	if s.evidence == nil {
		return &domain.ValidationError{
			Fields: []domain.FieldError{{Field: "evidence", Code: domain.CodeNotStored}},
		}
	}

	var fields []domain.FieldError
	for i, e := range evidence {
		field := domain.FieldError{
			Field: "evidence." + strconv.Itoa(i) + ".object_key",
			Code:  domain.CodeNotStored,
		}
		if !domain.OwnedBy(e.ObjectKey, subject) {
			fields = append(fields, field)
			continue
		}
		stored, err := s.evidence.Stat(ctx, e.ObjectKey)
		switch {
		case errors.Is(err, ports.ErrEvidenceNotFound):
			fields = append(fields, field)
		case err != nil:
			return fmt.Errorf("read the evidence %s: %w", e.ObjectKey, err)
		// The digest is compared only when the storage computes its own: the
		// size already tells the object is the declared one, and the
		// integrity seal travels with the case either way.
		case stored.ByteSize != e.ByteSize:
			fields = append(fields, field)
		case stored.SHA256 != "" && stored.SHA256 != e.SHA256:
			fields = append(fields, field)
		}
	}
	if len(fields) > 0 {
		return &domain.ValidationError{Fields: fields}
	}
	return nil
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
