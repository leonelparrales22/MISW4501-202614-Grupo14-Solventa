package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// DecisionStore is the ports.DecisionStore over the claim_decision table
// (HU-W2.2-03). Every write is one statement that also moves the status of
// the claim, and the payout follows ADR-01 like the premium of origination:
// the claim moves a payout to in_progress only if it is still claimable, and
// every result is recorded only while that claim is held (same
// claimed_until), never with a row lock held during the call to the gateway.
type DecisionStore struct {
	db *sql.DB
}

var _ ports.DecisionStore = (*DecisionStore)(nil)

// NewDecisionStore builds the store over an open, migrated database.
func NewDecisionStore(db *sql.DB) *DecisionStore { return &DecisionStore{db: db} }

// DecisionStoreOf builds the store over the database of the claims, so the
// composition root does not open a second pool.
func DecisionStoreOf(records *ClaimRecordStore) *DecisionStore { return &DecisionStore{db: records.db} }

// decisionColumns are read from a claim_decision aliased d joined to its
// claim aliased c.
const decisionColumns = `d.claim_id, c.filing_number, d.outcome, d.decided_by, d.decided_at,
	d.approved_amount_minor, d.insured_value_minor, d.deductible_basis_points, d.currency,
	d.payout_key, d.beneficiary_id, d.payout_amount_minor, d.bank_code, d.bank_name, d.account_type, d.account_number,
	d.payout_status, d.payout_attempts, d.next_attempt_at, d.claimed_until, d.last_attempt_at, d.last_failure_at,
	d.payout_failure, d.last_error, d.gateway_code, d.payout_reference, d.paid_at,
	d.manual_retries, d.last_retried_by, d.last_retried_at,
	d.rejection_reason, d.notice_to, d.notice_sent_at, d.notice_attempts, d.notice_next_attempt_at, d.notice_last_error`

func scanDecision(row scanner) (domain.Decision, error) {
	var (
		d                                                   domain.Decision
		outcome                                             string
		approved, insured, payoutAmount                     sql.NullInt64
		deductible                                          sql.NullInt64
		currency, key, beneficiary, bank, bankName, accType sql.NullString
		account, status, failure, lastErr, code, reference  sql.NullString
		retriedBy, reason, noticeTo, noticeErr              sql.NullString
		attempts, retries, noticeAttempts                   int
		next, claimed, lastAttempt, lastFailure, paid       sql.NullTime
		retriedAt, noticeSent, noticeNext                   sql.NullTime
	)
	err := row.Scan(&d.ClaimID, &d.FilingNumber, &outcome, &d.DecidedBy, &d.DecidedAt,
		&approved, &insured, &deductible, &currency,
		&key, &beneficiary, &payoutAmount, &bank, &bankName, &accType, &account,
		&status, &attempts, &next, &claimed, &lastAttempt, &lastFailure,
		&failure, &lastErr, &code, &reference, &paid,
		&retries, &retriedBy, &retriedAt,
		&reason, &noticeTo, &noticeSent, &noticeAttempts, &noticeNext, &noticeErr)
	if err != nil {
		return domain.Decision{}, err
	}
	d.Outcome, d.DecidedAt = domain.Outcome(outcome), d.DecidedAt.UTC()
	d.ApprovedAmountMinor, d.InsuredValueMinor = approved.Int64, insured.Int64
	d.DeductibleBasisPoints, d.Currency, d.Reason = int(deductible.Int64), currency.String, reason.String
	if key.Valid {
		d.Payout = &domain.Payout{
			Key: key.String, BeneficiaryID: beneficiary.String, AmountMinor: payoutAmount.Int64, Currency: currency.String,
			Destination: domain.PayoutAccount{BankCode: bank.String, BankName: bankName.String, AccountType: accType.String,
				AccountNumber: account.String},
			Status: idempotency.Status(status.String), Attempts: attempts,
			NextAttemptAt: utc(next), ClaimedUntil: utc(claimed), LastAttemptAt: utc(lastAttempt),
			LastFailureAt: utc(lastFailure), Failure: domain.PayoutFailure(failure.String), LastError: lastErr.String,
			GatewayCode: code.String, Reference: reference.String, ConfirmedAt: utc(paid),
			ManualRetries: retries, LastRetriedBy: retriedBy.String, LastRetriedAt: utc(retriedAt),
		}
	}
	if noticeTo.Valid {
		d.Notice = &domain.Notice{To: noticeTo.String, SentAt: utc(noticeSent), Attempts: noticeAttempts,
			NextAttemptAt: utc(noticeNext), LastError: noticeErr.String}
	}
	return d, nil
}

