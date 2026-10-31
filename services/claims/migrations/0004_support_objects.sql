-- HU-W2.1-01 (provisional point of section 9 of its plan): the file of a
-- support no longer lives in PostgreSQL. The browser uploads it to the object
-- store of the environment with a signed URL (the same store as the evidence
-- of HU-M2.1-01, under supports/<sub>/<support_id>), and this table keeps its
-- metadata and whether the upload was confirmed.

-- The support files of the web filing were never published to an environment
-- (the routes needed POLICY_CORE_URL, which no environment set), so the only
-- rows that can exist are from a local run. A file already attached to a
-- claim cannot be dropped silently: the claim is immutable and its supports
-- are evidence. Moving such content to the store has to be done by hand
-- before this migration.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM claim_support) THEN
        RAISE EXCEPTION 'support_file.content holds files attached to claims: upload them to the object store (supports/<owner_sub>/<support_id>) before dropping the column'
            USING ERRCODE = 'restrict_violation';
    END IF;
END
$$;

-- The supports that nobody attached were only waiting for a filing and their
-- content is about to disappear: they are declared again.
DELETE FROM support_file;

ALTER TABLE support_file DROP COLUMN content;
ALTER TABLE support_file ADD COLUMN object_key TEXT NOT NULL;
-- Null until the stored file was checked against its declaration: only a
-- confirmed support can be attached to a claim.
ALTER TABLE support_file ADD COLUMN confirmed_at TIMESTAMPTZ;
