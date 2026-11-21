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

// HU-W1.6-02: the monthly instalments and the cancellation for non-payment
// (migration 0017).

// InstallmentStore is the ports.InstallmentStore over premium_installment,
// premium_installment_attempt and policy_cancellation, and the payment method
// of the charge of the issuance (ports.PaymentMethodReader).
type InstallmentStore struct {
	db *sql.DB
}

var (
	_ ports.InstallmentStore    = (*InstallmentStore)(nil)
	_ ports.PaymentMethodReader = (*InstallmentStore)(nil)
)

// NewInstallmentStore builds the store over an open, migrated database.
func NewInstallmentStore(db *sql.DB) *InstallmentStore { return &InstallmentStore{db: db} }

const installmentColumns = `i.policy_id, i.seq, i.due_at, i.grace_ends_at, i.cancels_at, i.amount_minor, i.currency,
	i.status, i.attempts, i.next_attempt_at, i.paid_at, i.provider_reference, i.created_at`

// installmentDest are the destinations of installmentColumns.
type installmentDest struct {
	i                  domain.Installment
	status             string
	nextAttempt, paid  sql.NullTime
	providerReference  sql.NullString
	due, grace, cancel time.Time
	created            time.Time
}

func (d *installmentDest) ptrs() []any {
	return []any{&d.i.PolicyID, &d.i.Seq, &d.due, &d.grace, &d.cancel, &d.i.AmountMinor, &d.i.Currency, &d.status,
		&d.i.Attempts, &d.nextAttempt, &d.paid, &d.providerReference, &d.created}
}

func (d *installmentDest) installment() domain.Installment {
	i := d.i
	i.Status = domain.InstallmentStatus(d.status)
	i.DueAt, i.GraceEndsAt, i.CancelsAt, i.CreatedAt = d.due.UTC(), d.grace.UTC(), d.cancel.UTC(), d.created.UTC()
	i.NextAttemptAt, i.PaidAt = utc(d.nextAttempt), utc(d.paid)
	i.ProviderReference = d.providerReference.String
	return i
}

// BillingStart reads when the instalments started to be billed.
func (s *InstallmentStore) BillingStart(ctx context.Context) (time.Time, error) {
	var at time.Time
	if err := s.db.QueryRowContext(ctx, `SELECT started_at FROM installment_billing`).Scan(&at); err != nil {
		return time.Time{}, fmt.Errorf("read when the instalments started to be billed: %w", err)
	}
	return at.UTC(), nil
}

// ListBillable returns the policies in force at now that are not cancelled
// and have no month pending. HU-W1.4-02: it reads the view policy_current, so
// the next month is billed with the premium of the last endorsement
// (adjustment A-10); the months already billed keep theirs.
func (s *InstallmentStore) ListBillable(ctx context.Context, now time.Time) ([]domain.Policy, error) {
	ps := &PolicyStore{db: s.db}
	return ps.list(ctx, `SELECT `+prefixed("p", policyColumns)+` FROM policy_current p
		WHERE p.coverage_start <= $1 AND p.coverage_end > $1
		  AND NOT EXISTS (SELECT 1 FROM policy_cancellation c WHERE c.policy_id = p.policy_id)
		  AND NOT EXISTS (SELECT 1 FROM premium_installment i WHERE i.policy_id = p.policy_id AND i.status = 'pending')
		ORDER BY p.policy_id`, now.UTC())
}

