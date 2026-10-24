-- HU-M1.3-02: the revocation of a consent and its notice to each financial
-- entity. Append-only like the rest of the audit database: no row is ever
-- updated nor deleted.
--
-- consent_revocation is the immutable record of criterion 4 (holder, consent,
-- scope, request and effective times, channel and device), written in the
-- same transaction as the revocation_requested and revoked events of
-- consent_event, before the holder gets the confirmation.
--
-- The notice to each entity is a queue made only of inserts (decision D-3 a):
-- revocation_notice is its header (one per consent and entity, with the
-- idempotency key) and every change of state is a new row of
-- revocation_notice_step with the next seq. The claim of pkg/idempotency is
-- one INSERT ... SELECT seq + 1 ... ON CONFLICT DO NOTHING over the last
-- step: atomic thanks to the primary key.
--
-- No table has a FOREIGN KEY (decision #89): a foreign key to an append-only
-- table makes PostgreSQL refuse its TRUNCATE before its trigger runs. The
-- references are validated by BEFORE INSERT triggers instead:
-- require_existing_consent() of migration 0002 for the consent_id of
-- consent_revocation and revocation_notice, and require_existing_notice()
-- below for the header of a step. Both raise foreign_key_violation (23503).

-- The receipt numbers, REV-<year>-<sequence> (decision D-5 a). A number taken
-- by a transaction that rolls back is lost: there may be gaps, never repeats.
CREATE SEQUENCE IF NOT EXISTS revocation_receipt_seq;

CREATE TABLE IF NOT EXISTS consent_revocation (
    revocation_id       TEXT        PRIMARY KEY,
    consent_id          TEXT        NOT NULL,
    receipt_number      TEXT        NOT NULL,
    holder_document_id  TEXT        NOT NULL,
    holder_full_name    TEXT        NOT NULL,
    -- Copy of the scope of the consent as it was revoked.
    scope               JSONB       NOT NULL,
    -- The entities notified, with the name shown at the time.
    entities            JSONB       NOT NULL,
    -- The device's time of the request (criterion 5) and the server time
    -- when audit recorded it and stopped verifying the consent as active.
    requested_at        TIMESTAMPTZ NOT NULL,
    effective_at        TIMESTAMPTZ NOT NULL,
    notice_deadline_at  TIMESTAMPTZ NOT NULL,
    channel             TEXT        NOT NULL,
    device              TEXT,
    confirmation_method TEXT,
    actor_type          TEXT        NOT NULL,
    actor_id            TEXT        NOT NULL,
    reason              TEXT,
    reference           TEXT,
    -- The Idempotency-Key of the request.
    request_id          TEXT        NOT NULL,
    recorded_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT consent_revocation_consent UNIQUE (consent_id),
    CONSTRAINT consent_revocation_receipt UNIQUE (receipt_number),
    CONSTRAINT consent_revocation_request UNIQUE (request_id)
);

CREATE TABLE IF NOT EXISTS revocation_notice (
    consent_id      TEXT        NOT NULL,
    entity_code     TEXT        NOT NULL,
    -- Sent to the entity on every attempt: revocation-<consent>-<entity>.
    idempotency_key TEXT        NOT NULL UNIQUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (consent_id, entity_code)
);

CREATE TABLE IF NOT EXISTS revocation_notice_step (
    consent_id      TEXT        NOT NULL,
    entity_code     TEXT        NOT NULL,
    seq             INTEGER     NOT NULL CHECK (seq >= 1),
    status          TEXT        NOT NULL CHECK (status IN ('pending', 'in_progress', 'confirmed', 'failed')),
    failed_attempts INTEGER     NOT NULL DEFAULT 0 CHECK (failed_attempts >= 0),
    window_ends_at  TIMESTAMPTZ NOT NULL,
    next_attempt_at TIMESTAMPTZ,
    claimed_until   TIMESTAMPTZ,
    -- The entity's reference of a confirmed notice.
    reference       TEXT,
    -- The last error, and why a failed step failed (rejected or
    -- retry_window_expired).
    cause           TEXT,
    reason          TEXT,
    recorded_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (consent_id, entity_code, seq)
);

-- The header of a step must exist, as a foreign key would require.
CREATE OR REPLACE FUNCTION require_existing_notice() RETURNS trigger AS $$
BEGIN
    PERFORM 1 FROM revocation_notice WHERE consent_id = NEW.consent_id AND entity_code = NEW.entity_code;
    IF NOT FOUND THEN
        RAISE EXCEPTION '% references the notice of the consent % to %, which does not exist',
            TG_TABLE_NAME, NEW.consent_id, NEW.entity_code
            USING ERRCODE = 'foreign_key_violation';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER consent_revocation_requires_consent
    BEFORE INSERT ON consent_revocation
    FOR EACH ROW EXECUTE FUNCTION require_existing_consent();

CREATE TRIGGER revocation_notice_requires_consent
    BEFORE INSERT ON revocation_notice
    FOR EACH ROW EXECUTE FUNCTION require_existing_consent();

CREATE TRIGGER revocation_notice_step_requires_notice
    BEFORE INSERT ON revocation_notice_step
    FOR EACH ROW EXECUTE FUNCTION require_existing_notice();

CREATE TRIGGER consent_revocation_no_update_delete
    BEFORE UPDATE OR DELETE ON consent_revocation
    FOR EACH ROW EXECUTE FUNCTION forbid_append_only_mutation();

CREATE TRIGGER consent_revocation_no_truncate
    BEFORE TRUNCATE ON consent_revocation
    FOR EACH STATEMENT EXECUTE FUNCTION forbid_append_only_mutation();

CREATE TRIGGER revocation_notice_no_update_delete
    BEFORE UPDATE OR DELETE ON revocation_notice
    FOR EACH ROW EXECUTE FUNCTION forbid_append_only_mutation();

CREATE TRIGGER revocation_notice_no_truncate
    BEFORE TRUNCATE ON revocation_notice
    FOR EACH STATEMENT EXECUTE FUNCTION forbid_append_only_mutation();

CREATE TRIGGER revocation_notice_step_no_update_delete
    BEFORE UPDATE OR DELETE ON revocation_notice_step
    FOR EACH ROW EXECUTE FUNCTION forbid_append_only_mutation();

CREATE TRIGGER revocation_notice_step_no_truncate
    BEFORE TRUNCATE ON revocation_notice_step
    FOR EACH STATEMENT EXECUTE FUNCTION forbid_append_only_mutation();
