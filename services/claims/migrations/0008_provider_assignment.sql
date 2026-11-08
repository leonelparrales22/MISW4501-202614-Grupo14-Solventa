-- HU-W2.3-01: the assignment of the nearest provider to a claim.
--
-- Numbered 0008, after 0007_claim_review of HU-W2.1-03. It depends on the
-- claim table only, so another 0008_* of a story developed at the same time is
-- applied by its own name, in lexical order, without touching this one. The
-- status of the claim does not change with the assignment (decision D2): it
-- lives in these tables.

-- The registry of providers (criterion 2): who they are, what they do, where
-- they are, whether they can take a claim and how they are reached. Operations
-- keeps it; the assignment only makes a provider busy with a claim
-- (decision D13).
CREATE TABLE IF NOT EXISTS provider (
    id                TEXT             PRIMARY KEY CHECK (id ~ '^prv-[a-z0-9-]{3,40}$'),
    name              TEXT             NOT NULL CHECK (char_length(name) BETWEEN 3 AND 120),
    specialties       TEXT[]           NOT NULL CHECK (cardinality(specialties) BETWEEN 1 AND 6),
    latitude          DOUBLE PRECISION NOT NULL CHECK (latitude BETWEEN -90 AND 90),
    longitude         DOUBLE PRECISION NOT NULL CHECK (longitude BETWEEN -180 AND 180),
    address           TEXT             NOT NULL CHECK (char_length(address) BETWEEN 5 AND 200),
    contact_email     TEXT             NOT NULL,
    contact_phone     TEXT,
    availability      TEXT             NOT NULL CHECK (availability IN ('available', 'unavailable', 'busy')),
    -- The day an unavailable provider expects to be back; informative.
    unavailable_until DATE,
    busy_claim_id     TEXT             REFERENCES claim (id),
    updated_by        TEXT             NOT NULL,
    updated_at        TIMESTAMPTZ      NOT NULL,
    CONSTRAINT provider_busy_with_a_claim CHECK ((availability = 'busy') = (busy_claim_id IS NOT NULL)),
    CONSTRAINT provider_until_when_unavailable CHECK (unavailable_until IS NULL OR availability = 'unavailable')
);

-- The search reads the providers of one specialty.
CREATE INDEX IF NOT EXISTS provider_by_specialty ON provider USING GIN (specialties);

-- Every address and pin the analyst recorded for a claim with no coordinates
-- from the app (criterion 1, decision D1). The last one is the one in force.
CREATE TABLE IF NOT EXISTS claim_incident_location (
    claim_id      TEXT             NOT NULL REFERENCES claim (id),
    seq           INTEGER          NOT NULL CHECK (seq >= 1),
    address       TEXT             NOT NULL CHECK (char_length(address) BETWEEN 5 AND 200),
    latitude      DOUBLE PRECISION NOT NULL CHECK (latitude BETWEEN -90 AND 90),
    longitude     DOUBLE PRECISION NOT NULL CHECK (longitude BETWEEN -180 AND 180),
    recorded_by   TEXT             NOT NULL,
    recorder_name TEXT             NOT NULL DEFAULT '',
    recorded_at   TIMESTAMPTZ      NOT NULL,
    PRIMARY KEY (claim_id, seq)
);

-- Every search for the provider of a claim, by an analyst or by the automatic
-- retry, with the location it measured from and the providers it found as
-- the analyst saw them (criteria 2, 3 and 6). seq numbers the searches of the
-- claim from 1: two searches that race cannot take the same place.
CREATE TABLE IF NOT EXISTS claim_provider_search (
    claim_id             TEXT             NOT NULL REFERENCES claim (id),
    seq                  INTEGER          NOT NULL CHECK (seq >= 1),
    trigger              TEXT             NOT NULL CHECK (trigger IN ('analyst', 'automatic_retry')),
    searched_by          TEXT,
    searcher_name        TEXT,
    specialty            TEXT             NOT NULL,
    location_source      TEXT             NOT NULL CHECK (location_source IN ('app', 'analyst')),
    latitude             DOUBLE PRECISION NOT NULL,
    longitude            DOUBLE PRECISION NOT NULL,
    accuracy_m           DOUBLE PRECISION,
    address              TEXT,
    location_seq         INTEGER          NOT NULL DEFAULT 0,
    location_recorded_at TIMESTAMPTZ      NOT NULL,
    from_radius_km       DOUBLE PRECISION NOT NULL CHECK (from_radius_km > 0),
    radius_km            DOUBLE PRECISION NOT NULL CHECK (radius_km > 0),
    outcome              TEXT             NOT NULL CHECK (outcome IN ('found', 'exhausted')),
    reason               TEXT,
    -- [{provider_id, name, specialties, distance_km, availability,
    -- unavailable_until, eligibility, suggested}], nearest first.
    candidates           JSONB            NOT NULL CHECK (jsonb_typeof(candidates) = 'array'),
    policy_version       TEXT             NOT NULL,
    searched_at          TIMESTAMPTZ      NOT NULL,
    PRIMARY KEY (claim_id, seq),
    CONSTRAINT claim_provider_search_shape CHECK (
        (trigger = 'analyst') = (searched_by IS NOT NULL)
        AND (outcome = 'exhausted') = (reason IS NOT NULL))
);

