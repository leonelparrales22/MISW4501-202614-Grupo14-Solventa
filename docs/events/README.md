# Catálogo de eventos

Los servicios publican hechos de negocio en Amazon SNS (un tópico por dominio) y cada consumidor tiene su cola SQS con DLQ. Reglas: publicación con *transactional outbox*, consumidores idempotentes, entrega *at-least-once* sin orden estricto.

| Evento | Nombre en la Wiki | Publica | Consume | Propósito |
|---|---|---|---|---|
| `PolicyIssued` | `PolizaEmitida` | origination | audit, notifications | Póliza emitida tras aceptación y cobro |
| `ChargeCompleted` | `CobroRealizado` | origination | audit | Cobro confirmado (idempotente) |
| `ClaimApproved` | `SiniestroAprobado` | claims | notifications, audit | Dispara notificación y pago paramétrico |

**HU-W1.3-01 (aceptación de la oferta) no publica ni consume eventos:** la aceptación se persiste en origination y la suscripción, el envío a la bandeja del asesor (HU-W1.1-02) y el inicio de la emisión ocurren en proceso, con adaptadores en memoria. `PolicyIssued` y `ChargeCompleted` siguen pendientes de HU-W1.3-02 / HA-D02. Si la bandeja o la auditoría de la decisión de suscripción pasan a otro servicio, el evento correspondiente se registrará aquí.

Los esquemas de cada evento se agregarán aquí al implementarlos. Los consumidores exactos pueden cambiar; confirmar en la Wiki ([Arquitectura final](https://github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/wiki/Arquitectura_final)).
