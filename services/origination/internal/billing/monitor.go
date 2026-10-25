package billing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// ErrInvalidFilter is returned for a status, channel or date range the monitor
// does not understand.
var ErrInvalidFilter = errors.New("the issuance filter is not valid")

// ErrIssuanceNotFound is returned when an acceptance has neither a premium
// charge nor a contractual issuance to retry.
var ErrIssuanceNotFound = errors.New("issuance not found")

// ErrIssuanceNotRetryable is returned when nothing of an issuance is failed
// (or another analyst, or the automatic process, already retried it).
var ErrIssuanceNotRetryable = errors.New("issuance not retryable")

// WithContractRetry lets the service retry a failed contractual issuance;
// wake, when not nil, makes its worker look for due issuances at once.
func (s *Service) WithContractRetry(r ports.ContractRetrier, wake func()) *Service {
	if wake == nil {
		wake = func() {}
	}
	s.contracts, s.wakeContracts = r, wake
	return s
}

// IssuanceOverview is what the monitoring screen shows: the issuances that
// match the filter and the counts of every issuance.
type IssuanceOverview struct {
	Items  []domain.Issuance
	Counts domain.IssuanceCounts
}

// Overview returns the issuances for the monitoring screen of operations
// (HU-W1.3-04), the latest accepted first, and the counts of all of them. An
// exception that has waited for an analyst longer than domain.EscalationAfter
// comes flagged as unattended. dayFrom is the start of the day of the caller.
func (s *Service) Overview(ctx context.Context, f domain.IssuanceFilter, dayFrom time.Time) (IssuanceOverview, error) {
	if err := security.RequireAnyRole(ctx, security.RoleOperations); err != nil {
		return IssuanceOverview{}, err
	}
	if f.Limit == 0 {
		f.Limit = DefaultExceptionsLimit
	}
	if f.Limit < 1 || f.Limit > MaxExceptionsLimit {
		return IssuanceOverview{}, ErrInvalidLimit
	}
	if err := f.Validate(); err != nil {
		return IssuanceOverview{}, errors.Join(ErrInvalidFilter, err)
	}
	now := s.clock.Now().UTC()
	items, err := s.repo.ListIssuances(ctx, f)
	if err != nil {
		return IssuanceOverview{}, err
	}
	for i := range items {
		items[i].Unattended = items[i].IsUnattended(now)
	}
	counts, err := s.repo.CountIssuances(ctx, dayFrom, now)
	if err != nil {
		return IssuanceOverview{}, err
	}
	return IssuanceOverview{Items: items, Counts: counts}, nil
}

// ListActions returns what the analysts did on an acceptance, the latest
// first, to operations.
func (s *Service) ListActions(ctx context.Context, acceptanceID string) ([]domain.IssuanceAction, error) {
	if err := security.RequireAnyRole(ctx, security.RoleOperations); err != nil {
		return nil, err
	}
	return s.repo.ListActions(ctx, acceptanceID)
}

// RetryIssuance retries what failed in the issuance of an acceptance, from
// the registered acceptance (CA-3 of HU-W1.3-04): the premium charge if that
// is what failed, otherwise the contractual issuance (document or
// notification). The note is required and the action is recorded with the
// author and the date. It returns which of the two was retried.
func (s *Service) RetryIssuance(ctx context.Context, acceptanceID, note string) (domain.IssuanceActionKind, error) {
	if err := security.RequireAnyRole(ctx, security.RoleOperations); err != nil {
		return "", err
	}
	note, err := cleanNote(note)
	if err != nil {
		return "", err
	}
	_, err = s.resume(ctx, acceptanceID, note)
	switch {
	case err == nil:
		return domain.IssuanceActionResume, nil
	case errors.Is(err, ports.ErrChargeNotFound):
		return "", ErrIssuanceNotFound
	case !errors.Is(err, ports.ErrChargeNotResumable):
		return "", err
	}
	// The charge is not failed: it can still be the contractual issuance.
	if s.contracts == nil {
		return "", ErrIssuanceNotRetryable
	}
	caller, _ := security.ClaimsFromContext(ctx)
	c, err := s.contracts.ReopenContract(ctx, acceptanceID, domain.IssuanceRetry{
		By: caller.Subject, Note: note, At: s.clock.Now().UTC(),
	})
	switch {
	case err == nil:
	case errors.Is(err, ports.ErrContractNotFound), errors.Is(err, ports.ErrContractNotRetryable):
		return "", ErrIssuanceNotRetryable
	default:
		return "", fmt.Errorf("could not retry the contractual issuance of acceptance %s: %w", acceptanceID, err)
	}
	slog.InfoContext(ctx, "contractual issuance retried", "acceptance_id", acceptanceID, "policy_id", c.Event.PolicyID,
		"retried_by", caller.Subject)
	s.wakeContracts()
	return domain.IssuanceActionRetry, nil
}
