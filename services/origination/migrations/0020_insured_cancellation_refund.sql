-- HU-W1.6-01: the cancellation of a policy at the request of its insured, with
-- the refund of the part of the current month the coverage will not use.
--
-- The cancellation reuses policy_cancellation of HU-W1.6-02 (0017) with a new
-- reason (decision D-4 a): everything that already asks whether a policy was
-- cancelled (the renewal, the alerts of expiry, the billing of the
-- instalments, the change requests and the endorsements) honours it with no
-- change. Its primary key still cancels a policy once, even when two
-- confirmations, or a confirmation and the sweep for non-payment, arrive at
-- the same time.

ALTER TABLE policy_cancellation DROP CONSTRAINT IF EXISTS policy_cancellation_reason_check;
ALTER TABLE policy_cancellation ADD CONSTRAINT policy_cancellation_reason_check
    CHECK (reason IN ('non_payment', 'insured_request'));

-- A cancellation at the request of the insured has no unpaid instalment and
-- publishes no event. The foreign key to premium_installment is not checked
-- when installment_seq is NULL.
ALTER TABLE policy_cancellation ALTER COLUMN installment_seq DROP NOT NULL;
ALTER TABLE policy_cancellation ALTER COLUMN event_id DROP NOT NULL;

-- effective_at is when the coverage ends: the cancellation itself for
-- non-payment, the end of the day for one at the request of the insured.
ALTER TABLE policy_cancellation ADD COLUMN IF NOT EXISTS effective_at TIMESTAMPTZ;
UPDATE policy_cancellation SET effective_at = cancelled_at WHERE effective_at IS NULL;
ALTER TABLE policy_cancellation ALTER COLUMN effective_at SET NOT NULL;

-- Why the insured cancelled (one of the list of decision D-9 a), their
-- optional comments and who asked (the subject of their token).
ALTER TABLE policy_cancellation ADD COLUMN IF NOT EXISTS insured_reason TEXT
    CHECK (insured_reason IN ('property_sold', 'mortgage_paid', 'better_offer', 'no_longer_needed',
                              'financial_reasons', 'other'));
ALTER TABLE policy_cancellation ADD COLUMN IF NOT EXISTS comments TEXT CHECK (char_length(comments) <= 500);
ALTER TABLE policy_cancellation ADD COLUMN IF NOT EXISTS requested_by_sub TEXT;

ALTER TABLE policy_cancellation ADD CONSTRAINT policy_cancellation_reason_columns CHECK (
    (reason = 'non_payment' AND installment_seq IS NOT NULL AND event_id IS NOT NULL AND insured_reason IS NULL)
    OR (reason = 'insured_request' AND installment_seq IS NULL AND event_id IS NULL AND insured_reason IS NOT NULL
        AND requested_by_sub IS NOT NULL));

-- refund_counter gives the number of each refund of a civil year of
-- Colombia: REF-<year>-<number>, taken in the transaction of the cancellation
-- (decision D-5r a), like endorsement_counter (0019).
CREATE TABLE IF NOT EXISTS refund_counter (
    year        INTEGER PRIMARY KEY,
    last_number INTEGER NOT NULL CHECK (last_number > 0)
);

