-- HU-W1.4-01: change requests the insured files on their policies in force
-- (beneficiaries, insured value or contact data). HU-W1.4-02 reviews and
-- applies them: it only writes status, status_changed_at and decision_reason,
-- and keeps what the analyst does in tables of its own.

-- change_request_counter gives the number of each request of a civil year of
-- Colombia: SOL-MOD-<year>-<number>. The number is taken in the same
-- transaction as the request, so a request that is not stored (another open
-- one of the same type won) gives its number back and there are no gaps.
CREATE TABLE IF NOT EXISTS change_request_counter (
    year        INTEGER PRIMARY KEY,
    last_number INTEGER NOT NULL CHECK (last_number > 0)
);

-- change_request is one request. previous is the photo of the current value
-- when it was filed (with the onerous beneficiary for a change of
-- beneficiaries) and requested what the insured asks for; both are written
-- once.
CREATE TABLE IF NOT EXISTS change_request (
    request_id         TEXT        PRIMARY KEY CHECK (request_id ~ '^SOL-MOD-[0-9]{4}-[0-9]{4,}$'),
    policy_id          TEXT        NOT NULL REFERENCES policy (policy_id),
    -- The owner of the policy (the subject of their token) when it was filed.
    customer_id        TEXT        NOT NULL,
    change_type        TEXT        NOT NULL CHECK (change_type IN ('beneficiaries', 'insured_amount', 'personal_data')),
    previous           JSONB       NOT NULL,
    requested          JSONB       NOT NULL,
    channel            TEXT        NOT NULL CHECK (channel IN ('web', 'mobile')),
    -- Who filed it: the subject, e-mail and name of the token (the name is
    -- empty when the account has none).
    requested_by_sub   TEXT        NOT NULL,
    requested_by_email TEXT        NOT NULL DEFAULT '',
    requested_by_name  TEXT        NOT NULL DEFAULT '',
    filed_at           TIMESTAMPTZ NOT NULL,
    status             TEXT        NOT NULL CHECK (status IN ('filed', 'in_review', 'applied', 'rejected', 'not_applied')),
    status_changed_at  TIMESTAMPTZ NOT NULL,
    -- What the analyst tells the insured when rejecting or returning the
    -- request (HU-W1.4-02); NULL otherwise.
    decision_reason    TEXT,
    recorded_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One open request per policy and type (decision D-6 a, adjustment A-2): filed,
-- in review, or not applied and not returned to the insured. Between two
-- requests filed at once, this index decides; the loser is told which one is
-- open.
CREATE UNIQUE INDEX IF NOT EXISTS change_request_one_open ON change_request (policy_id, change_type)
    WHERE status IN ('filed', 'in_review') OR (status = 'not_applied' AND decision_reason IS NULL);
-- The list of the insured (screen A11), the latest filed first.
CREATE INDEX IF NOT EXISTS change_request_customer_idx ON change_request (customer_id, filed_at DESC);
-- The inbox of the operations analyst (HU-W1.4-02).
CREATE INDEX IF NOT EXISTS change_request_status_idx ON change_request (status, filed_at);

-- The content of a request never changes and a request is never deleted: an
-- update may only change status, status_changed_at and decision_reason.
CREATE OR REPLACE FUNCTION guard_change_request_change() RETURNS trigger AS $$
BEGIN
    IF TG_OP = 'UPDATE' THEN
        IF NEW.request_id IS DISTINCT FROM OLD.request_id
            OR NEW.policy_id IS DISTINCT FROM OLD.policy_id
            OR NEW.customer_id IS DISTINCT FROM OLD.customer_id
            OR NEW.change_type IS DISTINCT FROM OLD.change_type
            OR NEW.previous IS DISTINCT FROM OLD.previous
            OR NEW.requested IS DISTINCT FROM OLD.requested
            OR NEW.channel IS DISTINCT FROM OLD.channel
            OR NEW.requested_by_sub IS DISTINCT FROM OLD.requested_by_sub
            OR NEW.requested_by_email IS DISTINCT FROM OLD.requested_by_email
            OR NEW.requested_by_name IS DISTINCT FROM OLD.requested_by_name
            OR NEW.filed_at IS DISTINCT FROM OLD.filed_at
            OR NEW.recorded_at IS DISTINCT FROM OLD.recorded_at THEN
            RAISE EXCEPTION 'change_request %: only the status and the decision reason can change', OLD.request_id
                USING ERRCODE = 'restrict_violation';
        END IF;
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'change_request: % is not allowed', TG_OP
        USING ERRCODE = 'restrict_violation';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER change_request_guard BEFORE UPDATE OR DELETE ON change_request
    FOR EACH ROW EXECUTE FUNCTION guard_change_request_change();
CREATE TRIGGER change_request_no_truncate BEFORE TRUNCATE ON change_request
    FOR EACH STATEMENT EXECUTE FUNCTION guard_change_request_change();
