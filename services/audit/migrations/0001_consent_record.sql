-- HU-W1.2-01: explicit consent of the customer to be profiled. The table is
-- append-only: a row is never updated or deleted (criterion 4). A future
-- revocation (out of scope, decision D2) will be a new row, not an UPDATE.
-- Partitioning by date is pending.
CREATE TABLE IF NOT EXISTS consent_record (
    consent_id         TEXT        PRIMARY KEY,
    customer_id        TEXT        NOT NULL,
    holder_document_id TEXT        NOT NULL,
    holder_full_name   TEXT        NOT NULL,
    terms_version      TEXT        NOT NULL,
    -- Copy of the sources as the customer saw them (data, validity).
    scope              JSONB       NOT NULL,
    purpose            TEXT        NOT NULL,
    channel            TEXT        NOT NULL,
    partner_id         TEXT        NOT NULL,
    application_id     TEXT        NOT NULL,
    user_agent         TEXT        NOT NULL DEFAULT '',
    granted_at         TIMESTAMPTZ NOT NULL,
    recorded_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Granting again for the same application and terms is idempotent.
    CONSTRAINT consent_record_idempotency UNIQUE (customer_id, application_id, terms_version)
);

CREATE INDEX IF NOT EXISTS consent_record_customer_granted_idx
    ON consent_record (customer_id, granted_at DESC);

CREATE OR REPLACE FUNCTION forbid_consent_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'consent_record is append-only: % is not allowed', TG_OP
        USING ERRCODE = 'restrict_violation';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER consent_record_no_update_delete
    BEFORE UPDATE OR DELETE ON consent_record
    FOR EACH ROW EXECUTE FUNCTION forbid_consent_mutation();

CREATE TRIGGER consent_record_no_truncate
    BEFORE TRUNCATE ON consent_record
    FOR EACH STATEMENT EXECUTE FUNCTION forbid_consent_mutation();
