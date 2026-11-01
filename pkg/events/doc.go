// Package events holds the contract of the domain events that services
// exchange over SNS and SQS, and the pieces to publish and consume them.
//
// The rules every service follows (docs/events/README.md, AGENTS.md §2.1):
//
//   - The emitter never publishes inside its business transaction: it writes
//     the event to its own transactional outbox (pkg/outbox) in the same
//     transaction as the operation, and a relay publishes it afterwards. An
//     event is therefore never lost when the broker is down, nor published
//     for an operation that was rolled back.
//   - Delivery is at-least-once and without a strict order: the same event can
//     arrive twice and two events can arrive in any order. Consumers are
//     idempotent, keyed by what the event is about (for example the subject of
//     the account in CustomerLinked), never by the arrival order.
//   - A message that cannot be processed is not deleted: it reappears when its
//     visibility timeout ends and, after the queue's maxReceiveCount, it lands
//     in the dead-letter queue of that queue (infra/terraform/modules/messaging).
//   - Payloads are minimal (personal data protection): an event carries the
//     identifiers the consumers need, never names, e-mails or documents beyond
//     what the contract of the event lists.
//
// The pieces:
//
//   - Envelope is the JSON every event travels in: id, type, version, source,
//     instant, optional correlation id and the payload object.
//   - Publisher publishes an envelope; snspublisher implements it over SNS.
//   - Handler handles one envelope; Router dispatches by event type and
//     answers ErrUnhandledType for a type nobody consumes.
//   - sqsconsumer polls a queue and hands each message to a Handler.
package events
