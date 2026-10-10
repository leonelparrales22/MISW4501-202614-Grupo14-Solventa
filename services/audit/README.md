# Servicio `audit` (auditoría y consentimientos)

Garantiza trazabilidad: cualquier decisión de suscripción o precio debe poder reconstruirse al 100 %, y los consentimientos de Open Finance deben ser verificables y revocables.

**Estado:** esqueleto; solo expone `GET /health`.

## Responsabilidades

| Módulo (`internal/`) | Qué hace |
|---|---|
| `decisions` | Registro inmutable (*append-only*) de qué datos y reglas llevaron a un precio o decisión de aceptación/rechazo |
| `consents` | Registro, consulta y revocación de consentimientos de datos financieros (revocación efectiva ≤ 5 min) |

## Reglas de diseño

- La tabla de auditoría es *append-only*, particionada por fecha; no se permite `UPDATE` ni `DELETE` (HA-03).
- Consume eventos por SQS con consumidores **idempotentes** y DLQ.
- Mínimo privilegio y minimización de datos (Ley 1581 de 2012, Decreto 1297 de 2022, Circular 004 de 2024 SFC).

## Datos y eventos

- **BD propia:** PostgreSQL (auditoría, append-only).
- **Consume:** eventos de negocio de originación y siniestros. Ver [catálogo de eventos](../../docs/events/README.md).

## Historias asociadas

Épica web 4 (F4.1, F4.2), épica móvil 1 (F1.3), HA-03, EC-S01 y EC-S02.

## Desarrollo

```
go run ./services/audit/cmd/audit
```
