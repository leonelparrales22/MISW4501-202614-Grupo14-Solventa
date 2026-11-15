-- HU-M2.3-01: the request for on-site assistance from the phone.
--
-- Numbered 0013, after 0012_claim_stage_notice of HU-M3.3-01. It depends on no
-- other table, so another 0013_* of a story developed at the same time is
-- applied by its own name, in lexical order, without touching this one. A
-- request is not a claim (decision D6): it has its own table and its own
-- numbering, and the inbox of operations (HU-W2.3-03) reads it here, in the
-- same database, without events (decision D8).

CREATE TABLE IF NOT EXISTS assistance_request (
    id                   TEXT             PRIMARY KEY,
    -- AST-<year>-<4 digits or more>, the number the insured sees.
    request_number       TEXT             NOT NULL UNIQUE,
    cognito_sub          TEXT             NOT NULL,
    request_key          TEXT             NOT NULL,
    fingerprint          TEXT             NOT NULL,
    -- The name of the token, for the inbox of operations; '' when it had none.
    requester_name       TEXT             NOT NULL DEFAULT '',
    policy_id            TEXT             NOT NULL,
    -- {policy_id, line, product, insured_object} as the policy core described
    -- the policy when the request was recorded (assumption S7).
    policy_snapshot      JSONB            NOT NULL CHECK (jsonb_typeof(policy_snapshot) = 'object'),
    location_status      TEXT             NOT NULL CHECK (location_status IN ('sent', 'unavailable')),
    latitude             DOUBLE PRECISION CHECK (latitude BETWEEN -90 AND 90),
    longitude            DOUBLE PRECISION CHECK (longitude BETWEEN -180 AND 180),
    accuracy_m           DOUBLE PRECISION CHECK (accuracy_m > 0 AND accuracy_m <= 10000),
    -- The instant of the fix on the phone.
    location_captured_at TIMESTAMPTZ,
    unavailable_reason   TEXT             CHECK (unavailable_reason IN ('permission_denied', 'location_disabled', 'no_fix')),
    -- The readable address the phone sent; the service does not geocode.
    place                TEXT             CHECK (char_length(place) BETWEEN 3 AND 160),
    -- new is the only status HU-M2.3-01 writes; the other three are the
    -- inbox of HU-W2.3-03 (assumption S8).
    status               TEXT             NOT NULL DEFAULT 'new'
                                          CHECK (status IN ('new', 'in_call', 'dispatched', 'closed')),
    -- The clock of the server when the request was recorded.
    requested_at         TIMESTAMPTZ      NOT NULL,
    -- Every request keeps the result of its location (criterion 5): a fix
    -- with its accuracy and instant, or the reason there was none.
    CONSTRAINT assistance_request_location_shape CHECK (
        (location_status = 'sent'
            AND latitude IS NOT NULL AND longitude IS NOT NULL AND accuracy_m IS NOT NULL
            AND location_captured_at IS NOT NULL AND unavailable_reason IS NULL)
        OR (location_status = 'unavailable'
            AND latitude IS NULL AND longitude IS NULL AND accuracy_m IS NULL
            AND location_captured_at IS NULL AND unavailable_reason IS NOT NULL))
);

-- The request is identified by its key for the insured who sent it: a retry
-- after a lost response finds the request instead of creating a second one
-- (assumption S6).
CREATE UNIQUE INDEX IF NOT EXISTS assistance_request_key ON assistance_request (cognito_sub, request_key);

-- The number of a request. The sequence may leave gaps; the number only has
-- to be unique.
CREATE SEQUENCE IF NOT EXISTS assistance_request_number_seq AS BIGINT START 1;

-- What was sent is evidence and is never rewritten (decision D7): a request is
-- not deleted, and an UPDATE may change only status, which the inbox of
-- HU-W2.3-03 moves. The columns a later story adds are not compared here.
CREATE OR REPLACE FUNCTION forbid_assistance_request_rewrite() RETURNS trigger AS $$
BEGIN
    IF TG_OP = 'UPDATE'
        AND NEW.id IS NOT DISTINCT FROM OLD.id
        AND NEW.request_number IS NOT DISTINCT FROM OLD.request_number
        AND NEW.cognito_sub IS NOT DISTINCT FROM OLD.cognito_sub
        AND NEW.request_key IS NOT DISTINCT FROM OLD.request_key
        AND NEW.fingerprint IS NOT DISTINCT FROM OLD.fingerprint
        AND NEW.requester_name IS NOT DISTINCT FROM OLD.requester_name
        AND NEW.policy_id IS NOT DISTINCT FROM OLD.policy_id
        AND NEW.policy_snapshot IS NOT DISTINCT FROM OLD.policy_snapshot
        AND NEW.location_status IS NOT DISTINCT FROM OLD.location_status
        AND NEW.latitude IS NOT DISTINCT FROM OLD.latitude
        AND NEW.longitude IS NOT DISTINCT FROM OLD.longitude
        AND NEW.accuracy_m IS NOT DISTINCT FROM OLD.accuracy_m
        AND NEW.location_captured_at IS NOT DISTINCT FROM OLD.location_captured_at
        AND NEW.unavailable_reason IS NOT DISTINCT FROM OLD.unavailable_reason
        AND NEW.place IS NOT DISTINCT FROM OLD.place
        AND NEW.requested_at IS NOT DISTINCT FROM OLD.requested_at THEN
        RETURN NEW;
    END IF;
    RAISE EXCEPTION '% keeps what was sent: % is not allowed', TG_TABLE_NAME, TG_OP
        USING ERRCODE = 'restrict_violation';
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE TRIGGER assistance_request_no_rewrite
    BEFORE UPDATE OR DELETE ON assistance_request
    FOR EACH ROW EXECUTE FUNCTION forbid_assistance_request_rewrite();
CREATE OR REPLACE TRIGGER assistance_request_no_truncate
    BEFORE TRUNCATE ON assistance_request
    FOR EACH STATEMENT EXECUTE FUNCTION forbid_assistance_request_rewrite();