// CreateInstallment stores the instalment once per policy and month.
func (s *InstallmentStore) CreateInstallment(ctx context.Context, i domain.Installment) (bool, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO premium_installment
			(policy_id, seq, due_at, grace_ends_at, cancels_at, amount_minor, currency, status, attempts,
			 next_attempt_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'pending', 0, $8, $9)
		ON CONFLICT (policy_id, seq) DO NOTHING`,
		i.PolicyID, i.Seq, i.DueAt.UTC(), i.GraceEndsAt.UTC(), i.CancelsAt.UTC(), i.AmountMinor, i.Currency,
		nullTime(i.NextAttemptAt), i.CreatedAt.UTC())
	if err != nil {
		return false, fmt.Errorf("store month %d of policy %s: %w", i.Seq, i.PolicyID, err)
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// held is the condition of an instalment no task holds at $1.
const notHeld = `(i.claimed_until IS NULL OR i.claimed_until <= $1)`

// ClaimDueAttempts holds the pending instalments whose attempt is due.
func (s *InstallmentStore) ClaimDueAttempts(ctx context.Context, now time.Time, lease time.Duration,
	limit int) ([]ports.PolicyInstallment, error) {
	rows, err := s.db.QueryContext(ctx, `WITH due AS (
			SELECT i.policy_id, i.seq FROM premium_installment i
			WHERE i.status = 'pending' AND i.next_attempt_at <= $1 AND `+notHeld+`
			  -- HU-W1.6-01: never charge a month of a policy already cancelled.
			  AND NOT EXISTS (SELECT 1 FROM policy_cancellation c WHERE c.policy_id = i.policy_id)
			ORDER BY i.next_attempt_at, i.policy_id, i.seq
			LIMIT $3
			FOR UPDATE SKIP LOCKED)
		UPDATE premium_installment i SET claimed_until = $2
		FROM due WHERE i.policy_id = due.policy_id AND i.seq = due.seq
		RETURNING `+installmentColumns, now.UTC(), now.Add(lease).UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("hold the instalments due: %w", err)
	}
	held, err := scanInstallments(rows)
	if err != nil {
		return nil, err
	}
	return s.withPolicies(ctx, held)
}

// withPolicies pairs each instalment with its policy.
func (s *InstallmentStore) withPolicies(ctx context.Context, items []domain.Installment) ([]ports.PolicyInstallment, error) {
	out := make([]ports.PolicyInstallment, 0, len(items))
	ps := &PolicyStore{db: s.db}
	for _, i := range items {
		p, err := ps.Get(ctx, i.PolicyID)
		if err != nil {
			return nil, err
		}
		out = append(out, ports.PolicyInstallment{Policy: p, Installment: i})
	}
	return out, nil
}

func scanInstallments(rows *sql.Rows) ([]domain.Installment, error) {
	defer func() { _ = rows.Close() }()
	var out []domain.Installment
	for rows.Next() {
		var d installmentDest
		if err := rows.Scan(d.ptrs()...); err != nil {
			return nil, fmt.Errorf("read an instalment: %w", err)
		}
		out = append(out, d.installment())
	}
	return out, rows.Err()
}

// insertAttempt records an answer of the gateway.
func insertAttempt(ctx context.Context, tx *sql.Tx, i domain.Installment, a domain.InstallmentAttempt) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO premium_installment_attempt
			(policy_id, seq, attempt, idempotency_key, outcome, decline_code, provider_reference, attempted_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		i.PolicyID, i.Seq, a.Attempt, a.IdempotencyKey, string(a.Outcome), nullString(a.DeclineCode),
		nullString(a.ProviderReference), a.AttemptedAt.UTC())
	if err != nil {
		return fmt.Errorf("record attempt %d of month %d of policy %s: %w", a.Attempt, i.Seq, i.PolicyID, err)
	}
	return nil
}

// update runs the conditional update of an instalment the task still holds
// (pending, with the same attempts) and the attempt in one transaction.
func (s *InstallmentStore) update(ctx context.Context, i domain.Installment, a *domain.InstallmentAttempt,
	query string, args ...any) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, query, append([]any{i.PolicyID, i.Seq, i.Attempts}, args...)...)
	if err != nil {
		return fmt.Errorf("update month %d of policy %s: %w", i.Seq, i.PolicyID, err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ports.ErrInstallmentClaimLost
	}
	if a != nil {
		if err := insertAttempt(ctx, tx, i, *a); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// stillHeld is the condition of the instalment $1/$2 the task holds with $3
// attempts.
const stillHeld = ` WHERE policy_id = $1 AND seq = $2 AND attempts = $3 AND status = 'pending'`

// RecordApproved records the payment.
func (s *InstallmentStore) RecordApproved(ctx context.Context, i domain.Installment, a domain.InstallmentAttempt) error {
	return s.update(ctx, i, &a, `UPDATE premium_installment SET status = 'paid', paid_at = $4,
			provider_reference = $5, next_attempt_at = NULL, claimed_until = NULL`+stillHeld,
		a.AttemptedAt.UTC(), nullString(a.ProviderReference))
}

// RecordDeclined counts the declined attempt and schedules the next one.
func (s *InstallmentStore) RecordDeclined(ctx context.Context, i domain.Installment, a domain.InstallmentAttempt,
	next time.Time) error {
	return s.update(ctx, i, &a, `UPDATE premium_installment SET attempts = attempts + 1, next_attempt_at = $4,
			claimed_until = NULL`+stillHeld, nullTime(next))
}

// RescheduleUnknown releases the instalment, to try the same attempt at next.
func (s *InstallmentStore) RescheduleUnknown(ctx context.Context, i domain.Installment, next time.Time) error {
	return s.update(ctx, i, nil, `UPDATE premium_installment SET next_attempt_at = $4, claimed_until = NULL`+stillHeld,
		next.UTC())
}

// ListOverdue returns the pending instalments whose grace period ended.
func (s *InstallmentStore) ListOverdue(ctx context.Context, now time.Time, limit int) ([]ports.PolicyInstallment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+installmentColumns+` FROM premium_installment i
		WHERE i.status = 'pending' AND i.cancels_at <= $1 AND `+notHeld+`
		ORDER BY i.cancels_at, i.policy_id, i.seq
		LIMIT $2`, now.UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("list the overdue instalments: %w", err)
	}
	items, err := scanInstallments(rows)
	if err != nil {
		return nil, err
	}
	return s.withPolicies(ctx, items)
}

// Cancel cancels the policy, marks the instalment unpaid and stores the
// event, in one transaction.
func (s *InstallmentStore) Cancel(ctx context.Context, p domain.Policy, i domain.Installment, now time.Time) (bool, error) {
	e := domain.NewPolicyCancelledForNonPayment(p, i, now)
	env, err := e.Envelope()
	if err != nil {
		return false, fmt.Errorf("build the PolicyCancelledForNonPayment event: %w", err)
	}
	body, err := env.Encode()
	if err != nil {
		return false, fmt.Errorf("encode the PolicyCancelledForNonPayment event: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("cancel policy %s: %w", p.ID, err)
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `UPDATE premium_installment i SET status = 'unpaid', next_attempt_at = NULL
		WHERE i.policy_id = $2 AND i.seq = $3 AND i.status = 'pending' AND i.cancels_at <= $1 AND `+notHeld,
		now.UTC(), p.ID, i.Seq)
	if err != nil {
		return false, fmt.Errorf("mark month %d of policy %s unpaid: %w", i.Seq, p.ID, err)
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return false, err
	}
	// HU-W1.6-01: a cancellation for non-payment takes effect when it is made.
	res, err = tx.ExecContext(ctx, `INSERT INTO policy_cancellation
			(policy_id, reason, installment_seq, cancelled_at, event_id, effective_at)
		VALUES ($1, $2, $3, $4, $5, $4)
		ON CONFLICT (policy_id) DO NOTHING`,
		p.ID, string(domain.CancellationNonPayment), i.Seq, now.UTC(), env.EventID)
	if err != nil {
		return false, fmt.Errorf("store the cancellation of policy %s: %w", p.ID, err)
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return false, err
	}
	// The months billed after the unpaid one will never be charged.
	if _, err := tx.ExecContext(ctx, `UPDATE premium_installment SET status = 'unpaid', next_attempt_at = NULL
		WHERE policy_id = $1 AND seq > $2 AND status = 'pending'`, p.ID, i.Seq); err != nil {
		return false, fmt.Errorf("close the later months of policy %s: %w", p.ID, err)
	}
	if err := insertOutboxEvent(ctx, tx, env.EventID, env.EventType, body, env.OccurredAt); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("cancel policy %s: %w", p.ID, err)
	}
	return true, nil
}

// ListUnnotified returns the cancellations for non-payment whose e-mail is
// still due. HU-W1.6-01: a cancellation at the request of the insured has no
// such e-mail (it says "cancelled for non-payment, with no refund").
func (s *InstallmentStore) ListUnnotified(ctx context.Context, maxAttempts, limit int) ([]ports.CancellationNotice, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT c.policy_id FROM policy_cancellation c
		WHERE c.reason = 'non_payment' AND c.email_sent_at IS NULL AND c.email_attempts < $1
		ORDER BY c.cancelled_at, c.policy_id
		LIMIT $2`, maxAttempts, limit)
	if err != nil {
		return nil, fmt.Errorf("list the cancellations to e-mail: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	billing, err := s.Billing(ctx, ids)
	if err != nil {
		return nil, err
	}
	ps := &PolicyStore{db: s.db}
	out := make([]ports.CancellationNotice, 0, len(ids))
	for _, id := range ids {
		p, err := ps.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		if c := billing[id].Cancellation; c != nil {
			out = append(out, ports.CancellationNotice{Policy: p, Cancellation: *c})
		}
	}
	return out, nil
}

// MarkEmailed records that the e-mail went out.
func (s *InstallmentStore) MarkEmailed(ctx context.Context, policyID string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE policy_cancellation SET email_sent_at = $2
		WHERE policy_id = $1 AND email_sent_at IS NULL`, policyID, at.UTC())
	if err != nil {
		return fmt.Errorf("record the e-mail of the cancellation of policy %s: %w", policyID, err)
	}
	return nil
}

// CountEmailFailure counts a failed e-mail.
func (s *InstallmentStore) CountEmailFailure(ctx context.Context, policyID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE policy_cancellation SET email_attempts = email_attempts + 1
		WHERE policy_id = $1 AND email_sent_at IS NULL`, policyID)
	if err != nil {
		return fmt.Errorf("count the failed e-mail of the cancellation of policy %s: %w", policyID, err)
	}
	return nil
}

