package domain_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
)

// The policy checks of the web filing (HU-W2.1-01 criterion 3).

var (
	policyFrom = time.Date(2025, 11, 1, 5, 0, 0, 0, time.UTC)
	policyTo   = time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC)
)

func autoPolicy() domain.Policy {
	return domain.Policy{
		ID: "AU-2025-118204", Line: domain.LineAuto, Product: "Autos", InsuredObject: "KJT-482",
		Status: domain.PolicyActive, ValidFrom: policyFrom, ValidTo: policyTo,
		CoveredIncidents: []domain.IncidentType{
			domain.IncidentCollision, domain.IncidentTheft, domain.IncidentThirdPartyDamage, domain.IncidentOther,
		},
	}
}

func TestThePolicyIsActiveWithinItsHalfOpenValidity(t *testing.T) {
	p := autoPolicy()
	tests := []struct {
		name string
		at   time.Time
		want bool
	}{
		{"one nanosecond before it starts", policyFrom.Add(-time.Nanosecond), false},
		{"the instant it starts", policyFrom, true},
		{"in the middle", now, true},
		{"one nanosecond before it ends", policyTo.Add(-time.Nanosecond), true},
		{"the instant it ends", policyTo, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, p.ActiveAt(tt.at))
			assert.Equal(t, tt.want, p.Within(tt.at))
		})
	}
}

func TestAPolicyThatIsNotActiveTakesNoClaim(t *testing.T) {
	for _, status := range []domain.PolicyStatus{"expired", "cancelled", "suspended", ""} {
		p := autoPolicy()
		p.Status = status

		assert.False(t, p.ActiveAt(now), status)
		assert.ErrorIs(t, p.EligibleFor(domain.CatalogV1(), domain.IncidentCollision, now, now), domain.ErrPolicyNotActive)
	}
}

func TestThePolicyCoversOnlyWhatItListsUnderItsLine(t *testing.T) {
	catalog := domain.CatalogV1()
	p := autoPolicy()

	assert.True(t, p.Covers(catalog, domain.IncidentCollision))
	assert.False(t, p.Covers(catalog, domain.IncidentGlass), "the core does not list it")

	p.CoveredIncidents = append(p.CoveredIncidents, domain.IncidentWaterDamage, "flight_delay")
	assert.False(t, p.Covers(catalog, domain.IncidentWaterDamage), "water damage is filed under the home line")
	assert.False(t, p.Covers(catalog, "flight_delay"), "not in the catalog")
}

func TestEligibility(t *testing.T) {
	catalog := domain.CatalogV1()
	expired := autoPolicy()
	expired.Status = "expired"

	tests := []struct {
		name       string
		policy     domain.Policy
		incident   domain.IncidentType
		occurredAt time.Time
		want       error
	}{
		{"eligible", autoPolicy(), domain.IncidentCollision, now.Add(-time.Hour), nil},
		{"incident the instant the policy starts", autoPolicy(), domain.IncidentCollision, policyFrom, nil},
		{"incident one nanosecond before it starts", autoPolicy(), domain.IncidentCollision, policyFrom.Add(-time.Nanosecond), domain.ErrOccurredOutsideValidity},
		{"type not covered", autoPolicy(), domain.IncidentGlass, now.Add(-time.Hour), domain.ErrIncidentNotCovered},
		{"not active wins over not covered", expired, domain.IncidentGlass, now.Add(-time.Hour), domain.ErrPolicyNotActive},
		{"not covered wins over the validity", autoPolicy(), domain.IncidentGlass, policyFrom.Add(-time.Hour), domain.ErrIncidentNotCovered},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.policy.EligibleFor(catalog, tt.incident, tt.occurredAt, now)
			if tt.want == nil {
				assert.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, tt.want)
		})
	}
}

func TestAnIncidentAtTheEndOfTheValidityIsOutsideIt(t *testing.T) {
	p := autoPolicy()
	p.ValidTo = now.Add(time.Hour)

	assert.NoError(t, p.EligibleFor(domain.CatalogV1(), domain.IncidentCollision, now.Add(-time.Nanosecond), now))
	assert.ErrorIs(t, p.EligibleFor(domain.CatalogV1(), domain.IncidentCollision, p.ValidTo, now),
		domain.ErrOccurredOutsideValidity)
}

func TestTheCopyOfAPolicySharesNothing(t *testing.T) {
	p := autoPolicy()
	c := p.Copy()
	c.CoveredIncidents[0] = domain.IncidentGlass

	assert.Equal(t, domain.IncidentCollision, p.CoveredIncidents[0])
}
