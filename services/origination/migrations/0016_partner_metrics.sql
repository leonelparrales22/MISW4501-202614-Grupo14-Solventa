-- HU-W3.2-01: summary of the integration of a commercial partner on its
-- dashboard. Two indexes so the counts of a range read only the rows of the
-- partner and of the range:
--   - the quotes it requested: its embedded offers by creation time (the ones
--     the advisor adjusted and the renewals are not requests of the partner);
--   - the policies issued: the new policies by the start of their coverage,
--     which for a new policy is the moment its premium was confirmed.
CREATE INDEX IF NOT EXISTS offer_partner_created_idx ON offer (partner_id, created_at)
    WHERE channel = 'embedded' AND supersedes IS NULL AND renews IS NULL;

CREATE INDEX IF NOT EXISTS policy_issued_idx ON policy (coverage_start)
    WHERE renewal_of IS NULL;
