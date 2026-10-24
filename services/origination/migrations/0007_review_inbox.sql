-- HU-W1.1-02: the advisor inbox. The requests that the automatic underwriting
-- routes to a human decision (review_request), the append-only record of the
-- advisor decisions (human_decision, CA-4) and the offers an advisor adjusts
-- (adjusted_offer, decision D-1 a of the fix C5). They live next to the
-- acceptance (0006) in origination's own database, so a restart never leaves
-- a case without its acceptance nor a decision without its case.
--
-- The advisor is never the owner nor the applicant of anything: the case and
-- the adjusted offer carry the customer of the acceptance (composite foreign
-- keys), and the advisor only appears as decided_by (fix C5, decision #75).

-- The case: what underwriting routed, with a snapshot of the routed offer
-- (decision D-7 a), its state and the customer notice of the decision.
CREATE TABLE IF NOT EXISTS review_request (
    acceptance_id           TEXT             PRIMARY KEY,
    offer_id                TEXT             NOT NULL,
    customer_id             TEXT             NOT NULL,
    -- {full_name, document_id, email} of the customer.
    applicant               JSONB            NOT NULL,
    premium_minor           BIGINT           NOT NULL CHECK (premium_minor > 0),
    currency                TEXT             NOT NULL,
    reason                  TEXT             NOT NULL,
    received_at             TIMESTAMPTZ      NOT NULL,
    status                  TEXT             NOT NULL CHECK (status IN ('pending', 'decided')),

    -- Snapshot of the routed offer: the inbox never reads the offer again.
    offer_mode              TEXT             CHECK (offer_mode IN ('normal', 'provisional')),
    offer_profile_score     DOUBLE PRECISION NOT NULL,
    offer_expires_at        TIMESTAMPTZ,
    -- Same shape as offer.terms; NULL when the routed offer carries none.
    offer_terms             JSONB,

    -- Customer notice of an adjust or reject decision; NULL status = no notice.
    notice_channel          TEXT,
    notice_status           TEXT             CHECK (notice_status IN ('pending', 'sent', 'failed')),
    notice_attempts         INTEGER          NOT NULL DEFAULT 0 CHECK (notice_attempts >= 0),
    notice_next_attempt_at  TIMESTAMPTZ,
    notice_sent_at          TIMESTAMPTZ,
    notice_last_failure_at  TIMESTAMPTZ,
    notice_last_failure     TEXT,

    recorded_at             TIMESTAMPTZ      NOT NULL DEFAULT now(),

    -- No case without its acceptance, and with the customer of the acceptance.
    FOREIGN KEY (acceptance_id, customer_id) REFERENCES acceptance (acceptance_id, customer_id),
    UNIQUE (acceptance_id, customer_id)
);

-- The inbox lists by state and arrival; the notice worker looks up the due notices.
CREATE INDEX IF NOT EXISTS review_request_status_idx ON review_request (status, received_at, acceptance_id);
CREATE INDEX IF NOT EXISTS review_request_notice_due_idx ON review_request (notice_next_attempt_at, acceptance_id)
    WHERE notice_status = 'pending';

-- A case is never deleted and a decided case never becomes pending again.
CREATE OR REPLACE FUNCTION guard_review_request_change() RETURNS trigger AS $$
BEGIN
    IF TG_OP = 'UPDATE' THEN
        IF OLD.status = 'decided' AND NEW.status <> 'decided' THEN
            RAISE EXCEPTION 'review_request %: a decided case cannot go back to %', OLD.acceptance_id, NEW.status
                USING ERRCODE = 'restrict_violation';
        END IF;
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'review_request: % is not allowed', TG_OP
        USING ERRCODE = 'restrict_violation';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER review_request_guard BEFORE UPDATE OR DELETE ON review_request
    FOR EACH ROW EXECUTE FUNCTION guard_review_request_change();
CREATE TRIGGER review_request_no_truncate BEFORE TRUNCATE ON review_request
    FOR EACH STATEMENT EXECUTE FUNCTION guard_review_request_change();

-- The auditable record of the advisor decision (CA-4): who decided, when,
-- what and why. One per case: the unique acceptance_id is the second barrier
-- behind the claim of RecordDecision (EC-D02).
CREATE TABLE IF NOT EXISTS human_decision (
    decision_id             TEXT         PRIMARY KEY,
    acceptance_id           TEXT         NOT NULL UNIQUE REFERENCES review_request (acceptance_id),
    type                    TEXT         NOT NULL CHECK (type IN ('accept', 'adjust', 'reject')),
    -- The subject ("sub") of the advisor who decided.
    decided_by              TEXT         NOT NULL,
    decided_by_email        TEXT,
    decided_at              TIMESTAMPTZ  NOT NULL,
    observations            TEXT         NOT NULL,
    premium_adjustment_pct  NUMERIC(5,2) CHECK (premium_adjustment_pct > 0 AND premium_adjustment_pct <= 100),
    adjusted_offer_id       TEXT         UNIQUE,
    adjusted_premium_minor  BIGINT       CHECK (adjusted_premium_minor > 0),
    adjusted_currency       TEXT,
    adjusted_expires_at     TIMESTAMPTZ,
    recorded_at             TIMESTAMPTZ  NOT NULL DEFAULT now(),

    -- The surcharge and the adjusted offer exist if and only if it is an adjust.
    CHECK (
        (type = 'adjust' AND premium_adjustment_pct IS NOT NULL AND adjusted_offer_id IS NOT NULL
            AND adjusted_premium_minor IS NOT NULL AND adjusted_currency IS NOT NULL AND adjusted_expires_at IS NOT NULL)
        OR
        (type <> 'adjust' AND premium_adjustment_pct IS NULL AND adjusted_offer_id IS NULL
            AND adjusted_premium_minor IS NULL AND adjusted_currency IS NULL AND adjusted_expires_at IS NULL)
    )
);

-- To count the cases each advisor resolved (fix C5, decision #75).
CREATE INDEX IF NOT EXISTS human_decision_decided_by_idx ON human_decision (decided_by, decided_at);

CREATE OR REPLACE FUNCTION forbid_human_decision_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'human_decision is append-only: % is not allowed', TG_OP
        USING ERRCODE = 'restrict_violation';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER human_decision_no_update_delete BEFORE UPDATE OR DELETE ON human_decision
    FOR EACH ROW EXECUTE FUNCTION forbid_human_decision_mutation();
CREATE TRIGGER human_decision_no_truncate BEFORE TRUNCATE ON human_decision
    FOR EACH STATEMENT EXECUTE FUNCTION forbid_human_decision_mutation();

-- The offer of an adjust decision (decision D-1 a), immutable. It exists only
-- for the decision that won the claim (the invariant of B-1, decision #51) and
-- belongs to the customer of the case. It keeps the coverage terms of the
-- routed offer, so its charge activates them (decision D-5 a).
CREATE TABLE IF NOT EXISTS adjusted_offer (
    offer_id       TEXT             PRIMARY KEY CHECK (offer_id LIKE 'adj-%')
                                    REFERENCES human_decision (adjusted_offer_id),
    acceptance_id  TEXT             NOT NULL,
    customer_id    TEXT             NOT NULL,
    applicant      JSONB            NOT NULL,
    premium_minor  BIGINT           NOT NULL CHECK (premium_minor > 0),
    currency       TEXT             NOT NULL,
    mode           TEXT             NOT NULL CHECK (mode IN ('normal', 'provisional')),
    profile_score  DOUBLE PRECISION NOT NULL,
    expires_at     TIMESTAMPTZ      NOT NULL,
    -- Same shape as offer.terms; NULL when the routed offer carried none.
    terms          JSONB,
    recorded_at    TIMESTAMPTZ      NOT NULL DEFAULT now(),

    CHECK (offer_id = 'adj-' || acceptance_id),
    FOREIGN KEY (acceptance_id, customer_id) REFERENCES review_request (acceptance_id, customer_id)
);

CREATE OR REPLACE FUNCTION forbid_adjusted_offer_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'adjusted_offer is immutable: % is not allowed', TG_OP
        USING ERRCODE = 'restrict_violation';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER adjusted_offer_no_update_delete BEFORE UPDATE OR DELETE ON adjusted_offer
    FOR EACH ROW EXECUTE FUNCTION forbid_adjusted_offer_mutation();
CREATE TRIGGER adjusted_offer_no_truncate BEFORE TRUNCATE ON adjusted_offer
    FOR EACH STATEMENT EXECUTE FUNCTION forbid_adjusted_offer_mutation();
