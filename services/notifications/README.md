# Servicio `notifications` (notificaciones)

Envía notificaciones push al asegurado: pago paramétrico automático, vencimientos/renovaciones y cambios de estado del siniestro.

**Estado:** esqueleto; solo expone `GET /health`.

## Responsabilidades

| Módulo (`internal/`) | Qué hace |
|---|---|
| `push` | Reglas de envío push (destinatarios, preferencias, reintentos). El cliente HTTP de Firebase Cloud Messaging vive en `internal/adapters/fcm` |
| `templates` | Plantillas de mensajes por tipo de evento e idioma |

## Datos y eventos

- **BD propia:** PostgreSQL (notificaciones).
- **Consume:** eventos de siniestros y originación mediante una cola SQS propia (con DLQ), con consumidores idempotentes. Ver [catálogo de eventos](../../docs/events/README.md).
- **Salida:** HTTPS hacia FCM (asíncrona).

## Historias asociadas

Épica móvil 3 (F3.1–F3.3).

## Desarrollo

```
go run ./services/notifications/cmd/notifications
```
