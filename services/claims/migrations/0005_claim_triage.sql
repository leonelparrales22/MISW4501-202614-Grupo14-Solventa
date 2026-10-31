-- HU-W2.2-01: the automatic priority of the claim (triage).
--
-- Numbered 0005, after 0004_support_objects of HU-W2.1-01. It depends on no
-- other migration, so another 0005_* of a story developed at the same time is
-- applied by its own name, in lexical order, without touching this one.

-- What the insured estimates the loss at, in cents of COP, declared in the web
-- filing (decision D12). Null when it was not declared, and always in a claim
-- filed from the mobile app. The triage reads it; the evaluation of the claim
-- (HU-W2.2-03) reuses it.
ALTER TABLE claim ADD COLUMN IF NOT EXISTS estimated_amount_minor BIGINT;
ALTER TABLE claim ADD CONSTRAINT claim_estimated_amount_range
    CHECK (estimated_amount_minor BETWEEN 1 AND 10000000000000);

-- Every calculation of the level of a claim, the first one and each
-- recalculation, with what it was calculated from and why (criteria 1 to 3).
-- seq numbers the calculations of the claim from 1: two calculations that
-- race for the same claim cannot take the same place, and the one that loses
-- reads the winner instead of recording twice.
CREATE TABLE IF NOT EXISTS claim_triage (
    claim_id                 TEXT        NOT NULL REFERENCES claim (id),
    seq                      INTEGER     NOT NULL CHECK (seq >= 1),
    level                    SMALLINT    NOT NULL CHECK (level BETWEEN 1 AND 3),
    severity                 TEXT        NOT NULL CHECK (severity IN ('high', 'medium', 'low')),
    amount_band              TEXT        NOT NULL CHECK (amount_band IN ('high', 'medium', 'low', 'unknown')),
    estimated_amount_minor   BIGINT,
    incident_type            TEXT        NOT NULL,
    policy_line              TEXT        NOT NULL,
    coverage                 TEXT        NOT NULL,
    policy_version           TEXT        NOT NULL,
    input_fingerprint        TEXT        NOT NULL,
    -- The target of the level under the policy of the calculation, kept so a
    -- later version of the policy does not rewrite what was promised.
    target_kind              TEXT        NOT NULL CHECK (target_kind IN ('immediate', 'business_days')),
    target_business_days     SMALLINT    NOT NULL CHECK (target_business_days >= 0),
    resolution_business_days SMALLINT    NOT NULL CHECK (resolution_business_days >= 0),
    causes                   JSONB       NOT NULL
        CHECK (jsonb_typeof(causes) = 'array' AND jsonb_array_length(causes) > 0),
    previous_level           SMALLINT    CHECK (previous_level BETWEEN 1 AND 3),
    actor                    TEXT        NOT NULL,
    assessed_at              TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (claim_id, seq)
);

-- A calculation is evidence of why a claim was attended when it was: it is
-- never corrected nor deleted, a new calculation is added instead.
CREATE OR REPLACE FUNCTION forbid_claim_triage_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'claim_triage is append-only: % is not allowed', TG_OP
        USING ERRCODE = 'restrict_violation';
END;
$$ LANGUAGE plpgsql;

-- OR REPLACE (PostgreSQL 14+) keeps the migration applicable again over a
-- claim_triage that survived a rebuild of the claim table.
CREATE OR REPLACE TRIGGER claim_triage_no_update_delete
    BEFORE UPDATE OR DELETE ON claim_triage
    FOR EACH ROW EXECUTE FUNCTION forbid_claim_triage_mutation();

CREATE OR REPLACE TRIGGER claim_triage_no_truncate
    BEFORE TRUNCATE ON claim_triage
    FOR EACH STATEMENT EXECUTE FUNCTION forbid_claim_triage_mutation();

-- The level in force of each claim: its last calculation. Any query of this
-- service (the list of the claims in evaluation of HU-W2.2-03, the queue of
-- HU-W2.2-02) joins it without knowing the history.
CREATE OR REPLACE VIEW claim_current_triage AS
    SELECT DISTINCT ON (claim_id) *
    FROM claim_triage
    ORDER BY claim_id, seq DESC;

-- The sweep of the triage reads the claims of a few statuses, oldest first.
CREATE INDEX IF NOT EXISTS claim_by_status ON claim (status, filed_at, id);
