package billing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"unicode/utf8"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// Limits of the issuance exceptions list.
const (
	DefaultExceptionsLimit = 50
	MaxExceptionsLimit     = 200
)

// ErrInvalidNote is returned when a resumption has no note or a note longer
// than domain.MaxResumeNoteLength characters.
var ErrInvalidNote = errors.New("a note of 1 to 500 characters is required to resume a charge")

// ErrInvalidLimit is returned for a list limit outside 1..MaxExceptionsLimit.
var ErrInvalidLimit = errors.New("limit must be between 1 and 200")

// Service answers the state of a premium charge (screen E4b, CA-2) and lets
// operations see the issuance exceptions and resume them (CA-3; the monitoring
// screen is HU-W1.3-04).
type Service struct {
	repo   ports.ChargeRepository
	clock  ports.Clock
	policy resilience.RetryPolicy
	notify func()
}

// NewService builds a Service. policy gives the window of a resumed charge;
// notify, when not nil, wakes the worker after a resumption.
func NewService(repo ports.ChargeRepository, clock ports.Clock, policy resilience.RetryPolicy, notify func()) *Service {
	if notify == nil {
		notify = func() {}
	}
	return &Service{repo: repo, clock: clock, policy: policy, notify: notify}
}

// Get returns the charge of an acceptance to the insured who owns it, an
// advisor or an operations analyst.
func (s *Service) Get(ctx context.Context, acceptanceID string) (domain.Charge, error) {
	c, err := s.repo.Get(ctx, acceptanceID)
	if err != nil {
		return domain.Charge{}, err
	}
	if err := security.RequireOwnerOrRole(ctx, c.CustomerID, security.RoleAdvisor, security.RoleOperations); err != nil {
		return domain.Charge{}, err
	}
	return c, nil
}

// ListExceptions returns the failed charges, the latest first, to operations.
func (s *Service) ListExceptions(ctx context.Context, limit int) ([]domain.Charge, error) {
	if err := security.RequireAnyRole(ctx, security.RoleOperations); err != nil {
		return nil, err
	}
	if limit < 1 || limit > MaxExceptionsLimit {
		return nil, ErrInvalidLimit
	}
	return s.repo.ListExceptions(ctx, limit)
}

// Resume moves a failed charge back to pending with the same idempotency key
// and a new retry window. Only operations may do it, with a note; the author
// is the subject of their token.
func (s *Service) Resume(ctx context.Context, acceptanceID, note string) (domain.Charge, error) {
	if err := security.RequireAnyRole(ctx, security.RoleOperations); err != nil {
		return domain.Charge{}, err
	}
	note = strings.TrimSpace(note)
	if note == "" || utf8.RuneCountInString(note) > domain.MaxResumeNoteLength {
		return domain.Charge{}, ErrInvalidNote
	}
	caller, _ := security.ClaimsFromContext(ctx)
	now := s.clock.Now().UTC()
	c, err := s.repo.Resume(ctx, acceptanceID, domain.ChargeResumption{
		By: caller.Subject, Note: note, At: now, WindowEndsAt: s.policy.WindowEnd(now),
	})
	if err != nil {
		if errors.Is(err, ports.ErrChargeNotFound) || errors.Is(err, ports.ErrChargeNotResumable) {
			return domain.Charge{}, err
		}
		return domain.Charge{}, fmt.Errorf("could not resume the charge of acceptance %s: %w", acceptanceID, err)
	}
	slog.InfoContext(ctx, "premium charge resumed", "acceptance_id", c.AcceptanceID, "resumed_by", c.LastResumedBy,
		"resume_count", c.ResumeCount, "retry_window_ends_at", c.WindowEndsAt)
	s.notify()
	return c, nil
}
