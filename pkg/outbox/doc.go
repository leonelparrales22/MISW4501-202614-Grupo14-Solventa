// Package outbox is the transactional outbox of the services (docs/events):
// the event is stored together with the business operation and a relay
// publishes it afterwards, so an event is never lost when the broker is down
// and never published for an operation that was rolled back.
//
// How a service uses it:
//
//  1. A table of its own, in its own database (one database per service,
//     AGENTS.md §2), with one row per event: the encoded envelope (pkg/events),
//     the attempts, when the next attempt is due, the lease of the current
//     claim and when it was published.
//  2. The business operation writes the row in the same transaction as the
//     change it describes, with the deterministic event id as the primary key
//     (writing the same fact twice inserts nothing).
//  3. A Store over that table. Its Claim must be a single atomic statement
//     with a lease (for PostgreSQL, an UPDATE over a SELECT … FOR UPDATE SKIP
//     LOCKED): several tasks of the service run a relay each, and a message
//     whose lease is live is never handed to another caller. MarkPublished and
//     Reschedule apply only while the claim is still the same one; otherwise
//     they return ErrClaimLost. This is the same contract as
//     idempotency.Store (pkg/README.md).
//  4. One Relay per process, started from cmd/<service>/main.go.
//
// Delivery is at-least-once: a relay that dies after publishing and before
// marking the row publishes it again once the lease expires, so consumers are
// idempotent. Failed publications are retried with a growing wait and no
// window: an event is never discarded. An envelope that cannot be decoded is
// rescheduled with its cause and logged; it never blocks the other events.
package outbox
