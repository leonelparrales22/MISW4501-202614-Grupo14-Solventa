package orchestrator

import (
	"context"
	"log/slog"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// Outcomes of an underwriting decision as audit stores them (HU-W4.1-01).
const (
	decisionAccepted = "accepted"
	decisionDeferred = "deferred"

	// reasonProvisional is the reason of a deferred offer whose provisional
	// reason is unknown.
	reasonProvisional = "provisional_offer"
)

// decisionOf maps a priced offer to the record audit keeps. A normal offer is
// an accepted risk; a provisional one is deferred until the source that was
// down comes back, and says why.
func decisionOf(o domain.PersonalizedOffer) ports.DecisionRecord {
	r := ports.DecisionRecord{
		OfferID: o.ID, CustomerID: o.CustomerID, PartnerID: o.PartnerID, ApplicationID: o.ApplicationID,
		Channel: string(o.Channel), RequestedAt: o.CreatedAt.UTC(), RaterVersion: o.RaterVersion,
		ProfileScore: o.Profile.Score, ProfileCategory: o.Profile.Category, ProfilePartial: o.Profile.Partial,
		Premium: o.Premium, Currency: o.Currency, Decision: decisionAccepted,
	}
	if o.Mode == domain.ModeProvisional {
		r.Decision = decisionDeferred
		r.DecisionReason = o.ProvisionalReason
		if r.DecisionReason == "" {
			r.DecisionReason = reasonProvisional
		}
	}
	for _, s := range o.Profile.Sources {
		r.Sources = append(r.Sources, ports.DecisionSource{Source: s.Source, Status: string(s.Status), LatencyMS: s.Latency.Milliseconds()})
	}
	for i, rule := range o.Rules {
		r.Rules = append(r.Rules, ports.DecisionRule{Order: i + 1, Code: rule.Code, Value: rule.Value})
	}
	return r
}

// recordDecision hands the decision to audit. The customer's answer does not
// wait on a failure here: it is logged for the operator to replay, because the
// offer is already stored and audit records idempotently by offer id. A nil
// recorder (no AUDIT_URL) records nothing.
func recordDecision(ctx context.Context, recorder ports.DecisionRecorder, o domain.PersonalizedOffer) {
	if recorder == nil {
		return
	}
	if err := recorder.Record(ctx, decisionOf(o)); err != nil {
		slog.ErrorContext(ctx, "could not record the underwriting decision in audit", "offer_id", o.ID, "error", err)
	}
}
