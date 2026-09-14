-- Estructura de referencia de la tabla siniestros
CREATE TABLE IF NOT EXISTS siniestros (
    id             UUID PRIMARY KEY,
    poliza_id      UUID NOT NULL,
    prioridad      INTEGER NOT NULL DEFAULT 0,
    estado_actual  VARCHAR(50) NOT NULL,
    -- actualizado_en permite saber qué tan vieja es una respuesta servida
    -- desde la caché (stale=true) cuando Postgres no responde a tiempo.
    actualizado_en TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Migración para bases que ya existían sin la columna.
ALTER TABLE siniestros
    ADD COLUMN IF NOT EXISTS actualizado_en TIMESTAMPTZ NOT NULL DEFAULT NOW();

CREATE INDEX IF NOT EXISTS idx_siniestros_poliza_id ON siniestros (poliza_id);
CREATE INDEX IF NOT EXISTS idx_siniestros_estado_actual ON siniestros (estado_actual);
