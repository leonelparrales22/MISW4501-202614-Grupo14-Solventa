// Package memory holds in-memory adapters that stand in for the parts of the
// acceptance journey (HU-W1.3-01) that do not exist yet. Everything here is
// lost on restart and is PENDING replacement:
//
//   - AcceptanceStore: the PostgreSQL acceptance repository of origination.
//   - OfferStub: the offers generated and persisted by HU-W1.2-03.
//   - UnderwriterStub: the real automatic underwriting engine.
//   - ReviewQueueStub: the advisor inbox of HU-W1.1-02.
//   - IssuanceStub: the charge and issuance flow of HU-W1.3-02 and HA-D02.
package memory
