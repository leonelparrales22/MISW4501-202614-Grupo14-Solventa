-- HU-W2.1-03: the review of the advisor, and the history of the status of
-- every claim.
--
-- Numbered 0007, after 0006_claim_decision of HU-W2.2-03. It depends on the
-- claim table only.

-- Every review of a filing: the advisor approves it, and it enters
-- evaluation, or returns it to the insured with the reason, the message the
-- insured reads and the verdict on each support. A returned filing that the
-- insured sends again (HU-W2.1-04) is reviewed again, so a claim can have
-- several; seq orders them. It is insert-only, like the rest of the case.
CREATE TABLE IF NOT EXISTS claim_review (
    claim_id          TEXT        NOT NULL REFERENCES claim (id),
    seq               INTEGER     NOT NULL CHECK (seq >= 1),
    outcome           TEXT        NOT NULL CHECK (outcome IN ('approved', 'returned')),
    reviewed_by       TEXT        NOT NULL,
    reviewer_name     TEXT        NOT NULL DEFAULT '',
    reviewed_at       TIMESTAMPTZ NOT NULL,
    -- The verdict on each support (screen S8): [{requirement_code, label,
    -- verdict, note}], valid, illegible or missing.
    supports          JSONB       NOT NULL DEFAULT '[]'::jsonb,
    reason_code       TEXT        CHECK (reason_code IN ('illegible_or_incomplete_supports', 'inconsistent_facts',
                                                         'missing_information', 'other')),
    message           TEXT        CHECK (char_length(message) BETWEEN 1 AND 1000),
    facts_to_correct  JSONB       NOT NULL DEFAULT '[]'::jsonb,
    PRIMARY KEY (claim_id, seq),
    CONSTRAINT claim_review_shape CHECK (
        (outcome = 'approved' AND reason_code IS NULL AND message IS NULL
            AND jsonb_array_length(facts_to_correct) = 0)
        OR (outcome = 'returned' AND reason_code IS NOT NULL AND message IS NOT NULL))
);

-- A review and a change of status are evidence of how the case went: they are
-- never corrected nor deleted.
CREATE OR REPLACE FUNCTION forbid_claim_history_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION '% is append-only: % is not allowed', TG_TABLE_NAME, TG_OP
        USING ERRCODE = 'restrict_violation';
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE TRIGGER claim_review_no_update_delete
    BEFORE UPDATE OR DELETE ON claim_review
    FOR EACH ROW EXECUTE FUNCTION forbid_claim_history_mutation();

CREATE OR REPLACE TRIGGER claim_review_no_truncate
    BEFORE TRUNCATE ON claim_review
    FOR EACH STATEMENT EXECUTE FUNCTION forbid_claim_history_mutation();

-- Every change of status of a claim, as it happens. The database records it,
-- not the code that changes the status, so no stage is missed whoever moves
-- the claim: the filing, the review, the decision, the payout or what the
-- next stories add. It is the source of the timeline of the case (HU-W2.1-02,
-- HU-M2.4-01) and of the notice of a change of stage (HU-M3.3-01): id grows
-- with every change of every claim, so a reader can follow them all.
CREATE TABLE IF NOT EXISTS claim_status_change (
    id               BIGSERIAL   PRIMARY KEY,
    claim_id         TEXT        NOT NULL REFERENCES claim (id),
    status           TEXT        NOT NULL,
    previous_status  TEXT,
    changed_at       TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS claim_status_change_by_claim ON claim_status_change (claim_id, id);

-- The filing takes the time it was filed at; a later change, the time of the
-- transaction that made it.
CREATE OR REPLACE FUNCTION record_claim_status_change() RETURNS trigger AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        INSERT INTO claim_status_change (claim_id, status, previous_status, changed_at)
        VALUES (NEW.id, NEW.status, NULL, NEW.filed_at);
    ELSIF NEW.status IS DISTINCT FROM OLD.status THEN
        INSERT INTO claim_status_change (claim_id, status, previous_status, changed_at)
        VALUES (NEW.id, NEW.status, OLD.status, now());
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE TRIGGER claim_status_change_on_insert AFTER INSERT ON claim
    FOR EACH ROW EXECUTE FUNCTION record_claim_status_change();
CREATE OR REPLACE TRIGGER claim_status_change_on_update AFTER UPDATE OF status ON claim
    FOR EACH ROW EXECUTE FUNCTION record_claim_status_change();

CREATE OR REPLACE TRIGGER claim_status_change_no_update_delete
    BEFORE UPDATE OR DELETE ON claim_status_change
    FOR EACH ROW EXECUTE FUNCTION forbid_claim_history_mutation();

CREATE OR REPLACE TRIGGER claim_status_change_no_truncate
    BEFORE TRUNCATE ON claim_status_change
    FOR EACH STATEMENT EXECUTE FUNCTION forbid_claim_history_mutation();

-- The claims filed before this migration get the history that can be rebuilt
-- from what was recorded: the filing, and the decision when there is one. A
-- claim decided before keeps its current status at the time of the decision
-- (paid or payment_exception included): the intermediate steps of its payout
-- were not recorded.
INSERT INTO claim_status_change (claim_id, status, previous_status, changed_at)
SELECT c.id, 'active', NULL, c.filed_at
FROM claim c
WHERE NOT EXISTS (SELECT 1 FROM claim_status_change h WHERE h.claim_id = c.id)
ORDER BY c.filed_at, c.id;

INSERT INTO claim_status_change (claim_id, status, previous_status, changed_at)
SELECT c.id, c.status, 'active', COALESCE(d.decided_at, c.filed_at)
FROM claim c LEFT JOIN claim_decision d ON d.claim_id = c.id
WHERE c.status <> 'active'
  AND NOT EXISTS (SELECT 1 FROM claim_status_change h WHERE h.claim_id = c.id AND h.status = c.status)
ORDER BY COALESCE(d.decided_at, c.filed_at), c.id;
