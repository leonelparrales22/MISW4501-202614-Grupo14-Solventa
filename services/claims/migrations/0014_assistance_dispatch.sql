-- HU-W2.3-03: the inbox of operations that attends the requests for
-- assistance of HU-M2.3-01.
--
-- Numbered 0014, after 0013_assistance_request. It depends on
-- assistance_request and provider only, so another 0014_* of a story
-- developed at the same time is applied by its own name, in lexical order,
-- without touching this one. The request itself only changes its status,
-- which its trigger already allows (decision D7 of HU-M2.3-01); everything
-- the inbox decides lives in these tables.
--
-- No table declares a foreign key to assistance_request: one would make
-- PostgreSQL refuse TRUNCATE assistance_request before its trigger explains
-- why. The request is never deleted (its trigger), and every write of the
-- inbox locks it first (SELECT ... FOR UPDATE), so no decision names a
-- request that does not exist.

-- Every decision on a request: who, when and what (criterion 5, decision
-- D7). seq numbers them from 1; a write names the number it expects, so two
-- analysts deciding at once cannot both write.
CREATE TABLE IF NOT EXISTS assistance_action (
    request_id  TEXT        NOT NULL,
    seq         INTEGER     NOT NULL CHECK (seq >= 1),
    action      TEXT        NOT NULL CHECK (action IN ('attended', 'taken_over', 'need_recorded',
                                                       'voice_location_recorded', 'dispatched', 'closed')),
    actor_email TEXT        NOT NULL,
    actor_name  TEXT        NOT NULL DEFAULT '',
    at          TIMESTAMPTZ NOT NULL,
    -- The detail of the action; NULL when it does not apply.
    need        TEXT,
    provider_id TEXT,
    reason      TEXT,
    -- The e-mail of the analyst a request was taken over from.
    taken_from  TEXT,
    PRIMARY KEY (request_id, seq)
);

-- Every need the analyst recorded on the call (decision D1); the last one is
-- in force.
CREATE TABLE IF NOT EXISTS assistance_need (
    request_id     TEXT        NOT NULL,
    seq            INTEGER     NOT NULL CHECK (seq >= 1),
    need           TEXT        NOT NULL,
    specialty      TEXT        NOT NULL,
    policy_version TEXT        NOT NULL,
    recorded_by    TEXT        NOT NULL,
    recorder_name  TEXT        NOT NULL DEFAULT '',
    recorded_at    TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (request_id, seq)
);

-- Every address the insured gave by voice, with the pin of the map
-- (decision D3); the last one is in force.
CREATE TABLE IF NOT EXISTS assistance_voice_location (
    request_id    TEXT             NOT NULL,
    seq           INTEGER          NOT NULL CHECK (seq >= 1),
    address       TEXT             NOT NULL CHECK (char_length(address) BETWEEN 5 AND 200),
    latitude      DOUBLE PRECISION NOT NULL CHECK (latitude BETWEEN -90 AND 90),
    longitude     DOUBLE PRECISION NOT NULL CHECK (longitude BETWEEN -180 AND 180),
    recorded_by   TEXT             NOT NULL,
    recorder_name TEXT             NOT NULL DEFAULT '',
    recorded_at   TIMESTAMPTZ      NOT NULL,
    PRIMARY KEY (request_id, seq)
);

-- The provider sent to a request (criterion 3): one per request, with a copy
-- of the provider and of the location as they were shared, and what the
-- analyst saw (the suggested one, the candidates and the attempts). The
-- provider does not become busy (decision D2).
CREATE TABLE IF NOT EXISTS assistance_dispatch (
    request_id                TEXT             PRIMARY KEY,
    provider_id               TEXT             NOT NULL REFERENCES provider (id),
    provider_name             TEXT             NOT NULL,
    contact_email             TEXT             NOT NULL,
    contact_phone             TEXT,
    need                      TEXT             NOT NULL,
    specialty                 TEXT             NOT NULL,
    distance_km               DOUBLE PRECISION NOT NULL CHECK (distance_km >= 0),
    eta_minutes               INTEGER          NOT NULL CHECK (eta_minutes >= 0),
    location_source           TEXT             NOT NULL CHECK (location_source IN ('app', 'voice')),
    latitude                  DOUBLE PRECISION NOT NULL CHECK (latitude BETWEEN -90 AND 90),
    longitude                 DOUBLE PRECISION NOT NULL CHECK (longitude BETWEEN -180 AND 180),
    accuracy_m                DOUBLE PRECISION,
    address                   TEXT,
    place                     TEXT,
    suggested_provider_id     TEXT             NOT NULL,
    candidates                JSONB            NOT NULL CHECK (jsonb_typeof(candidates) = 'array'),
    attempts                  JSONB            NOT NULL CHECK (jsonb_typeof(attempts) = 'array'),
    assistance_policy_version TEXT             NOT NULL,
    assignment_policy_version TEXT             NOT NULL,
    dispatched_by             TEXT             NOT NULL,
    dispatcher_name           TEXT             NOT NULL DEFAULT '',
    dispatched_at             TIMESTAMPTZ      NOT NULL,
    -- The phone shares its accuracy; the voice, its address.
    CONSTRAINT assistance_dispatch_location_shape CHECK (
        (location_source = 'app' AND accuracy_m IS NOT NULL AND address IS NULL)
        OR (location_source = 'voice' AND accuracy_m IS NULL AND address IS NOT NULL))
);

