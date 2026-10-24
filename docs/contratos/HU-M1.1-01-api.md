# Contrato de API — HU-M1.1-01 · Registro del asegurado

Servicio: **originación**. Consumidor: cliente móvil.

---

## 1. `POST /v1/registrations`

Crea el cliente y la cuenta con la que inicia sesión.

| | |
|---|---|
| Autenticación | Ninguna. La cuenta no existe todavía |
| Autorización | Ninguna. El *user pool* solo permite crear cuentas desde el servidor, de modo que originación decide quién la obtiene |
| Límite | 2 peticiones por segundo, ráfaga 5, en el API Gateway |
| Caché | `Cache-Control: no-store` |

### Petición

```json
{
  "document_id": "1020345678",
  "full_name": "Carolina Pérez Ruiz",
  "email": "carolina.perez@example.com",
  "birth_date": "1992-04-18",
  "password": "Solventa2026!",
  "document_front": "/9j/4AAQSkZJRgABAQ…",
  "document_back": "/9j/4AAQSkZJRgABAQ…"
}
```

| Campo | Tipo | Reglas |
|---|---|---|
| `document_id` | string | 5 a 20 caracteres alfanuméricos |
| `full_name` | string | 3 a 120 caracteres, al menos una letra. Los espacios sobrantes se colapsan |
| `email` | string | Correo válido. Es el nombre de usuario en el *user pool* |
| `birth_date` | `YYYY-MM-DD` | Fecha pasada, mayor de edad |
| `password` | string | Mínimo 12 caracteres, con minúscula, mayúscula, dígito y símbolo |
| `document_front` | string | Base64 del JPEG del frente del documento. Entre 1 KiB y 1,5 MB una vez decodificado |
| `document_back` | string | Base64 del JPEG del reverso. Las mismas reglas, y no puede ser la misma imagen del frente |

La contraseña viaja al *user pool* y no se almacena.

Las dos caras van en el mismo cuerpo, que admite **4 MiB**. Un cuerpo mayor responde `400`.

### Respuesta `201`

```json
{
  "customer_id": "7c3e85c7-1ddd-4603-c8e9-0def9f4ce9cd",
  "document_id": "1020345678",
  "full_name": "Carolina Pérez Ruiz",
  "email": "carolina.perez@example.com",
  "birth_date": "1992-04-18",
  "subject": "9f4ce9cd-85c7-4603-b8e9-0def7c3e1ddd",
  "registered_at": "2026-10-22T15:00:00Z",
  "identity_documents": [
    { "face": "front", "object_key": "identity-documents/8f14e45fce…b2438.jpg" },
    { "face": "back", "object_key": "identity-documents/c4ca4238a0…c1c9.jpg" }
  ]
}
```

| Campo | Tipo | Descripción |
|---|---|---|
| `customer_id` | string | Identificador del cliente en originación |
| `subject` | string | Identificador de la cuenta en el *user pool*. Es el mismo valor que el token lleva en `sub` al iniciar sesión |
| `registered_at` | RFC 3339, UTC | Instante del registro |
| `identity_documents` | arreglo | Las dos caras guardadas, el frente primero: `face` (`front`, `back`) y `object_key`, el objeto de cada una en el repositorio documental |

Con esas credenciales el cliente inicia sesión de inmediato contra el *user pool*: la cuenta queda confirmada y la contraseña es definitiva.

---

## 2. Errores

| Código | `error` | Condición |
|---|---|---|
| `400` | `invalid_request` | El cuerpo no es JSON válido |
| `409` | `already_registered` | El documento o el correo ya tienen cuenta |
| `422` | `validation_failed` | Uno o más campos incumplen sus reglas |
| `503` | `registration_unavailable` | El registro no se pudo completar. Reintentable |

`422` lista todos los campos inválidos a la vez:

```json
{
  "error": "validation_failed",
  "message": "one or more fields are missing or invalid",
  "fields": [
    { "field": "email", "code": "invalid_format" },
    { "field": "password", "code": "out_of_range" }
  ]
}
```

Códigos de campo: `required`, `invalid_format`, `out_of_range`.

---

## 3. Reintento

El registro se escribe en tres pasos: primero las dos caras del documento, después el cliente y por último la cuenta en el *user pool*.

Si falla el repositorio documental, la respuesta es `503` y no se guarda ni el cliente ni la cuenta. Si falla la cuenta, el cliente queda guardado sin ella y la respuesta también es `503`. **Reintentar con el mismo documento completa ese registro**: conserva el `customer_id` y la fecha de creación originales, y no crea un cliente nuevo.

La llave de cada cara se deriva del contenido de la imagen, así que un reintento con las mismas fotos apunta a los mismos objetos y no deja copias.

Un documento o un correo que ya tienen cuenta responden `409`, no `503`.

---

## 4. Tratamiento de datos

El documento y el correo identifican a la persona y son únicos en la plataforma. La comparación del correo ignora mayúsculas y minúsculas.

La contraseña no se registra, no se almacena y no aparece en ninguna respuesta.

Las imágenes del documento se guardan en el repositorio documental del ambiente —el *bucket* `documents` en AWS, cifrado, versionado, sin acceso público y solo por TLS— bajo el prefijo `identity-documents/`. El cliente conserva la llave de cada cara; la imagen no se guarda en la base de datos y no se devuelve en ninguna respuesta. La llave no contiene el documento, el nombre ni el correo de la persona.
