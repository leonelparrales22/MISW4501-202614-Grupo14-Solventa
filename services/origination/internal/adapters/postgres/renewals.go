package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// RenewalStore is the ports.RenewalRepository over the renewal table
// (HU-W1.5-02). UNIQUE (policy_id, period_end) is what keeps one outcome per
// period of a policy, whoever gets there first: the insured accepting twice,
// or accepting while the sweep closes the window.
type RenewalStore struct {
	db *sql.DB
}

var _ ports.RenewalRepository = (*RenewalStore)(nil)

// NewRenewalStore builds a RenewalStore over db (already migrated).
func NewRenewalStore(db *sql.DB) *RenewalStore { return &RenewalStore{db: db} }

const renewalColumns = `renewal_id, policy_id, customer_id, period_end, status, reason, premium_minor, currency,
	insured_amount_minor, term_months, coverages, discount_rate, requoted, charge_id, new_policy_id,
	accepted_at, retry_until, closed_at, notified_at, notification_failed, created_at`

func scanRenewal(s scanner) (domain.Renewal, error) {
	var (
		r                                      domain.Renewal
		status                                 string
		reason, chargeID, newPolicy            sql.NullString
		premiumMinor, insuredMinor             int64
		coverages                              []byte
		accepted, retryUntil, closed, notified sql.NullTime
	)
	if err := s.Scan(&r.ID, &r.PolicyID, &r.CustomerID, &r.PeriodEnd, &status, &reason, &premiumMinor, &r.Currency,
		&insuredMinor, &r.Terms.TermMonths, &coverages, &r.DiscountRate, &r.Requoted, &chargeID, &newPolicy,
		&accepted, &retryUntil, &closed, &notified, &r.NotificationFailed, &r.CreatedAt); err != nil {
		return domain.Renewal{}, err
	}
	var err error
	if r.Terms.Coverages, err = decodeCoverages(coverages); err != nil {
		return domain.Renewal{}, fmt.Errorf("decode the coverages of renewal %s: %w", r.ID, err)
	}
	r.Status, r.Reason = domain.RenewalStatus(status), domain.NotRenewedReason(reason.String)
	r.Premium, r.Terms.InsuredAmount = domain.FromMinorUnits(premiumMinor), domain.FromMinorUnits(insuredMinor)
	r.ChargeID, r.NewPolicyID = chargeID.String, newPolicy.String
	r.AcceptedAt, r.RetryUntil, r.ClosedAt, r.NotifiedAt = accepted.Time, retryUntil.Time, closed.Time, notified.Time
	r.PeriodEnd, r.CreatedAt = r.PeriodEnd.UTC(), r.CreatedAt.UTC()
	for _, t := range []*time.Time{&r.AcceptedAt, &r.RetryUntil, &r.ClosedAt, &r.NotifiedAt} {
		if !t.IsZero() {
			*t = t.UTC()
		}
	}
	return r, nil
}

