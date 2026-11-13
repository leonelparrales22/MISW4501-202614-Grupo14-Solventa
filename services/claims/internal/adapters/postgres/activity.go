package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

var _ ports.ClaimActivity = (*ClaimRecordStore)(nil)

// LastChanges reads when each claim reached its current status, from the
// history the trigger of 0007 records (HU-W2.1-02).
func (s *ClaimRecordStore) LastChanges(ctx context.Context, ids []string) (map[string]time.Time, error) {
	out := make(map[string]time.Time, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT claim_id, max(changed_at) FROM claim_status_change
		WHERE claim_id = ANY($1) GROUP BY claim_id`, ids)
	if err != nil {
		return nil, fmt.Errorf("read the last change of the claims: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			id string
			at time.Time
		)
		if err := rows.Scan(&id, &at); err != nil {
			return nil, fmt.Errorf("read the last change of the claims: %w", err)
		}
		out[id] = at.UTC()
	}
	return out, rows.Err()
}

// Tally counts the subject's claims by status, the ones paid since then with
// what they paid, and names the ones returned to the subject (screen A7).
func (s *ClaimRecordStore) Tally(ctx context.Context, subject string, since time.Time) (domain.ClaimTally, error) {
	t := domain.ClaimTally{ByStatus: map[domain.Status]int{}}
	rows, err := s.db.QueryContext(ctx, `SELECT status, count(*) FROM claim WHERE cognito_sub = $1 GROUP BY status`,
		subject)
	if err != nil {
		return domain.ClaimTally{}, fmt.Errorf("count the claims of the insured: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			status string
			n      int
		)
		if err := rows.Scan(&status, &n); err != nil {
			return domain.ClaimTally{}, fmt.Errorf("count the claims of the insured: %w", err)
		}
		t.ByStatus[domain.Status(status)] = n
	}
	if err := rows.Err(); err != nil {
		return domain.ClaimTally{}, fmt.Errorf("count the claims of the insured: %w", err)
	}

	var currency sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT count(*), COALESCE(sum(d.payout_amount_minor), 0), max(d.currency)
		FROM claim c JOIN claim_decision d ON d.claim_id = c.id
		WHERE c.cognito_sub = $1 AND c.status = 'paid' AND d.paid_at >= $2`, subject, since.UTC()).
		Scan(&t.PaidSince, &t.PaidAmountMinor, &currency); err != nil {
		return domain.ClaimTally{}, fmt.Errorf("add up the claims paid: %w", err)
	}
	t.Currency = currency.String

	returned, err := s.db.QueryContext(ctx, `SELECT id, filing_number FROM claim
		WHERE cognito_sub = $1 AND status = $2 ORDER BY filed_at DESC, id DESC LIMIT $3`,
		subject, string(domain.StatusReturned), domain.MaxPendingAction)
	if err != nil {
		return domain.ClaimTally{}, fmt.Errorf("read the claims returned to the insured: %w", err)
	}
	defer func() { _ = returned.Close() }()
	for returned.Next() {
		var r domain.ClaimRef
		if err := returned.Scan(&r.ID, &r.FilingNumber); err != nil {
			return domain.ClaimTally{}, fmt.Errorf("read the claims returned to the insured: %w", err)
		}
		t.PendingAction = append(t.PendingAction, r)
	}
	if err := returned.Err(); err != nil {
		return domain.ClaimTally{}, fmt.Errorf("read the claims returned to the insured: %w", err)
	}
	if t.Policies, err = s.policiesOf(ctx, subject); err != nil {
		return domain.ClaimTally{}, err
	}
	return t, nil
}

// policiesOf are the policies the subject filed on, by id, each with the
// newest copy a claim kept of it.
func (s *ClaimRecordStore) policiesOf(ctx context.Context, subject string) ([]domain.PolicyRef, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT policy_id,
			(array_agg(policy_snapshot ORDER BY filed_at DESC) FILTER (WHERE policy_snapshot IS NOT NULL))[1]
		FROM claim WHERE cognito_sub = $1 GROUP BY policy_id ORDER BY policy_id`, subject)
	if err != nil {
		return nil, fmt.Errorf("read the policies of the insured's claims: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []domain.PolicyRef
	for rows.Next() {
		var (
			ref      domain.PolicyRef
			snapshot []byte
		)
		if err := rows.Scan(&ref.ID, &snapshot); err != nil {
			return nil, fmt.Errorf("read the policies of the insured's claims: %w", err)
		}
		p, err := decodePolicy(snapshot)
		if err != nil {
			return nil, err
		}
		if p != nil {
			ref.Line, ref.Product, ref.InsuredObject = p.Line, p.Product, p.InsuredObject
		}
		out = append(out, ref)
	}
	return out, rows.Err()
}
