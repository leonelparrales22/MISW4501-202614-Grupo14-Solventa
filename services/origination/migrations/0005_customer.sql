-- El cliente de Solventa (HU-M1.1-01).
--
-- Hasta ahora vivía solo en memoria, de modo que se perdía en cada
-- redespliegue: tanto el que registra el asegurado desde el móvil como el que
-- consulta el asesor por documento en la cotización asistida (HU-W1.1-01).
--
-- El documento es único porque identifica a la persona: es la llave con la
-- que el asesor consulta y la que impide que un reintento del registro cree
-- dos clientes.
--
-- cognito_sub queda nulo mientras la cuenta no se haya creado en el user
-- pool. Un cliente sin sub es un registro a medias y se puede reintentar.

CREATE TABLE IF NOT EXISTS customer (
    id           TEXT        PRIMARY KEY,
    document_id  TEXT        NOT NULL UNIQUE,
    full_name    TEXT        NOT NULL,
    email        TEXT        NOT NULL,
    birth_date   DATE        NOT NULL,
    cognito_sub  TEXT        UNIQUE,
    created_at   TIMESTAMPTZ NOT NULL,
    updated_at   TIMESTAMPTZ NOT NULL
);

-- El registro busca por correo antes de crear la cuenta: dos personas no
-- pueden compartirlo porque es el nombre de usuario en el user pool.
CREATE UNIQUE INDEX IF NOT EXISTS customer_email_key ON customer (lower(email));
