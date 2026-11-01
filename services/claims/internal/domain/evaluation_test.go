package domain_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
)

func TestTheAccountShowsOnlyItsLastFourDigits(t *testing.T) {
	for number, want := range map[string]string{
		"0123-4567-12": "•••• 6712",
		"0123456789":   "•••• 6789",
		"12":           "•••• 12",
		"":             "",
		"sin dígitos":  "",
	} {
		assert.Equal(t, want, domain.PayoutAccount{AccountNumber: number}.MaskedNumber(), number)
	}
}

func TestTheCoverageOfTheIncident(t *testing.T) {
	agua := domain.Coverage{Incident: domain.IncidentWaterDamage, InsuredValueMinor: 1}
	terms := domain.PolicyTerms{Coverages: []domain.Coverage{{Incident: domain.IncidentCollision}, agua}}

	got, ok := terms.CoverageOf(domain.IncidentWaterDamage)
	assert.True(t, ok)
	assert.Equal(t, agua, got)

	_, ok = terms.CoverageOf(domain.IncidentTheft)
	assert.False(t, ok)
}
