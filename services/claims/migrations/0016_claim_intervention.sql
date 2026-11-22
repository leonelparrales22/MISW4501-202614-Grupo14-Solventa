-- HU-W2.3-02: follow-up of the intervention of the provider and closing of
-- the case. 0016 because it goes after the assignment (0008) whose row each
-- step hangs from, and after the last migration of develop (0015, HU-W2.3-03).
--
-- The status of the claim does not change: the intervention has its own, as
-- the assignment does (decision D2), and closing the case never approves,
-- rejects nor pays it.

-- A progress the provider reported, as the analyst recorded it: one per step
-- of the intervention, in order. The visit carries the report of the
-- provider.
CREATE TABLE IF NOT EXISTS claim_intervention_step (
    claim_id          TEXT        NOT NULL REFERENCES claim_assignment (claim_id),
    step              TEXT        NOT NULL CHECK (step IN ('accepted', 'en_route', 'visit_done',
                                                           'repair_authorized', 'repair_executed')),
    position          SMALLINT    NOT NULL CHECK (position BETWEEN 1 AND 5),
    occurred_at       TIMESTAMPTZ NOT NULL,
    note              TEXT        NOT NULL DEFAULT '',
    cause             TEXT,
    damage            TEXT,
    repair_cost_minor BIGINT      CHECK (repair_cost_minor >= 0),
    repair_days       INTEGER     CHECK (repair_days BETWEEN 0 AND 365),
    photos            INTEGER     CHECK (photos BETWEEN 0 AND 50),
    recorded_by       TEXT        NOT NULL,
    recorder_name     TEXT        NOT NULL DEFAULT '',
    recorded_at       TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (claim_id, step),
    UNIQUE (claim_id, position),
    -- Only the visit carries the report, and it carries all of it.
    CONSTRAINT claim_intervention_step_report CHECK (
        (step = 'visit_done') = (cause IS NOT NULL AND damage IS NOT NULL AND repair_cost_minor IS NOT NULL
                                 AND repair_days IS NOT NULL AND photos IS NOT NULL)
        AND (step = 'visit_done' OR (cause IS NULL AND damage IS NULL AND repair_cost_minor IS NULL
                                     AND repair_days IS NULL AND photos IS NULL)))
);

-- The closing of the case: one per claim, with its mandatory result and the
-- analyst responsible (criteria 3 and 5).
CREATE TABLE IF NOT EXISTS claim_closure (
    claim_id    TEXT        PRIMARY KEY REFERENCES claim_assignment (claim_id),
    result      TEXT        NOT NULL CHECK (result IN ('repaired', 'partially_repaired', 'no_repair_needed',
                                                       'not_repairable')),
    closed_at   TIMESTAMPTZ NOT NULL,
    notes       TEXT        NOT NULL DEFAULT '',
    closed_by   TEXT        NOT NULL,
    closer_name TEXT        NOT NULL DEFAULT '',
    recorded_at TIMESTAMPTZ NOT NULL
);

-- The steps and the closing are the trace of the intervention: they are never
-- corrected nor deleted (forbid_assignment_history_mutation, 0008).
CREATE OR REPLACE TRIGGER claim_intervention_step_no_update_delete
    BEFORE UPDATE OR DELETE ON claim_intervention_step
    FOR EACH ROW EXECUTE FUNCTION forbid_assignment_history_mutation();
CREATE OR REPLACE TRIGGER claim_intervention_step_no_truncate
    BEFORE TRUNCATE ON claim_intervention_step
    FOR EACH STATEMENT EXECUTE FUNCTION forbid_assignment_history_mutation();

CREATE OR REPLACE TRIGGER claim_closure_no_update_delete
    BEFORE UPDATE OR DELETE ON claim_closure
    FOR EACH ROW EXECUTE FUNCTION forbid_assignment_history_mutation();
CREATE OR REPLACE TRIGGER claim_closure_no_truncate
    BEFORE TRUNCATE ON claim_closure
    FOR EACH STATEMENT EXECUTE FUNCTION forbid_assignment_history_mutation();
