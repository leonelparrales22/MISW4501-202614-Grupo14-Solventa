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

// HU-W1.6-01: the cancellation of a policy at the request of its insured and
// its refund (migration 0020), over policy_cancellation (0017, decision D-4 a),
// policy_refund and refund_counter.

// CancellationStore is the ports.CancellationStore over origination's own
// database.
type CancellationStore struct {
	db *sql.DB
}

var _ ports.CancellationStore = (*CancellationStore)(nil)

// NewCancellationStore builds the store over an open, migrated database.
func NewCancellationStore(db *sql.DB) *CancellationStore { return &CancellationStore{db: db} }

// Facts reads the facts of the policy in one snapshot, without holding
// anything.
func (s *CancellationStore) Facts(ctx context.Context, policyID string, now time.Time) (ports.CancellationFacts, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return ports.CancellationFacts{}, fmt.Errorf("read the cancellation facts of policy %s: %w", policyID, err)
	}
	defer func() { _ = tx.Rollback() }()
	f, err := readFacts(ctx, tx, policyID, now, "")
	if err != nil {
		return ports.CancellationFacts{}, err
	}
	return f, tx.Commit()
}

// readFacts reads the facts of the policy within q; lock is appended to the
// query of the instalments (FOR UPDATE inside the cancellation).
func readFacts(ctx context.Context, q querier, policyID string, now time.Time, lock string) (ports.CancellationFacts, error) {
	var f ports.CancellationFacts
	p, err := scanPolicy(q.QueryRowContext(ctx, `SELECT `+policyColumns+` FROM policy_current WHERE policy_id = $1`, policyID))
	if errors.Is(err, sql.ErrNoRows) {
		return f, ports.ErrPolicyNotFound
	}
	if err != nil {
		return f, fmt.Errorf("read policy %s: %w", policyID, err)
	}
	f.Policy = p

	rows, err := q.QueryContext(ctx, `SELECT `+installmentColumns+`, i.claimed_until FROM premium_installment i
		WHERE i.policy_id = $1 ORDER BY i.seq`+lock, policyID)
	if err != nil {
		return f, fmt.Errorf("read the instalments of policy %s: %w", policyID, err)
	}
	for rows.Next() {
		var (
			d       installmentDest
			claimed sql.NullTime
		)
		if err := rows.Scan(append(d.ptrs(), &claimed)...); err != nil {
			_ = rows.Close()
			return f, fmt.Errorf("read an instalment of policy %s: %w", policyID, err)
		}
		i := d.installment()
		if i.Status == domain.InstallmentPending && claimed.Valid && claimed.Time.After(now) {
			f.InstallmentHeld = true
		}
		f.Installments = append(f.Installments, i)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return f, err
	}

	c, err := scanCharge(q.QueryRowContext(ctx, `SELECT `+chargeColumns+` FROM charge_attempt WHERE acceptance_id = $1`,
		p.AcceptanceID))
	switch {
	case err == nil:
		f.Issuance = &c
	case !errors.Is(err, sql.ErrNoRows):
		return f, fmt.Errorf("read the charge of the issuance of policy %s: %w", policyID, err)
	}

	var renewal sql.NullString
	err = q.QueryRowContext(ctx, `SELECT status FROM renewal WHERE policy_id = $1 AND period_end = $2`,
		policyID, p.CoverageEnd.UTC()).Scan(&renewal)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return f, fmt.Errorf("read the renewal of policy %s: %w", policyID, err)
	}
	f.Renewal = domain.RenewalStatus(renewal.String)

	cancellations, err := cancellationsOf(ctx, q, []string{policyID})
	if err != nil {
		return f, err
	}
	if c, ok := cancellations[policyID]; ok {
		f.Cancellation = &c
	}
	return f, nil
}

