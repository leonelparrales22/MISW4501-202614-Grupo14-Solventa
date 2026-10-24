-- HU-M1.3-01: the changes of state of a consent, append-only like the rest of
-- the audit database. The status of a consent is never a column that changes:
-- it is derived from consent_record and these events (decision D-4 a). This
-- story only reads them; the revocation (HU-M1.3-02) and the revocation by
-- entity (HU-W4.2-02) write them.
--
-- entity_code is NULL for an event of the whole consent and names the entity
-- for an event of one of them. status has no CHECK of values on purpose: the
-- stories that add statuses do it without DDL, and the domain validates them.
--
-- consent_id has no FOREIGN KEY to consent_record on purpose: PostgreSQL
-- refuses to TRUNCATE a referenced table before its triggers run, which would
-- replace the append-only error of consent_record (restrict_violation,
-- HU-W1.2-01 criterion 4) with another one. A consent_record row is never
-- deleted, so the reference cannot dangle; whoever appends an event checks
-- that the consent exists in the same statement.

-- One function for every append-only table of the database.
CREATE OR REPLACE FUNCTION forbid_append_only_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION '% is append-only: % is not allowed', TG_TABLE_NAME, TG_OP
        USING ERRCODE = 'restrict_violation';
END;
$$ LANGUAGE plpgsql;

CREATE TABLE IF NOT EXISTS consent_event (
    event_id    TEXT        PRIMARY KEY,
    consent_id  TEXT        NOT NULL,
    entity_code TEXT,
    status      TEXT        NOT NULL CHECK (status <> ''),
    occurred_at TIMESTAMPTZ NOT NULL,
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    actor_type  TEXT,
    actor_id    TEXT,
    channel     TEXT,
    device      TEXT,
    receipt_id  TEXT,
    reason      TEXT,
    reference   TEXT,
    details     JSONB
);

CREATE INDEX IF NOT EXISTS consent_event_consent_occurred_idx
    ON consent_event (consent_id, occurred_at);

CREATE TRIGGER consent_event_no_update_delete
    BEFORE UPDATE OR DELETE ON consent_event
    FOR EACH ROW EXECUTE FUNCTION forbid_append_only_mutation();

CREATE TRIGGER consent_event_no_truncate
    BEFORE TRUNCATE ON consent_event
    FOR EACH STATEMENT EXECUTE FUNCTION forbid_append_only_mutation();

-- The holder lists their consents by document, compared without case.
CREATE INDEX IF NOT EXISTS consent_record_holder_document_idx
    ON consent_record (upper(holder_document_id), granted_at DESC);
