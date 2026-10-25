-- HU-W1.3-04: the contractual issuance of each policy (HU-W1.3-03) in
-- PostgreSQL. It lived in memory, so a restart lost which policies had their
-- document and their notification, and the monitoring of the issuances could
-- not tell an active policy from a notified one, nor show that the document
-- generator or the e-mail failed.
--
-- One row per policy. event keeps the whole CoverageActivated the document is
-- built from and document the stored version, both as JSON. The worker of
-- each task claims the due rows with a lease (claimed_until), so two tasks
-- never advance the same issuance at once.
CREATE TABLE IF NOT EXISTS contract_issuance (
    policy_id            TEXT        PRIMARY KEY,
    acceptance_id        TEXT        NOT NULL,
    event_id             TEXT        NOT NULL,
    event                JSONB       NOT NULL,
    status               TEXT        NOT NULL CHECK (status IN ('pending', 'document_ready', 'completed', 'failed')),
    document             JSONB,
    notification_status  TEXT        NOT NULL CHECK (notification_status IN ('pending', 'sent', 'failed')),
    notification_channel TEXT        NOT NULL,
    notified_at          TIMESTAMPTZ,
    available_at         TIMESTAMPTZ,
    attempts             INTEGER     NOT NULL DEFAULT 0,
    last_failure_at      TIMESTAMPTZ,
    last_failure         TEXT        NOT NULL DEFAULT '',
    next_attempt_at      TIMESTAMPTZ,
    claimed_until        TIMESTAMPTZ,
    received_at          TIMESTAMPTZ NOT NULL,
    updated_at           TIMESTAMPTZ NOT NULL
);

-- The monitor joins the issuance of an acceptance.
CREATE INDEX IF NOT EXISTS contract_issuance_acceptance_idx ON contract_issuance (acceptance_id);

-- The worker looks up the issuances still in progress by their next attempt.
CREATE INDEX IF NOT EXISTS contract_issuance_due_idx ON contract_issuance (next_attempt_at)
    WHERE status IN ('pending', 'document_ready');
