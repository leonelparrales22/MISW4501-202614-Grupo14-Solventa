# Módulo `messaging` — SNS/SQS con dead-letter queues (T4)

Un tópico SNS por dominio y una cola SQS por consumidor, cada una con su **DLQ**: un mensaje que falla `max_receive_count` veces
(5 por defecto) pasa a `<cola>-dlq`, donde espera 14 días a un operador en lugar de bloquear la cola. Ver el
[catálogo de eventos](../../../../docs/events/README.md).

- Tópicos cifrados con `alias/aws/sns`; colas con SSE-SQS y *long polling* de 20 s.
- Cada cola acepta mensajes solo de los tópicos a los que se suscribe (política con `aws:SourceArn`); cada DLQ solo admite
  *redrive* desde su cola.
- Suscripciones con `raw_message_delivery` y `filter_policy` opcional por cola.

| Variable | Defecto | Descripción |
|---|---|---|
| `name` | — | Prefijo |
| `topics` | — | Dominios publicadores |
| `queues` | — | Por consumidor: `topics`, `max_receive_count` (5), `visibility_timeout_seconds` (30), `filter_policy` |
| `message_retention_seconds` / `dlq_retention_seconds` | 4 días / 14 días | — |

Salidas: `topic_arns`, `queue_urls`, `queue_arns` y `dlq_urls`.

## Pruebas (locales, sin AWS)

```bash
terraform init -backend=false
terraform test
```

Usan un proveedor simulado: no necesitan credenciales y no crean nada.
