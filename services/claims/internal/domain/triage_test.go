package domain_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
)

// The rule of the level, the input of a calculation and the record of each
// calculation (HU-W2.2-01).

func amount(minor int64) *int64 { return &minor }

func TestTheLevelCombinesTheSeverityAndTheAmountBand(t *testing.T) {
	want := map[domain.Severity]map[domain.AmountBand]domain.PriorityLevel{
		domain.SeverityHigh: {
			domain.AmountLow: domain.Triage1, domain.AmountMedium: domain.Triage1,
			domain.AmountHigh: domain.Triage1, domain.AmountUnknown: domain.Triage1,
		},
		domain.SeverityMedium: {
			domain.AmountLow: domain.Triage2, domain.AmountMedium: domain.Triage2,
			domain.AmountHigh: domain.Triage1, domain.AmountUnknown: domain.Triage2,
		},
		domain.SeverityLow: {
			domain.AmountLow: domain.Triage3, domain.AmountMedium: domain.Triage2,
			domain.AmountHigh: domain.Triage1, domain.AmountUnknown: domain.Triage2,
		},
	}
	for severity, bands := range want {
		for band, level := range bands {
			assert.Equal(t, level, domain.LevelOf(severity, band), "%s/%s", severity, band)
		}
	}
}

func TestOnlyTheThreeLevelsAreValid(t *testing.T) {
	for _, l := range []domain.PriorityLevel{0, 4, -1} {
		assert.False(t, l.Valid(), l)
	}
	for _, l := range []domain.PriorityLevel{domain.Triage1, domain.Triage2, domain.Triage3} {
		assert.True(t, l.Valid(), l)
	}
}

func TestTheTriageEvaluatesTheActiveAndTheInEvaluationClaims(t *testing.T) {
	for _, s := range []domain.Status{domain.StatusActive, domain.StatusInEvaluation} {
		assert.True(t, domain.Triageable(s), s)
	}
	for _, s := range []domain.Status{"paid", "rejected", "payment_exception", "returned", ""} {
		assert.False(t, domain.Triageable(s), s)
	}

	statuses := domain.TriageableStatuses()
	statuses[0] = "paid"
	assert.True(t, domain.Triageable(domain.StatusActive), "a caller cannot change the statuses")
}

func waterDamageInput() domain.TriageInput {
	return domain.TriageInput{
		IncidentType: domain.IncidentWaterDamage, Line: domain.LineHome, Coverage: "water_damage",
		EstimatedAmountMinor: amount(4_800_000_00), PolicyVersion: domain.TriagePolicyVersionV1,
	}
}

func TestTheFingerprintChangesWithEveryInput(t *testing.T) {
	base := waterDamageInput()
	assert.Equal(t, base.Fingerprint(), waterDamageInput().Fingerprint(), "stable")

	changes := map[string]func(*domain.TriageInput){
		"amount":         func(in *domain.TriageInput) { in.EstimatedAmountMinor = amount(4_800_000_01) },
		"no amount":      func(in *domain.TriageInput) { in.EstimatedAmountMinor = nil },
		"incident":       func(in *domain.TriageInput) { in.IncidentType = domain.IncidentGlass },
		"line":           func(in *domain.TriageInput) { in.Line = domain.LineAuto },
		"coverage":       func(in *domain.TriageInput) { in.Coverage = "glass" },
		"policy version": func(in *domain.TriageInput) { in.PolicyVersion = "triage-policy-v2" },
	}
	for name, change := range changes {
		in := waterDamageInput()
		change(&in)
		assert.NotEqual(t, base.Fingerprint(), in.Fingerprint(), name)
	}
}

