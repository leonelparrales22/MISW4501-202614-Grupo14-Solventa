package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// ExpiringPolicyStore is the ports.ExpiringPolicyQuery over the policy and
// renewal tables of HU-W1.5-02 (migration 0012), for the list of the advisor
// (HU-W1.5-01). The database filters, counts and pages; it only reads.
//
// The renewal of the current period of a policy is the row of renewal with
// period_end = coverage_end (UNIQUE (policy_id, period_end)). A period is open
// when that row does not exist (expiring) or is accepted (renewal in
// progress); renewed and not_renewed periods leave the list (decision D-3 a).
// It needs no index of its own: policy_coverage_end_idx serves the range and
// the unique index of renewal the join.
type ExpiringPolicyStore struct {
	db *sql.DB
}

var _ ports.ExpiringPolicyQuery = (*ExpiringPolicyStore)(nil)

// NewExpiringPolicyStore builds an ExpiringPolicyStore over db (already migrated).
func NewExpiringPolicyStore(db *sql.DB) *ExpiringPolicyStore { return &ExpiringPolicyStore{db: db} }

// expiringFrom is the join and the condition of an open period shared by the
// list, its total and the indicator.
const expiringFrom = ` FROM policy p
	LEFT JOIN renewal r ON r.policy_id = p.policy_id AND r.period_end = p.coverage_end
	WHERE (r.renewal_id IS NULL OR r.status = 'accepted')`

// expiringFilter adds the range [$1, $2), the policies still in force at $3
// and the status $4 (an empty status for both).
const expiringFilter = expiringFrom + `
	  AND p.coverage_end >= $1 AND p.coverage_end < $2 AND p.coverage_end > $3
	  AND ($4::text = ''
	       OR ($4::text = 'expiring' AND r.renewal_id IS NULL)
	       OR ($4::text = 'renewal_in_progress' AND r.renewal_id IS NOT NULL))`

// expiringRenewalColumns are the columns of the renewal that the list shows
// (its summary), NULL when the period has no renewal.
const expiringRenewalColumns = `r.renewal_id, r.customer_id, r.period_end, r.status, r.reason, r.charge_id,
	r.new_policy_id, r.accepted_at, r.retry_until, r.closed_at`

// expiringListQuery is the page of the list: the policy columns that
// scanPolicy reads followed by the renewal summary.
var expiringListQuery = `SELECT ` + prefixed("p", policyColumns) + `, ` + expiringRenewalColumns + expiringFilter + `
	ORDER BY p.coverage_end, p.policy_id
	LIMIT $5 OFFSET $6`

// withColumns is a scanner that appends more destinations to the ones of the
// scanner it wraps, so scanPolicy reads the policy columns of a row that has
// more.
type withColumns struct {
	row   scanner
	extra []any
}

func (w withColumns) Scan(dest ...any) error { return w.row.Scan(append(dest, w.extra...)...) }

// ListExpiring returns the page of open periods ending in [start, end) and
// after now, and the total that match, read in one snapshot so they agree.
func (s *ExpiringPolicyStore) ListExpiring(ctx context.Context, start, end, now time.Time, status domain.ExpiringStatus,
	offset, limit int) ([]domain.ExpiringPolicy, int, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, 0, fmt.Errorf("list the expiring policies: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	args := []any{start.UTC(), end.UTC(), now.UTC(), string(status)}
	rows, err := tx.QueryContext(ctx, expiringListQuery, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("list the expiring policies: %w", err)
	}
	items, err := scanExpiring(rows)
	if err != nil {
		return nil, 0, err
	}

	var total int
	if err := tx.QueryRowContext(ctx, `SELECT count(*)`+expiringFilter, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count the expiring policies: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, 0, fmt.Errorf("list the expiring policies: %w", err)
	}
	return items, total, nil
}

func scanExpiring(rows *sql.Rows) ([]domain.ExpiringPolicy, error) {
	defer func() { _ = rows.Close() }()
	out := []domain.ExpiringPolicy{}
	for rows.Next() {
		var (
			renewalID, customerID, status, reason, chargeID, newPolicy sql.NullString
			periodEnd, accepted, retryUntil, closed                    sql.NullTime
		)
		p, err := scanPolicy(withColumns{row: rows, extra: []any{&renewalID, &customerID, &periodEnd, &status, &reason,
			&chargeID, &newPolicy, &accepted, &retryUntil, &closed}})
		if err != nil {
			return nil, fmt.Errorf("read an expiring policy: %w", err)
		}
		item := domain.ExpiringPolicy{Policy: p}
		if renewalID.Valid {
			item.Renewal = &domain.Renewal{
				ID: renewalID.String, PolicyID: p.ID, CustomerID: customerID.String, PeriodEnd: utc(periodEnd),
				Status: domain.RenewalStatus(status.String), Reason: domain.NotRenewedReason(reason.String),
				ChargeID: chargeID.String, NewPolicyID: newPolicy.String,
				AcceptedAt: utc(accepted), RetryUntil: utc(retryUntil), ClosedAt: utc(closed),
			}
		}
		var listed bool
		if item.Status, listed = domain.ExpiringStatusOf(item.Renewal); !listed {
			return nil, fmt.Errorf("read an expiring policy: policy %s has a closed period", p.ID)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// CountDueWithinWindow counts the open periods ending in (now, until].
func (s *ExpiringPolicyStore) CountDueWithinWindow(ctx context.Context, now, until time.Time) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*)`+expiringFrom+`
		  AND p.coverage_end > $1 AND p.coverage_end <= $2`, now.UTC(), until.UTC()).Scan(&n); err != nil {
		return 0, fmt.Errorf("count the policies due within the renewal window: %w", err)
	}
	return n, nil
}