// Cancel cancels the policy at the request of its insured in one transaction
// (consistencia fuerte intra-servicio): the lock on the months of the policy
// keeps the sweep from charging one of them meanwhile, and the primary key of
// policy_cancellation lets one of several confirmations, or the cancellation
// for non-payment, win.
func (s *CancellationStore) Cancel(ctx context.Context, policyID string, now time.Time,
	decide ports.CancellationDecision) (domain.PolicyCancellation, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.PolicyCancellation{}, fmt.Errorf("cancel policy %s: %w", policyID, err)
	}
	defer func() { _ = tx.Rollback() }()

	facts, err := readFacts(ctx, tx, policyID, now, ` FOR UPDATE`)
	if err != nil {
		return domain.PolicyCancellation{}, err
	}
	c, err := decide(facts)
	if err != nil {
		return domain.PolicyCancellation{}, err
	}

	res, err := tx.ExecContext(ctx, `INSERT INTO policy_cancellation
			(policy_id, reason, installment_seq, cancelled_at, event_id, effective_at, insured_reason, comments,
			 requested_by_sub)
		VALUES ($1, $2, NULL, $3, NULL, $4, $5, $6, $7)
		ON CONFLICT (policy_id) DO NOTHING`,
		policyID, string(domain.CancellationInsuredRequest), c.CancelledAt.UTC(), c.EffectiveAt.UTC(), string(c.Reason),
		nullString(c.Comments), c.RequestedBy)
	if err != nil {
		return domain.PolicyCancellation{}, fmt.Errorf("store the cancellation of policy %s: %w", policyID, err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return domain.PolicyCancellation{}, err
	} else if n == 0 {
		return domain.PolicyCancellation{}, ports.ErrPolicyAlreadyCancelled
	}

	// 0017 already defines it: pending -> unpaid when the policy is cancelled.
	// A month in its grace period is closed without being charged.
	if _, err := tx.ExecContext(ctx, `UPDATE premium_installment SET status = 'unpaid', next_attempt_at = NULL
		WHERE policy_id = $1 AND status = 'pending'`, policyID); err != nil {
		return domain.PolicyCancellation{}, fmt.Errorf("close the pending months of policy %s: %w", policyID, err)
	}

	r := c.Refund
	if r.AmountMinor > 0 {
		year := domain.RefundYear(c.CancelledAt)
		var n int
		if err := tx.QueryRowContext(ctx, `INSERT INTO refund_counter (year, last_number) VALUES ($1, 1)
			ON CONFLICT (year) DO UPDATE SET last_number = refund_counter.last_number + 1
			RETURNING last_number`, year).Scan(&n); err != nil {
			return domain.PolicyCancellation{}, fmt.Errorf("number the refund of policy %s: %w", policyID, err)
		}
		r.Reference = domain.FormatRefundReference(year, n)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO policy_refund
			(policy_id, reference, amount_minor, currency, method, brand, last4, bank_code, paid_seq, paid_kind,
			 paid_amount_minor, period_start, period_end, period_days, unused_days, charge_reference, status,
			 registered_at, idempotency_key, attempts, next_attempt_at, retry_until, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, 0, $20, $21, $18)`,
		policyID, nullString(r.Reference), r.AmountMinor, r.Currency, nullString(string(r.Method.Kind)),
		nullString(r.Method.Brand), nullString(r.Method.Last4), nullString(r.Method.BankCode), r.Paid.Seq,
		string(r.Paid.Kind), r.Paid.AmountMinor, r.PeriodStart.UTC(), r.PeriodEnd.UTC(), r.PeriodDays, r.UnusedDays,
		nullString(r.Paid.ChargeReference), string(r.Status), r.RegisteredAt.UTC(), nullString(r.IdempotencyKey),
		nullTime(r.NextAttemptAt), nullTime(r.RetryUntil)); err != nil {
		return domain.PolicyCancellation{}, fmt.Errorf("store the refund of policy %s: %w", policyID, err)
	}
	if err := tx.Commit(); err != nil {
		return domain.PolicyCancellation{}, fmt.Errorf("cancel policy %s: %w", policyID, err)
	}
	return domain.PolicyCancellation{
		PolicyID: policyID, Reason: domain.CancellationInsuredRequest, CancelledAt: c.CancelledAt.UTC(),
		EffectiveAt: c.EffectiveAt.UTC(), InsuredReason: c.Reason, Comments: c.Comments, RequestedBy: c.RequestedBy,
		Refund: &r,
	}, nil
}

const cancellationColumns = `c.policy_id, c.reason, c.installment_seq, c.cancelled_at, c.effective_at, c.event_id,
	c.email_sent_at, c.insured_reason, c.comments, c.requested_by_sub`

const refundColumns = `r.policy_id, r.reference, r.amount_minor, r.currency, r.method, r.brand, r.last4, r.bank_code,
	r.paid_seq, r.paid_kind, r.paid_amount_minor, r.period_start, r.period_end, r.period_days, r.unused_days,
	r.charge_reference, r.status, r.registered_at, r.idempotency_key, r.attempts, r.next_attempt_at, r.claimed_until,
	r.retry_until, r.provider_reference, r.completed_at, r.failure_reason, r.last_error`

// cancellation is a stored cancellation with the seq of its unpaid
// instalment (0 for one at the request of the insured).
type cancellation struct {
	domain.PolicyCancellation
	seq int
}

// cancellationsOf reads the cancellations of the policies, with their
// refunds, by policy id.
func cancellationsOf(ctx context.Context, q querier, ids []string) (map[string]domain.PolicyCancellation, error) {
	stored, err := readCancellationRows(ctx, q, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[string]domain.PolicyCancellation, len(stored))
	for id, c := range stored {
		out[id] = c.PolicyCancellation
	}
	return out, nil
}

func readCancellationRows(ctx context.Context, q querier, ids []string) (map[string]cancellation, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+cancellationColumns+`, `+refundColumns+`
		FROM policy_cancellation c LEFT JOIN policy_refund r ON r.policy_id = c.policy_id
		WHERE c.policy_id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("read the cancellations of the policies: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]cancellation{}
	for rows.Next() {
		var (
			c                                     cancellation
			reason                                string
			seq                                   sql.NullInt64
			eventID, insured, comments, requested sql.NullString
			sent                                  sql.NullTime
			refund                                refundDest
		)
		dest := []any{&c.PolicyID, &reason, &seq, &c.CancelledAt, &c.EffectiveAt, &eventID, &sent, &insured, &comments,
			&requested}
		if err := rows.Scan(append(dest, refund.ptrs()...)...); err != nil {
			return nil, fmt.Errorf("read a cancellation: %w", err)
		}
		c.Reason, c.seq = domain.CancellationReason(reason), int(seq.Int64)
		c.CancelledAt, c.EffectiveAt, c.EmailSentAt = c.CancelledAt.UTC(), c.EffectiveAt.UTC(), utc(sent)
		c.EventID, c.InsuredReason = eventID.String, domain.InsuredCancellationReason(insured.String)
		c.Comments, c.RequestedBy = comments.String, requested.String
		if refund.policyID.Valid {
			r := refund.refund()
			c.Refund = &r
		}
		out[c.PolicyID] = c
	}
	return out, rows.Err()
}

// refundDest are the destinations of refundColumns, all nullable because they
// come from a LEFT JOIN.
type refundDest struct {
	policyID, reference, currency, method, brand, last4, bank    sql.NullString
	paidKind, chargeRef, status, key, provider, failure, lastErr sql.NullString
	amount, paidAmount, paidSeq, periodDays, unusedDays, tries   sql.NullInt64
	periodStart, periodEnd, registered, next, claimed, retry     sql.NullTime
	completed                                                    sql.NullTime
}

func (d *refundDest) ptrs() []any {
	return []any{&d.policyID, &d.reference, &d.amount, &d.currency, &d.method, &d.brand, &d.last4, &d.bank, &d.paidSeq,
		&d.paidKind, &d.paidAmount, &d.periodStart, &d.periodEnd, &d.periodDays, &d.unusedDays, &d.chargeRef, &d.status,
		&d.registered, &d.key, &d.tries, &d.next, &d.claimed, &d.retry, &d.provider, &d.completed, &d.failure, &d.lastErr}
}

func (d *refundDest) refund() domain.Refund {
	return domain.Refund{
		PolicyID: d.policyID.String, Reference: d.reference.String, AmountMinor: d.amount.Int64, Currency: d.currency.String,
		Method: domain.PaymentMethod{Kind: domain.PaymentMethodKind(d.method.String), Brand: d.brand.String,
			Last4: d.last4.String, BankCode: d.bank.String},
		Paid: domain.PaidMonth{Seq: int(d.paidSeq.Int64), Kind: domain.PaidPeriodKind(d.paidKind.String),
			AmountMinor: d.paidAmount.Int64, Currency: d.currency.String, ChargeReference: d.chargeRef.String},
		PeriodStart: utc(d.periodStart), PeriodEnd: utc(d.periodEnd), PeriodDays: int(d.periodDays.Int64),
		UnusedDays: int(d.unusedDays.Int64), Status: domain.RefundStatus(d.status.String), RegisteredAt: utc(d.registered),
		IdempotencyKey: d.key.String, Attempts: int(d.tries.Int64), NextAttemptAt: utc(d.next),
		ClaimedUntil: utc(d.claimed), RetryUntil: utc(d.retry), ProviderReference: d.provider.String,
		CompletedAt: utc(d.completed), FailureReason: d.failure.String, LastError: d.lastErr.String,
	}
}
