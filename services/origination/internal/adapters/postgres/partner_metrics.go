package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// PartnerActivityStore is the ports.PartnerActivityQuery over the offer,
// acceptance and policy tables (HU-W3.2-01, migration 0016). Every count is
// filtered by the partner in SQL, so the rows of another partner never leave
// the database (CA-4).
//
//   - A quote requested is an embedded offer of the partner created in the
//     range. The offer the advisor adjusts (supersedes) and the renewal of an
//     offer (renews) are not new requests of the partner.
//   - A policy issued is a new policy (renewal_of IS NULL) whose coverage
//     started in the range, reached from its acceptance to the offer of the
//     partner. For a new policy the coverage starts when the premium is
//     confirmed, which is when it is issued.
type PartnerActivityStore struct {
	db *sql.DB
}

var _ ports.PartnerActivityQuery = (*PartnerActivityStore)(nil)

// NewPartnerActivityStore builds a PartnerActivityStore over db (already migrated).
func NewPartnerActivityStore(db *sql.DB) *PartnerActivityStore { return &PartnerActivityStore{db: db} }

// partnerActivityQuery reads both counts in one statement, so they come from
// the same snapshot.
const partnerActivityQuery = `SELECT
	(SELECT count(*) FROM offer o
	  WHERE o.partner_id = $1 AND o.channel = 'embedded' AND o.supersedes IS NULL AND o.renews IS NULL
	    AND o.created_at >= $2 AND o.created_at < $3),
	(SELECT count(*) FROM policy p
	   JOIN acceptance a ON a.acceptance_id = p.acceptance_id
	   JOIN offer o ON o.offer_id = a.offer_id
	  WHERE o.partner_id = $1 AND o.channel = 'embedded' AND p.renewal_of IS NULL
	    AND p.coverage_start >= $2 AND p.coverage_start < $3)`

// CountPartnerActivity counts the quotes and the policies of partnerID in
// [start, end).
func (s *PartnerActivityStore) CountPartnerActivity(ctx context.Context, partnerID string, start, end time.Time) (domain.PartnerActivity, error) {
	var a domain.PartnerActivity
	if err := s.db.QueryRowContext(ctx, partnerActivityQuery, partnerID, start.UTC(), end.UTC()).
		Scan(&a.QuotesRequested, &a.PoliciesIssued); err != nil {
		return domain.PartnerActivity{}, fmt.Errorf("count the activity of the partner: %w", err)
	}
	return a, nil
}
