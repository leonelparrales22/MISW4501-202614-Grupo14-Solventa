package memory

import (
	"context"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// Demo values of the activation the stub simulates: the real ones come from
// the quote and the charge of HU-W1.3-02.
const (
	// DemoInsuredAmount is the insured amount of the demo policies, in COP.
	DemoInsuredAmount = 250_000_000
	// DemoTermMonths is the coverage period of the demo policies.
	DemoTermMonths = 120
)

// ActivationStub is a ports.IssuanceStarter that stands in for the charge of
// the premium and the activation of the coverage: when an accepted risk
// starts issuance it publishes the CoverageActivated event, in process, to the
// handler of the contractual document (HU-W1.3-03). It is idempotent by
// acceptance because the event id and the policy id derive from it.
// PENDING: HU-W1.3-02 and HA-D02 publish the real event after the charge.
type ActivationStub struct {
	inner   *IssuanceStub
	handler ports.CoverageActivationHandler
	clock   ports.Clock
}

// NewActivationStub builds an ActivationStub.
func NewActivationStub(handler ports.CoverageActivationHandler, clock ports.Clock) *ActivationStub {
	return &ActivationStub{inner: NewIssuanceStub(), handler: handler, clock: clock}
}

// Start records the acceptance and publishes the activation of its coverage.
func (s *ActivationStub) Start(ctx context.Context, a domain.Acceptance, offer domain.Offer) error {
	if err := s.inner.Start(ctx, a, offer); err != nil {
		return err
	}
	now := s.clock.Now().UTC()
	return s.handler.Receive(ctx, domain.CoverageActivated{
		EventID:       "evt-coverage-activated-" + a.ID,
		PolicyID:      "pol-" + a.ID,
		AcceptanceID:  a.ID,
		CustomerID:    offer.CustomerID,
		Holder:        offer.Applicant,
		Premium:       offer.Premium,
		Currency:      offer.Currency,
		InsuredAmount: DemoInsuredAmount,
		Coverages: []domain.Coverage{
			{Code: domain.CoverageDeath, InsuredAmount: DemoInsuredAmount},
			{Code: domain.CoverageDisability, InsuredAmount: DemoInsuredAmount},
		},
		ActivatedAt:   now,
		CoverageStart: now,
		CoverageEnd:   now.AddDate(0, DemoTermMonths, 0),
	})
}

// Started tells whether issuance started for the acceptance.
func (s *ActivationStub) Started(acceptanceID string) bool { return s.inner.Started(acceptanceID) }
