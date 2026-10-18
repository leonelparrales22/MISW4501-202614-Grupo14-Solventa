-- The payment method the customer hands over in screen E4 (HU-W1.3-02).
--
-- The card number is never stored, and never reaches this service: the
-- browser tokenises it against the gateway and only the token, the brand and
-- the last four digits travel. That is what keeps the platform out of
-- PCI-DSS scope, so none of these columns may ever hold a PAN.
--
-- Every column is nullable and the counter defaults to 0, so the charges that
-- already exist stay valid without a backfill.

ALTER TABLE charge_attempt
    ADD COLUMN IF NOT EXISTS payment_method_kind         TEXT,
    ADD COLUMN IF NOT EXISTS payment_token               TEXT,
    ADD COLUMN IF NOT EXISTS payment_brand               TEXT,
    ADD COLUMN IF NOT EXISTS payment_last4               TEXT,
    ADD COLUMN IF NOT EXISTS payment_installments        INTEGER,
    ADD COLUMN IF NOT EXISTS payment_bank_code           TEXT,
    ADD COLUMN IF NOT EXISTS payment_method_captured_at  TIMESTAMPTZ,
    -- How many payment methods the customer has handed over. It is not
    -- attempts, which counts the failed gateway attempts of the window.
    ADD COLUMN IF NOT EXISTS payment_attempt             INTEGER NOT NULL DEFAULT 0;

-- A charge is now pending with no due date while it waits for a payment
-- method, and the worker finds the due ones with (status, next_attempt_at).
-- The existing index already covers that read; this comment records why the
-- NULL is meaningful, so nobody "fixes" it with a NOT NULL later.
COMMENT ON COLUMN charge_attempt.next_attempt_at IS
    'When the next attempt is due. NULL means the charge is pending but not due: it is waiting for its payment method (HU-W1.3-02).';
