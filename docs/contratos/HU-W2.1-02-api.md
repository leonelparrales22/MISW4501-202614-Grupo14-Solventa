# Contrato de API — HU-W2.1-02 · Consulta de estado del siniestro por el cliente

Servicio: **siniestros** (`claims`). Consumidores: portal web del asegurado, «Mis siniestros» (A7) y detalle y estado del
siniestro (A8).

> Contrato formal: `services/claims/api/openapi.yaml` (0.14.0). Este documento resume lo que necesita cada consumidor.

---

## 1. Criterios y cómo se cumplen

| Criterio | Cómo |
|---|---|
| CA-1: el estado actual y la fecha del último cambio | `status` y `last_changed_at` en el listado y en el detalle. La línea de tiempo sale de `GET /v1/claims/{id}/history`. |
| CA-2: el cambio se refleja sin un retraso mayor a **5 minutos** (umbral fijado en la revisión INVEST) | La base registra cada cambio al instante con el *trigger* de `0007`, y la lectura siguiente ya lo trae. El portal vuelve a leer cada 60 s mientras la pestaña está visible. La caché HA-L02 solo responde si la base tarda más de 900 ms, y nunca con datos de más de 5 minutos. |

## 2. Rutas (rol asegurado, solo sus casos)

| Ruta | Qué agrega esta historia |
|---|---|
| `GET /v1/claims` | `items[].last_changed_at` y `summary` |
| `GET /v1/claims/{id}` | `last_changed_at` |
| `GET /v1/claims/{id}/history` | `decision` y `provider` (también los leen el asesor y operaciones) |
| `GET /v1/claims/{id}/supports/{support_id}/download` | nueva: un enlace firmado de 10 minutos |

## 3. `last_changed_at`

Es el instante en que el caso llegó a su estado actual: el último cambio que registró `claim_status_change`. Si el caso
nunca se movió, es su radicación. Va en el listado y en el detalle del asegurado. En otras vistas que reutilizan el resumen
del caso (los siniestros previos de S8) no se lee y no aparece.

## 4. `summary` (A7)

Es el conteo de **todos** los casos del asegurado, sin importar los filtros ni la página:

```json
{
  "in_progress": 2,
  "by_status": { "returned": 1, "in_evaluation": 1, "paid": 3 },
  "paid_this_year": { "claims": 1, "amount_minor": 18000000, "currency": "COP" },
  "pending_action": [ { "claim_id": "…", "filing_number": "SIN-2026-01877" } ]
}
```

- `in_progress`: los casos que no están pagados ni rechazados.
- `paid_this_year`: los pagos confirmados desde el 1 de enero en hora de Colombia, netos del deducible.
- `pending_action`: los casos devueltos al asegurado para corregir (A9), del más reciente al más antiguo, hasta 20.

## 5. `decision` y `provider` del historial (A8)

- **`decision`** es `null` hasta que operaciones decide:
  - `outcome` y `decided_at`;
  - `amount_minor`, lo que paga la aseguradora neto del deducible (`null` en un rechazo);
  - `currency`, `payout_status` (`pending`, `in_progress`, `confirmed`, `failed`) y `paid_at`;
  - `rejection_reason`.
  - Nunca la cuenta, el monto bruto ni quién decidió.
- **`provider`** es `null` hasta la asignación (HU-W2.3-01): `name`, `distance_km` y `assigned_at`. Nunca el contacto.

## 6. Descarga de un soporte

`GET /v1/claims/{id}/supports/{support_id}/download` responde
`{support_id, file_name, content_type, byte_size, download_url, expires_at}`.

- El enlace es una URL firmada de S3 que vence en 10 minutos. No pasa por la caché.
- Un soporte que no es del caso, o que un reenvío reemplazó (HU-W2.1-04), responde `404 support_not_found`.
- Un caso de otro asegurado responde `403`.
- Sin almacén de objetos responde `503 downloads_unavailable`.

## 7. Lo que no hace esta historia

- No avisa del cambio de etapa por correo ni por push: eso es HU-M3.3-01, que puede leer el historial.
- No programa la visita del prestador. A8 muestra quién atiende y a qué distancia; la cita es de HU-W2.3-02.
