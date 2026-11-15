package postgres

import (
	"context"
	"fmt"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

var _ ports.OpenClaimsReader = (*ClaimRecordStore)(nil)

// OpenClaims counts the claims of the policy that are not closed (HU-W1.4-02).
// The database counts them by status (index claim_policy_status_idx, 0013) and
// the rule of the service tells which statuses are closed, so a new status
// counts as in progress without touching this query.
func (s *ClaimRecordStore) OpenClaims(ctx context.Context, policyID string) (int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT status, count(*) FROM claim WHERE policy_id = $1 GROUP BY status`, policyID)
	if err != nil {
		return 0, fmt.Errorf("count the claims of policy %s: %w", policyID, err)
	}
	defer func() { _ = rows.Close() }()
	open := 0
	for rows.Next() {
		var (
			status string
			n      int
		)
		if err := rows.Scan(&status, &n); err != nil {
			return 0, fmt.Errorf("count the claims of policy %s: %w", policyID, err)
		}
		if !domain.Closed(domain.Status(status)) {
			open += n
		}
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("count the claims of policy %s: %w", policyID, err)
	}
	return open, nil
}
