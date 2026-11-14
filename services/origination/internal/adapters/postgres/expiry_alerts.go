package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// HU-M3.2-01: the alerts 30, 15 and 5 days before a policy expires.

var (
	_ ports.ExpiringOwnPolicies = (*PolicyStore)(nil)
	_ ports.LinkedHolders       = (*CustomerStore)(nil)
	_ ports.ExpiryAlertRecorder = (*ExpiryAlertStore)(nil)
)

// ListExpiringUnrenewed returns the policies whose coverage ends in (after,
// before) with no renewal row for that period, whatever its status, and no
// policy that renews them.
func (s *PolicyStore) ListExpiringUnrenewed(ctx context.Context, after, before time.Time) ([]domain.Policy, error) {
	return s.list(ctx, `SELECT `+prefixed("p", policyColumns)+` FROM policy p
		WHERE p.coverage_end > $1 AND p.coverage_end < $2
		  AND NOT EXISTS (SELECT 1 FROM renewal r WHERE r.policy_id = p.policy_id AND r.period_end = p.coverage_end)
		  AND NOT EXISTS (SELECT 1 FROM policy n WHERE n.renewal_of = p.policy_id)
		ORDER BY p.coverage_end, p.policy_id`, after.UTC(), before.UTC())
}

// LinkedSubjects returns the subjects of the customers with an account.
func (s *CustomerStore) LinkedSubjects(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT cognito_sub FROM customer WHERE cognito_sub IS NOT NULL ORDER BY cognito_sub`)
	if err != nil {
		return nil, fmt.Errorf("list the linked customers: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var sub string
		if err := rows.Scan(&sub); err != nil {
			return nil, fmt.Errorf("read a linked customer: %w", err)
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}

// ExpiryAlertStore is the ports.ExpiryAlertRecorder over the
// policy_expiry_notice table (migration 0015) and the outbox.
type ExpiryAlertStore struct {
	db *sql.DB
}

// NewExpiryAlertStore builds the store over an open, migrated database.
func NewExpiryAlertStore(db *sql.DB) *ExpiryAlertStore { return &ExpiryAlertStore{db: db} }

// RecordExpiryAlert writes the alert and its event in one transaction. An
// alert of the same policy, period and milestone already there leaves both
// untouched and answers false.
func (s *ExpiryAlertStore) RecordExpiryAlert(ctx context.Context, e domain.PolicyExpiryApproaching) (bool, error) {
	env, err := e.Envelope()
	if err != nil {
		return false, fmt.Errorf("build the PolicyExpiryApproaching event: %w", err)
	}
	body, err := env.Encode()
	if err != nil {
		return false, fmt.Errorf("encode the PolicyExpiryApproaching event: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("record the expiry alert of policy %s: %w", e.PolicyID, err)
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `INSERT INTO policy_expiry_notice
			(policy_id, period_end, milestone_days, source, subject, days_left, event_id, detected_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT DO NOTHING`,
		e.PolicyID, e.CoverageEnd.UTC(), e.MilestoneDays, string(e.Source), e.Subject, e.DaysLeft, env.EventID,
		e.DetectedAt.UTC())
	if err != nil {
		return false, fmt.Errorf("record the expiry alert of policy %s: %w", e.PolicyID, err)
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return false, err
	}
	if err := insertOutboxEvent(ctx, tx, env.EventID, env.EventType, body, env.OccurredAt); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("record the expiry alert of policy %s: %w", e.PolicyID, err)
	}
	return true, nil
}
