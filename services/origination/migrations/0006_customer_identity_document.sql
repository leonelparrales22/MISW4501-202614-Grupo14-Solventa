-- Las dos caras del documento de identidad del registro (HU-M1.1-01).
--
-- La captura del frente y del reverso terminaba en la caché del teléfono y no
-- salía de ahí: la verificación de identidad quedaba sin el documento que la
-- sustenta. Ahora cada cara se guarda en el repositorio de documentos y el
-- cliente conserva la llave con la que se recupera.
--
-- La llave se deriva del contenido de la imagen, así que un reintento del
-- registro con la misma foto apunta al mismo objeto y no duplica nada. La
-- imagen no vive en la base de datos: aquí solo queda la referencia.
--
-- Las dos columnas son nulas para el cliente que cotizó un asesor
-- (HU-W1.1-01), que nunca capturó un documento.

ALTER TABLE customer ADD COLUMN IF NOT EXISTS document_front_key TEXT;
ALTER TABLE customer ADD COLUMN IF NOT EXISTS document_back_key  TEXT;
