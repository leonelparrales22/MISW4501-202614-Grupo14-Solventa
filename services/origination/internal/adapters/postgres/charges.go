package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// ChargeStore is the ports.ChargeRepository over the charge_attempt table
// (HA-D02). Every transition is one conditional statement, so PostgreSQL
// serializes the workers of every origination task (ADR-01): the claim moves
// a row to in_progress only if it is still claimable, and every write that
// records a result only if the claim is still the one that wrote it (same
// claimed_until), never with SELECT ... FOR UPDATE held during the call to
// the gateway.
type ChargeStore struct {
	db *sql.DB
}

var _ ports.ChargeRepository = (*ChargeStore)(nil)

// NewChargeStore builds a ChargeStore over db (already migrated).
func NewChargeStore(db *sql.DB) *ChargeStore { return &ChargeStore{db: db} }

const chargeColumns = `acceptance_id, idempotency_key, offer_id, customer_id, channel, accepted_at,
	holder_full_name, holder_document_id, holder_email, amount_minor, currency, insured_amount_minor, term_months, coverages,
	status, attempts, next_attempt_at, claimed_until, window_started_at, window_ends_at, last_attempt_at, last_failure_at,
	failure_reason, last_error, provider_reference, confirmed_at,
	policy_id, activation_event_id, activated_at, coverage_start, coverage_end, activation_published_at,
	activation_attempts, activation_next_attempt_at, activation_last_error,
	resume_count, last_resumed_by, last_resumed_at, resume_note,
	payment_method_kind, payment_token, payment_brand, payment_last4, payment_installments,
	payment_bank_code, payment_method_captured_at, payment_attempt, created_at, updated_at`

// coverageJSON is how a coverage is kept in the coverages column.
type coverageJSON struct {
	Code               string `json:"code"`
	InsuredAmountMinor int64  `json:"insured_amount_minor"`
}

func encodeCoverages(cs []domain.Coverage) ([]byte, error) {
	out := make([]coverageJSON, 0, len(cs))
	for _, c := range cs {
		out = append(out, coverageJSON{Code: c.Code, InsuredAmountMinor: domain.ToMinorUnits(c.InsuredAmount)})
	}
	return json.Marshal(out)
}

func decodeCoverages(raw []byte) ([]domain.Coverage, error) {
	var in []coverageJSON
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	out := make([]domain.Coverage, 0, len(in))
	for _, c := range in {
		out = append(out, domain.Coverage{Code: c.Code, InsuredAmount: domain.FromMinorUnits(c.InsuredAmountMinor)})
	}
	return out, nil
}

// nullTime keeps the zero time as NULL.
func nullTime(t time.Time) sql.NullTime { return sql.NullTime{Time: t.UTC(), Valid: !t.IsZero()} }

