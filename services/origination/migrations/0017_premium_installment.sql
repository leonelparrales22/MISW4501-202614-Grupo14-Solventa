-- HU-W1.6-02: monthly premium instalments and the cancellation of a policy
-- for non-payment.
--
-- The premium of a policy origination issues is monthly. The charge of the
-- issuance (charge, HA-D02) pays the first month; every later month is an
-- instalment, due on the day of the month the coverage started. An instalment
-- not paid within its grace period cancels the policy, with no refund.
--
-- installment_billing has one row: when the instalments started to be billed.
-- A month that fell due before it was never billed by this system, so it is
-- not charged now (and a policy issued long ago is not cancelled for months
-- it was never asked to pay).
CREATE TABLE IF NOT EXISTS installment_billing (
    singleton  BOOLEAN     PRIMARY KEY DEFAULT true CHECK (singleton),
    started_at TIMESTAMPTZ NOT NULL
);
INSERT INTO installment_billing (singleton, started_at) VALUES (true, now()) ON CONFLICT DO NOTHING;

-- premium_installment is one month of one policy. It is created when it falls
-- due (seq 2 is the second month: the first one is the charge of the
-- issuance) and moves pending -> paid, or pending -> unpaid when the policy is
-- cancelled. attempts counts the attempts the gateway declined; an attempt
-- whose outcome is unknown keeps its key and is tried again without counting.
-- claimed_until is the lease of the task that is charging it, so two tasks
-- never charge the same attempt at once.
CREATE TABLE IF NOT EXISTS premium_installment (
    policy_id          TEXT        NOT NULL REFERENCES policy (policy_id),
    seq                INTEGER     NOT NULL CHECK (seq >= 2),
    due_at             TIMESTAMPTZ NOT NULL,
    grace_ends_at      TIMESTAMPTZ NOT NULL,
    cancels_at         TIMESTAMPTZ NOT NULL,
    amount_minor       BIGINT      NOT NULL CHECK (amount_minor > 0),
    currency           TEXT        NOT NULL,
    status             TEXT        NOT NULL CHECK (status IN ('pending', 'paid', 'unpaid')),
    attempts           INTEGER     NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at    TIMESTAMPTZ,
    claimed_until      TIMESTAMPTZ,
    paid_at            TIMESTAMPTZ,
    provider_reference TEXT,
    created_at         TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (policy_id, seq),
    CHECK (due_at < grace_ends_at AND grace_ends_at < cancels_at),
    CHECK (status <> 'paid' OR paid_at IS NOT NULL)
);

CREATE INDEX IF NOT EXISTS premium_installment_due_idx ON premium_installment (next_attempt_at)
    WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS premium_installment_cancels_idx ON premium_installment (cancels_at)
    WHERE status = 'pending';

-- premium_installment_attempt is every attempt the gateway answered, approved
-- or declined: the history of charges the insured sees (screen A2b). It is
-- only inserted.
CREATE TABLE IF NOT EXISTS premium_installment_attempt (
    policy_id          TEXT        NOT NULL,
    seq                INTEGER     NOT NULL,
    attempt            INTEGER     NOT NULL CHECK (attempt >= 1),
    idempotency_key    TEXT        NOT NULL UNIQUE,
    outcome            TEXT        NOT NULL CHECK (outcome IN ('approved', 'declined')),
    decline_code       TEXT,
    provider_reference TEXT,
    attempted_at       TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (policy_id, seq, attempt),
    FOREIGN KEY (policy_id, seq) REFERENCES premium_installment (policy_id, seq)
);

CREATE TRIGGER premium_installment_attempt_no_update_delete BEFORE UPDATE OR DELETE ON premium_installment_attempt
    FOR EACH ROW EXECUTE FUNCTION forbid_offer_mutation();

-- policy_cancellation is the cancellation of a policy. It is written once, in
-- the same transaction as the PolicyCancelledForNonPayment event in
-- outbox_event; the primary key cancels a policy once even when several tasks
-- sweep at the same time. Only the columns of the e-mail to the insured
-- change afterwards. A cancellation for non-payment never has a refund.
CREATE TABLE IF NOT EXISTS policy_cancellation (
    policy_id        TEXT        PRIMARY KEY REFERENCES policy (policy_id),
    reason           TEXT        NOT NULL CHECK (reason IN ('non_payment')),
    installment_seq  INTEGER     NOT NULL,
    cancelled_at     TIMESTAMPTZ NOT NULL,
    event_id         TEXT        NOT NULL UNIQUE,
    email_sent_at    TIMESTAMPTZ,
    email_attempts   INTEGER     NOT NULL DEFAULT 0,
    FOREIGN KEY (policy_id, installment_seq) REFERENCES premium_installment (policy_id, seq)
);

CREATE INDEX IF NOT EXISTS policy_cancellation_recent_idx ON policy_cancellation (cancelled_at);
CREATE INDEX IF NOT EXISTS policy_cancellation_unnotified_idx ON policy_cancellation (cancelled_at)
    WHERE email_sent_at IS NULL;
