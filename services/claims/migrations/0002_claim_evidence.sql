-- La evidencia del siniestro (HU-M2.1-01): las fotografías y el video que el
-- asegurado capturó en el lugar.
--
-- El archivo no vive aquí: está en el repositorio de evidencia del ambiente y
-- esta tabla guarda su llave, dónde y cuándo se capturó, con qué precisión y
-- su resumen de integridad, que es lo que permite comprobar después que el
-- archivo es el mismo.
--
-- Es de solo inserción, como el resto del caso: la evidencia de un siniestro
-- radicado no se corrige.
CREATE TABLE IF NOT EXISTS claim_evidence (
    claim_id         TEXT        NOT NULL REFERENCES claim (id) ON DELETE CASCADE,
    position         INTEGER     NOT NULL,
    object_key       TEXT        NOT NULL,
    kind             TEXT        NOT NULL CHECK (kind IN ('photo', 'video')),
    captured_at      TIMESTAMPTZ NOT NULL,
    latitude         DOUBLE PRECISION NOT NULL,
    longitude        DOUBLE PRECISION NOT NULL,
    altitude_m       DOUBLE PRECISION,
    accuracy_m       DOUBLE PRECISION NOT NULL,
    location_trusted BOOLEAN     NOT NULL,
    sha256           TEXT        NOT NULL,
    byte_size        BIGINT      NOT NULL CHECK (byte_size > 0),
    duration_s       INTEGER     NOT NULL DEFAULT 0,
    PRIMARY KEY (claim_id, position)
);

-- El orden de captura es el que ve quien evalúa el caso.
CREATE INDEX IF NOT EXISTS claim_evidence_by_claim ON claim_evidence (claim_id, position);
