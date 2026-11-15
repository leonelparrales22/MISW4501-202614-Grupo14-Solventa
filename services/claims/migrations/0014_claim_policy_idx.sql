-- HU-W1.4-02: before an endorsement is applied, origination asks how many
-- claims of the policy are in progress (GET /internal/policies/{policy_id}/
-- open-claims). The count reads the claims of one policy by status.
CREATE INDEX IF NOT EXISTS claim_policy_status_idx ON claim (policy_id, status);
