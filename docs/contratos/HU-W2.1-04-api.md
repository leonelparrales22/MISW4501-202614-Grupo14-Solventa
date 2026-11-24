# Contrato de API — HU-W2.1-04 · Edición de la radicación devuelta

Servicio: **siniestros** (`claims`). Consumidores: portal web del asegurado (pantalla A9, ruta `/siniestros/:id/corregir`),
la revisión del asesor (S8, que vuelve a ver el caso) y la línea de tiempo del caso (A8, HU-W2.1-02).

> Contrato formal: `services/claims/api/openapi.yaml` (0.13.0). Este documento resume lo que necesita cada consumidor.

---

## 1. El ciclo

```
active ──(asesor devuelve, HU-W2.1-03)──▶ returned ──(asegurado corrige y reenvía)──▶ active ──▶ nueva revisión (seq 2)
```

El caso conserva su id y su número de radicado. Lo que el asegurado no cambia se conserva: los hechos que no envía y los
soportes de los requisitos que no toca. Cada reenvío queda registrado en `claim_resubmission` con quién, cuándo, a qué
devolución responde y qué cambió.

## 2. Rutas

| Método y ruta | Rol | Para qué |
|---|---|---|
| `GET /v1/claims/{id}/history` | asegurado (solo los suyos), asesor, operaciones | `last_return` (motivo, mensaje, veredicto por soporte, `facts_to_correct`, `seq`) y ahora `resubmissions` |
| `GET /v1/claims/{id}` | asegurado dueño | hechos radicados y soportes vigentes, con su `support_id` |
| `POST /v1/claims/supports` y `/confirmation` | asegurado | subir los archivos nuevos (igual que en la radicación, HU-W2.1-01) |
| `POST /v1/claims/{id}/resubmission` | asegurado dueño | corregir y reenviar |

## 3. Qué ve el asegurado (A9, CA-1)

Todo sale de dos lecturas que ya existen:

- **Motivo de la devolución:** `history.last_return.message`, `reason_code`, `reviewer_name` y `reviewed_at`.
- **Lo que pidió el asesor:** `last_return.supports[]`, con un veredicto por soporte (`valid`, `illegible` o `missing`), y
  `last_return.facts_to_correct[]`.
- **Lo que ya radicó:** los hechos y `supports.items[].files[]` de `GET /v1/claims/{id}`.
- **Las reglas de cada soporte** (`history.requirements[]`, el mismo `Requirement` de las opciones de radicación):
  obligatorio, mínimo y máximo de archivos y formatos, en el orden del catálogo. No dependen de que la póliza siga
  vigente. En una radicación del móvil, `vehicle_photos` es la única obligatoria.
- **`review_seq`** del reenvío: `last_return.seq`.

## 4. Reenviar (`POST /v1/claims/{id}/resubmission`, CA-2 a CA-4)

```json
{
  "review_seq": 1,
  "place": "Calle 80 con carrera 30, frente al centro comercial, Bogotá",
  "estimated_amount_minor": 520000000,
  "supports": [
    { "requirement_code": "traffic_report", "support_ids": ["sup-…nuevo"] },
    { "requirement_code": "vehicle_photos", "support_ids": ["sup-…1", "sup-…2", "sup-…3", "sup-…4", "sup-…5", "sup-…6", "sup-…nuevo"] }
  ]
}
```

- **Hechos editables:** `occurred_at`, `place`, `description` y `estimated_amount_minor`. Uno que no se envía (o el monto en
  `null`) se conserva. La póliza y el tipo de siniestro no se cambian. Se validan con las reglas de la radicación, y la fecha
  del hecho, además, no puede ser posterior a la radicación y debe estar dentro de la vigencia de la póliza copiada al
  radicar.
- **Un grupo de soportes es el conjunto completo que queda en ese requisito:** los `support_id` del caso que se conservan
  más los nuevos.
  - Reemplazar el informe es enviar solo el nuevo.
  - Agregar una foto es enviar las seis de antes más la nueva.
  - Un requisito que no se envía conserva sus archivos.
  - Los archivos que salen se retiran (`claim_support_withdrawal`) y dejan de mostrarse, pero no se borran.
- **Validación de lo que falta (CA-3).** Un `422 validation_failed` lista todo lo pendiente a la vez:

  | Código | Cuándo |
  |---|---|
  | `not_replaced` | un soporte que el asesor marcó ilegible no tiene un archivo nuevo |
  | `missing` | un soporte que el asesor marcó como faltante, o uno obligatorio del tipo de siniestro, quedó sin archivos |
  | `too_few`, `too_many`, `invalid_format`, `not_found`, `not_applicable` | las mismas reglas de la radicación, para los grupos enviados |
  | `required` en `estimated_amount_minor` | el asesor pidió el monto y no se declaró |
  | `required`, `invalid_format`, `out_of_range` en un hecho | las reglas de la radicación |

- **Respuesta `200`:** `{claim_id, filing_number, status: "active", resubmission}`. `resubmission` lleva `seq`,
  `review_seq`, `resubmitted_by` (el `sub` del asegurado), `resubmitted_at`, `changed_facts[]` (`fact`, `before`, `after`) y
  `changed_supports[]` (`requirement_code`, `label`, `added[]`, `withdrawn[]`).
- **Reintento:** si el caso ya no está devuelto y el mismo asegurado ya respondió esa devolución, se responde `200` con el
  mismo reenvío y el estado actual del caso. No se aplica dos veces.
- **Errores:**
  - `404 claim_not_found`: el caso no existe o es de otro asegurado.
  - `409 claim_not_returned`: el caso no está devuelto.
  - `409 stale_return`: `review_seq` no es la última devolución; hay que releer el historial.
  - `400 invalid_request`: el cuerpo no sigue el esquema, incluido un campo que no es del contrato, como `policy_id`.

## 5. Radicaciones del móvil

También se corrigen desde el portal. Su evidencia cuenta como `vehicle_photos`. Si el asesor la marcó ilegible, se suben
al menos 4 fotos nuevas como soportes, y esas reemplazan a las del móvil en el *checklist* del asesor. Los demás soportes
que pida (por ejemplo `driver_document`) se suben igual que en la web. Una radicación del móvil no tiene póliza copiada,
así que la fecha del hecho solo se valida contra la radicación.

## 6. Lo que no hace esta historia

- No avisa al asesor del reenvío: el caso vuelve a `GET /v1/claims/reviews` como cualquier radicación `active`.
- La pantalla S8 no resalta todavía qué cambió. El asesor lo lee en `GET /v1/claims/{id}/history` (`resubmissions`).
- No permite cambiar la póliza ni el tipo de siniestro: eso sería una radicación nueva.