// utc is the time of a nullable column in UTC, or the zero time.
func utc(t sql.NullTime) time.Time {
	if !t.Valid {
		return time.Time{}
	}
	return t.Time.UTC()
}

func nullTimeOf(t time.Time) sql.NullTime { return sql.NullTime{Time: t.UTC(), Valid: !t.IsZero()} }

// optional keeps the empty string as NULL.
func optional(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

// one runs a statement that returns at most one decision; found is false when
// it returned none.
func (s *DecisionStore) one(ctx context.Context, query string, args ...any) (domain.Decision, bool, error) {
	d, err := scanDecision(s.db.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Decision{}, false, nil
	}
	if err != nil {
		return domain.Decision{}, false, err
	}
	return d, true, nil
}

// held runs a statement conditional on the claim of a payout and maps "no
// row" to idempotency.ErrClaimLost.
func (s *DecisionStore) held(ctx context.Context, query string, args ...any) (domain.Decision, error) {
	d, found, err := s.one(ctx, query, args...)
	if err != nil {
		return domain.Decision{}, err
	}
	if !found {
		return domain.Decision{}, idempotency.ErrClaimLost
	}
	return d, nil
}

// Decide records the decision and moves the claim in the same statement,
// conditionally on the claim being in evaluation: of two analysts deciding at
// once, the second finds the claim decided.
func (s *DecisionStore) Decide(ctx context.Context, d domain.Decision) (domain.Decision, error) {
	var (
		approved, insured, payoutAmount            sql.NullInt64
		deductible                                 sql.NullInt64
		currency, key, beneficiary, bank, bankName sql.NullString
		accType, account, status, reason, noticeTo sql.NullString
		next, noticeNext                           sql.NullTime
	)
	if p := d.Payout; p != nil {
		approved, insured = sql.NullInt64{Int64: d.ApprovedAmountMinor, Valid: true}, sql.NullInt64{Int64: d.InsuredValueMinor, Valid: true}
		deductible = sql.NullInt64{Int64: int64(d.DeductibleBasisPoints), Valid: true}
		currency, key, beneficiary = optional(d.Currency), optional(p.Key), optional(p.BeneficiaryID)
		payoutAmount = sql.NullInt64{Int64: p.AmountMinor, Valid: true}
		bank, bankName = optional(p.Destination.BankCode), optional(p.Destination.BankName)
		accType, account = optional(p.Destination.AccountType), optional(p.Destination.AccountNumber)
		status, next = optional(string(p.Status)), nullTimeOf(p.NextAttemptAt)
	}
	if n := d.Notice; n != nil {
		reason, noticeTo, noticeNext = optional(d.Reason), optional(n.To), nullTimeOf(n.NextAttemptAt)
	}
	var id string
	err := s.db.QueryRowContext(ctx, `WITH target AS (
			UPDATE claim SET status = $2 WHERE id = $1 AND status = ANY($3) RETURNING id
		)
		INSERT INTO claim_decision (claim_id, outcome, decided_by, decided_at,
			approved_amount_minor, insured_value_minor, deductible_basis_points, currency,
			payout_key, beneficiary_id, payout_amount_minor, bank_code, bank_name, account_type, account_number,
			payout_status, next_attempt_at, rejection_reason, notice_to, notice_next_attempt_at)
		SELECT id, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22 FROM target
		RETURNING claim_id`,
		d.ClaimID, string(d.StatusOf()), statusNames(domain.DecidableStatuses()), string(d.Outcome), d.DecidedBy, d.DecidedAt.UTC(),
		approved, insured, deductible, currency, key, beneficiary, payoutAmount, bank, bankName, accType, account,
		status, next, reason, noticeTo, noticeNext).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if err := s.claimExists(ctx, d.ClaimID); err != nil {
			return domain.Decision{}, err
		}
		return domain.Decision{}, ports.ErrNotDecidable
	case err != nil:
		return domain.Decision{}, fmt.Errorf("record the decision of the claim %s: %w", d.ClaimID, err)
	}
	return s.Decision(ctx, id)
}

