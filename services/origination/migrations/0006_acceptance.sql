-- HU-W1.3-01: the customer's acceptance of an offer, the no-loss point of the
-- origination journey (CA-1). It is persisted before any later step runs, so
-- it survives a failed dependency and a restart of the platform (CA-5, fix C5).
-- The acceptance processor and the advisor decision (HU-W1.1-02) update the
-- row as the journey goes on.
--
-- There is no foreign key to the offer table: the demo (demo-*) and adjusted
-- (adj-*) offers do not live there. There is no version column either: with a
-- single origination task the writes are not concurrent (decision D-3 c of the
-- fix C5); several tasks need one before they can share this table.
CREATE TABLE IF NOT EXISTS acceptance (
    acceptance_id            TEXT        PRIMARY KEY,
    -- An offer has at most one acceptance.
    offer_id                 TEXT        NOT NULL UNIQUE,
    -- The owner of the accepted offer: the Cognito subject of the customer.
    customer_id              TEXT        NOT NULL,
    channel                  TEXT        NOT NULL CHECK (channel IN ('web', 'mobile')),
    accepted_at              TIMESTAMPTZ NOT NULL,
    status                   TEXT        NOT NULL
        CHECK (status IN ('registered', 'issuance_pending', 'in_review', 'adjusted', 'rejected')),

    -- Automatic underwriting, set once it decided (never run twice).
    underwriting_outcome     TEXT        CHECK (underwriting_outcome IN ('approved', 'human_review')),
    underwriting_reason      TEXT,
    underwriting_decided_at  TIMESTAMPTZ,

    -- The advisor decision as the customer sees it (HU-W1.1-02).
    review_outcome           TEXT        CHECK (review_outcome IN ('accept', 'adjust', 'reject')),
    review_decided_at        TIMESTAMPTZ,
    -- {offer_id, premium_minor, currency, expires_at} of the adjusted offer.
    review_adjusted_offer    JSONB,

    -- Processing attempts (CA-5).
    attempts                 INTEGER     NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    last_failure_at          TIMESTAMPTZ,
    -- NULL is the zero time: due at once while registered.
    next_attempt_at          TIMESTAMPTZ,
    updated_at               TIMESTAMPTZ,
    recorded_at              TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- The advisor inbox points here with the customer, so a case always has
    -- the customer of its acceptance (fix C5, decision #75).
    UNIQUE (acceptance_id, customer_id),

    CHECK ((underwriting_outcome IS NULL) = (underwriting_decided_at IS NULL)),
    CHECK (underwriting_outcome IS NOT NULL OR underwriting_reason IS NULL),
    CHECK ((review_outcome IS NULL) = (review_decided_at IS NULL)),
    CHECK (review_adjusted_offer IS NULL OR review_outcome = 'adjust')
);

-- The acceptance processor looks up the registered acceptances that are due,
-- oldest first.
CREATE INDEX IF NOT EXISTS acceptance_due_idx ON acceptance (accepted_at, acceptance_id)
    WHERE status = 'registered';