func nullString(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

// scanner is a *sql.Row or *sql.Rows.
type scanner interface{ Scan(dest ...any) error }

func scanCharge(s scanner) (domain.Charge, error) {
	var (
		c                                                                     domain.Charge
		channel, status                                                       string
		insuredMinor                                                          int64
		coverages                                                             []byte
		next, claimed, windowEnds, lastAttempt, lastFailure, confirmed        sql.NullTime
		activated, covStart, covEnd, published, activationNext, lastResumedAt sql.NullTime
		failureReason, lastError, providerRef, policyID, eventID, actErr      sql.NullString
		resumedBy, resumeNote                                                 sql.NullString
		payKind, payToken, payBrand, payLast4, payBank                        sql.NullString
		payInstallments                                                       sql.NullInt64
		payCapturedAt                                                         sql.NullTime
	)
	err := s.Scan(&c.AcceptanceID, &c.IdempotencyKey, &c.OfferID, &c.CustomerID, &channel, &c.AcceptedAt,
		&c.Holder.FullName, &c.Holder.DocumentID, &c.Holder.Email, &c.AmountMinor, &c.Currency, &insuredMinor, &c.Terms.TermMonths, &coverages,
		&status, &c.Attempts, &next, &claimed, &c.WindowStartedAt, &windowEnds, &lastAttempt, &lastFailure,
		&failureReason, &lastError, &providerRef, &confirmed,
		&policyID, &eventID, &activated, &covStart, &covEnd, &published,
		&c.ActivationAttempts, &activationNext, &actErr,
		&c.ResumeCount, &resumedBy, &lastResumedAt, &resumeNote,
		&payKind, &payToken, &payBrand, &payLast4, &payInstallments, &payBank, &payCapturedAt, &c.PaymentAttempt,
		&c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return domain.Charge{}, err
	}
	if c.Terms.Coverages, err = decodeCoverages(coverages); err != nil {
		return domain.Charge{}, fmt.Errorf("decode the coverages of %s: %w", c.AcceptanceID, err)
	}
	c.Channel, c.Status = domain.Channel(channel), domain.ChargeStatus(status)
	c.Terms.InsuredAmount = domain.FromMinorUnits(insuredMinor)
	c.NextAttemptAt, c.ClaimedUntil, c.WindowEndsAt = next.Time, claimed.Time, windowEnds.Time
	c.LastAttemptAt, c.LastFailureAt, c.ConfirmedAt = lastAttempt.Time, lastFailure.Time, confirmed.Time
	c.FailureReason = domain.ChargeFailureReason(failureReason.String)
	c.LastError, c.ProviderReference = lastError.String, providerRef.String
	c.PolicyID, c.ActivationEventID = policyID.String, eventID.String
	c.ActivatedAt, c.CoverageStart, c.CoverageEnd = activated.Time, covStart.Time, covEnd.Time
	c.ActivationPublishedAt, c.ActivationNextAttemptAt, c.ActivationLastError = published.Time, activationNext.Time, actErr.String
	c.LastResumedBy, c.LastResumedAt, c.ResumeNote = resumedBy.String, lastResumedAt.Time, resumeNote.String
	c.PaymentMethod = domain.PaymentMethod{
		Kind: domain.PaymentMethodKind(payKind.String), Token: payToken.String,
		Brand: payBrand.String, Last4: payLast4.String, Installments: int(payInstallments.Int64),
		BankCode: payBank.String, CapturedAt: payCapturedAt.Time,
	}
	c.AcceptedAt, c.WindowStartedAt = c.AcceptedAt.UTC(), c.WindowStartedAt.UTC()
	c.CreatedAt, c.UpdatedAt = c.CreatedAt.UTC(), c.UpdatedAt.UTC()
	for _, t := range []*time.Time{&c.NextAttemptAt, &c.ClaimedUntil, &c.WindowEndsAt, &c.LastAttemptAt, &c.LastFailureAt,
		&c.ConfirmedAt, &c.ActivatedAt, &c.CoverageStart, &c.CoverageEnd, &c.ActivationPublishedAt,
		&c.ActivationNextAttemptAt, &c.LastResumedAt, &c.PaymentMethod.CapturedAt} {
		if !t.IsZero() {
			*t = t.UTC()
		}
	}
	return c, nil
}

// one runs a statement that returns at most one charge; found is false when
// it returned none.
func (s *ChargeStore) one(ctx context.Context, query string, args ...any) (c domain.Charge, found bool, err error) {
	c, err = scanCharge(s.db.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Charge{}, false, nil
	}
	if err != nil {
		return domain.Charge{}, false, err
	}
	return c, true, nil
}

// held runs a statement conditional on the claim of c and maps "no row" to
// idempotency.ErrClaimLost.
func (s *ChargeStore) held(ctx context.Context, query string, args ...any) (domain.Charge, error) {
	c, found, err := s.one(ctx, query, args...)
	if err != nil {
		return domain.Charge{}, err
	}
	if !found {
		return domain.Charge{}, idempotency.ErrClaimLost
	}
	return c, nil
}

// CreatePending stores the charge unless its acceptance already has one.
func (s *ChargeStore) CreatePending(ctx context.Context, c domain.Charge) (domain.Charge, bool, error) {
	coverages, err := encodeCoverages(c.Terms.Coverages)
	if err != nil {
		return domain.Charge{}, false, err
	}
	stored, created, err := s.one(ctx, `INSERT INTO charge_attempt (
			acceptance_id, idempotency_key, offer_id, customer_id, channel, accepted_at,
			holder_full_name, holder_document_id, holder_email, amount_minor, currency, insured_amount_minor, term_months, coverages,
			status, attempts, next_attempt_at, window_started_at, window_ends_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, 0, $16, $17, $18, $19, $19)
		ON CONFLICT DO NOTHING
		RETURNING `+chargeColumns,
		c.AcceptanceID, c.IdempotencyKey, c.OfferID, c.CustomerID, string(c.Channel), c.AcceptedAt.UTC(),
		c.Holder.FullName, c.Holder.DocumentID, c.Holder.Email, c.AmountMinor, c.Currency,
		domain.ToMinorUnits(c.Terms.InsuredAmount), c.Terms.TermMonths, string(coverages),
		string(domain.ChargePending), nullTime(c.NextAttemptAt), c.WindowStartedAt.UTC(), nullTime(c.WindowEndsAt), c.CreatedAt.UTC())
	if err != nil {
		return domain.Charge{}, false, fmt.Errorf("insert the charge of %s: %w", c.AcceptanceID, err)
	}
	if created {
		return stored, true, nil
	}
	existing, err := s.Get(ctx, c.AcceptanceID)
	if err != nil {
		return domain.Charge{}, false, err
	}
	return existing, false, nil
}

// Get returns the charge of an acceptance.
func (s *ChargeStore) Get(ctx context.Context, acceptanceID string) (domain.Charge, error) {
	c, found, err := s.one(ctx, `SELECT `+chargeColumns+` FROM charge_attempt WHERE acceptance_id = $1`, acceptanceID)
	if err != nil {
		return domain.Charge{}, fmt.Errorf("read the charge of %s: %w", acceptanceID, err)
	}
	if !found {
		return domain.Charge{}, ports.ErrChargeNotFound
	}
	return c, nil
}

func (s *ChargeStore) ids(ctx context.Context, query string, args ...any) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
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

// ListDue returns the acceptances whose charge is due at now.
func (s *ChargeStore) ListDue(ctx context.Context, now time.Time, limit int) ([]string, error) {
	return s.ids(ctx, `SELECT acceptance_id FROM charge_attempt
		WHERE (status = 'pending' AND next_attempt_at <= $1) OR (status = 'in_progress' AND claimed_until < $1)
		ORDER BY next_attempt_at NULLS FIRST, acceptance_id LIMIT $2`, now.UTC(), limit)
}

// ListActivationsDue returns the acceptances whose activation must be published.
func (s *ChargeStore) ListActivationsDue(ctx context.Context, now time.Time, limit int) ([]string, error) {
	return s.ids(ctx, `SELECT acceptance_id FROM charge_attempt
		WHERE status = 'confirmed' AND activation_published_at IS NULL AND activation_next_attempt_at <= $1
		  AND (claimed_until IS NULL OR claimed_until < $1)
		ORDER BY activation_next_attempt_at, acceptance_id LIMIT $2`, now.UTC(), limit)
}

// ListExceptions returns the failed charges, the latest failure first.
func (s *ChargeStore) ListExceptions(ctx context.Context, limit int) ([]domain.Charge, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+chargeColumns+` FROM charge_attempt
		WHERE status = 'failed' ORDER BY last_failure_at DESC NULLS LAST, acceptance_id LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list the issuance exceptions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []domain.Charge
	for rows.Next() {
		c, err := scanCharge(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Claim is the atomic claim of ADR-01 with the lease of decision D-8.
func (s *ChargeStore) Claim(ctx context.Context, acceptanceID string, now time.Time, lease time.Duration) (domain.Charge, bool, error) {
	now = now.UTC()
	c, claimed, err := s.one(ctx, `UPDATE charge_attempt
		SET status = 'in_progress', claimed_until = $3, last_attempt_at = $2, updated_at = $2
		WHERE acceptance_id = $1
		  AND ((status = 'pending' AND next_attempt_at <= $2) OR (status = 'in_progress' AND claimed_until < $2))
		RETURNING `+chargeColumns, acceptanceID, now, now.Add(lease))
	if err != nil {
		return domain.Charge{}, false, fmt.Errorf("claim the charge of %s: %w", acceptanceID, err)
	}
	return c, claimed, nil
}

// Confirm records the confirmed charge and, in the same statement, the
// coverage activation it produces (policy, event and coverage period). The
// claim stays held so the same worker publishes the activation right away.
func (s *ChargeStore) Confirm(ctx context.Context, c domain.Charge, now time.Time, out idempotency.Outcome) (domain.Charge, error) {
	cc := c.Confirmed(now.UTC(), out.Reference)
	return s.held(ctx, `UPDATE charge_attempt
		SET status = 'confirmed', confirmed_at = $3, provider_reference = $4, next_attempt_at = NULL,
		    failure_reason = NULL, policy_id = $5, activation_event_id = $6, activated_at = $7, coverage_start = $8,
		    coverage_end = $9, activation_next_attempt_at = $10, updated_at = $3
		WHERE acceptance_id = $1 AND status = 'in_progress' AND claimed_until = $2
		RETURNING `+chargeColumns,
		c.AcceptanceID, c.ClaimedUntil.UTC(), cc.ConfirmedAt, nullString(cc.ProviderReference), cc.PolicyID,
		cc.ActivationEventID, cc.ActivatedAt, cc.CoverageStart, cc.CoverageEnd, cc.ActivationNextAttemptAt)
}

// Reschedule makes the charge pending again with one more failed attempt.
func (s *ChargeStore) Reschedule(ctx context.Context, c domain.Charge, now, next time.Time, cause string) (domain.Charge, error) {
	return s.held(ctx, `UPDATE charge_attempt
		SET status = 'pending', attempts = attempts + 1, next_attempt_at = $4, claimed_until = NULL,
		    last_failure_at = $3, last_error = $5, updated_at = $3
		WHERE acceptance_id = $1 AND status = 'in_progress' AND claimed_until = $2
		RETURNING `+chargeColumns, c.AcceptanceID, c.ClaimedUntil.UTC(), now.UTC(), next.UTC(), cause)
}

// Fail marks the charge failed (an issuance exception) for the reason.
func (s *ChargeStore) Fail(ctx context.Context, c domain.Charge, now time.Time, reason idempotency.FailureReason, cause string) (domain.Charge, error) {
	return s.held(ctx, `UPDATE charge_attempt
		SET status = 'failed', attempts = attempts + 1, next_attempt_at = NULL, claimed_until = NULL,
		    failure_reason = $4, last_failure_at = $3, last_error = $5, updated_at = $3
		WHERE acceptance_id = $1 AND status = 'in_progress' AND claimed_until = $2
		RETURNING `+chargeColumns,
		c.AcceptanceID, c.ClaimedUntil.UTC(), now.UTC(), string(domain.ChargeFailureReasonFor(reason)), cause)
}

// Resume moves a failed charge back to pending, conditionally on it being
// failed, so of two analysts resuming at once only one wins.
func (s *ChargeStore) Resume(ctx context.Context, acceptanceID string, r domain.ChargeResumption) (domain.Charge, error) {
	at := r.At.UTC()
	// A charge that failed with payment_method_missing never had a payment
	// method, so it must not become due: the worker would call the gateway
	// with no token. It goes back to waiting for one -pending with no due
	// date- which is the state HU-W1.3-02 already uses for that, and the
	// customer can pay from screen E4 within the new window.
	c, found, err := s.one(ctx, `UPDATE charge_attempt
		SET status = 'pending', attempts = 0, claimed_until = NULL, failure_reason = NULL,
		    next_attempt_at = CASE WHEN payment_method_kind IS NULL THEN NULL ELSE $2::timestamptz END,
		    window_started_at = $2, window_ends_at = $3, resume_count = resume_count + 1, last_resumed_by = $4,
		    last_resumed_at = $2, resume_note = $5, updated_at = $2
		WHERE acceptance_id = $1 AND status = 'failed'
		RETURNING `+chargeColumns, acceptanceID, at, nullTime(r.WindowEndsAt), r.By, r.Note)
	if err != nil {
		return domain.Charge{}, fmt.Errorf("resume the charge of %s: %w", acceptanceID, err)
	}
	if found {
		return c, nil
	}
	if _, err := s.Get(ctx, acceptanceID); err != nil {
		return domain.Charge{}, err
	}
	return domain.Charge{}, ports.ErrChargeNotResumable
}

// ClaimActivation holds the publication of a confirmed, unpublished activation.
func (s *ChargeStore) ClaimActivation(ctx context.Context, acceptanceID string, now time.Time, lease time.Duration) (domain.Charge, bool, error) {
	now = now.UTC()
	c, claimed, err := s.one(ctx, `UPDATE charge_attempt
		SET claimed_until = $3, updated_at = $2
		WHERE acceptance_id = $1 AND status = 'confirmed' AND activation_published_at IS NULL
		  AND activation_next_attempt_at <= $2 AND (claimed_until IS NULL OR claimed_until < $2)
		RETURNING `+chargeColumns, acceptanceID, now, now.Add(lease))
	if err != nil {
		return domain.Charge{}, false, fmt.Errorf("claim the activation of %s: %w", acceptanceID, err)
	}
	return c, claimed, nil
}

// MarkActivationPublished records the published activation and releases the claim.
func (s *ChargeStore) MarkActivationPublished(ctx context.Context, c domain.Charge, now time.Time) (domain.Charge, error) {
	return s.held(ctx, `UPDATE charge_attempt
		SET activation_published_at = $3, activation_next_attempt_at = NULL, claimed_until = NULL, updated_at = $3
		WHERE acceptance_id = $1 AND status = 'confirmed' AND activation_published_at IS NULL AND claimed_until = $2
		RETURNING `+chargeColumns, c.AcceptanceID, c.ClaimedUntil.UTC(), now.UTC())
}

// RescheduleActivation records a failed publication and releases the claim.
func (s *ChargeStore) RescheduleActivation(ctx context.Context, c domain.Charge, now, next time.Time, cause string) (domain.Charge, error) {
	return s.held(ctx, `UPDATE charge_attempt
		SET activation_attempts = activation_attempts + 1, activation_next_attempt_at = $4, activation_last_error = $5,
		    claimed_until = NULL, updated_at = $3
		WHERE acceptance_id = $1 AND status = 'confirmed' AND activation_published_at IS NULL AND claimed_until = $2
		RETURNING `+chargeColumns, c.AcceptanceID, c.ClaimedUntil.UTC(), now.UTC(), next.UTC(), cause)
}

// AttachPaymentMethod stores the payment method and makes the charge due.
//
// It is one statement on purpose. The WHERE carries the rule of
// domain.TakesPaymentMethod, so two requests racing to pay the same
// acceptance cannot both win, and the new key is computed from the counter
// the database itself increments, so it can never be stale.
func (s *ChargeStore) AttachPaymentMethod(ctx context.Context, acceptanceID string,
	m domain.PaymentMethod, now time.Time) (domain.Charge, error) {
	at := now.UTC()
	// The key of a retry after a decline: "cobro-<acceptance>-<nth payment>".
	// The first payment keeps the key HA-D02 already uses.
	keyBase, _ := idempotency.Key(domain.ChargeKeyPrefix, acceptanceID)

	c, found, err := s.one(ctx, `UPDATE charge_attempt SET
			payment_method_kind = $2, payment_token = $3, payment_brand = $4, payment_last4 = $5,
			payment_installments = $6, payment_bank_code = $7, payment_method_captured_at = $8,
			payment_attempt = payment_attempt + 1,
			status = 'pending', next_attempt_at = $8, claimed_until = NULL,
			attempts = CASE WHEN status = 'failed' THEN 0 ELSE attempts END,
			idempotency_key = CASE WHEN status = 'failed'
				THEN $9 || '-' || (payment_attempt + 1)::text ELSE idempotency_key END,
			failure_reason = NULL, last_error = NULL, updated_at = $8
		WHERE acceptance_id = $1
		  AND ((status = 'pending' AND attempts = 0)
		       OR (status = 'failed' AND failure_reason = 'payment_rejected'))
		RETURNING `+chargeColumns,
		acceptanceID, string(m.Kind), nullString(m.Token), nullString(m.Brand), nullString(m.Last4),
		m.Installments, nullString(m.BankCode), at, keyBase)
	if err != nil {
		return domain.Charge{}, fmt.Errorf("attach the payment method of %s: %w", acceptanceID, err)
	}
	if found {
		return c, nil
	}
	// It matched nothing: either there is no charge, or it does not take one.
	if _, err := s.Get(ctx, acceptanceID); err != nil {
		return domain.Charge{}, err
	}
	return domain.Charge{}, ports.ErrChargeNotPayable
}

// ExpireWithoutPaymentMethod fails the charges that ran out of window while
// waiting for a payment method. The reason is not retry_window_expired: no
// retry ever happened, and saying so would send operations looking for a
// gateway problem that never existed.
func (s *ChargeStore) ExpireWithoutPaymentMethod(ctx context.Context, now time.Time) ([]string, error) {
	at := now.UTC()
	// It returns the acceptances and not a count so the worker can log one
	// line per charge, with its id, like every other failure. A count told
	// operations that something expired but not what, and the exception in
	// their tray had no line behind it.
	ids, err := s.ids(ctx, `UPDATE charge_attempt
		SET status = 'failed', failure_reason = $2, last_failure_at = $1, next_attempt_at = NULL,
		    last_error = 'the customer did not hand over a payment method before the retry window ended',
		    updated_at = $1
		WHERE status = 'pending' AND next_attempt_at IS NULL
		  AND window_ends_at IS NOT NULL AND window_ends_at <= $1
		RETURNING acceptance_id`,
		at, string(domain.ChargePaymentMethodMissing))
	if err != nil {
		return nil, fmt.Errorf("expire the charges with no payment method: %w", err)
	}
	return ids, nil
}
