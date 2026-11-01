-- HU-W4.2-01: indexes of the list of consents of the compliance portal. The
-- list is filtered, counted and paged in the database with the status rule of
-- domain.DeriveStatus written in SQL (decision D-2 b′,
-- internal/adapters/postgres/compliance_list.go). This migration only creates
-- indexes: no table, row or trigger changes, and the append-only rule of
-- 0001-0005 stays as it is.

-- Trigram search over a part of the name of the holder. pg_trgm is a trusted
-- extension: the owner of the database creates it without being a superuser.
CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- The order of the list (most recently granted first, the id compared byte by
-- byte) and the range of grant dates. The expiry is a function of granted_at,
-- so the filters by status bound granted_at too.
CREATE INDEX IF NOT EXISTS consent_record_compliance_order_idx
    ON consent_record (granted_at DESC, consent_id COLLATE "C");

-- The revocation of the whole consent that decides its status: the first
-- revoked event without an entity, in the order of domain.DeriveStatus.
CREATE INDEX IF NOT EXISTS consent_event_consent_revoked_idx
    ON consent_event (consent_id, occurred_at, recorded_at, event_id COLLATE "C")
    WHERE status = 'revoked' AND coalesce(entity_code, '') = '';

-- The search by a part of the name, without capitals or accents: the
-- expression is domain.FoldName written with translate(), so it does not
-- depend on the locale of the database. The list writes the same expression.
CREATE INDEX IF NOT EXISTS consent_record_holder_name_trgm_idx
    ON consent_record USING gin (
        translate(holder_full_name, 'ABCDEFGHIJKLMNOPQRSTUVWXYZÁÉÍÓÚÜÑáéíóúüñ', 'abcdefghijklmnopqrstuvwxyzaeiouunaeiouun') gin_trgm_ops
    );
