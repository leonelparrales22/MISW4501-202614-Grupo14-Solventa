-- HA-D02: premium charge of each acceptance (Intento_Cobro), one row per
-- acceptance with its transitions (ADR-01, experiment EC-D02). The row keeps a
-- snapshot of the acceptance, the offer and the coverage terms, so the
-- coverage activation published after the payment is always the same event.
CREATE TABLE IF NOT EXISTS charge_attempt (
    -- Identity: one charge per acceptance, one deterministic key per charge.
    acceptance_id               TEXT        PRIMARY KEY,
    idempotency_key             TEXT        NOT NULL UNIQUE,
    offer_id                    TEXT        NOT NULL,
    customer_id                 TEXT        NOT NULL,
    channel                     TEXT        NOT NULL,
    accepted_at                 TIMESTAMPTZ NOT NULL,

    -- Snapshot used to charge and to build the CoverageActivated event.
    holder_full_name            TEXT        NOT NULL,
    holder_document_id          TEXT        NOT NULL,
    holder_email                TEXT        NOT NULL,
    amount_minor                BIGINT      NOT NULL CHECK (amount_minor > 0),
    currency                    TEXT        NOT NULL,
    insured_amount_minor        BIGINT      NOT NULL,
    term_months                 INTEGER     NOT NULL,
    coverages                   JSONB       NOT NULL,

    -- Charge.
    status                      TEXT        NOT NULL CHECK (status IN ('pending', 'in_progress', 'confirmed', 'failed')),
    attempts                    INTEGER     NOT NULL DEFAULT 0,
    next_attempt_at             TIMESTAMPTZ,
    claimed_until               TIMESTAMPTZ,
    window_started_at           TIMESTAMPTZ NOT NULL,
    window_ends_at              TIMESTAMPTZ,
    last_attempt_at             TIMESTAMPTZ,
    last_failure_at             TIMESTAMPTZ,
    failure_reason              TEXT        CHECK (failure_reason IN ('retry_window_expired', 'payment_rejected')),
    last_error                  TEXT,
    provider_reference          TEXT,
    confirmed_at                TIMESTAMPTZ,

    -- Coverage activation, set in the same statement that confirms the charge.
    policy_id                   TEXT        UNIQUE,
    activation_event_id         TEXT,
    activated_at                TIMESTAMPTZ,
    coverage_start              TIMESTAMPTZ,
    coverage_end                TIMESTAMPTZ,
    activation_published_at     TIMESTAMPTZ,
    activation_attempts         INTEGER     NOT NULL DEFAULT 0,
    activation_next_attempt_at  TIMESTAMPTZ,
    activation_last_error       TEXT,

    -- Resumptions by an operations analyst.
    resume_count                INTEGER     NOT NULL DEFAULT 0,
    last_resumed_by             TEXT,
    last_resumed_at             TIMESTAMPTZ,
    resume_note                 TEXT,

    created_at                  TIMESTAMPTZ NOT NULL,
    updated_at                  TIMESTAMPTZ NOT NULL
);

-- The worker looks up the due charges by state and next attempt.
CREATE INDEX IF NOT EXISTS charge_attempt_due_idx ON charge_attempt (status, next_attempt_at);

-- The worker looks up the confirmed charges whose activation is unpublished.
CREATE INDEX IF NOT EXISTS charge_attempt_activation_due_idx ON charge_attempt (activation_next_attempt_at)
    WHERE status = 'confirmed' AND activation_published_at IS NULL;
