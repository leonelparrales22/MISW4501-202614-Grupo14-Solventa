-- HU-W1.4-02: an operations analyst reviews the change requests of HU-W1.4-01
-- (0017) and applies or rejects them. What the analyst does lives in tables of
-- its own that only receive rows; of change_request it only writes status,
-- status_changed_at and decision_reason (the three columns its trigger lets
-- change).
--
-- The current contractual state of a policy is the photo of its last applied
-- endorsement (decision D-3 a): policy stays immutable (0012), and the view
-- policy_current lays the last photo over it, so every reader of policies
-- (the wallet of the web and the phone, the renewals, the list of the advisor
-- and the alerts of expiry) sees the new terms in the same COMMIT that applies
-- the endorsement. There is no copy to keep in sync.

-- endorsement_counter gives the number of each endorsement of a civil year of
-- Colombia: END-<year>-<number>, taken in the transaction that applies it.
CREATE TABLE IF NOT EXISTS endorsement_counter (
    year        INTEGER PRIMARY KEY,
    last_number INTEGER NOT NULL CHECK (last_number > 0)
);

-- endorsement_attempt is every press of "Aplicar endoso" that reached a
-- decision, with the validations it ran (CA-2): applied, or not applied with
-- the first validation that failed. An unverifiable validation decides nothing
-- and records nothing.
CREATE TABLE IF NOT EXISTS endorsement_attempt (
    attempt_id          BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    request_id          TEXT        NOT NULL REFERENCES change_request (request_id),
    policy_id           TEXT        NOT NULL REFERENCES policy (policy_id),
    outcome             TEXT        NOT NULL CHECK (outcome IN ('applied', 'not_applied')),
    failed_code         TEXT,
    -- [{code, outcome, guard, params}] in the order of the screen.
    validations         JSONB       NOT NULL,
    attempted_by_sub    TEXT        NOT NULL,
    attempted_by_name   TEXT        NOT NULL DEFAULT '',
    attempted_by_email  TEXT        NOT NULL DEFAULT '',
    attempted_at        TIMESTAMPTZ NOT NULL,
    recorded_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((outcome = 'not_applied') = (failed_code IS NOT NULL))
);

CREATE INDEX IF NOT EXISTS endorsement_attempt_request_idx ON endorsement_attempt (request_id, attempt_id DESC);
CREATE INDEX IF NOT EXISTS endorsement_attempt_policy_idx ON endorsement_attempt (policy_id, attempted_at);
-- The indicator of the inbox: the validations that failed in the last days.
CREATE INDEX IF NOT EXISTS endorsement_attempt_not_applied_idx ON endorsement_attempt (attempted_at)
    WHERE outcome = 'not_applied';

-- endorsement is an applied request (CA-4): who applied it, when, and the
-- photo of the contractual terms that result. version numbers the
-- endorsements of a policy: UNIQUE (policy_id, version) keeps two endorsements
-- from starting from the same terms (the optimistic lock of decision D-3 a),
-- and its index serves the last one of a policy, read backwards.
CREATE TABLE IF NOT EXISTS endorsement (
    endorsement_id          TEXT        PRIMARY KEY CHECK (endorsement_id ~ '^END-[0-9]{4}-[0-9]{4,}$'),
    request_id              TEXT        NOT NULL UNIQUE REFERENCES change_request (request_id),
    attempt_id              BIGINT      NOT NULL UNIQUE REFERENCES endorsement_attempt (attempt_id),
    policy_id               TEXT        NOT NULL REFERENCES policy (policy_id),
    version                 INTEGER     NOT NULL CHECK (version > 0),
    change_type             TEXT        NOT NULL CHECK (change_type IN ('beneficiaries', 'insured_amount', 'personal_data')),
    -- The photo: the columns policy_current lays over policy, and the rest of
    -- the terms (beneficiaries, onerous beneficiary, contact data) in terms.
    insured_amount_minor    BIGINT      NOT NULL CHECK (insured_amount_minor > 0),
    premium_minor           BIGINT      NOT NULL CHECK (premium_minor > 0),
    previous_premium_minor  BIGINT      NOT NULL CHECK (previous_premium_minor > 0),
    currency                TEXT        NOT NULL,
    coverages               JSONB       NOT NULL,
    contact_email           TEXT,
    terms                   JSONB       NOT NULL,
    applied_by_sub          TEXT        NOT NULL,
    applied_by_name         TEXT        NOT NULL DEFAULT '',
    applied_by_email        TEXT        NOT NULL DEFAULT '',
    applied_at              TIMESTAMPTZ NOT NULL,
    recorded_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (policy_id, version)
);

-- The indicator of the endorsements applied today.
CREATE INDEX IF NOT EXISTS endorsement_applied_at_idx ON endorsement (applied_at);

