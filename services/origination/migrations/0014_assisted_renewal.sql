-- HU-W1.5-03: assisted renewal with a requote.
--
-- The advisor requotes a policy about to expire, records how the customer
-- accepted and formalizes the renewal. The renewal itself is the renewal row
-- of HU-W1.5-02 (one per policy and period, whatever the channel); these
-- tables keep what the online renewal does not need.

-- renewal_requote is the requote an advisor made of one period of a policy:
-- the renewed conditions, how they were priced (the profile used, with the
-- notice of a degraded one, decision D-7) and on what consent the external
-- data was consulted (decisions D-8 and D-13 c). It is immutable: the
-- formalization cites it by id, so the price the customer agreed to is the
-- price charged. Only the last one of the period can be formalized.
CREATE TABLE IF NOT EXISTS renewal_requote (
    requote_id           TEXT             PRIMARY KEY,
    policy_id            TEXT             NOT NULL REFERENCES policy (policy_id),
    period_end           TIMESTAMPTZ      NOT NULL,
    advisor_id           TEXT             NOT NULL,
    premium_minor        BIGINT           NOT NULL CHECK (premium_minor > 0),
    currency             TEXT             NOT NULL,
    insured_amount_minor BIGINT           NOT NULL,
    term_months          INTEGER          NOT NULL CHECK (term_months > 0),
    coverages            JSONB            NOT NULL,
    discount_rate        DOUBLE PRECISION NOT NULL DEFAULT 0,
    requoted             BOOLEAN          NOT NULL,
    rating_version       TEXT             NOT NULL DEFAULT '',
    applied_rules        JSONB            NOT NULL DEFAULT '[]',
    -- The profile that priced the requote; empty when none was needed
    -- (requoted = false).
    mode                 TEXT             NOT NULL DEFAULT '' CHECK (mode IN ('', 'normal', 'provisional')),
    profile_source       TEXT             NOT NULL DEFAULT '',
    profile_notice       TEXT             NOT NULL DEFAULT ''
        CHECK (profile_notice IN ('', 'last_profile_in_use', 'personalization_unavailable')),
    profile_score        DOUBLE PRECISION NOT NULL DEFAULT 0,
    profile_category     TEXT             NOT NULL DEFAULT '',
    profile_date         TIMESTAMPTZ,
    unavailable_source   TEXT             NOT NULL DEFAULT '',
    sources              JSONB            NOT NULL DEFAULT '[]',
    -- The customer the profile and the consent are keyed by, and the channel
    -- of the policy's original offer (decision D-6).
    profile_customer_id  TEXT             NOT NULL DEFAULT '',
    origin_channel       TEXT             NOT NULL DEFAULT '' CHECK (origin_channel IN ('', 'embedded', 'assisted')),
    -- NULL only when no external data was consulted (no customer was found
    -- for the policy).
    consent_basis        TEXT             CHECK (consent_basis IN ('audit_verified', 'advisor_affirmed')),
    consent_id           TEXT             NOT NULL DEFAULT '',
    consent_affirmed_by  TEXT,
    consent_affirmed_at  TIMESTAMPTZ,
    created_at           TIMESTAMPTZ      NOT NULL,
    expires_at           TIMESTAMPTZ      NOT NULL,
    recorded_at          TIMESTAMPTZ      NOT NULL DEFAULT now(),
    CHECK (expires_at > created_at),
    -- An affirmed consent says who affirmed it and when (decision D-13 c).
    CHECK (consent_basis IS DISTINCT FROM 'advisor_affirmed'
           OR (consent_affirmed_by IS NOT NULL AND consent_affirmed_by <> '' AND consent_affirmed_at IS NOT NULL))
);

CREATE INDEX IF NOT EXISTS renewal_requote_period_idx ON renewal_requote (policy_id, period_end, created_at DESC);

CREATE TRIGGER renewal_requote_no_update_delete BEFORE UPDATE OR DELETE ON renewal_requote
    FOR EACH ROW EXECUTE FUNCTION forbid_offer_mutation();
CREATE TRIGGER renewal_requote_no_truncate BEFORE TRUNCATE ON renewal_requote
    FOR EACH STATEMENT EXECUTE FUNCTION forbid_offer_mutation();

-- renewal_formalization is the record of a renewal formalized by an advisor
-- (CA-4): who (with the name of the token when it has one), when, from which
-- requote and how the customer accepted (CA-2). It is written in the same
-- transaction as its renewal row, so a renewal of the period exists with its
-- record or not at all. It is append-only.
CREATE TABLE IF NOT EXISTS renewal_formalization (
    renewal_id        TEXT        PRIMARY KEY REFERENCES renewal (renewal_id),
    requote_id        TEXT        NOT NULL UNIQUE REFERENCES renewal_requote (requote_id),
    advisor_id        TEXT        NOT NULL,
    advisor_email     TEXT        NOT NULL DEFAULT '',
    advisor_name      TEXT        NOT NULL DEFAULT '',
    consent_method    TEXT        NOT NULL CHECK (consent_method IN ('recorded_call', 'remote_signature', 'confirmation_code')),
    accepted_at       TIMESTAMPTZ NOT NULL,
    consent_reference TEXT        NOT NULL DEFAULT '',
    formalized_at     TIMESTAMPTZ NOT NULL,
    recorded_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (consent_method = 'confirmation_code' OR consent_reference <> '')
);

CREATE TRIGGER renewal_formalization_no_update_delete BEFORE UPDATE OR DELETE ON renewal_formalization
    FOR EACH ROW EXECUTE FUNCTION forbid_offer_mutation();
CREATE TRIGGER renewal_formalization_no_truncate BEFORE TRUNCATE ON renewal_formalization
    FOR EACH STATEMENT EXECUTE FUNCTION forbid_offer_mutation();

-- renewal_consent_challenge is the confirmation code the customer receives and
-- the advisor types to formalize a renewal (decision D-9 a). It has the
-- columns and rules of consent_challenge (0011), which belongs to the quotes:
-- only the hash of the code is kept, and a new code replaces the previous one
-- while the counters survive, so the codes and the attempts stay bounded.
CREATE TABLE IF NOT EXISTS renewal_consent_challenge (
    requote_id TEXT        PRIMARY KEY REFERENCES renewal_requote (requote_id),
    advisor_id TEXT        NOT NULL,
    code_hash  TEXT        NOT NULL,
    sends      INTEGER     NOT NULL CHECK (sends > 0),
    attempts   INTEGER     NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    created_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL
);
