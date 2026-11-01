-- HU-W3.1-01: audit record of the credentials of the commercial partners.
--
-- partners publishes PartnerCredentialsIssued (approval), PartnerCredentialsRotated
-- and PartnerCredentialsRevoked (end of the grace period) to its topic; audit
-- consumes them from its queue and keeps one row per event, with the date and
-- the analyst or process that issued them (criterion 3). The secret never
-- reaches audit: the events do not carry it nor its hint.
--
-- Append-only like the rest of the database (forbid_append_only_mutation() of
-- 0002): a row is never updated or deleted. The queue delivers at least once,
-- so event_id is unique and the store inserts with ON CONFLICT DO NOTHING.
-- No foreign keys: partner_id and credential_id belong to partners' database.
CREATE TABLE IF NOT EXISTS partner_credential_record (
    record_id              BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    event_id               TEXT        NOT NULL UNIQUE,
    event_type             TEXT        NOT NULL CHECK (event_type IN (
                               'PartnerCredentialsIssued', 'PartnerCredentialsRotated', 'PartnerCredentialsRevoked')),
    partner_id             TEXT        NOT NULL,
    partner_code           TEXT        NOT NULL,
    partner_name           TEXT        NOT NULL,
    credential_id          TEXT        NOT NULL,
    client_id              TEXT        NOT NULL,
    previous_credential_id TEXT,
    previous_client_id     TEXT,
    previous_expires_at    TIMESTAMPTZ,
    grace_period_hours     INTEGER,
    reason                 TEXT        NOT NULL CHECK (reason IN ('approval', 'rotation', 'grace_period_ended')),
    daily_limit            BIGINT      NOT NULL,
    rate_limit             BIGINT      NOT NULL,
    burst_limit            BIGINT      NOT NULL,
    actor_type             TEXT        NOT NULL CHECK (actor_type IN ('operations', 'system')),
    actor_id               TEXT        NOT NULL,
    actor_label            TEXT        NOT NULL DEFAULT '',
    occurred_at            TIMESTAMPTZ NOT NULL,
    recorded_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The history of one partner, newest first.
CREATE INDEX IF NOT EXISTS partner_credential_record_partner_idx
    ON partner_credential_record (partner_id, occurred_at DESC);

CREATE TRIGGER partner_credential_record_no_update_delete
    BEFORE UPDATE OR DELETE ON partner_credential_record
    FOR EACH ROW EXECUTE FUNCTION forbid_append_only_mutation();

CREATE TRIGGER partner_credential_record_no_truncate
    BEFORE TRUNCATE ON partner_credential_record
    FOR EACH STATEMENT EXECUTE FUNCTION forbid_append_only_mutation();