-- change_request_action is the taking, the rejection or the return of a
-- request, with who and when; the rejection and the return carry the
-- observations the insured reads. Each happens at most once per request.
CREATE TABLE IF NOT EXISTS change_request_action (
    action_id     BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    request_id    TEXT        NOT NULL REFERENCES change_request (request_id),
    action        TEXT        NOT NULL CHECK (action IN ('review', 'rejection', 'return')),
    observations  TEXT,
    actor_sub     TEXT        NOT NULL,
    actor_name    TEXT        NOT NULL DEFAULT '',
    actor_email   TEXT        NOT NULL DEFAULT '',
    acted_at      TIMESTAMPTZ NOT NULL,
    recorded_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (request_id, action),
    CHECK ((action = 'review') = (observations IS NULL))
);

-- The three history tables only receive rows (CA-4).
CREATE TRIGGER endorsement_attempt_no_update_delete BEFORE UPDATE OR DELETE ON endorsement_attempt
    FOR EACH ROW EXECUTE FUNCTION forbid_offer_mutation();
CREATE TRIGGER endorsement_attempt_no_truncate BEFORE TRUNCATE ON endorsement_attempt
    FOR EACH STATEMENT EXECUTE FUNCTION forbid_offer_mutation();
CREATE TRIGGER endorsement_no_update_delete BEFORE UPDATE OR DELETE ON endorsement
    FOR EACH ROW EXECUTE FUNCTION forbid_offer_mutation();
CREATE TRIGGER endorsement_no_truncate BEFORE TRUNCATE ON endorsement
    FOR EACH STATEMENT EXECUTE FUNCTION forbid_offer_mutation();
CREATE TRIGGER change_request_action_no_update_delete BEFORE UPDATE OR DELETE ON change_request_action
    FOR EACH ROW EXECUTE FUNCTION forbid_offer_mutation();
CREATE TRIGGER change_request_action_no_truncate BEFORE TRUNCATE ON change_request_action
    FOR EACH STATEMENT EXECUTE FUNCTION forbid_offer_mutation();

-- endorsement_notice is the e-mail of each decision to the insured (decision
-- D-12 a): applied, rejected or returned. It is written in the transaction of
-- the decision and sent by a worker with retries; only its delivery changes,
-- and it is never deleted.
CREATE TABLE IF NOT EXISTS endorsement_notice (
    request_id       TEXT        PRIMARY KEY REFERENCES change_request (request_id),
    outcome          TEXT        NOT NULL CHECK (outcome IN ('applied', 'rejected', 'returned')),
    status           TEXT        NOT NULL CHECK (status IN ('pending', 'sent', 'failed')),
    attempts         INTEGER     NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at  TIMESTAMPTZ,
    sent_at          TIMESTAMPTZ,
    last_failure_at  TIMESTAMPTZ,
    last_failure     TEXT,
    created_at       TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS endorsement_notice_due_idx ON endorsement_notice (next_attempt_at, request_id)
    WHERE status = 'pending';

CREATE OR REPLACE FUNCTION guard_endorsement_notice_change() RETURNS trigger AS $$
BEGIN
    IF TG_OP = 'UPDATE' THEN
        IF NEW.request_id IS DISTINCT FROM OLD.request_id OR NEW.outcome IS DISTINCT FROM OLD.outcome
            OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
            RAISE EXCEPTION 'endorsement_notice %: only the delivery can change', OLD.request_id
                USING ERRCODE = 'restrict_violation';
        END IF;
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'endorsement_notice: % is not allowed', TG_OP
        USING ERRCODE = 'restrict_violation';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER endorsement_notice_guard BEFORE UPDATE OR DELETE ON endorsement_notice
    FOR EACH ROW EXECUTE FUNCTION guard_endorsement_notice_change();
CREATE TRIGGER endorsement_notice_no_truncate BEFORE TRUNCATE ON endorsement_notice
    FOR EACH STATEMENT EXECUTE FUNCTION guard_endorsement_notice_change();

-- policy_current has the columns of policy, with the insured value, the
-- premium, the coverages and the contact e-mail of the last endorsement of the
-- policy laid over them. Every SQL reader of policies reads this view.
CREATE OR REPLACE VIEW policy_current AS
SELECT p.policy_id, p.acceptance_id, p.customer_id, p.holder_full_name, p.holder_document_id,
       COALESCE(NULLIF(e.contact_email, ''), p.holder_email) AS holder_email,
       COALESCE(e.premium_minor, p.premium_minor) AS premium_minor,
       COALESCE(e.currency, p.currency) AS currency,
       COALESCE(e.insured_amount_minor, p.insured_amount_minor) AS insured_amount_minor,
       COALESCE(e.coverages, p.coverages) AS coverages,
       p.coverage_start, p.coverage_end, p.renewal_of, p.recorded_at
FROM policy p
LEFT JOIN LATERAL (
    SELECT x.contact_email, x.premium_minor, x.currency, x.insured_amount_minor, x.coverages
    FROM endorsement x
    WHERE x.policy_id = p.policy_id
    ORDER BY x.version DESC
    LIMIT 1
) e ON true;