-- policy_refund is the refund of one cancellation at the request of the
-- insured, written in the same transaction as the cancellation, with the
-- calculation the insured saw (CA-1) and where the money goes back to (CA-2).
-- A refund of nothing (the current month was not paid) is not_applicable and
-- has no reference. One in process is sent to the gateway in the background
-- (decision D-5 b, EC-D02): its key is deterministic, claimed_until is the
-- lease of the worker that sends it, and it ends completed or, when the
-- gateway rejects it or the retry window ends, failed.
CREATE TABLE IF NOT EXISTS policy_refund (
    policy_id          TEXT        PRIMARY KEY REFERENCES policy_cancellation (policy_id),
    reference          TEXT        UNIQUE CHECK (reference ~ '^REF-[0-9]{4}-[0-9]{5,}$'),
    amount_minor       BIGINT      NOT NULL CHECK (amount_minor >= 0),
    currency           TEXT        NOT NULL,
    -- The payment method of the issuance, without its token.
    method             TEXT        CHECK (method IN ('card', 'pse')),
    brand              TEXT,
    last4              TEXT,
    bank_code          TEXT,
    -- The calculation: the month paid, what was paid for it, its period
    -- [period_start, period_end) and the days of it not used.
    paid_seq           INTEGER     NOT NULL CHECK (paid_seq >= 1),
    paid_kind          TEXT        NOT NULL CHECK (paid_kind IN ('issuance', 'installment')),
    paid_amount_minor  BIGINT      NOT NULL CHECK (paid_amount_minor >= 0),
    period_start       TIMESTAMPTZ NOT NULL,
    period_end         TIMESTAMPTZ NOT NULL,
    period_days        INTEGER     NOT NULL CHECK (period_days >= 1),
    unused_days        INTEGER     NOT NULL CHECK (unused_days >= 0 AND unused_days <= period_days),
    charge_reference   TEXT,
    status             TEXT        NOT NULL CHECK (status IN ('not_applicable', 'in_process', 'completed', 'failed')),
    registered_at      TIMESTAMPTZ NOT NULL,
    -- The processing against the gateway.
    idempotency_key    TEXT        UNIQUE,
    attempts           INTEGER     NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at    TIMESTAMPTZ,
    claimed_until      TIMESTAMPTZ,
    retry_until        TIMESTAMPTZ,
    provider_reference TEXT,
    completed_at       TIMESTAMPTZ,
    failure_reason     TEXT        CHECK (failure_reason IN ('rejected', 'retry_window_expired')),
    last_error         TEXT,
    updated_at         TIMESTAMPTZ NOT NULL,
    CHECK (period_start < period_end),
    CHECK (amount_minor <= paid_amount_minor),
    CHECK ((status = 'not_applicable') = (amount_minor = 0)),
    CHECK ((amount_minor > 0) = (reference IS NOT NULL)),
    CHECK ((status = 'not_applicable') = (idempotency_key IS NULL)),
    CHECK (status <> 'completed' OR completed_at IS NOT NULL),
    CHECK (status <> 'failed' OR failure_reason IS NOT NULL)
);

-- The refunds the worker sends next.
CREATE INDEX IF NOT EXISTS policy_refund_due_idx ON policy_refund (next_attempt_at)
    WHERE status = 'in_process';

-- Only the processing of a refund changes; the calculation and where it goes
-- back to are written once, and a refund is never deleted (pattern of
-- endorsement_notice, 0019).
CREATE OR REPLACE FUNCTION guard_policy_refund_change() RETURNS trigger AS $$
BEGIN
    IF TG_OP = 'UPDATE' THEN
        IF NEW.policy_id IS DISTINCT FROM OLD.policy_id OR NEW.reference IS DISTINCT FROM OLD.reference
            OR NEW.amount_minor IS DISTINCT FROM OLD.amount_minor OR NEW.currency IS DISTINCT FROM OLD.currency
            OR NEW.method IS DISTINCT FROM OLD.method OR NEW.brand IS DISTINCT FROM OLD.brand
            OR NEW.last4 IS DISTINCT FROM OLD.last4 OR NEW.bank_code IS DISTINCT FROM OLD.bank_code
            OR NEW.paid_seq IS DISTINCT FROM OLD.paid_seq OR NEW.paid_kind IS DISTINCT FROM OLD.paid_kind
            OR NEW.paid_amount_minor IS DISTINCT FROM OLD.paid_amount_minor
            OR NEW.period_start IS DISTINCT FROM OLD.period_start OR NEW.period_end IS DISTINCT FROM OLD.period_end
            OR NEW.period_days IS DISTINCT FROM OLD.period_days OR NEW.unused_days IS DISTINCT FROM OLD.unused_days
            OR NEW.charge_reference IS DISTINCT FROM OLD.charge_reference
            OR NEW.registered_at IS DISTINCT FROM OLD.registered_at
            OR NEW.idempotency_key IS DISTINCT FROM OLD.idempotency_key
            OR NEW.retry_until IS DISTINCT FROM OLD.retry_until THEN
            RAISE EXCEPTION 'policy_refund %: only its processing can change', OLD.policy_id
                USING ERRCODE = 'restrict_violation';
        END IF;
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'policy_refund: % is not allowed', TG_OP
        USING ERRCODE = 'restrict_violation';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER policy_refund_guard BEFORE UPDATE OR DELETE ON policy_refund
    FOR EACH ROW EXECUTE FUNCTION guard_policy_refund_change();
CREATE TRIGGER policy_refund_no_truncate BEFORE TRUNCATE ON policy_refund
    FOR EACH STATEMENT EXECUTE FUNCTION guard_policy_refund_change();
