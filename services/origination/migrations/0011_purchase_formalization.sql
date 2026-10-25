-- HU-W1.1-03: purchase formalization by the advisor on behalf of the customer.
--
-- assisted_quote persists the quotes of HU-W1.1-01, which lived in memory, so a
-- quote survives a restart until it is formalized. A quote is immutable.
CREATE TABLE IF NOT EXISTS assisted_quote (
    quote_id             TEXT             PRIMARY KEY,
    customer_id          TEXT             NOT NULL,
    advisor_id           TEXT             NOT NULL,
    applicant            JSONB            NOT NULL,
    birth_date           DATE             NOT NULL,
    insured_amount_minor BIGINT           NOT NULL CHECK (insured_amount_minor > 0),
    term_months          INTEGER          NOT NULL CHECK (term_months > 0),
    premium_minor        BIGINT           NOT NULL CHECK (premium_minor > 0),
    currency             TEXT             NOT NULL,
    coverages            JSONB            NOT NULL,
    rating_version       TEXT             NOT NULL,
    mode                 TEXT             NOT NULL CHECK (mode IN ('normal', 'provisional')),
    profile_source       TEXT             NOT NULL,
    profile_score        DOUBLE PRECISION NOT NULL,
    profile_category     TEXT             NOT NULL,
    profile_date         TIMESTAMPTZ,
    unavailable_source   TEXT,
    created_at           TIMESTAMPTZ      NOT NULL,
    expires_at           TIMESTAMPTZ      NOT NULL,
    recorded_at          TIMESTAMPTZ      NOT NULL DEFAULT now()
);

CREATE TRIGGER assisted_quote_no_update_delete BEFORE UPDATE OR DELETE ON assisted_quote
    FOR EACH ROW EXECUTE FUNCTION forbid_offer_mutation();
CREATE TRIGGER assisted_quote_no_truncate BEFORE TRUNCATE ON assisted_quote
    FOR EACH STATEMENT EXECUTE FUNCTION forbid_offer_mutation();

-- consent_challenge is the confirmation code the customer receives and the
-- advisor types to formalize (CA-5). Only the hash of the code is kept. A
-- quote has one challenge: asking for a new code replaces the previous one,
-- but the counters survive so the codes and the attempts stay bounded.
CREATE TABLE IF NOT EXISTS consent_challenge (
    quote_id   TEXT        PRIMARY KEY REFERENCES assisted_quote (quote_id),
    advisor_id TEXT        NOT NULL,
    code_hash  TEXT        NOT NULL,
    sends      INTEGER     NOT NULL CHECK (sends > 0),
    attempts   INTEGER     NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    created_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL
);

-- formalization is the record of a purchase formalized by an advisor: who, when
-- and from which quote (CA-4), with how and when the customer consented (CA-5). The
-- unique quote_id is what lets exactly one of two concurrent formalizations
-- complete (CA-6). The record is append-only.
CREATE TABLE IF NOT EXISTS formalization (
    formalization_id TEXT        PRIMARY KEY,
    quote_id         TEXT        NOT NULL UNIQUE REFERENCES assisted_quote (quote_id),
    offer_id         TEXT        NOT NULL UNIQUE,
    customer_id      TEXT        NOT NULL,
    advisor_id       TEXT        NOT NULL,
    advisor_email    TEXT        NOT NULL DEFAULT '',
    -- How and when the customer accepted (CA-5): a recorded call or a remote
    -- signature, identified by its reference, or the confirmation code the
    -- customer received.
    consent_method   TEXT        NOT NULL CHECK (consent_method IN ('recorded_call', 'remote_signature', 'confirmation_code')),
    accepted_at      TIMESTAMPTZ NOT NULL,
    consent_reference TEXT       NOT NULL DEFAULT '',
    formalized_at    TIMESTAMPTZ NOT NULL,
    recorded_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (consent_method = 'confirmation_code' OR consent_reference <> '')
);

CREATE TRIGGER formalization_no_update_delete BEFORE UPDATE OR DELETE ON formalization
    FOR EACH ROW EXECUTE FUNCTION forbid_offer_mutation();
CREATE TRIGGER formalization_no_truncate BEFORE TRUNCATE ON formalization
    FOR EACH STATEMENT EXECUTE FUNCTION forbid_offer_mutation();

-- The acceptance made by an advisor on behalf of the customer has its own
-- channel, so the monitor of HU-W1.3-04 tells it from the web and the mobile.
ALTER TABLE acceptance DROP CONSTRAINT IF EXISTS acceptance_channel_check;
ALTER TABLE acceptance ADD CONSTRAINT acceptance_channel_check CHECK (channel IN ('web', 'mobile', 'advisor'));
