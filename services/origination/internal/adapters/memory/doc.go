// Package memory holds in-memory adapters that stand in for the parts of the
// acceptance journey (HU-W1.3-01) that do not exist yet. Everything here is
// lost on restart.
//
// PENDING replacement:
//
//   - OfferStub: the demonstration offers (demo-*), which stay in memory
//     (fix C5, #77 B). It never creates one for an advisor or an operations
//     analyst: the staff never own an offer (decision #75). Without
//     DATABASE_URL it also stores the offers an advisor adjusts (HU-W1.1-02).
//   - UnderwriterStub: the real automatic underwriting engine.
//   - IssuanceStub: the charge and issuance flow of HU-W1.3-02 and HA-D02, when
//     no payment gateway is configured.
//   - QuoteStore: the PostgreSQL repository of the advisor quote (HU-W1.1-01).
//   - ContractStore, IssuanceAuditLog: the PostgreSQL repository and the audit
//     service for the contractual issuance (HU-W1.3-03).
//
// Used only without DATABASE_URL (fix C5, decision D-2 a: with it, all of
// them are in PostgreSQL together, in internal/adapters/postgres):
//
//   - PersonalizedOfferStore: the personalized offers of HU-W1.2-03.
//   - AcceptanceStore: the acceptances of HU-W1.3-01.
//   - ReviewQueueStub: the inbox of HU-W1.1-02: the requests routed to a human
//     decision with the snapshot of their offer, the append-only advisor
//     decisions and the state of the customer notices.
//   - CustomerStore: the customers of HU-W1.1-01 and HU-M1.1-01.
package memory
