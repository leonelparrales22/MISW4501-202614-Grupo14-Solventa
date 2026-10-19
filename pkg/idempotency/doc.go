// Package idempotency runs an operation against an external provider so that
// it takes effect exactly once, even when the provider is down, answers
// ambiguously or several workers race for the same operation. It is the
// mechanism of ADR-01, validated by experiment EC-D02 (HA-D02), and it is
// meant to be reused: origination charges the premium with it and claims will
// pay indemnities with it (HU-W2.2-03).
//
// The pieces:
//
//   - Key builds the deterministic idempotency key of an operation (for the
//     premium, "cobro-" + acceptance id); it never changes between retries.
//   - Store is implemented by each service over its own database (one
//     database per service, AGENTS.md §2). Its Claim must be a single atomic
//     conditional statement: only one caller moves an attempt to in_progress,
//     and only that caller calls the provider. A unique key alone is not
//     enough (EC-D02 §3.5).
//   - Runner claims the attempt, runs the call and classifies its result: a
//     success confirms it; an explicit rejection (ErrRejected) fails it; an
//     ambiguous answer (ErrAmbiguous) or a call that was never sent
//     (ErrNotSent) reschedules it with a growing wait and the same key until
//     the retry window ends, when it fails with ReasonWindowExpired. An
//     attempt never stays in limbo: a claim has a lease, so an attempt whose
//     worker died is claimed again once the lease expires.
//
// The guarantee in the ambiguous case depends on the provider honoring the
// idempotency key: a retry after an answer that never arrived must not charge
// twice. That requirement is part of the contract of any provider used with
// this package.
//
// The package has no database and no HTTP code: services bring their store
// and their provider client.
package idempotency
