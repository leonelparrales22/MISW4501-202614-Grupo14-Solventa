package ports

import (
	"context"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

// PartnerActivityQuery counts what a commercial partner did through the
// embedded channel (HU-W3.2-01). It is a read model over the offers, the
// acceptances and the policies: it never writes.
type PartnerActivityQuery interface {
	// CountPartnerActivity counts, for partnerID only, the quotes it requested
	// in [start, end) (its embedded offers, without the ones the advisor
	// adjusted or the renewals) and the new policies issued in [start, end)
	// from its offers (renewals excluded).
	CountPartnerActivity(ctx context.Context, partnerID string, start, end time.Time) (domain.PartnerActivity, error)
}