func (s *DecisionStore) claimExists(ctx context.Context, claimID string) error {
	var exists bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM claim WHERE id = $1)`, claimID).Scan(&exists); err != nil {
		return fmt.Errorf("read the claim %s: %w", claimID, err)
	}
	if !exists {
		return ports.ErrClaimNotFound
	}
	return nil
}

// Decision returns the decision of a claim.
func (s *DecisionStore) Decision(ctx context.Context, claimID string) (domain.Decision, error) {
	d, found, err := s.one(ctx, `SELECT `+decisionColumns+`
		FROM claim_decision d JOIN claim c ON c.id = d.claim_id WHERE d.claim_id = $1`, claimID)
	if err != nil {
		return domain.Decision{}, fmt.Errorf("read the decision of the claim %s: %w", claimID, err)
	}
	if !found {
		return domain.Decision{}, ports.ErrDecisionNotFound
	}
	return d, nil
}

// ListPayoutsDue returns the claims whose payout is due at now.
func (s *DecisionStore) ListPayoutsDue(ctx context.Context, now time.Time, limit int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT claim_id FROM claim_decision
		WHERE outcome = 'approved'
		  AND ((payout_status = 'pending' AND next_attempt_at <= $1) OR (payout_status = 'in_progress' AND claimed_until < $1))
		ORDER BY next_attempt_at NULLS FIRST, claim_id LIMIT $2`, now.UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("list the due payouts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// Claim is the atomic claim of ADR-01.
func (s *DecisionStore) Claim(ctx context.Context, claimID string, now time.Time, lease time.Duration) (domain.Decision, bool, error) {
	now = now.UTC()
	d, claimed, err := s.one(ctx, `WITH claimed AS (
			UPDATE claim_decision SET payout_status = 'in_progress', claimed_until = $3, last_attempt_at = $2
			WHERE claim_id = $1 AND outcome = 'approved'
			  AND ((payout_status = 'pending' AND next_attempt_at <= $2) OR (payout_status = 'in_progress' AND claimed_until < $2))
			RETURNING *
		)
		SELECT `+decisionColumns+` FROM claimed d JOIN claim c ON c.id = d.claim_id`, claimID, now, now.Add(lease))
	if err != nil {
		return domain.Decision{}, false, fmt.Errorf("claim the payout of the claim %s: %w", claimID, err)
	}
	return d, claimed, nil
}

// Confirm records the confirmed payout and moves the claim to paid.
func (s *DecisionStore) Confirm(ctx context.Context, d domain.Decision, now time.Time, out idempotency.Outcome) (domain.Decision, error) {
	return s.held(ctx, `WITH done AS (
			UPDATE claim_decision SET payout_status = 'confirmed', paid_at = $3, payout_reference = $4,
			    next_attempt_at = NULL, claimed_until = NULL, payout_failure = NULL
			WHERE claim_id = $1 AND payout_status = 'in_progress' AND claimed_until = $2
			RETURNING *
		), moved AS (
			UPDATE claim SET status = 'paid' WHERE id IN (SELECT claim_id FROM done)
		)
		SELECT `+decisionColumns+` FROM done d JOIN claim c ON c.id = d.claim_id`,
		d.ClaimID, d.Payout.ClaimedUntil.UTC(), now.UTC(), optional(out.Reference))
}

// Reschedule makes the payout pending again with one more failed attempt.
func (s *DecisionStore) Reschedule(ctx context.Context, d domain.Decision, now, next time.Time, cause string) (domain.Decision, error) {
	return s.held(ctx, `WITH done AS (
			UPDATE claim_decision SET payout_status = 'pending', payout_attempts = payout_attempts + 1,
			    next_attempt_at = $4, claimed_until = NULL, last_failure_at = $3, last_error = $5
			WHERE claim_id = $1 AND payout_status = 'in_progress' AND claimed_until = $2
			RETURNING *
		)
		SELECT `+decisionColumns+` FROM done d JOIN claim c ON c.id = d.claim_id`,
		d.ClaimID, d.Payout.ClaimedUntil.UTC(), now.UTC(), next.UTC(), cause)
}

// Fail marks the payout failed and moves the claim to payment_exception. The
// approval and its key are kept.
func (s *DecisionStore) Fail(ctx context.Context, d domain.Decision, now time.Time, reason idempotency.FailureReason, cause string) (domain.Decision, error) {
	return s.held(ctx, `WITH done AS (
			UPDATE claim_decision SET payout_status = 'failed', payout_attempts = payout_attempts + 1,
			    next_attempt_at = NULL, claimed_until = NULL, payout_failure = $4, last_failure_at = $3, last_error = $5
			WHERE claim_id = $1 AND payout_status = 'in_progress' AND claimed_until = $2
			RETURNING *
		), moved AS (
			UPDATE claim SET status = 'payment_exception' WHERE id IN (SELECT claim_id FROM done)
		)
		SELECT `+decisionColumns+` FROM done d JOIN claim c ON c.id = d.claim_id`,
		d.ClaimID, d.Payout.ClaimedUntil.UTC(), now.UTC(), string(domain.PayoutFailureFor(reason)), cause)
}

// RecordGatewayCode keeps the gateway's reason of a failed payout.
func (s *DecisionStore) RecordGatewayCode(ctx context.Context, claimID, code string) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE claim_decision SET gateway_code = $2
		WHERE claim_id = $1 AND payout_status = 'failed'`, claimID, code); err != nil {
		return fmt.Errorf("keep the reason of the gateway for the claim %s: %w", claimID, err)
	}
	return nil
}

// RetryPayout makes a failed payout due again, records the retry and moves
// the claim back to approved, all in one statement conditional on the payout
// being failed: of two analysts retrying at once only one wins.
func (s *DecisionStore) RetryPayout(ctx context.Context, claimID string, r domain.PayoutRetry) (domain.Decision, error) {
	var bank, bankName, accType, account sql.NullString
	if dst := r.Destination; dst != nil {
		bank, bankName = optional(dst.BankCode), optional(dst.BankName)
		accType, account = optional(dst.AccountType), optional(dst.AccountNumber)
	}
	d, found, err := s.one(ctx, `WITH retried AS (
			UPDATE claim_decision SET payout_status = 'pending', payout_attempts = 0, next_attempt_at = $2,
			    claimed_until = NULL, payout_failure = NULL, gateway_code = NULL,
			    bank_code = COALESCE($3, bank_code), bank_name = CASE WHEN $3::text IS NULL THEN bank_name ELSE $4 END,
			    account_type = COALESCE($5, account_type), account_number = COALESCE($6, account_number),
			    manual_retries = manual_retries + 1, last_retried_by = $7, last_retried_at = $2
			WHERE claim_id = $1 AND payout_status = 'failed'
			RETURNING *
		), logged AS (
			INSERT INTO claim_payout_retry (claim_id, seq, retried_by, retried_at, destination_changed)
			SELECT claim_id, manual_retries, $7::text, $2::timestamptz, $3::text IS NOT NULL FROM retried
		), moved AS (
			UPDATE claim SET status = 'approved' WHERE id IN (SELECT claim_id FROM retried)
		)
		SELECT `+decisionColumns+` FROM retried d JOIN claim c ON c.id = d.claim_id`,
		claimID, r.At.UTC(), bank, bankName, accType, account, r.By)
	if err != nil {
		return domain.Decision{}, fmt.Errorf("retry the payout of the claim %s: %w", claimID, err)
	}
	if found {
		return d, nil
	}
	if err := s.claimExists(ctx, claimID); err != nil {
		return domain.Decision{}, err
	}
	return domain.Decision{}, ports.ErrPayoutNotRetryable
}

// ListNoticesDue returns the rejections whose notice is due at now.
func (s *DecisionStore) ListNoticesDue(ctx context.Context, now time.Time, limit int) ([]domain.Decision, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+decisionColumns+`
		FROM claim_decision d JOIN claim c ON c.id = d.claim_id
		WHERE d.outcome = 'rejected' AND d.notice_sent_at IS NULL AND d.notice_next_attempt_at <= $1
		ORDER BY d.notice_next_attempt_at, d.claim_id LIMIT $2`, now.UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("list the due notices of rejection: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []domain.Decision
	for rows.Next() {
		d, err := scanDecision(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// NoticeSent records the delivery of the notice of a rejection.
func (s *DecisionStore) NoticeSent(ctx context.Context, claimID string, at time.Time) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE claim_decision
		SET notice_sent_at = $2, notice_attempts = notice_attempts + 1, notice_next_attempt_at = NULL, notice_last_error = NULL
		WHERE claim_id = $1 AND notice_sent_at IS NULL`, claimID, at.UTC()); err != nil {
		return fmt.Errorf("record the notice of the claim %s as sent: %w", claimID, err)
	}
	return nil
}

// NoticeFailed counts a failed delivery and makes the notice due at next.
func (s *DecisionStore) NoticeFailed(ctx context.Context, claimID string, _, next time.Time, cause string) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE claim_decision
		SET notice_attempts = notice_attempts + 1, notice_next_attempt_at = $2, notice_last_error = $3
		WHERE claim_id = $1 AND notice_sent_at IS NULL`, claimID, next.UTC(), cause); err != nil {
		return fmt.Errorf("record the failed notice of the claim %s: %w", claimID, err)
	}
	return nil
}
