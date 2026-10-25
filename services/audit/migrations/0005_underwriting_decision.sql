-- HU-W4.1-01: audit record of every underwriting decision (input data, rules,
-- premium and outcome). Append-only like consent_record: a row is never
-- updated or deleted. Partitioning by date is pending (HA-03).
CREATE TABLE IF NOT EXISTS underwriting_decision (
    offer_id         TEXT             PRIMARY KEY,
    customer_id      TEXT             NOT NULL,
    partner_id       TEXT             NOT NULL,
    application_id   TEXT             NOT NULL,
    consent_id       TEXT             NOT NULL DEFAULT '',
    channel          TEXT             NOT NULL,
    requested_at     TIMESTAMPTZ      NOT NULL,
    rater_version    TEXT             NOT NULL,
    sources          JSONB            NOT NULL,
    profile_score    DOUBLE PRECISION NOT NULL,
    profile_category TEXT             NOT NULL,
    profile_partial  BOOLEAN          NOT NULL,
    rules            JSONB            NOT NULL,
    premium          NUMERIC(14, 2)   NOT NULL,
    currency         TEXT             NOT NULL,
    decision         TEXT             NOT NULL CHECK (decision IN ('accepted', 'rejected', 'deferred')),
    decision_reason  TEXT             NOT NULL DEFAULT '',
    recorded_at      TIMESTAMPTZ      NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS underwriting_decision_application_idx
    ON underwriting_decision (application_id, requested_at DESC);

CREATE OR REPLACE FUNCTION forbid_decision_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'underwriting_decision is append-only: % is not allowed', TG_OP
        USING ERRCODE = 'restrict_violation';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER underwriting_decision_no_update_delete
    BEFORE UPDATE OR DELETE ON underwriting_decision
    FOR EACH ROW EXECUTE FUNCTION forbid_decision_mutation();

CREATE TRIGGER underwriting_decision_no_truncate
    BEFORE TRUNCATE ON underwriting_decision
    FOR EACH STATEMENT EXECUTE FUNCTION forbid_decision_mutation();
