-- HU-M1.3-01: the link between the account of an insured (the subject of the
-- user pool) and their identity document. It arrives with the CustomerLinked
-- event of origination and lets audit recognize the holder of the consents
-- granted with that document. Append-only like the rest of the database: a
-- link is never updated nor deleted. The first link of a subject is kept; a
-- different document for the same subject is ignored and logged.
CREATE TABLE IF NOT EXISTS holder_link (
    subject     TEXT        PRIMARY KEY,
    -- Normalized: trimmed and upper case (domain.NormalizeDocument).
    document_id TEXT        NOT NULL,
    customer_id TEXT        NOT NULL,
    linked_at   TIMESTAMPTZ NOT NULL,
    -- The CustomerLinked event that carried it.
    event_id    TEXT        NOT NULL UNIQUE,
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS holder_link_document_idx ON holder_link (document_id);

CREATE TRIGGER holder_link_no_update_delete
    BEFORE UPDATE OR DELETE ON holder_link
    FOR EACH ROW EXECUTE FUNCTION forbid_append_only_mutation();

CREATE TRIGGER holder_link_no_truncate
    BEFORE TRUNCATE ON holder_link
    FOR EACH STATEMENT EXECUTE FUNCTION forbid_append_only_mutation();
