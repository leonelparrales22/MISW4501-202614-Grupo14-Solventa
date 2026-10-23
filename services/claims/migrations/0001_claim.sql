-- The filed claim (HU-M2.2-01). The insured files it from the guided
-- assistant of the mobile app, and it is the case the rest of the journey
-- works on: triage, assessment and payment.
CREATE TABLE IF NOT EXISTS claim (
    id              TEXT        PRIMARY KEY,
    filing_number   TEXT        NOT NULL UNIQUE,
    cognito_sub     TEXT        NOT NULL,
    filing_key      TEXT        NOT NULL,
    fingerprint     TEXT        NOT NULL,
    policy_id       TEXT        NOT NULL,
    incident_type   TEXT        NOT NULL,
    occurred_at     TIMESTAMPTZ NOT NULL,
    place           TEXT        NOT NULL,
    description     TEXT        NOT NULL,
    details         JSONB       NOT NULL DEFAULT '{}'::jsonb,
    status          TEXT        NOT NULL,
    filed_at        TIMESTAMPTZ NOT NULL
);

-- The filing is identified by its key for the insured who sent it: a retry
-- after a lost response finds the case instead of creating a second one.
CREATE UNIQUE INDEX IF NOT EXISTS claim_filing_key ON claim (cognito_sub, filing_key);

-- The listing of the insured's own claims reads the newest first.
CREATE INDEX IF NOT EXISTS claim_by_insured ON claim (cognito_sub, filed_at DESC);

-- The filing number the insured quotes to support. The sequence may leave
-- gaps, which is cheaper than serializing every filing to keep them
-- consecutive, and the number only has to be unique.
CREATE SEQUENCE IF NOT EXISTS claim_filing_number_seq AS BIGINT START 1;
