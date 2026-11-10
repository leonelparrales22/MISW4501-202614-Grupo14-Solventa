-- HU-W2.1-04: the insured corrects a returned filing and sends it again.
--
-- Numbered 0010, after 0009_provider_seed of HU-W2.3-01. It depends on claim,
-- claim_support (0003) and claim_review (0007).

-- Every resubmission of a returned filing: who sent it, when, the return it
-- answers and what changed. The facts are corrected in the claim itself; this
-- keeps what they were, so the advisor and the insured see what changed. A
-- return is answered once.
CREATE TABLE IF NOT EXISTS claim_resubmission (
    claim_id          TEXT        NOT NULL REFERENCES claim (id),
    seq               INTEGER     NOT NULL CHECK (seq >= 1),
    review_seq        INTEGER     NOT NULL,
    resubmitted_by    TEXT        NOT NULL,
    resubmitted_at    TIMESTAMPTZ NOT NULL,
    -- [{fact, before, after}]
    changed_facts     JSONB       NOT NULL DEFAULT '[]'::jsonb,
    -- [{requirement_code, label, added: [support_id], withdrawn: [support_id]}]
    changed_supports  JSONB       NOT NULL DEFAULT '[]'::jsonb,
    PRIMARY KEY (claim_id, seq),
    CONSTRAINT claim_resubmission_answers_once UNIQUE (claim_id, review_seq)
);

-- The supports a resubmission replaced. claim_support stays append-only: a
-- withdrawn support keeps its row, and the claim stops showing it.
--
-- review_seq and support_id point to claim_review and claim_support without a
-- foreign key: the resubmission writes them in the transaction that checks
-- them, and a key would stop those append-only tables from being truncated
-- with their own error.
CREATE TABLE IF NOT EXISTS claim_support_withdrawal (
    support_id        TEXT    PRIMARY KEY,
    claim_id          TEXT    NOT NULL,
    resubmission_seq  INTEGER NOT NULL,
    FOREIGN KEY (claim_id, resubmission_seq) REFERENCES claim_resubmission (claim_id, seq)
);

CREATE OR REPLACE TRIGGER claim_resubmission_no_update_delete
    BEFORE UPDATE OR DELETE ON claim_resubmission
    FOR EACH ROW EXECUTE FUNCTION forbid_claim_history_mutation();

CREATE OR REPLACE TRIGGER claim_resubmission_no_truncate
    BEFORE TRUNCATE ON claim_resubmission
    FOR EACH STATEMENT EXECUTE FUNCTION forbid_claim_history_mutation();

CREATE OR REPLACE TRIGGER claim_support_withdrawal_no_update_delete
    BEFORE UPDATE OR DELETE ON claim_support_withdrawal
    FOR EACH ROW EXECUTE FUNCTION forbid_claim_history_mutation();

CREATE OR REPLACE TRIGGER claim_support_withdrawal_no_truncate
    BEFORE TRUNCATE ON claim_support_withdrawal
    FOR EACH STATEMENT EXECUTE FUNCTION forbid_claim_history_mutation();
