-- El outbox de originación (HU-M1.3-01).
--
-- Los eventos de dominio no se publican dentro de la transacción del negocio:
-- se escriben aquí, en la misma transacción que el cambio que describen, y un
-- relevo los envía después al tópico de originación (pkg/outbox, pkg/events).
-- Así un evento nunca se pierde si SNS no responde, ni se publica por una
-- operación que se deshizo.
--
-- El primero es CustomerLinked: el registro guarda el sub de la cuenta del
-- asegurado y, con él, el evento que le dice a auditoría quién es el titular
-- de sus consentimientos.
--
-- event_id es la llave: el id del evento es determinista (uno por hecho), así
-- que escribir el mismo hecho dos veces no duplica nada. payload guarda el
-- sobre completo. attempts, next_attempt_at y claimed_until son del relevo:
-- cada tarea reclama con un lease y solo quien tiene el reclamo puede marcar
-- el evento como publicado o reprogramarlo. Las filas publicadas no se borran
-- todavía (la retención está pendiente).

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

-- El relevo solo mira lo pendiente.
CREATE INDEX IF NOT EXISTS outbox_event_pending_idx
    ON outbox_event (next_attempt_at) WHERE published_at IS NULL;

-- Relleno: quien se registró antes de esta historia ya tiene sub pero nunca
-- tuvo su evento, y auditoría lo vería para siempre como «cuenta sin
-- vincular». Se encola un CustomerLinked por cada cliente con sub, con el
-- mismo id determinista y el mismo sobre que arma el servicio en Go
-- (internal/domain/customer_linked.go); el instante del vínculo es su
-- updated_at. Si el evento ya existe no se toca.
INSERT INTO outbox_event (event_id, event_type, payload, occurred_at, next_attempt_at)
SELECT 'customer-linked-' || c.id,
       'CustomerLinked',
       jsonb_build_object(
           'event_id',      'customer-linked-' || c.id,
           'event_type',    'CustomerLinked',
           'event_version', 1,
           'source',        'origination',
           'occurred_at',   to_char(c.updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
           'payload',       jsonb_build_object(
               'customer_id', c.id,
               'subject',     c.cognito_sub,
               'document_id', c.document_id,
               'linked_at',   to_char(c.updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'))),
       c.updated_at,
       now()
FROM customer c
WHERE c.cognito_sub IS NOT NULL
ON CONFLICT (event_id) DO NOTHING;
