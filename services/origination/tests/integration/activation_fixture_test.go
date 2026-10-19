package integration_test

import (
	"context"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// Demo coverage terms of the fixture: the ones origination activates for the
// demo and adjusted offers (a personalized offer of HU-W1.2-03 activates its
// own, decision D-16 of HA-D02).
const (
	fixtureInsuredAmount = 250_000_000
	fixtureTermMonths    = 120
)

// activationFixture is a ports.IssuanceStarter for the tests of the
// contractual document (HU-W1.3-03): when an accepted risk starts issuance it
// publishes, in process, the CoverageActivated event that the premium charge
// of HA-D02 publishes once the payment is confirmed. It lets those tests run
// without a payment gateway or a database, as the INVEST review of HU-W1.3-03
// foresees ("publishing that event from a fixture").
type activationFixture struct {
	handler ports.CoverageActivationHandler
	clock   ports.Clock
}

var _ ports.IssuanceStarter = (*activationFixture)(nil)

func newActivationFixture(handler ports.CoverageActivationHandler, clock ports.Clock) *activationFixture {
	return &activationFixture{handler: handler, clock: clock}
}

// Start publishes the activation of the acceptance's coverage. The event and
// policy ids derive from the acceptance, so a repeated start is recognized.
func (f *activationFixture) Start(ctx context.Context, a domain.Acceptance, offer domain.Offer) error {
	now := f.clock.Now().UTC()
	return f.handler.Receive(ctx, domain.CoverageActivated{
		EventID:       domain.ActivationEventIDFor(a.ID),
		PolicyID:      domain.PolicyIDFor(a.ID),
		AcceptanceID:  a.ID,
		CustomerID:    offer.CustomerID,
		Holder:        offer.Applicant,
		Premium:       offer.Premium,
		Currency:      offer.Currency,
		InsuredAmount: fixtureInsuredAmount,
		Coverages: []domain.Coverage{
			{Code: domain.CoverageDeath, InsuredAmount: fixtureInsuredAmount},
			{Code: domain.CoverageDisability, InsuredAmount: fixtureInsuredAmount},
		},
		ActivatedAt:   now,
		CoverageStart: now,
		CoverageEnd:   now.AddDate(0, fixtureTermMonths, 0),
	})
}