-- Every radius a search tried, in order (criterion 4).
CREATE TABLE IF NOT EXISTS claim_provider_search_attempt (
    claim_id       TEXT             NOT NULL,
    seq            INTEGER          NOT NULL,
    position       SMALLINT         NOT NULL CHECK (position >= 0),
    radius_km      DOUBLE PRECISION NOT NULL CHECK (radius_km > 0),
    eligible_count INTEGER          NOT NULL CHECK (eligible_count >= 0),
    attempted_at   TIMESTAMPTZ      NOT NULL,
    PRIMARY KEY (claim_id, seq, position),
    FOREIGN KEY (claim_id, seq) REFERENCES claim_provider_search (claim_id, seq)
);

-- The provider confirmed for a claim (criterion 5): one per claim. It keeps a
-- copy of the provider and of the location as they were shared.
CREATE TABLE IF NOT EXISTS claim_assignment (
    claim_id             TEXT             PRIMARY KEY REFERENCES claim (id),
    provider_id          TEXT             NOT NULL REFERENCES provider (id),
    provider_name        TEXT             NOT NULL,
    contact_email        TEXT             NOT NULL,
    contact_phone        TEXT,
    search_seq           INTEGER          NOT NULL,
    distance_km          DOUBLE PRECISION NOT NULL CHECK (distance_km >= 0),
    location_source      TEXT             NOT NULL CHECK (location_source IN ('app', 'analyst')),
    latitude             DOUBLE PRECISION NOT NULL,
    longitude            DOUBLE PRECISION NOT NULL,
    accuracy_m           DOUBLE PRECISION,
    address              TEXT,
    location_seq         INTEGER          NOT NULL DEFAULT 0,
    location_recorded_at TIMESTAMPTZ      NOT NULL,
    policy_version       TEXT             NOT NULL,
    assigned_by          TEXT             NOT NULL,
    assigner_name        TEXT             NOT NULL DEFAULT '',
    assigned_at          TIMESTAMPTZ      NOT NULL,
    FOREIGN KEY (claim_id, search_seq) REFERENCES claim_provider_search (claim_id, seq)
);

-- The e-mail that gives the assigned provider the location (decision D12). It
-- is written with the assignment and changes only as it is sent.
CREATE TABLE IF NOT EXISTS provider_notice (
    claim_id        TEXT        PRIMARY KEY REFERENCES claim_assignment (claim_id),
    to_email        TEXT        NOT NULL,
    subject         TEXT        NOT NULL,
    body            TEXT        NOT NULL,
    status          TEXT        NOT NULL CHECK (status IN ('pending', 'sent')),
    attempts        INTEGER     NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at TIMESTAMPTZ,
    sent_at         TIMESTAMPTZ,
    last_error      TEXT,
    CONSTRAINT provider_notice_shape CHECK ((status = 'sent') = (sent_at IS NOT NULL))
);

-- The worker looks up the due notices.
CREATE INDEX IF NOT EXISTS provider_notice_due ON provider_notice (status, next_attempt_at);

-- A location, a search, an attempt and an assignment are evidence of how the
-- provider was chosen: they are never corrected nor deleted.
CREATE OR REPLACE FUNCTION forbid_assignment_history_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION '% is append-only: % is not allowed', TG_TABLE_NAME, TG_OP
        USING ERRCODE = 'restrict_violation';
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE TRIGGER claim_incident_location_no_update_delete
    BEFORE UPDATE OR DELETE ON claim_incident_location
    FOR EACH ROW EXECUTE FUNCTION forbid_assignment_history_mutation();
CREATE OR REPLACE TRIGGER claim_incident_location_no_truncate
    BEFORE TRUNCATE ON claim_incident_location
    FOR EACH STATEMENT EXECUTE FUNCTION forbid_assignment_history_mutation();

CREATE OR REPLACE TRIGGER claim_provider_search_no_update_delete
    BEFORE UPDATE OR DELETE ON claim_provider_search
    FOR EACH ROW EXECUTE FUNCTION forbid_assignment_history_mutation();
CREATE OR REPLACE TRIGGER claim_provider_search_no_truncate
    BEFORE TRUNCATE ON claim_provider_search
    FOR EACH STATEMENT EXECUTE FUNCTION forbid_assignment_history_mutation();

CREATE OR REPLACE TRIGGER claim_provider_search_attempt_no_update_delete
    BEFORE UPDATE OR DELETE ON claim_provider_search_attempt
    FOR EACH ROW EXECUTE FUNCTION forbid_assignment_history_mutation();
CREATE OR REPLACE TRIGGER claim_provider_search_attempt_no_truncate
    BEFORE TRUNCATE ON claim_provider_search_attempt
    FOR EACH STATEMENT EXECUTE FUNCTION forbid_assignment_history_mutation();

CREATE OR REPLACE TRIGGER claim_assignment_no_update_delete
    BEFORE UPDATE OR DELETE ON claim_assignment
    FOR EACH ROW EXECUTE FUNCTION forbid_assignment_history_mutation();
CREATE OR REPLACE TRIGGER claim_assignment_no_truncate
    BEFORE TRUNCATE ON claim_assignment
    FOR EACH STATEMENT EXECUTE FUNCTION forbid_assignment_history_mutation();
