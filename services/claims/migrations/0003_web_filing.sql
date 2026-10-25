-- HU-W2.1-01: the filing of the claim with supports from the web portal.
--
-- Numbered 0003 so it does not collide with 0002_claim_evidence of
-- HU-M2.1-01, which is still in its own branch.

-- Where the claim was filed. The claims filed before this migration came
-- from the mobile app (HU-M2.2-01).
ALTER TABLE claim ADD COLUMN IF NOT EXISTS channel TEXT NOT NULL DEFAULT 'mobile';
ALTER TABLE claim ADD CONSTRAINT claim_channel_known CHECK (channel IN ('mobile', 'web'));

-- The copy of the policy taken when a web claim was filed (decision D7): its
-- line, product, insured object, validity and covered types, so the checks
-- of the filing can be rebuilt even if the policy core changes later. Null
-- for the claims filed from the mobile app.
ALTER TABLE claim ADD COLUMN IF NOT EXISTS policy_snapshot JSONB;

-- The catalog of claim types and supports the web claim was filed with.
ALTER TABLE claim ADD COLUMN IF NOT EXISTS catalog_version TEXT;

-- The list of the insured's claims, filtered by status, newest first.
CREATE INDEX IF NOT EXISTS claim_by_insured_status ON claim (cognito_sub, status, filed_at DESC, id DESC);

-- An uploaded support. It belongs to whoever uploaded it and waits there
-- until a filing attaches it.
--
-- Provisional (HU-W2.1-01 §9): the content lives here until the supports move
-- to the evidence store of HU-M2.1-01 (S3 with a signed upload URL); then a
-- new migration drops the content column.
CREATE TABLE IF NOT EXISTS support_file (
    support_id   TEXT        PRIMARY KEY,
    owner_sub    TEXT        NOT NULL,
    file_name    TEXT        NOT NULL,
    content_type TEXT        NOT NULL CHECK (content_type IN ('image/jpeg', 'image/png', 'application/pdf')),
    byte_size    BIGINT      NOT NULL CHECK (byte_size BETWEEN 1 AND 10485760),
    sha256       TEXT        NOT NULL,
    content      BYTEA       NOT NULL,
    uploaded_at  TIMESTAMPTZ NOT NULL
);

-- The supports of an insured, to count the ones not attached yet.
CREATE INDEX IF NOT EXISTS support_file_by_owner ON support_file (owner_sub, uploaded_at);

-- The supports attached to a web claim, in the order the claim shows them. A
-- support is attached to one claim only.
CREATE TABLE IF NOT EXISTS claim_support (
    claim_id         TEXT    NOT NULL REFERENCES claim (id),
    position         INTEGER NOT NULL CHECK (position >= 0),
    requirement_code TEXT    NOT NULL,
    support_id       TEXT    NOT NULL REFERENCES support_file (support_id),
    PRIMARY KEY (claim_id, position),
    CONSTRAINT claim_support_once UNIQUE (support_id)
);

-- The supports of a filed claim are not corrected: the correction of a
-- returned filing is HU-W2.1-04, and it will add rows, not change these.
CREATE OR REPLACE FUNCTION forbid_claim_support_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'claim_support is append-only: % is not allowed', TG_OP
        USING ERRCODE = 'restrict_violation';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER claim_support_no_update_delete
    BEFORE UPDATE OR DELETE ON claim_support
    FOR EACH ROW EXECUTE FUNCTION forbid_claim_support_mutation();

CREATE TRIGGER claim_support_no_truncate
    BEFORE TRUNCATE ON claim_support
    FOR EACH STATEMENT EXECUTE FUNCTION forbid_claim_support_mutation();
