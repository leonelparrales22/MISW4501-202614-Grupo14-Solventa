-- Socios comerciales, sus credenciales y su historial (HU-W3.1-01).
--
-- partner: el socio. Nace en pending_approval (sin credenciales) y pasa a
-- active al aprobarlo; usage_plan_id es su usage plan en API Gateway, donde se
-- aplican la cuota diaria y la ráfaga y que lo aísla de los demás socios.
--
-- partner_credential: cada credencial es una API key del gateway en el usage
-- plan del socio. El secreto nunca se guarda: solo sus últimos 4 caracteres
-- (secret_hint). Estados: active (la vigente), retiring (rotada, sigue vigente
-- hasta expires_at, el periodo de transición) y revoked (su key ya no existe).
--
-- partner_history: lo que le pasó al socio y quién lo hizo, append-only.
--
-- outbox_event: los eventos de dominio hacia audit (tópico partners), escritos
-- en la misma transacción que el cambio; mismo diseño que el de originación.

CREATE TABLE IF NOT EXISTS partner (
    partner_id              TEXT        PRIMARY KEY,
    code                    TEXT        NOT NULL UNIQUE,
    name                    TEXT        NOT NULL,
    type                    TEXT        NOT NULL,
    products                JSONB       NOT NULL,
    technical_contact_email TEXT        NOT NULL,
    status                  TEXT        NOT NULL CHECK (status IN ('pending_approval', 'active', 'offboarded')),
    daily_limit             INTEGER     NOT NULL CHECK (daily_limit > 0),
    rate_limit              INTEGER     NOT NULL CHECK (rate_limit > 0),
    burst_limit             INTEGER     NOT NULL CHECK (burst_limit >= rate_limit),
    usage_plan_id           TEXT,
    registered_at           TIMESTAMPTZ NOT NULL,
    registered_by_type      TEXT        NOT NULL,
    registered_by_id        TEXT        NOT NULL,
    registered_by_label     TEXT        NOT NULL,
    approved_at             TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS partner_credential (
    credential_id     TEXT        PRIMARY KEY,
    partner_id        TEXT        NOT NULL REFERENCES partner (partner_id),
    client_id         TEXT        NOT NULL UNIQUE,
    gateway_key_id    TEXT        NOT NULL,
    secret_hint       TEXT        NOT NULL,
    status            TEXT        NOT NULL CHECK (status IN ('active', 'retiring', 'revoked')),
    reason            TEXT        NOT NULL,
    issued_at         TIMESTAMPTZ NOT NULL,
    issued_by_type    TEXT        NOT NULL,
    issued_by_id      TEXT        NOT NULL,
    issued_by_label   TEXT        NOT NULL,
    expires_at        TIMESTAMPTZ,
    revoked_at        TIMESTAMPTZ
);

-- Una sola credencial vigente y una sola en transición por socio.
CREATE UNIQUE INDEX IF NOT EXISTS partner_credential_one_active
    ON partner_credential (partner_id) WHERE status = 'active';
CREATE UNIQUE INDEX IF NOT EXISTS partner_credential_one_retiring
    ON partner_credential (partner_id) WHERE status = 'retiring';
-- El trabajador de revocación solo mira las que están en transición.
CREATE INDEX IF NOT EXISTS partner_credential_retiring_idx
    ON partner_credential (expires_at) WHERE status = 'retiring';

CREATE TABLE IF NOT EXISTS partner_history (
    event_id     TEXT        PRIMARY KEY,
    partner_id   TEXT        NOT NULL REFERENCES partner (partner_id),
    type         TEXT        NOT NULL,
    occurred_at  TIMESTAMPTZ NOT NULL,
    actor_type   TEXT        NOT NULL,
    actor_id     TEXT        NOT NULL,
    actor_label  TEXT        NOT NULL,
    details      JSONB       NOT NULL DEFAULT '{}'::jsonb,
    recorded_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS partner_history_partner_idx
    ON partner_history (partner_id, occurred_at DESC);

CREATE OR REPLACE FUNCTION forbid_append_only_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION '% is append-only: % is not allowed', TG_TABLE_NAME, TG_OP
        USING ERRCODE = 'restrict_violation';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER partner_history_no_update_delete
    BEFORE UPDATE OR DELETE ON partner_history
    FOR EACH ROW EXECUTE FUNCTION forbid_append_only_mutation();

CREATE TRIGGER partner_history_no_truncate
    BEFORE TRUNCATE ON partner_history
    FOR EACH STATEMENT EXECUTE FUNCTION forbid_append_only_mutation();

CREATE TABLE IF NOT EXISTS outbox_event (
    event_id        TEXT        PRIMARY KEY,
    event_type      TEXT        NOT NULL,
    payload         JSONB       NOT NULL,
    occurred_at     TIMESTAMPTZ NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    attempts        INTEGER     NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    claimed_until   TIMESTAMPTZ,
    published_at    TIMESTAMPTZ,
    last_error      TEXT        NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS outbox_event_pending_idx
    ON outbox_event (next_attempt_at) WHERE published_at IS NULL;