func (s *RenewalStore) list(ctx context.Context, query string, args ...any) ([]domain.Renewal, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list renewals: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []domain.Renewal
	for rows.Next() {
		r, err := scanRenewal(rows)
		if err != nil {
			return nil, fmt.Errorf("read a renewal: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Create stores the renewal unless its policy and period already have one.
func (s *RenewalStore) Create(ctx context.Context, r domain.Renewal) (domain.Renewal, bool, error) {
	coverages, err := encodeCoverages(r.Terms.Coverages)
	if err != nil {
		return domain.Renewal{}, false, err
	}
	rows, err := s.list(ctx, `INSERT INTO renewal (`+renewalColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21)
		ON CONFLICT (policy_id, period_end) DO NOTHING
		RETURNING `+renewalColumns,
		r.ID, r.PolicyID, r.CustomerID, r.PeriodEnd.UTC(), string(r.Status), nullString(string(r.Reason)),
		domain.ToMinorUnits(r.Premium), r.Currency, domain.ToMinorUnits(r.Terms.InsuredAmount), r.Terms.TermMonths,
		string(coverages), r.DiscountRate, r.Requoted, nullString(r.ChargeID), nullString(r.NewPolicyID),
		nullTime(r.AcceptedAt), nullTime(r.RetryUntil), nullTime(r.ClosedAt), nullTime(r.NotifiedAt),
		r.NotificationFailed, r.CreatedAt.UTC())
	if err != nil {
		return domain.Renewal{}, false, fmt.Errorf("store renewal %s: %w", r.ID, err)
	}
	if len(rows) == 1 {
		return rows[0], true, nil
	}
	existing, err := s.Get(ctx, r.PolicyID, r.PeriodEnd)
	if err != nil {
		return domain.Renewal{}, false, err
	}
	return existing, false, nil
}

// Get returns ports.ErrRenewalNotFound when the period has no renewal.
func (s *RenewalStore) Get(ctx context.Context, policyID string, periodEnd time.Time) (domain.Renewal, error) {
	r, err := scanRenewal(s.db.QueryRowContext(ctx,
		`SELECT `+renewalColumns+` FROM renewal WHERE policy_id = $1 AND period_end = $2`, policyID, periodEnd.UTC()))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Renewal{}, ports.ErrRenewalNotFound
	}
	if err != nil {
		return domain.Renewal{}, fmt.Errorf("read the renewal of %s: %w", policyID, err)
	}
	return r, nil
}

// ListByPolicies returns the renewals of the given policies.
func (s *RenewalStore) ListByPolicies(ctx context.Context, policyIDs []string) ([]domain.Renewal, error) {
	if len(policyIDs) == 0 {
		return nil, nil
	}
	args := make([]any, len(policyIDs))
	marks := ""
	for i, id := range policyIDs {
		args[i] = id
		if i > 0 {
			marks += ", "
		}
		marks += fmt.Sprintf("$%d", i+1)
	}
	return s.list(ctx, `SELECT `+renewalColumns+` FROM renewal WHERE policy_id IN (`+marks+`)`, args...)
}

func (s *RenewalStore) change(ctx context.Context, query string, args ...any) (bool, error) {
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// MarkRenewed closes an accepted renewal as renewed.
func (s *RenewalStore) MarkRenewed(ctx context.Context, renewalID, newPolicyID string, at time.Time) (bool, error) {
	changed, err := s.change(ctx, `UPDATE renewal SET status = 'renewed', new_policy_id = $2, closed_at = $3
		WHERE renewal_id = $1 AND status = 'accepted'`, renewalID, newPolicyID, at.UTC())
	if err != nil {
		return false, fmt.Errorf("mark renewal %s renewed: %w", renewalID, err)
	}
	return changed, nil
}

// MarkNotRenewed closes an accepted renewal as not renewed.
func (s *RenewalStore) MarkNotRenewed(ctx context.Context, renewalID string, reason domain.NotRenewedReason, at time.Time) (bool, error) {
	changed, err := s.change(ctx, `UPDATE renewal SET status = 'not_renewed', reason = $2, closed_at = $3
		WHERE renewal_id = $1 AND status = 'accepted'`, renewalID, string(reason), at.UTC())
	if err != nil {
		return false, fmt.Errorf("mark renewal %s not renewed: %w", renewalID, err)
	}
	return changed, nil
}

// ListAccepted returns the renewals still waiting for their payment.
func (s *RenewalStore) ListAccepted(ctx context.Context, limit int) ([]domain.Renewal, error) {
	return s.list(ctx, `SELECT `+renewalColumns+` FROM renewal WHERE status = 'accepted' ORDER BY retry_until LIMIT $1`, limit)
}

// ListUnnotified returns the not-renewed renewals whose insured was not told.
func (s *RenewalStore) ListUnnotified(ctx context.Context, limit int) ([]domain.Renewal, error) {
	return s.list(ctx, `SELECT `+renewalColumns+` FROM renewal
		WHERE status = 'not_renewed' AND notified_at IS NULL AND NOT notification_failed
		ORDER BY created_at LIMIT $1`, limit)
}

// MarkNotified records that the insured was told, or that the notice failed.
func (s *RenewalStore) MarkNotified(ctx context.Context, renewalID string, at time.Time, failed bool) error {
	var err error
	if failed {
		_, err = s.db.ExecContext(ctx, `UPDATE renewal SET notification_failed = true WHERE renewal_id = $1`, renewalID)
	} else {
		_, err = s.db.ExecContext(ctx, `UPDATE renewal SET notified_at = $2 WHERE renewal_id = $1`, renewalID, at.UTC())
	}
	if err != nil {
		return fmt.Errorf("record the notice of renewal %s: %w", renewalID, err)
	}
	return nil
}
