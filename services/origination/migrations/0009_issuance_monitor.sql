-- HU-W1.3-04: monitoring of the issuances and their exceptions.
--
-- issuance_action keeps every action an operations analyst takes on an
-- issuance, with its author and date (CA-3). charge_attempt only keeps the
-- last resumption, so the history lives here. The row is written by the same
-- statement that resumes the charge, so there is never an action without its
-- resumption or a resumption without its action.
--
-- The table is append-only, enforced by the database as the audit tables are:
-- an action that happened is not edited or erased.
CREATE TABLE IF NOT EXISTS issuance_action (
    id            BIGSERIAL   PRIMARY KEY,
    acceptance_id TEXT        NOT NULL,
    action        TEXT        NOT NULL CHECK (action IN ('resume', 'retry')),
    actor         TEXT        NOT NULL,
    note          TEXT        NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS issuance_action_acceptance_idx
    ON issuance_action (acceptance_id, created_at DESC, id DESC);

CREATE OR REPLACE FUNCTION forbid_issuance_action_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'issuance_action is append-only: % is not allowed', TG_OP
        USING ERRCODE = 'restrict_violation';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER issuance_action_no_update_delete
    BEFORE UPDATE OR DELETE ON issuance_action
    FOR EACH ROW EXECUTE FUNCTION forbid_issuance_action_mutation();

CREATE TRIGGER issuance_action_no_truncate
    BEFORE TRUNCATE ON issuance_action
    FOR EACH STATEMENT EXECUTE FUNCTION forbid_issuance_action_mutation();

-- The monitor filters by date of acceptance and lists the latest first.
CREATE INDEX IF NOT EXISTS charge_attempt_accepted_idx ON charge_attempt (accepted_at DESC);
