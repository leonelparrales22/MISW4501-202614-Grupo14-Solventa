package domain_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
)

// The provisional assignment policy (HU-W2.3-01, decision D6, assumption S2).

func TestTheSpecialtyOfEveryClaimTypeOfTheCatalog(t *testing.T) {
	p := domain.AssignmentPolicyV1()
	want := map[domain.IncidentType]domain.Specialty{
		domain.IncidentCollision:        domain.SpecialtyVehicleAssessment,
		domain.IncidentTheft:            "",
		domain.IncidentThirdPartyDamage: domain.SpecialtyVehicleAssessment,
		domain.IncidentGlass:            domain.SpecialtyAutoGlass,
		domain.IncidentOther:            domain.SpecialtyVehicleAssessment,
		domain.IncidentWaterDamage:      domain.SpecialtyPlumbing,
	}
	for _, o := range domain.CatalogV1().Incidents {
		specialty, known := p.SpecialtyOf(o.Line, o.Type)
		assert.True(t, known, o.Type)
		assert.Equal(t, want[o.Type], specialty, o.Type)
	}

	specialty, known := p.SpecialtyOf(domain.LineHome, "fire")
	assert.False(t, known)
	assert.Empty(t, specialty, "an unknown type needs no visit")

	_, known = p.SpecialtyOf(domain.LineHome, domain.IncidentCollision)
	assert.False(t, known, "the type is known on its own line only")
}

func TestTheSpecialtyOfAClaimComesFromItsCatalog(t *testing.T) {
	p := domain.AssignmentPolicyV1()
	line, specialty, known := p.SpecialtyFor(claimOf(domain.IncidentWaterDamage))
	assert.True(t, known)
	assert.Equal(t, domain.LineHome, line)
	assert.Equal(t, domain.SpecialtyPlumbing, specialty)

	mobile := claimOf(domain.IncidentCollision)
	mobile.CatalogVersion = ""
	line, specialty, _ = p.SpecialtyFor(mobile)
	assert.Equal(t, domain.LineAuto, line, "a claim of the phone has no catalog version")
	assert.Equal(t, domain.SpecialtyVehicleAssessment, specialty)

	_, specialty, known = p.SpecialtyFor(claimOf("fire"))
	assert.False(t, known)
	assert.Empty(t, specialty)
}

func TestTheRadiiOfTheSearch(t *testing.T) {
	p := domain.AssignmentPolicyV1()
	assert.Equal(t, 5.0, p.InitialRadiusKm())
	assert.Equal(t, 20.0, p.MaxRadiusKm())

	cases := []struct {
		from float64
		want []float64
		ok   bool
	}{
		{5, []float64{5, 10, 20}, true},
		{10, []float64{10, 20}, true},
		{20, []float64{20}, true},
		{0, nil, false},
		{7.5, nil, false},
		{40, nil, false},
	}
	for _, c := range cases {
		ladder, ok := p.Ladder(c.from)
		assert.Equal(t, c.ok, ok, c.from)
		assert.Equal(t, c.want, ladder, c.from)
	}
}

func TestEachCallReturnsItsOwnAssignmentPolicy(t *testing.T) {
	p := domain.AssignmentPolicyV1()
	p.Specialties[0].Specialty = domain.SpecialtyPainting
	p.RadiiKm[0] = 1
	ladder, _ := p.Ladder(1)
	ladder[0] = 99

	fresh := domain.AssignmentPolicyV1()
	assert.Equal(t, domain.SpecialtyVehicleAssessment, fresh.Specialties[0].Specialty)
	assert.Equal(t, []float64{5, 10, 20}, fresh.RadiiKm)
	assert.Equal(t, 1.0, p.RadiiKm[0], "the ladder is a copy")
	assert.Equal(t, domain.AssignmentPolicyVersionV1, fresh.Version)
}
