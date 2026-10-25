package ports

import (
	"context"
	"errors"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

// ErrContractNotRetryable is returned when an issuance whose contractual
// issuance is not failed is asked to retry (or two analysts retry it at once
// and the other one won).
var ErrContractNotRetryable = errors.New("contractual issuance not retryable")

// IssuanceMonitor reads the issuances for the monitoring screen of operations
// (HU-W1.3-04). Writing the history of actions is not here: Resume of the
// ChargeRepository and ReopenContract of the ContractRetrier record it in the
// same statement as the change they make.
type IssuanceMonitor interface {
	// ListIssuances returns up to filter.Limit issuances matching the filter,
	// the latest accepted first. A filter that matches none returns an empty
	// list, not an error.
	ListIssuances(ctx context.Context, filter domain.IssuanceFilter) ([]domain.Issuance, error)
	// CountIssuances summarizes every issuance, whatever the filter of the
	// list. dayFrom is the start of the day of the caller ("today") and now
	// the time the unattended exceptions are measured against.
	CountIssuances(ctx context.Context, dayFrom, now time.Time) (domain.IssuanceCounts, error)
	// ListActions returns the actions taken on an acceptance, the latest first.
	ListActions(ctx context.Context, acceptanceID string) ([]domain.IssuanceAction, error)
}

// ContractRetrier lets an analyst retry the contractual issuance of an
// acceptance that failed after every automatic attempt.
type ContractRetrier interface {
	// ReopenContract moves a failed contractual issuance back to pending, due
	// at r.At, with its attempts reset, keeping the document it already has.
	// It records the action in the same statement. It returns
	// ErrContractNotFound or ErrContractNotRetryable.
	ReopenContract(ctx context.Context, acceptanceID string, r domain.IssuanceRetry) (domain.ContractIssuance, error)
}
