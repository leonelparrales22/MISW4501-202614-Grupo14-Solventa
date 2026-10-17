package ports

import (
	"context"
	"errors"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

// ErrChargeNotFound is returned by a ChargeRepository that has no charge for
// the requested acceptance.
var ErrChargeNotFound = errors.New("charge not found")

// ErrChargeNotResumable is returned when a charge that is not failed is asked
// to resume (or two analysts resume it at once and the other one won).
var ErrChargeNotResumable = errors.New("charge not resumable")

// ChargeRepository persists the premium charges (HA-D02): one per acceptance,
// in origination's own database. The idempotent mechanism of pkg/idempotency
// claims, confirms, reschedules and fails them; every write that records a
// result is conditional on the claim (idempotency.ErrClaimLost otherwise).
type ChargeRepository interface {
	idempotency.Store[domain.Charge]

	// CreatePending stores the charge unless its acceptance already has one;
	// then it returns the existing charge and created=false.
	CreatePending(ctx context.Context, c domain.Charge) (stored domain.Charge, created bool, err error)
	// Get returns ErrChargeNotFound when the acceptance has no charge.
	Get(ctx context.Context, acceptanceID string) (domain.Charge, error)
	// ListDue returns up to limit acceptance ids whose charge is due at now:
	// pending and due, or in progress with an expired lease.
	ListDue(ctx context.Context, now time.Time, limit int) ([]string, error)
	// ListExceptions returns up to limit failed charges, the latest failure first.
	ListExceptions(ctx context.Context, limit int) ([]domain.Charge, error)
	// Resume moves a failed charge back to pending with the same idempotency
	// key and a new retry window. It returns ErrChargeNotFound or
	// ErrChargeNotResumable (not failed, or another resumption won).
	Resume(ctx context.Context, acceptanceID string, r domain.ChargeResumption) (domain.Charge, error)

	// ClaimActivation holds a confirmed charge whose activation is not
	// published yet and is due at now, with a lease until now+lease, so only
	// one worker publishes it at a time. claimed=false when it is not due,
	// already published or held by another worker.
	ClaimActivation(ctx context.Context, acceptanceID string, now time.Time, lease time.Duration) (c domain.Charge, claimed bool, err error)
	// MarkActivationPublished records that the activation was handed to its
	// consumers and releases the claim.
	MarkActivationPublished(ctx context.Context, c domain.Charge, now time.Time) (domain.Charge, error)
	// RescheduleActivation counts a failed publication, releases the claim and
	// makes the publication due at next.
	RescheduleActivation(ctx context.Context, c domain.Charge, now, next time.Time, cause string) (domain.Charge, error)
	// ListActivationsDue returns up to limit acceptance ids whose activation
	// is confirmed, unpublished, due and not held.
	ListActivationsDue(ctx context.Context, now time.Time, limit int) ([]string, error)
}

// PaymentGateway charges the premium of a charge with its idempotency key. Its
// errors wrap idempotency.ErrRejected (declined: final),
// idempotency.ErrAmbiguous (unknown outcome: retry with the same key) or
// idempotency.ErrNotSent (never sent).
type PaymentGateway interface {
	Charge(ctx context.Context, c domain.Charge) (providerReference string, err error)
}
