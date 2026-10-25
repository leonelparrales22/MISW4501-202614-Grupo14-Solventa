-- What screen E4c shows when the issuer declines the charge, and what the
-- offer's validity is (HU-W1.3-02).
--
-- The decline code is the issuer's, as the gateway reports it (51 for
-- insufficient funds, for instance). It is kept apart from last_error, which
-- is a free-form diagnostic message for operations: this one is shown to the
-- customer, so it has to be a stable code the client can translate.
--
-- The gateway's reference for a declined charge goes to the existing
-- provider_reference column, the same one a confirmed charge uses: it is the
-- same thing, the id the customer quotes to the issuer.
--
-- Every column is nullable, so the charges that already exist stay valid
-- without a backfill.

ALTER TABLE charge_attempt
    ADD COLUMN IF NOT EXISTS decline_code       TEXT,
    -- Snapshot of the offer's validity, taken when the charge is created. It
    -- is a snapshot and not a read of the offer because the offers live in
    -- memory (decision D-2), so the charge has to carry it.
    ADD COLUMN IF NOT EXISTS offer_expires_at   TIMESTAMPTZ,
    -- Natural or legal person, which PSE asks for. Empty for a card.
    ADD COLUMN IF NOT EXISTS payment_payer_type TEXT;
