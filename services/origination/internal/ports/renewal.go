package ports

import (
	"context"
	"errors"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

// ErrPolicyNotFound is returned by a PolicyRepository that has no policy with
// the requested id.
var ErrPolicyNotFound = errors.New("policy not found")

// ErrRenewalNotFound is returned by a RenewalRepository that has no renewal
// for the requested policy and period.
var ErrRenewalNotFound = errors.New("renewal not found")

// PolicyRepository keeps the policies the insured sees in the portal
// (HU-W1.5-02). It implements CoverageActivationHandler through Receive, so
// the activation of a coverage projects its policy.
type PolicyRepository interface {
	// Create stores the policy unless it already exists; it is idempotent by
	// policy id, so a redelivered activation changes nothing.
	Create(ctx context.Context, p domain.Policy) error
	// Get returns ErrPolicyNotFound when the id is unknown.
	Get(ctx context.Context, policyID string) (domain.Policy, error)
	// ListByCustomer returns the policies of an insured, the ones ending last first.
	ListByCustomer(ctx context.Context, customerID string) ([]domain.Policy, error)
	// ListLapsed returns up to limit policies whose coverage ended between
	// since and now (both inclusive) and whose period has no renewal at all
	// (neither accepted nor lapsed).
	ListLapsed(ctx context.Context, since, now time.Time, limit int) ([]domain.Policy, error)
}

// RenewalRepository keeps the renewal of each period of each policy.
type RenewalRepository interface {
	// Create stores the renewal unless its policy and period already have one,
	// in which case it returns the existing renewal and created=false.
	Create(ctx context.Context, r domain.Renewal) (stored domain.Renewal, created bool, err error)
	// Get returns ErrRenewalNotFound when the period has no renewal.
	Get(ctx context.Context, policyID string, periodEnd time.Time) (domain.Renewal, error)
	// ListByPolicies returns the renewals of the given policies.
	ListByPolicies(ctx context.Context, policyIDs []string) ([]domain.Renewal, error)
	// MarkRenewed closes an accepted renewal as renewed, once its charge was
	// confirmed. It reports whether it changed anything.
	MarkRenewed(ctx context.Context, renewalID, newPolicyID string, at time.Time) (bool, error)
	// MarkNotRenewed closes an accepted renewal as not renewed. It reports
	// whether it changed anything.
	MarkNotRenewed(ctx context.Context, renewalID string, reason domain.NotRenewedReason, at time.Time) (bool, error)
	// ListAccepted returns up to limit renewals still waiting for their payment.
	ListAccepted(ctx context.Context, limit int) ([]domain.Renewal, error)
	// ListUnnotified returns up to limit not-renewed renewals whose insured was
	// not told yet.
	ListUnnotified(ctx context.Context, limit int) ([]domain.Renewal, error)
	// MarkNotified records that the insured was told, or that the notice
	// could not be delivered (failed).
	MarkNotified(ctx context.Context, renewalID string, at time.Time, failed bool) error
}

// RenewalNotifier tells the insured their policy was not renewed.
type RenewalNotifier interface {
	Notify(ctx context.Context, n domain.RenewalNotice) error
}

// CustomerBySubject finds the customer registered under the subject of an
// account, to know the birth date a renewal is re-priced with.
type CustomerBySubject interface {
	// GetBySubject returns ErrCustomerNotFound when the subject has no customer.
	GetBySubject(ctx context.Context, subject string) (domain.Customer, error)
}
