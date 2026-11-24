package ports

import (
	"context"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

// HU-M3.2-01: the alerts 30, 15 and 5 days before a policy expires.

// ExpiringOwnPolicies lists the policies origination issued whose coverage
// ends in (after, before) and that the insured has not acted on: no renewal of
// that period exists (accepted, renewed or not renewed) and no policy renews
// them. They come ordered by the end of their coverage.
type ExpiringOwnPolicies interface {
	ListExpiringUnrenewed(ctx context.Context, after, before time.Time) ([]domain.Policy, error)
}

// LinkedHolders lists the subjects of the customers that have an account: the
// holders the policy core is asked about.
type LinkedHolders interface {
	LinkedSubjects(ctx context.Context) ([]string, error)
}

// ExpiryAlertRecorder records an alert and writes its PolicyExpiryApproaching
// event to the outbox in the same transaction. It answers false, and writes
// nothing, when the alert of that policy, period and milestone was already
// recorded: several tasks sweep at once and a milestone is alerted once.
type ExpiryAlertRecorder interface {
	RecordExpiryAlert(ctx context.Context, e domain.PolicyExpiryApproaching) (bool, error)
}
