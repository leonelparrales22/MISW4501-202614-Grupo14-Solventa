-- HU-W1.2-03: personalized offer of the embedded journey. One immutable row per
-- offer, with the copy of the risk profile it was priced with, the rules of the
-- rating engine and its version (CA-2). Nothing about an offer is ever updated:
-- the owner (offer_owner), the replacement (another offer whose supersedes
-- points here) and the renewal (renews) are separate rows, so the record that
-- explains a price cannot be rewritten.
CREATE TABLE IF NOT EXISTS offer (
    offer_id            TEXT        PRIMARY KEY,
    -- Id the partner uses for the customer; the consent and the profile are keyed by it.
    customer_id         TEXT        NOT NULL,
    channel             TEXT        NOT NULL CHECK (channel IN ('embedded', 'assisted')),
    quote_id            TEXT,
    consent_id          TEXT        NOT NULL,
    partner_id          TEXT        NOT NULL,
    application_id      TEXT        NOT NULL,

    applicant           JSONB       NOT NULL,
    birth_date          DATE        NOT NULL,
    age_at_quote        INTEGER     NOT NULL,

    terms               JSONB       NOT NULL,
    premium_minor       BIGINT      NOT NULL CHECK (premium_minor > 0),
    currency            TEXT        NOT NULL,
    conditions          JSONB       NOT NULL,

    mode                TEXT        NOT NULL CHECK (mode IN ('normal', 'provisional')),
    profile_source      TEXT        NOT NULL,
    provisional_reason  TEXT        CHECK (provisional_reason IN ('cached_profile', 'base_actuarial_profile')),
    profile_date        TIMESTAMPTZ,
    unavailable_source  TEXT,
    profile             JSONB       NOT NULL,

    rules               JSONB       NOT NULL,
    rater_version       TEXT        NOT NULL,

    created_at          TIMESTAMPTZ NOT NULL,
    expires_at          TIMESTAMPTZ NOT NULL,
    supersedes          TEXT        REFERENCES offer (offer_id),
    renews              TEXT        REFERENCES offer (offer_id),
    recorded_at         TIMESTAMPTZ NOT NULL DEFAULT now(),

    CHECK (supersedes IS NULL OR renews IS NULL),
    CHECK ((mode = 'provisional') = (provisional_reason IS NOT NULL))
);

-- An offer has at most one replacement and one renewal: the unique indexes are
-- what makes concurrent requotes and renewals converge on a single new offer.
CREATE UNIQUE INDEX IF NOT EXISTS offer_supersedes_uidx ON offer (supersedes) WHERE supersedes IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS offer_renews_uidx ON offer (renews) WHERE renews IS NOT NULL;

-- The requote worker looks up the provisional offers that are still valid.
CREATE INDEX IF NOT EXISTS offer_requotable_idx ON offer (expires_at) WHERE mode = 'provisional';

-- The owner is the Cognito subject that first opened the offer with a session
-- (decision D4). Claiming is INSERT ... ON CONFLICT DO NOTHING: the first writer wins.
CREATE TABLE IF NOT EXISTS offer_owner (
    offer_id    TEXT        PRIMARY KEY REFERENCES offer (offer_id),
    owner_id    TEXT        NOT NULL,
    claimed_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE OR REPLACE FUNCTION forbid_offer_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION '% is append-only: % is not allowed', TG_TABLE_NAME, TG_OP
        USING ERRCODE = 'restrict_violation';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER offer_no_update_delete BEFORE UPDATE OR DELETE ON offer
    FOR EACH ROW EXECUTE FUNCTION forbid_offer_mutation();
CREATE TRIGGER offer_no_truncate BEFORE TRUNCATE ON offer
    FOR EACH STATEMENT EXECUTE FUNCTION forbid_offer_mutation();
CREATE TRIGGER offer_owner_no_update_delete BEFORE UPDATE OR DELETE ON offer_owner
    FOR EACH ROW EXECUTE FUNCTION forbid_offer_mutation();
