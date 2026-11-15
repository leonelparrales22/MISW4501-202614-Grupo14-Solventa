package ports

import (
	"context"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

// ExpiringPolicyQuery reads the policies about to expire that the advisor
// lists (HU-W1.5-01). It is a read model over the policies and the renewals
// of HU-W1.5-02: it never writes, and the status of each row is the renewal of
// its current period as stored (decision D-4 a), so an accepted renewal whose
// charge was already confirmed reads as in progress until the sweep settles
// it.
type ExpiringPolicyQuery interface {
	// ListExpiring returns, ordered by the end of the coverage and then by
	// policy id, the page (offset, limit) of the policies whose coverage ends
	// inside [start, end) and after now and whose current period is open: no
	// renewal, or a renewal accepted and still being charged. After them come
	// the policies cancelled for non-payment after cancelledSince, whatever
	// the range (HU-W1.6-02). status narrows it to one of them ("" for all).
	// It also returns how many policies match, whatever the page. Each row
	// comes with its Policy, its Renewal (nil when none), its Status and,
	// when cancelled, its CancelledAt.
	ListExpiring(ctx context.Context, start, end, now, cancelledSince time.Time, status domain.ExpiringStatus,
		offset, limit int) ([]domain.ExpiringPolicy, int, error)
	// CountDueWithinWindow counts the policies with an open period whose
	// coverage ends after now and no later than until, with no other filter.
	CountDueWithinWindow(ctx context.Context, now, until time.Time) (int, error)
	// CountCancelledSince counts the policies cancelled for non-payment after
	// since (HU-W1.6-02).
	CountCancelledSince(ctx context.Context, since time.Time) (int, error)
}
