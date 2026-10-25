# HU-W4.1-01 — Registro de auditoría de decisiones de suscripción

Servicio: `audit`. Contrato completo en [`services/audit/api/openapi.yaml`](../../services/audit/api/openapi.yaml).

## Consulta (portal web, rol `operations`)

`GET /v1/audit/decisions/{offer_id}` con el JWT de Cognito. Respuestas: `200` registro, `401` sin token, `403` rol distinto de `operations`,
`404 {"error":"decision_not_found"}` si no existe (mensaje genérico, sin datos internos), `503` si no se pudo leer.

```json
{
  "offer_id": "off-…", "customer_id": "…", "partner_id": "…", "application_id": "…", "channel": "embedded",
  "requested_at": "2026-10-20T15:04:05Z", "rater_version": "v1",
  "sources": [{"source": "open_finance", "status": "ok", "latency_ms": 80}],
  "profile": {"score": 0.4, "category": "standard", "partial": false},
  "rules": [{"order": 1, "code": "base_monthly_rate", "value": 0.0011}],
  "premium": 120000, "currency": "COP",
  "decision": "accepted | rejected | deferred", "decision_reason": "",
  "recorded_at": "2026-10-20T15:04:05Z"
}
```

Los códigos de `rules[].code` (`base_monthly_rate`, `age_factor`, `risk_factor`, `term_factor`, `rounding_step`) y de `sources[].status`
(`ok`, `timeout`, `error`, `circuit_open`) los traduce el cliente. Las fechas son UTC; el portal las muestra con la zona horaria del navegador.

## Registro (interno, origination → audit)

`POST /v1/audit/decisions` con el mismo cuerpo (sin `recorded_at`). `201` si lo guarda, `200` con el registro original si la oferta ya estaba
(idempotente; nunca se reemplaza), `400 validation_failed` con `fields[]`. Sin token: solo accesible dentro de la red.
