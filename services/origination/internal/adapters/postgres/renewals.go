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

var (
	_ ports.RenewalRepository         = (*RenewalStore)(nil)
	_ ports.AssistedRenewalRepository = (*RenewalStore)(nil)
)

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
	return listRenewals(ctx, s.db, query, args...)
}

func listRenewals(ctx context.Context, db querier, query string, args ...any) ([]domain.Renewal, error) {
	rows, err := db.QueryContext(ctx, query, args...)
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
	rows, err := insertRenewal(ctx, s.db, r)
	if err != nil {
		return domain.Renewal{}, false, err
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

// insertRenewal inserts the renewal unless its policy and period already have
// one, and returns the inserted row (none on a conflict). The conflict has no
// target, so every unique index arbitrates it: UNIQUE (policy_id, period_end)
// and the primary key renewal_id, which is derived from the same policy and
// period. Two inserts of one period that race past the first check therefore
// end in "no row" instead of a duplicate key error on renewal_pkey.
func insertRenewal(ctx context.Context, db querier, r domain.Renewal) ([]domain.Renewal, error) {
	coverages, err := encodeCoverages(r.Terms.Coverages)
	if err != nil {
		return nil, err
	}
	rows, err := listRenewals(ctx, db, `INSERT INTO renewal (`+renewalColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21)
		ON CONFLICT DO NOTHING
		RETURNING `+renewalColumns,
		r.ID, r.PolicyID, r.CustomerID, r.PeriodEnd.UTC(), string(r.Status), nullString(string(r.Reason)),
		domain.ToMinorUnits(r.Premium), r.Currency, domain.ToMinorUnits(r.Terms.InsuredAmount), r.Terms.TermMonths,
		string(coverages), r.DiscountRate, r.Requoted, nullString(r.ChargeID), nullString(r.NewPolicyID),
		nullTime(r.AcceptedAt), nullTime(r.RetryUntil), nullTime(r.ClosedAt), nullTime(r.NotifiedAt),
		r.NotificationFailed, r.CreatedAt.UTC())
	if err != nil {
		return nil, fmt.Errorf("store renewal %s: %w", r.ID, err)
	}
	return rows, nil
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

// renewal_formalization (HU-W1.5-03, migration 0014): the record of a renewal
// an advisor formalized, written with its renewal row.

const renewalFormalizationColumns = `renewal_id, requote_id, advisor_id, advisor_email, advisor_name,
	consent_method, accepted_at, consent_reference, formalized_at`

const insertRenewalFormalization = `INSERT INTO renewal_formalization (` + renewalFormalizationColumns + `)
	VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`

const selectRenewalFormalization = `SELECT ` + renewalFormalizationColumns + ` FROM renewal_formalization WHERE renewal_id = $1`

// CreateAssisted stores the renewal and its formalization in one transaction,
// unless the policy and period already have a renewal. UNIQUE (policy_id,
// period_end) decides who wins without locking: two advisors, or an advisor
// and the insured renewing online, produce one renewal (EC-D02).
func (s *RenewalStore) CreateAssisted(ctx context.Context, r domain.Renewal, f domain.RenewalFormalization) (
	domain.Renewal, *domain.RenewalFormalization, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Renewal{}, nil, false, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := insertRenewal(ctx, tx, r)
	if err != nil {
		return domain.Renewal{}, nil, false, err
	}
	if len(rows) == 0 {
		_ = tx.Rollback()
		existing, err := s.Get(ctx, r.PolicyID, r.PeriodEnd)
		if err != nil {
			return domain.Renewal{}, nil, false, err
		}
		stored, err := s.GetFormalization(ctx, existing.ID)
		switch {
		case errors.Is(err, ports.ErrRenewalFormalizationNotFound):
			return existing, nil, false, nil
		case err != nil:
			return domain.Renewal{}, nil, false, err
		}
		return existing, &stored, false, nil
	}
	if _, err := tx.ExecContext(ctx, insertRenewalFormalization,
		f.RenewalID, f.RequoteID, f.AdvisorID, f.AdvisorEmail, f.AdvisorName,
		string(f.Acceptance.Method), f.Acceptance.AcceptedAt.UTC(), f.Acceptance.Reference, f.FormalizedAt.UTC()); err != nil {
		return domain.Renewal{}, nil, false, fmt.Errorf("store the formalization of renewal %s: %w", r.ID, err)
	}
	if err := tx.Commit(); err != nil {
		return domain.Renewal{}, nil, false, fmt.Errorf("commit: %w", err)
	}
	return rows[0], &f, true, nil
}

// GetFormalization returns the formalization of the renewal.
func (s *RenewalStore) GetFormalization(ctx context.Context, renewalID string) (domain.RenewalFormalization, error) {
	var f domain.RenewalFormalization
	var method string
	err := s.db.QueryRowContext(ctx, selectRenewalFormalization, renewalID).Scan(
		&f.RenewalID, &f.RequoteID, &f.AdvisorID, &f.AdvisorEmail, &f.AdvisorName,
		&method, &f.Acceptance.AcceptedAt, &f.Acceptance.Reference, &f.FormalizedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.RenewalFormalization{}, ports.ErrRenewalFormalizationNotFound
	}
	if err != nil {
		return domain.RenewalFormalization{}, fmt.Errorf("read the formalization of renewal %s: %w", renewalID, err)
	}
	f.Acceptance.Method = domain.ConsentMethod(method)
	f.Acceptance.AcceptedAt, f.FormalizedAt = f.Acceptance.AcceptedAt.UTC(), f.FormalizedAt.UTC()
	return f, nil
}
