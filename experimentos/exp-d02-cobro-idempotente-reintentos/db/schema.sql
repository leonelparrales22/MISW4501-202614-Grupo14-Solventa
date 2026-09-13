-- Esquema del experimento EC-D02. Única fuente de verdad: se monta en
-- docker-compose.yml vía /docker-entrypoint-initdb.d/ y se reutiliza tal
-- cual en internal/store/postgres_test.go (Testcontainers) para que el
-- esquema de test y el de compose nunca diverjan.

CREATE TABLE IF NOT EXISTS aceptacion (
    id             UUID PRIMARY KEY,
    cliente_id     TEXT NOT NULL,
    monto_centavos BIGINT NOT NULL,
    moneda         TEXT NOT NULL DEFAULT 'COP',
    creada_en      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- El constraint UNIQUE sobre idempotency_key es el objeto bajo prueba del
-- experimento: la última línea de defensa contra un cobro duplicado,
-- incluso si la lógica de aplicación (internal/cobro) falla ante una
-- carrera entre dos workers.
CREATE TABLE IF NOT EXISTS intento_cobro (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    aceptacion_id      UUID NOT NULL REFERENCES aceptacion(id),
    idempotency_key    TEXT NOT NULL UNIQUE,
    estado             TEXT NOT NULL DEFAULT 'pendiente',
    intentos           INT NOT NULL DEFAULT 0,
    proximo_intento_en TIMESTAMPTZ NOT NULL DEFAULT now(),
    creado_en          TIMESTAMPTZ NOT NULL DEFAULT now(),
    actualizado_en     TIMESTAMPTZ NOT NULL DEFAULT now(),
    ultimo_error       TEXT
);

CREATE INDEX IF NOT EXISTS idx_intento_cobro_aceptacion ON intento_cobro (aceptacion_id);
