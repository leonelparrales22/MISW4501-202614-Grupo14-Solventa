package domain_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
)

// The assistance policy of HU-W2.3-03 (assumptions S4, S8 and S10).

func TestTheNeedsOfEveryLineAndTheSpecialtyTheyAskFor(t *testing.T) {
	p := domain.AssistanceInboxPolicyV1()
	assert.Equal(t, "assistance-policy-v1", p.Version)

	cases := []struct {
		line domain.PolicyLine
		need domain.AssistanceNeed
		want domain.Specialty
	}{
		{domain.LineAuto, domain.NeedTowing, domain.SpecialtyTowing},
		{domain.LineAuto, domain.NeedBattery, domain.SpecialtyRoadsideAssistance},
		{domain.LineAuto, domain.NeedFlatTire, domain.SpecialtyRoadsideAssistance},
		{domain.LineAuto, domain.NeedFuel, domain.SpecialtyRoadsideAssistance},
		{domain.LineAuto, domain.NeedVehicleLockout, domain.SpecialtyRoadsideAssistance},
		{domain.LineHome, domain.NeedPlumbingEmergency, domain.SpecialtyPlumbing},
	}
	for _, c := range cases {
		got, err := p.SpecialtyOf(c.line, c.need)
		require.NoError(t, err, c.need)
		assert.Equal(t, c.want, got, c.need)
		assert.True(t, p.Known(c.need), c.need)
		assert.True(t, got.Valid(), "%s is a specialty of the registry", got)
	}
	assert.Len(t, p.Needs, len(cases), "no need left untested")

	for _, wrong := range []struct {
		line domain.PolicyLine
		need domain.AssistanceNeed
	}{
		{domain.LineHome, domain.NeedTowing},
		{domain.LineAuto, domain.NeedPlumbingEmergency},
		{domain.LineTravel, domain.NeedBattery},
		{domain.LineTravel, domain.NeedPlumbingEmergency},
	} {
		_, err := p.SpecialtyOf(wrong.line, wrong.need)
		assert.ErrorIs(t, err, domain.ErrNeedNotForLine, "%s of %s", wrong.need, wrong.line)
	}
	assert.False(t, p.Known("ambulance"))
	assert.False(t, p.Known(""))
}

func TestTheAssistancePolicyIsAFreshCopyEachTime(t *testing.T) {
	p := domain.AssistanceInboxPolicyV1()
	p.Needs[0].Specialty = domain.SpecialtyPainting
	p.CloseReasons[0] = "changed"
	q := domain.AssistanceInboxPolicyV1()
	assert.Equal(t, domain.SpecialtyTowing, q.Needs[0].Specialty)
	assert.Equal(t, domain.CloseResolvedByPhone, q.CloseReasons[0])
	assert.Equal(t, 5*time.Minute, q.DispatchTarget, "objetivo: menos de 5 min")
}

func TestTheEstimatedArrivalAtItsLimits(t *testing.T) {
	p := domain.AssistanceInboxPolicyV1()
	cases := []struct {
		km   float64
		want int
	}{
		{0, 10},     // the preparation only
		{0.1, 11},   // any distance takes a minute more
		{0.5, 11},   // exactly one minute at 30 km/h
		{0.51, 12},  // a little more is the next minute
		{0.88, 12},  // the tow truck of the mockup
		{5, 20},     // the initial radius
		{9.86, 30},  // within the second radius
		{20, 50},    // the maximum radius
		{20.01, 51}, // rounded up
	}
	for _, c := range cases {
		assert.Equal(t, c.want, p.EtaMinutes(c.km), "%.2f km", c.km)
	}
}

func TestTheReasonsToCloseARequest(t *testing.T) {
	p := domain.AssistanceInboxPolicyV1()
	for _, r := range []domain.CloseReason{domain.CloseResolvedByPhone, domain.CloseDuplicate,
		domain.CloseCancelledByInsured, domain.CloseNotCovered, domain.CloseNoProviderAvailable, domain.CloseOther} {
		assert.True(t, p.Closes(r), r)
	}
	assert.Len(t, p.CloseReasons, 6)
	assert.False(t, p.Closes("dispatched"))
	assert.False(t, p.Closes(""))
}

func TestTheSpecialtiesOfTheAssistanceAreOfTheRegistry(t *testing.T) {
	for _, s := range domain.AssistanceSpecialties() {
		assert.True(t, s.Valid(), s)
		assert.NotContains(t, domain.Specialties(), s, "the claims do not ask for %s", s)
	}
	assert.Equal(t, []domain.Specialty{domain.SpecialtyTowing, domain.SpecialtyRoadsideAssistance},
		domain.AssistanceSpecialties())
}
