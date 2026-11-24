package domain_test

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
)

// HU-M2.4-01 (criterion 3): the automatic resolution of a parametric claim.

var (
	observedAt = time.Date(2026, 9, 11, 21, 39, 0, 0, time.UTC)
	resolvedAt = time.Date(2026, 9, 11, 21, 39, 30, 0, time.UTC)
)

// The flight of the prototype: AV-8421, 215 minutes late.
func lateFlight() domain.Observation {
	return domain.Observation{ID: "obs-1", Subject: "sub-a", PolicyID: "VJ-2026-000931",
		Parameter: domain.IncidentFlightDelay, EventReference: "AV-8421", Observed: 215, Unit: "minutes",
		ObservedAt: observedAt}
}

func travelTerms() domain.PolicyTerms {
	return domain.PolicyTerms{
		Policy: domain.Policy{ID: "VJ-2026-000931", Line: domain.LineTravel, Product: "Viaje",
			Status: domain.PolicyActive, ValidFrom: observedAt.AddDate(0, -1, 0), ValidTo: observedAt.AddDate(0, 11, 0),
			CoveredIncidents: []domain.IncidentType{domain.IncidentFlightDelay}},
		Coverages: []domain.Coverage{{Incident: domain.IncidentFlightDelay, InsuredValueMinor: 500_000_000,
			Currency: "COP", DeductibleBasisPoints: 1000}},
	}
}

func TestAFlightPastTheThresholdIsFiledApprovedAndDue(t *testing.T) {
	c, r, d, err := domain.ResolveParametric("cl-1", lateFlight(), domain.DefaultParametricRules(), travelTerms(),
		carolina, resolvedAt)
	require.NoError(t, err)

	assert.Equal(t, "cl-1", c.ID)
	assert.Equal(t, "sub-a", c.Subject)
	assert.Equal(t, domain.ChannelParametric, c.Channel)
	assert.Equal(t, domain.StatusInEvaluation, c.Status)
	assert.Equal(t, domain.IncidentFlightDelay, c.IncidentType)
	assert.Equal(t, "parametric-obs-1", c.FilingKey)
	assert.Equal(t, "AV-8421", c.Place)
	assert.Equal(t, "Resolución automática: retraso del vuelo AV-8421, 215 minutes (umbral 180 minutes).", c.Description)
	assert.Equal(t, observedAt, c.OccurredAt)
	require.NotNil(t, c.Policy)
	assert.Equal(t, "VJ-2026-000931", c.Policy.ID)

	assert.Equal(t, domain.ParametricResolution{ObservationID: "obs-1", RuleVersion: domain.ParametricRulesVersion,
		ResolvedAt: resolvedAt, Trigger: domain.Trigger{Parameter: domain.IncidentFlightDelay, EventReference: "AV-8421",
			Threshold: 180, Observed: 215, Unit: "minutes", ObservedAt: observedAt}}, r)

	// The rule pays $ 180.000 with no deductible, whatever the coverage says.
	assert.Equal(t, domain.OutcomeApproved, d.Outcome)
	assert.Equal(t, domain.ParametricEngine, d.DecidedBy)
	assert.Equal(t, int64(18_000_000), d.GrossAmountMinor)
	assert.Zero(t, d.DeductibleBasisPoints)
	require.NotNil(t, d.Payout)
	assert.Equal(t, int64(18_000_000), d.Payout.AmountMinor)
	assert.Equal(t, "COP", d.Payout.Currency)
	assert.Equal(t, domain.PayoutKey("cl-1"), d.Payout.Key)
	assert.Equal(t, idempotency.StatusPending, d.Payout.Status)
	assert.Equal(t, resolvedAt, d.Payout.NextAttemptAt)
	assert.Equal(t, carolina.PayoutAccount, d.Payout.Destination)
}

func TestTheThresholdItselfPays(t *testing.T) {
	o := lateFlight()
	o.Observed = 180
	_, _, _, err := domain.ResolveParametric("cl-1", o, domain.DefaultParametricRules(), travelTerms(), carolina, resolvedAt)
	assert.NoError(t, err)
}

func TestThePayoutIsCappedByTheInsuredValue(t *testing.T) {
	terms := travelTerms()
	terms.Coverages[0].InsuredValueMinor = 10_000_000
	_, _, d, err := domain.ResolveParametric("cl-1", lateFlight(), domain.DefaultParametricRules(), terms, carolina, resolvedAt)
	require.NoError(t, err)
	assert.Equal(t, int64(10_000_000), d.Payout.AmountMinor)
}

func TestObservationsThatDoNotPay(t *testing.T) {
	below := lateFlight()
	below.Observed = 179
	otherUnit := lateFlight()
	otherUnit.Unit = "hours"
	unknown := lateFlight()
	unknown.Parameter = "rainfall"
	expired := travelTerms()
	expired.Policy.ValidTo = observedAt
	uncovered := travelTerms()
	uncovered.Coverages = nil
	noAccount := carolina
	noAccount.PayoutAccount = domain.PayoutAccount{}

	for name, tc := range map[string]struct {
		o      domain.Observation
		terms  domain.PolicyTerms
		holder domain.Holder
		want   error
	}{
		"below the threshold":           {below, travelTerms(), carolina, domain.ErrBelowThreshold},
		"another unit":                  {otherUnit, travelTerms(), carolina, domain.ErrParameterNotCovered},
		"a parameter without rule":      {unknown, travelTerms(), carolina, domain.ErrParameterNotCovered},
		"a policy not in force":         {lateFlight(), expired, carolina, domain.ErrPolicyNotActive},
		"a coverage the policy has not": {lateFlight(), uncovered, carolina, domain.ErrParameterNotCovered},
		"an insured without account":    {lateFlight(), travelTerms(), noAccount, domain.ErrNoPayoutAccount},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, _, err := domain.ResolveParametric("cl-1", tc.o, domain.DefaultParametricRules(), tc.terms, tc.holder,
				resolvedAt)
			assert.ErrorIs(t, err, tc.want)
		})
	}
}

func TestAMalformedObservationIsInvalid(t *testing.T) {
	for name, change := range map[string]func(*domain.Observation){
		"no id":              func(o *domain.Observation) { o.ID = "" },
		"no subject":         func(o *domain.Observation) { o.Subject = " " },
		"no policy":          func(o *domain.Observation) { o.PolicyID = "" },
		"no event reference": func(o *domain.Observation) { o.EventReference = "" },
		"no instant":         func(o *domain.Observation) { o.ObservedAt = time.Time{} },
		"a negative value":   func(o *domain.Observation) { o.Observed = -1 },
		"not a number":       func(o *domain.Observation) { o.Observed = math.NaN() },
	} {
		t.Run(name, func(t *testing.T) {
			o := lateFlight()
			change(&o)
			_, _, _, err := domain.ResolveParametric("cl-1", o, domain.DefaultParametricRules(), travelTerms(), carolina,
				resolvedAt)
			assert.ErrorIs(t, err, domain.ErrInvalidObservation)
		})
	}
}

func TestAParametricClaimAsksForNoSupports(t *testing.T) {
	c, _, _, err := domain.ResolveParametric("cl-1", lateFlight(), domain.DefaultParametricRules(), travelTerms(),
		carolina, resolvedAt)
	require.NoError(t, err)
	assert.Empty(t, domain.RequirementsOf(c))
}
