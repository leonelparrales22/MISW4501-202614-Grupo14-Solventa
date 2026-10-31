-- HU-W2.2-03: the decision of operations on a claim, and the payout of its
-- indemnity.
--
-- Numbered 0006, after 0005_claim_triage of HU-W2.2-01. It depends on the
-- claim table only.

-- One decision per claim: who decided, when, and what (criterion 5). An
-- approval keeps the amount and the terms it was checked against, and the
-- payout of its indemnity as an idempotent attempt of pkg/idempotency
-- (HA-D02): one key per approval, so one approval is one disbursement
-- however many times it is retried. A rejection keeps its reason and the
-- notice to the insured.
CREATE TABLE IF NOT EXISTS claim_decision (
    claim_id                 TEXT        PRIMARY KEY REFERENCES claim (id),
    outcome                  TEXT        NOT NULL CHECK (outcome IN ('approved', 'rejected')),
    decided_by               TEXT        NOT NULL,
    decided_at               TIMESTAMPTZ NOT NULL,

    -- The approval.
    approved_amount_minor    BIGINT      CHECK (approved_amount_minor > 0),
    insured_value_minor      BIGINT,
    deductible_basis_points  INTEGER     CHECK (deductible_basis_points BETWEEN 0 AND 10000),
    currency                 TEXT,

    -- The payout. The account number is kept because it is needed to pay;
    -- the reads of the service only show it masked.
    payout_key               TEXT        UNIQUE,
    beneficiary_id           TEXT,
    payout_amount_minor      BIGINT      CHECK (payout_amount_minor > 0),
    bank_code                TEXT,
    bank_name                TEXT,
    account_type             TEXT,
    account_number           TEXT,
    payout_status            TEXT        CHECK (payout_status IN ('pending', 'in_progress', 'confirmed', 'failed')),
    payout_attempts          INTEGER     NOT NULL DEFAULT 0,
    next_attempt_at          TIMESTAMPTZ,
    claimed_until            TIMESTAMPTZ,
    last_attempt_at          TIMESTAMPTZ,
    last_failure_at          TIMESTAMPTZ,
    payout_failure           TEXT        CHECK (payout_failure IN ('rejected', 'retries_exhausted')),
    last_error               TEXT,
    gateway_code             TEXT,
    payout_reference         TEXT,
    paid_at                  TIMESTAMPTZ,
    manual_retries           INTEGER     NOT NULL DEFAULT 0,
    last_retried_by          TEXT,
    last_retried_at          TIMESTAMPTZ,

    -- The rejection and its notice.
    rejection_reason         TEXT        CHECK (char_length(rejection_reason) BETWEEN 1 AND 1000),
    notice_to                TEXT,
    notice_sent_at           TIMESTAMPTZ,
    notice_attempts          INTEGER     NOT NULL DEFAULT 0,
    notice_next_attempt_at   TIMESTAMPTZ,
    notice_last_error        TEXT,

    CONSTRAINT claim_decision_shape CHECK (
        (outcome = 'approved' AND approved_amount_minor IS NOT NULL AND payout_key IS NOT NULL
            AND payout_status IS NOT NULL AND rejection_reason IS NULL)
        OR (outcome = 'rejected' AND rejection_reason IS NOT NULL AND notice_to IS NOT NULL
            AND payout_key IS NULL))
);

-- The worker looks up the due payouts and the due notices.
CREATE INDEX IF NOT EXISTS claim_decision_payout_due ON claim_decision (payout_status, next_attempt_at)
    WHERE outcome = 'approved';
CREATE INDEX IF NOT EXISTS claim_decision_notice_due ON claim_decision (notice_next_attempt_at)
    WHERE outcome = 'rejected' AND notice_sent_at IS NULL;

-- Every manual retry of a payout, with who did it and whether the destination
-- was corrected, so the history of the case keeps them all.
CREATE TABLE IF NOT EXISTS claim_payout_retry (
    claim_id       TEXT        NOT NULL REFERENCES claim_decision (claim_id),
    seq            INTEGER     NOT NULL CHECK (seq >= 1),
    retried_by     TEXT        NOT NULL,
    retried_at     TIMESTAMPTZ NOT NULL,
    destination_changed BOOLEAN NOT NULL,
    PRIMARY KEY (claim_id, seq)
);
