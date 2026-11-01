-- HU-M3.1-01: the notification center of every insured, their devices and the
-- push of each notification to each device.

-- One row per notification. It is recorded when the event arrives, whether or
-- not a push ever reaches a device, and never overwritten: the id is derived
-- from the fact it reports (one per parametric payout).
CREATE TABLE notification (
    id          TEXT        PRIMARY KEY,
    subject     TEXT        NOT NULL,
    kind        TEXT        NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL,
    read_at     TIMESTAMPTZ,
    -- The data of the fact (the payout, its trigger and payment method).
    payload     JSONB       NOT NULL
);

CREATE INDEX notification_subject_occurred ON notification (subject, occurred_at DESC, id DESC);
CREATE INDEX notification_subject_unread ON notification (subject) WHERE read_at IS NULL;

-- One row per installation of the app. A phone that changes of account keeps
-- its installation id and moves to the new subject.
CREATE TABLE device (
    installation_id TEXT        PRIMARY KEY,
    subject         TEXT        NOT NULL,
    platform        TEXT        NOT NULL,
    token           TEXT        NOT NULL DEFAULT '',
    push_enabled    BOOLEAN     NOT NULL,
    locale          TEXT        NOT NULL,
    updated_at      TIMESTAMPTZ NOT NULL
);

CREATE INDEX device_subject ON device (subject);

-- The push of one notification to one device, with the claim and retry
-- protocol of pkg/idempotency: the same key on every attempt, a growing wait
-- between them and a window of 24 h.
CREATE TABLE push_delivery (
    id              TEXT        PRIMARY KEY,
    notification_id TEXT        NOT NULL REFERENCES notification (id),
    installation_id TEXT        NOT NULL,
    token           TEXT        NOT NULL,
    locale          TEXT        NOT NULL,
    idempotency_key TEXT        NOT NULL,
    status          TEXT        NOT NULL CHECK (status IN ('pending', 'in_progress', 'confirmed', 'failed')),
    failed_attempts INTEGER     NOT NULL DEFAULT 0,
    window_ends_at  TIMESTAMPTZ NOT NULL,
    next_attempt_at TIMESTAMPTZ NOT NULL,
    claimed_until   TIMESTAMPTZ,
    reference       TEXT,
    sent_at         TIMESTAMPTZ,
    failure_reason  TEXT,
    last_error      TEXT,
    created_at      TIMESTAMPTZ NOT NULL,
    updated_at      TIMESTAMPTZ NOT NULL
);

CREATE INDEX push_delivery_due ON push_delivery (next_attempt_at) WHERE status IN ('pending', 'in_progress');
