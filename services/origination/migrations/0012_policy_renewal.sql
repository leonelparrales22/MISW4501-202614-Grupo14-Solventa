-- HU-W1.5-02: online renewal of a policy by the insured.
--
-- policy is the portal's view of an active policy, projected from the
-- CoverageActivated event. It is written once per policy: a redelivered event
-- changes nothing.
CREATE TABLE IF NOT EXISTS policy (
    policy_id            TEXT        PRIMARY KEY,
    acceptance_id        TEXT        NOT NULL,
    customer_id          TEXT        NOT NULL,
    holder_full_name     TEXT        NOT NULL,
    holder_document_id   TEXT        NOT NULL,
    holder_email         TEXT        NOT NULL,
    premium_minor        BIGINT      NOT NULL CHECK (premium_minor > 0),
    currency             TEXT        NOT NULL,
    insured_amount_minor BIGINT      NOT NULL,
    coverages            JSONB       NOT NULL,
    coverage_start       TIMESTAMPTZ NOT NULL,
    coverage_end         TIMESTAMPTZ NOT NULL,
    renewal_of           TEXT,
    recorded_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS policy_customer_idx ON policy (customer_id, coverage_end);
CREATE INDEX IF NOT EXISTS policy_coverage_end_idx ON policy (coverage_end);

CREATE TRIGGER policy_no_update_delete BEFORE UPDATE OR DELETE ON policy
    FOR EACH ROW EXECUTE FUNCTION forbid_offer_mutation();
CREATE TRIGGER policy_no_truncate BEFORE TRUNCATE ON policy
    FOR EACH STATEMENT EXECUTE FUNCTION forbid_offer_mutation();

-- renewal is the renewal of one period of one policy. UNIQUE (policy_id,
-- period_end) lets exactly one outcome exist per period: the insured accepting
-- twice, or accepting while the sweep closes the window, produces one row.
CREATE TABLE IF NOT EXISTS renewal (
    renewal_id           TEXT        PRIMARY KEY,
    policy_id            TEXT        NOT NULL REFERENCES policy (policy_id),
    customer_id          TEXT        NOT NULL,
    period_end           TIMESTAMPTZ NOT NULL,
    status               TEXT        NOT NULL CHECK (status IN ('accepted', 'renewed', 'not_renewed')),
    reason               TEXT        CHECK (reason IN ('no_response', 'payment_failed')),
    premium_minor        BIGINT      NOT NULL DEFAULT 0,
    currency             TEXT        NOT NULL DEFAULT '',
    insured_amount_minor BIGINT      NOT NULL DEFAULT 0,
    term_months          INTEGER     NOT NULL DEFAULT 0,
    coverages            JSONB       NOT NULL DEFAULT '[]',
    discount_rate        DOUBLE PRECISION NOT NULL DEFAULT 0,
    requoted             BOOLEAN     NOT NULL DEFAULT false,
    charge_id            TEXT,
    new_policy_id        TEXT,
    accepted_at          TIMESTAMPTZ,
    retry_until          TIMESTAMPTZ,
    closed_at            TIMESTAMPTZ,
    notified_at          TIMESTAMPTZ,
    notification_failed  BOOLEAN     NOT NULL DEFAULT false,
    created_at           TIMESTAMPTZ NOT NULL,
    UNIQUE (policy_id, period_end),
    CHECK (status <> 'not_renewed' OR reason IS NOT NULL)
);

CREATE INDEX IF NOT EXISTS renewal_open_idx ON renewal (retry_until) WHERE status = 'accepted';
CREATE INDEX IF NOT EXISTS renewal_unnotified_idx ON renewal (created_at)
    WHERE status = 'not_renewed' AND notified_at IS NULL AND NOT notification_failed;

-- The charge of a renewal remembers the policy it renews and the earliest
-- start of its coverage: the renewed coverage starts when the old one ends.
ALTER TABLE charge_attempt ADD COLUMN IF NOT EXISTS renewal_of TEXT;
ALTER TABLE charge_attempt ADD COLUMN IF NOT EXISTS coverage_not_before TIMESTAMPTZ;
