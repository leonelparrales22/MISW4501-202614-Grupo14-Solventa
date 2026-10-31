package domain_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
)

// The provisional triage policy (HU-W2.2-01, decision D5, assumptions S1 to
// S3).

func TestTheSeverityOfEveryClaimTypeOfTheCatalog(t *testing.T) {
	p := domain.TriagePolicyV1()
	want := map[domain.IncidentType]domain.Severity{
		domain.IncidentCollision:        domain.SeverityMedium,
		domain.IncidentTheft:            domain.SeverityHigh,
		domain.IncidentThirdPartyDamage: domain.SeverityHigh,
		domain.IncidentGlass:            domain.SeverityLow,
		domain.IncidentOther:            domain.SeverityMedium,
		domain.IncidentWaterDamage:      domain.SeverityMedium,
	}
	for _, o := range domain.CatalogV1().Incidents {
		severity, known := p.SeverityOf(o.Line, o.Type)
		assert.True(t, known, o.Type)
		assert.Equal(t, want[o.Type], severity, o.Type)
	}

	severity, known := p.SeverityOf(domain.LineHome, "fire")
	assert.False(t, known)
	assert.Equal(t, domain.SeverityMedium, severity, "an unknown type is medium")

	_, known = p.SeverityOf(domain.LineHome, domain.IncidentTheft)
	assert.False(t, known, "the type is known on its own line only")
}

func TestTheAmountBandAtItsLimits(t *testing.T) {
	p := domain.TriagePolicyV1()
	cases := []struct {
		amount *int64
		want   domain.AmountBand
	}{
		{nil, domain.AmountUnknown},
		{amount(1), domain.AmountLow},
		{amount(4_999_999_99), domain.AmountLow},
		{amount(5_000_000_00), domain.AmountMedium},
		{amount(30_000_000_00), domain.AmountMedium},
		{amount(30_000_000_01), domain.AmountHigh},
		{amount(10_000_000_000_000), domain.AmountHigh},
	}
	for _, c := range cases {
		got := p.BandOf(c.amount)
		if c.amount == nil {
			assert.Equal(t, c.want, got, "nil")
			continue
		}
		assert.Equal(t, c.want, got, *c.amount)
	}
}

func TestTheTargetOfEachLevel(t *testing.T) {
	p := domain.TriagePolicyV1()
	assert.Equal(t, domain.TriageTarget{Kind: domain.TargetImmediate, ResolutionBusinessDays: 2}, p.TargetOf(domain.Triage1))
	assert.Equal(t, domain.TriageTarget{Kind: domain.TargetBusinessDays, BusinessDays: 5, ResolutionBusinessDays: 5},
		p.TargetOf(domain.Triage2))
	assert.Equal(t, domain.TriageTarget{Kind: domain.TargetBusinessDays, BusinessDays: 10, ResolutionBusinessDays: 10},
		p.TargetOf(domain.Triage3))
	assert.Zero(t, p.TargetOf(4))
}

func TestEachCallReturnsItsOwnPolicy(t *testing.T) {
	p := domain.TriagePolicyV1()
	p.Severity[0].Severity = domain.SeverityLow
	p.Targets[0].Target.ResolutionBusinessDays = 99

	fresh := domain.TriagePolicyV1()
	assert.Equal(t, domain.SeverityMedium, fresh.Severity[0].Severity)
	assert.Equal(t, 2, fresh.Targets[0].Target.ResolutionBusinessDays)
}

func TestTheInputOfAClaim(t *testing.T) {
	p := domain.TriagePolicyV1()
	web := domain.ClaimRecord{
		Claim:                domain.Claim{IncidentType: domain.IncidentWaterDamage},
		Channel:              domain.ChannelWeb,
		CatalogVersion:       domain.CatalogVersionV1,
		EstimatedAmountMinor: amount(4_800_000_00),
	}
	in := p.InputOf(web)
	assert.Equal(t, domain.TriageInput{
		IncidentType: domain.IncidentWaterDamage, Line: domain.LineHome, Coverage: "water_damage",
		EstimatedAmountMinor: amount(4_800_000_00), PolicyVersion: domain.TriagePolicyVersionV1,
	}, in)
	*web.EstimatedAmountMinor = 1
	assert.Equal(t, int64(4_800_000_00), *in.EstimatedAmountMinor, "the input does not share the amount of the claim")

	// A claim filed from the mobile app has no catalog version nor amount.
	mobile := p.InputOf(domain.ClaimRecord{Claim: domain.Claim{IncidentType: domain.IncidentTheft}, Channel: domain.ChannelMobile})
	assert.Equal(t, domain.LineAuto, mobile.Line)
	assert.Nil(t, mobile.EstimatedAmountMinor)

	unknown := p.InputOf(domain.ClaimRecord{Claim: domain.Claim{IncidentType: "fire"}})
	assert.Empty(t, unknown.Line)
}

func TestTheCasesOfTheMockup(t *testing.T) {
	p := domain.TriagePolicyV1()
	claim := func(incident domain.IncidentType, cop int64) domain.ClaimRecord {
		return domain.ClaimRecord{
			Claim:          domain.Claim{IncidentType: incident},
			CatalogVersion: domain.CatalogVersionV1, EstimatedAmountMinor: amount(cop * 100),
		}
	}

	// «SIN-2026-01655 · Hogar · daño por agua · $ 4.800.000 · Triage 2 · severidad media · monto bajo».
	a, known := p.Assess(p.InputOf(claim(domain.IncidentWaterDamage, 4_800_000)))
	require.True(t, known)
	assert.Equal(t, domain.Triage2, a.Level)
	assert.Equal(t, domain.SeverityMedium, a.Severity)
	assert.Equal(t, domain.AmountLow, a.AmountBand)
	assert.Equal(t, 5, a.Target.BusinessDays)

	// «Hogar · $ 48.000.000 · Triage 1».
	a, _ = p.Assess(p.InputOf(claim(domain.IncidentWaterDamage, 48_000_000)))
	assert.Equal(t, domain.Triage1, a.Level)
	assert.Equal(t, domain.AmountHigh, a.AmountBand)

	// «Autos · colisión · $ 3.200.000 · Triage 2».
	a, _ = p.Assess(p.InputOf(claim(domain.IncidentCollision, 3_200_000)))
	assert.Equal(t, domain.Triage2, a.Level)

	// Glass for little money is the only Triage 3.
	a, _ = p.Assess(p.InputOf(claim(domain.IncidentGlass, 900_000)))
	assert.Equal(t, domain.Triage3, a.Level)
	assert.Equal(t, domain.TriageTarget{Kind: domain.TargetBusinessDays, BusinessDays: 10, ResolutionBusinessDays: 10}, a.Target)
}

func TestTheAssessmentCarriesTheVersionOfThePolicy(t *testing.T) {
	p := domain.TriagePolicyV1()
	in := waterDamageInput()
	in.PolicyVersion = "an old one"

	a, _ := p.Assess(in)
	assert.Equal(t, domain.TriagePolicyVersionV1, a.Input.PolicyVersion)

	theft := domain.TriageInput{IncidentType: domain.IncidentTheft, Line: domain.LineAuto, Coverage: "theft"}
	a, _ = p.Assess(theft)
	assert.Equal(t, domain.Triage1, a.Level, "a high severity without amount is Triage 1")
	assert.Equal(t, domain.AmountUnknown, a.AmountBand)

	_, known := p.Assess(domain.TriageInput{IncidentType: "fire", Line: domain.LineHome})
	assert.False(t, known)
}
