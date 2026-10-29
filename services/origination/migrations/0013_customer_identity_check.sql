-- HU-M1.1-01: the identity check of the sign-up.
--
-- The account is only created once the identity provider approved the person
-- (criterion 5), so the customer records where the check stands and the
-- provider's last answer. The reason of a refusal is for the platform only:
-- the API never returns it (criterion 3).
--
-- identity_status is empty for a customer an advisor quoted for, who was never
-- asked to verify. The customers registered before this migration keep it
-- empty too: they signed up without a check, and saying otherwise would be
-- false.
ALTER TABLE customer ADD COLUMN IF NOT EXISTS identity_status          TEXT NOT NULL DEFAULT '';
ALTER TABLE customer ADD COLUMN IF NOT EXISTS identity_rejection       TEXT;
ALTER TABLE customer ADD COLUMN IF NOT EXISTS identity_verification_id TEXT;
ALTER TABLE customer ADD COLUMN IF NOT EXISTS identity_checked_at      TIMESTAMPTZ;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'customer_identity_status_known') THEN
        ALTER TABLE customer ADD CONSTRAINT customer_identity_status_known
            CHECK (identity_status IN ('', 'pending', 'verified', 'rejected'));
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'customer_identity_rejection_known') THEN
        ALTER TABLE customer ADD CONSTRAINT customer_identity_rejection_known
            CHECK (identity_rejection IS NULL OR identity_rejection IN ('restricted_list', 'unreadable', 'unverifiable'));
    END IF;
END $$;
