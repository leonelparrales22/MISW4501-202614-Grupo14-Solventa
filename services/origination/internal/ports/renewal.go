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

// Errors of the assisted renewal (HU-W1.5-03).
var (
	// ErrRenewalRequoteNotFound is returned when a requote id is unknown, or
	// when the period of a policy has no requote yet.
	ErrRenewalRequoteNotFound = errors.New("renewal requote not found")
	// ErrRenewalFormalizationNotFound is returned when a renewal was not
	// formalized by an advisor (the insured renewed it online).
	ErrRenewalFormalizationNotFound = errors.New("renewal formalization not found")
)

// RenewalRequoteRepository keeps the requotes advisors make of a renewal. There
// is deliberately no Update or Delete: a requote is a snapshot the
// formalization cites.
type RenewalRequoteRepository interface {
	// Create stores the requote.
	Create(ctx context.Context, r domain.RenewalRequote) error
	// Get returns ErrRenewalRequoteNotFound for an unknown id.
	Get(ctx context.Context, requoteID string) (domain.RenewalRequote, error)
	// Latest returns the last requote of the period of a policy, or
	// ErrRenewalRequoteNotFound when it has none.
	Latest(ctx context.Context, policyID string, periodEnd time.Time) (domain.RenewalRequote, error)
}

// AssistedRenewalRepository records the renewals advisors formalize.
type AssistedRenewalRepository interface {
	// CreateAssisted stores the renewal and its formalization in one
	// transaction, unless the policy and period already have a renewal; then
	// it stores nothing and returns the existing renewal with created=false,
	// and its formalization (nil when the insured renewed online). Two
	// advisors, or an advisor and the insured, get a single outcome.
	CreateAssisted(ctx context.Context, r domain.Renewal, f domain.RenewalFormalization) (stored domain.Renewal,
		formalization *domain.RenewalFormalization, created bool, err error)
	// GetFormalization returns ErrRenewalFormalizationNotFound when the
	// renewal was not formalized by an advisor.
	GetFormalization(ctx context.Context, renewalID string) (domain.RenewalFormalization, error)
}