// Billing reads the instalments, their attempts and the cancellation of the
// policies, in one snapshot.
func (s *InstallmentStore) Billing(ctx context.Context, policyIDs []string) (map[string]ports.BillingRecord, error) {
	out := map[string]ports.BillingRecord{}
	if len(policyIDs) == 0 {
		return out, nil
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("read the billing of the policies: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	ids := policyIDs

	rows, err := tx.QueryContext(ctx, `SELECT `+installmentColumns+` FROM premium_installment i
		WHERE i.policy_id = ANY($1) ORDER BY i.policy_id, i.seq`, ids)
	if err != nil {
		return nil, fmt.Errorf("read the instalments of the policies: %w", err)
	}
	installments, err := scanInstallments(rows)
	if err != nil {
		return nil, err
	}
	index := map[string]map[int]int{}
	for _, i := range installments {
		rec := out[i.PolicyID]
		if index[i.PolicyID] == nil {
			index[i.PolicyID] = map[int]int{}
		}
		index[i.PolicyID][i.Seq] = len(rec.Installments)
		rec.Installments = append(rec.Installments, i)
		out[i.PolicyID] = rec
	}

	if err := readAttempts(ctx, tx, ids, out, index); err != nil {
		return nil, err
	}
	if err := readCancellations(ctx, tx, ids, out, index); err != nil {
		return nil, err
	}
	return out, tx.Commit()
}

func readAttempts(ctx context.Context, tx *sql.Tx, ids []string, out map[string]ports.BillingRecord,
	index map[string]map[int]int) error {
	rows, err := tx.QueryContext(ctx, `SELECT policy_id, seq, attempt, idempotency_key, outcome, decline_code,
			provider_reference, attempted_at
		FROM premium_installment_attempt WHERE policy_id = ANY($1) ORDER BY policy_id, seq, attempt`, ids)
	if err != nil {
		return fmt.Errorf("read the attempts of the instalments: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			policyID, outcome string
			seq               int
			a                 domain.InstallmentAttempt
			code, ref         sql.NullString
		)
		if err := rows.Scan(&policyID, &seq, &a.Attempt, &a.IdempotencyKey, &outcome, &code, &ref,
			&a.AttemptedAt); err != nil {
			return fmt.Errorf("read an attempt of an instalment: %w", err)
		}
		a.Outcome, a.DeclineCode, a.ProviderReference = domain.AttemptOutcome(outcome), code.String, ref.String
		a.AttemptedAt = a.AttemptedAt.UTC()
		k, ok := index[policyID][seq]
		if !ok {
			continue
		}
		rec := out[policyID]
		rec.Installments[k].History = append(rec.Installments[k].History, a)
		out[policyID] = rec
	}
	return rows.Err()
}

// readCancellations reads the cancellations of the policies: for non-payment
// with their unpaid instalment, at the request of the insured (HU-W1.6-01)
// with their refund.
func readCancellations(ctx context.Context, tx *sql.Tx, ids []string, out map[string]ports.BillingRecord,
	index map[string]map[int]int) error {
	stored, err := readCancellationRows(ctx, tx, ids)
	if err != nil {
		return err
	}
	for id, sc := range stored {
		c := sc.PolicyCancellation
		rec := out[id]
		if k, ok := index[id][sc.seq]; ok && sc.seq > 0 {
			c.Installment = rec.Installments[k]
		}
		rec.Cancellation = &c
		out[id] = rec
	}
	return nil
}

var _ ports.CancellationChecker = (*InstallmentStore)(nil)

// IsCancelled tells whether the policy was cancelled.
func (s *InstallmentStore) IsCancelled(ctx context.Context, policyID string) (bool, error) {
	var cancelled bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM policy_cancellation WHERE policy_id = $1)`,
		policyID).Scan(&cancelled); err != nil {
		return false, fmt.Errorf("read whether policy %s was cancelled: %w", policyID, err)
	}
	return cancelled, nil
}

// PaymentMethodOf reads the payment method of the charge of the acceptance.
func (s *InstallmentStore) PaymentMethodOf(ctx context.Context, acceptanceID string) (domain.PaymentMethod, error) {
	c, _, err := s.IssuanceCharge(ctx, acceptanceID)
	return c.PaymentMethod, err
}

// IssuanceCharge reads the charge of the acceptance of a policy; found=false
// when it has none.
func (s *InstallmentStore) IssuanceCharge(ctx context.Context, acceptanceID string) (domain.Charge, bool, error) {
	c, err := (&ChargeStore{db: s.db}).Get(ctx, acceptanceID)
	if errors.Is(err, ports.ErrChargeNotFound) {
		return domain.Charge{}, false, nil
	}
	if err != nil {
		return domain.Charge{}, false, err
	}
	return c, true, nil
}
