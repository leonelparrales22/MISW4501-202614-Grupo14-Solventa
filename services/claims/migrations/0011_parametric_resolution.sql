-- HU-M2.4-01 (criterion 3): the automatic resolution of a parametric claim,
-- and the outbox of the domain events of claims.
--
-- Numbered 0011, after 0010_claim_resubmission of HU-W2.1-04. It depends on
-- the claim table and on forbid_claim_history_mutation (0007).

-- The parametric engine files its claims itself: a third channel besides the
-- phone and the portal.
ALTER TABLE claim DROP CONSTRAINT IF EXISTS claim_channel_known;
ALTER TABLE claim ADD CONSTRAINT claim_channel_known CHECK (channel IN ('mobile', 'web', 'parametric'));

-- What resolved a parametric claim: the observation of the external event,
-- the rule applied and the trigger (the parameter, its threshold and the value
-- observed), which the timeline of the app shows. One per claim and one per
-- observation: the same observation again resolves nothing new. It is
-- evidence of how the case went, so it is insert-only.
CREATE TABLE IF NOT EXISTS claim_parametric_resolution (
    claim_id         TEXT             PRIMARY KEY REFERENCES claim (id),
    observation_id   TEXT             NOT NULL UNIQUE,
    rule_version     TEXT             NOT NULL,
    parameter        TEXT             NOT NULL,
    event_reference  TEXT             NOT NULL,
    threshold        DOUBLE PRECISION NOT NULL,
    observed         DOUBLE PRECISION NOT NULL CHECK (observed >= threshold),
    unit             TEXT             NOT NULL,
    observed_at      TIMESTAMPTZ      NOT NULL,
    resolved_at      TIMESTAMPTZ      NOT NULL
);

CREATE OR REPLACE TRIGGER claim_parametric_resolution_no_update_delete
    BEFORE UPDATE OR DELETE ON claim_parametric_resolution
    FOR EACH ROW EXECUTE FUNCTION forbid_claim_history_mutation();

CREATE OR REPLACE TRIGGER claim_parametric_resolution_no_truncate
    BEFORE TRUNCATE ON claim_parametric_resolution
    FOR EACH STATEMENT EXECUTE FUNCTION forbid_claim_history_mutation();

-- The outbox of claims (pkg/outbox), the same table origination has. The
-- events are written here in the same statement as the change they describe
-- and a relay publishes them to the claims topic afterwards: the first is
-- ParametricPayoutCompleted, written when the gateway confirms the automatic
-- payout. event_id is deterministic (one per fact), so writing the same fact
-- twice duplicates nothing; payload is the whole envelope.
CREATE TABLE IF NOT EXISTS outbox_event (
    event_id        TEXT        PRIMARY KEY,
    event_type      TEXT        NOT NULL,
    payload         JSONB       NOT NULL,
    occurred_at     TIMESTAMPTZ NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    attempts        INTEGER     NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    claimed_until   TIMESTAMPTZ,
    published_at    TIMESTAMPTZ,
    last_error      TEXT        NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS outbox_event_pending_idx
    ON outbox_event (next_attempt_at) WHERE published_at IS NULL;
