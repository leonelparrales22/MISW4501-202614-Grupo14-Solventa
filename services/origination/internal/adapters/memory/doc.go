// Package memory holds in-memory adapters that stand in for the parts of the
// acceptance journey (HU-W1.3-01) that do not exist yet. Everything here is
// lost on restart and is PENDING replacement:
//
//   - AcceptanceStore: the PostgreSQL acceptance repository of origination.
//   - OfferStub: the offers generated and persisted by HU-W1.2-03 (it also
//     stores the offers an advisor adjusts, HU-W1.1-02).
//   - UnderwriterStub: the real automatic underwriting engine.
//   - ReviewQueueStub: the PostgreSQL inbox of HU-W1.1-02: the requests routed
//     to a human decision, the append-only advisor decisions and the state of
//     the customer notices.
//   - IssuanceStub: the charge and issuance flow of HU-W1.3-02 and HA-D02.
//   - CustomerStore, QuoteStore: the PostgreSQL repositories of the advisor
//     quote (HU-W1.1-01).
//   - ContractStore, IssuanceAuditLog: the PostgreSQL repository and the audit
//     service for the contractual issuance (HU-W1.3-03).
package memory