-- The indicators of the inbox read the dispatches of today, and the
-- candidates how many each provider took today.
CREATE INDEX IF NOT EXISTS assistance_dispatch_by_time ON assistance_dispatch (dispatched_at);
CREATE INDEX IF NOT EXISTS assistance_dispatch_by_provider ON assistance_dispatch (provider_id, dispatched_at);

-- A request closed without a dispatch (criterion 4): one per request, with a
-- required reason, and a note when the reason is other.
CREATE TABLE IF NOT EXISTS assistance_closure (
    request_id  TEXT        PRIMARY KEY,
    reason      TEXT        NOT NULL CHECK (reason IN ('resolved_by_phone', 'duplicate', 'cancelled_by_insured',
                                                       'not_covered', 'no_provider_available', 'other')),
    note        TEXT        CHECK (char_length(note) BETWEEN 3 AND 500),
    closed_by   TEXT        NOT NULL,
    closer_name TEXT        NOT NULL DEFAULT '',
    closed_at   TIMESTAMPTZ NOT NULL,
    CONSTRAINT assistance_closure_other_has_note CHECK (reason <> 'other' OR note IS NOT NULL)
);

-- The e-mail to the dispatched provider (decision D4), written with the
-- dispatch and sent by the worker with its retries. It is the only table of
-- the inbox that changes.
CREATE TABLE IF NOT EXISTS assistance_notice (
    request_id      TEXT        PRIMARY KEY REFERENCES assistance_dispatch (request_id),
    to_email        TEXT        NOT NULL,
    subject         TEXT        NOT NULL,
    body            TEXT        NOT NULL,
    status          TEXT        NOT NULL CHECK (status IN ('pending', 'sent')),
    attempts        INTEGER     NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at TIMESTAMPTZ,
    sent_at         TIMESTAMPTZ,
    last_error      TEXT,
    CONSTRAINT assistance_notice_shape CHECK ((status = 'sent') = (sent_at IS NOT NULL))
);

CREATE INDEX IF NOT EXISTS assistance_notice_due ON assistance_notice (status, next_attempt_at);

-- The inbox lists the latest requests first, of every status or of some.
CREATE INDEX IF NOT EXISTS assistance_request_latest ON assistance_request (requested_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS assistance_request_by_status ON assistance_request (status, requested_at DESC, id DESC);

-- What the inbox decided is evidence (criterion 5) and is never rewritten.
-- A function of its own, so this story does not depend on the one of
-- another.
CREATE OR REPLACE FUNCTION forbid_assistance_history_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION '% is append-only: % is not allowed', TG_TABLE_NAME, TG_OP
        USING ERRCODE = 'restrict_violation';
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE TRIGGER assistance_action_no_update_delete
    BEFORE UPDATE OR DELETE ON assistance_action
    FOR EACH ROW EXECUTE FUNCTION forbid_assistance_history_mutation();
CREATE OR REPLACE TRIGGER assistance_action_no_truncate
    BEFORE TRUNCATE ON assistance_action
    FOR EACH STATEMENT EXECUTE FUNCTION forbid_assistance_history_mutation();

CREATE OR REPLACE TRIGGER assistance_need_no_update_delete
    BEFORE UPDATE OR DELETE ON assistance_need
    FOR EACH ROW EXECUTE FUNCTION forbid_assistance_history_mutation();
CREATE OR REPLACE TRIGGER assistance_need_no_truncate
    BEFORE TRUNCATE ON assistance_need
    FOR EACH STATEMENT EXECUTE FUNCTION forbid_assistance_history_mutation();

CREATE OR REPLACE TRIGGER assistance_voice_location_no_update_delete
    BEFORE UPDATE OR DELETE ON assistance_voice_location
    FOR EACH ROW EXECUTE FUNCTION forbid_assistance_history_mutation();
CREATE OR REPLACE TRIGGER assistance_voice_location_no_truncate
    BEFORE TRUNCATE ON assistance_voice_location
    FOR EACH STATEMENT EXECUTE FUNCTION forbid_assistance_history_mutation();

CREATE OR REPLACE TRIGGER assistance_dispatch_no_update_delete
    BEFORE UPDATE OR DELETE ON assistance_dispatch
    FOR EACH ROW EXECUTE FUNCTION forbid_assistance_history_mutation();
CREATE OR REPLACE TRIGGER assistance_dispatch_no_truncate
    BEFORE TRUNCATE ON assistance_dispatch
    FOR EACH STATEMENT EXECUTE FUNCTION forbid_assistance_history_mutation();

CREATE OR REPLACE TRIGGER assistance_closure_no_update_delete
    BEFORE UPDATE OR DELETE ON assistance_closure
    FOR EACH ROW EXECUTE FUNCTION forbid_assistance_history_mutation();
CREATE OR REPLACE TRIGGER assistance_closure_no_truncate
    BEFORE TRUNCATE ON assistance_closure
    FOR EACH STATEMENT EXECUTE FUNCTION forbid_assistance_history_mutation();