func TestTheDiffNamesEveryInputThatChanged(t *testing.T) {
	base := waterDamageInput()
	assert.Empty(t, waterDamageInput().Diff(base))

	cases := map[string]struct {
		change func(*domain.TriageInput)
		want   []domain.TriageCause
	}{
		"amount": {func(in *domain.TriageInput) { in.EstimatedAmountMinor = amount(40_000_000_00) },
			[]domain.TriageCause{domain.CauseEstimatedAmountChanged}},
		"amount removed": {func(in *domain.TriageInput) { in.EstimatedAmountMinor = nil },
			[]domain.TriageCause{domain.CauseEstimatedAmountChanged}},
		"coverage": {func(in *domain.TriageInput) { in.Coverage = "fire" },
			[]domain.TriageCause{domain.CauseCoverageChanged}},
		"policy": {func(in *domain.TriageInput) { in.PolicyVersion = "triage-policy-v2" },
			[]domain.TriageCause{domain.CausePolicyUpdated}},
		"incident and its coverage": {func(in *domain.TriageInput) {
			in.IncidentType, in.Line, in.Coverage = domain.IncidentGlass, domain.LineAuto, "glass"
		}, []domain.TriageCause{domain.CauseIncidentTypeChanged, domain.CauseCoverageChanged}},
		"amount and incident": {func(in *domain.TriageInput) {
			in.EstimatedAmountMinor, in.IncidentType = nil, domain.IncidentOther
		}, []domain.TriageCause{domain.CauseEstimatedAmountChanged, domain.CauseIncidentTypeChanged}},
	}
	for name, c := range cases {
		in := waterDamageInput()
		c.change(&in)
		assert.Equal(t, c.want, in.Diff(base), name)
	}

	withoutAmount := waterDamageInput()
	withoutAmount.EstimatedAmountMinor = nil
	assert.Empty(t, withoutAmount.Diff(withoutAmount), "two claims without amount are the same input")
}

func TestTheFirstCalculationIsTheEntryIntoEvaluation(t *testing.T) {
	a, _ := domain.TriagePolicyV1().Assess(waterDamageInput())
	at := time.Date(2026, 9, 4, 8, 31, 7, 0, domain.ColombiaTime)

	r, ok := domain.NextTriage("claim-1", a, nil, at)

	require.True(t, ok)
	assert.Equal(t, 1, r.Seq)
	assert.Equal(t, []domain.TriageCause{domain.CauseEnteredEvaluation}, r.Causes)
	assert.Nil(t, r.PreviousLevel)
	assert.Equal(t, domain.TriageActorSystem, r.Actor)
	assert.Equal(t, time.UTC, r.AssessedAt.Location())
	assert.True(t, at.Equal(r.AssessedAt))
}

func TestARecalculationRecordsItsCausesAndThePreviousLevel(t *testing.T) {
	policy := domain.TriagePolicyV1()
	first, _ := policy.Assess(waterDamageInput())
	last, _ := domain.NextTriage("claim-1", first, nil, time.Now())

	unchanged, ok := domain.NextTriage("claim-1", first, &last, time.Now())
	assert.False(t, ok, "nothing changed")
	assert.Zero(t, unchanged)

	in := waterDamageInput()
	in.EstimatedAmountMinor = amount(40_000_000_00)
	higher, _ := policy.Assess(in)
	r, ok := domain.NextTriage("claim-1", higher, &last, time.Now())
	require.True(t, ok)
	assert.Equal(t, 2, r.Seq)
	assert.Equal(t, domain.Triage1, r.Assessment.Level)
	require.NotNil(t, r.PreviousLevel)
	assert.Equal(t, domain.Triage2, *r.PreviousLevel)
	assert.Equal(t, []domain.TriageCause{domain.CauseEstimatedAmountChanged}, r.Causes)

	// The information changed but not the level: it is recorded too (S7).
	in = waterDamageInput()
	in.EstimatedAmountMinor = amount(4_000_000_00)
	same, _ := policy.Assess(in)
	r, ok = domain.NextTriage("claim-1", same, &last, time.Now())
	require.True(t, ok)
	assert.Equal(t, domain.Triage2, r.Assessment.Level)
	assert.Equal(t, domain.Triage2, *r.PreviousLevel)
}

func TestTheStateOfTheTriageOfAClaim(t *testing.T) {
	last := &domain.TriageRecord{Seq: 1}
	assert.Equal(t, domain.TriagePending, domain.TriageStateOf(domain.StatusActive, nil))
	assert.Equal(t, domain.TriagePending, domain.TriageStateOf(domain.StatusInEvaluation, nil))
	assert.Equal(t, domain.TriageAssessed, domain.TriageStateOf(domain.StatusInEvaluation, last))
	assert.Equal(t, domain.TriageAssessed, domain.TriageStateOf("paid", last), "the last level stays visible")
	assert.Equal(t, domain.TriageNotApplicable, domain.TriageStateOf("rejected", nil))
}
