-- HU-M3.2-01: alerts 30, 15 and 5 days before a policy expires.
--
-- policy_expiry_notice is the alert of one milestone of one period of one
-- policy, of origination (life) or of the policy core (auto, home, travel), so
-- it has no foreign key to policy. It is written in the same transaction as
-- the PolicyExpiryApproaching event in outbox_event; the primary key makes
-- every milestone of a period alerted once even when several tasks sweep at
-- the same time (INSERT ... ON CONFLICT DO NOTHING). A renewal moves the end
-- of the coverage, so the new period starts its milestones again.
CREATE TABLE IF NOT EXISTS policy_expiry_notice (
    policy_id      TEXT        NOT NULL,
    period_end     TIMESTAMPTZ NOT NULL,
    milestone_days INTEGER     NOT NULL CHECK (milestone_days IN (5, 15, 30)),
    source         TEXT        NOT NULL CHECK (source IN ('origination', 'policy_core')),
    subject        TEXT        NOT NULL,
    days_left      INTEGER     NOT NULL CHECK (days_left >= 0 AND days_left <= milestone_days),
    event_id       TEXT        NOT NULL UNIQUE,
    detected_at    TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (policy_id, period_end, milestone_days)
);

-- The sweep leaves out the policies that already have a successor.
CREATE INDEX IF NOT EXISTS policy_renewal_of_idx ON policy (renewal_of) WHERE renewal_of IS NOT NULL;
