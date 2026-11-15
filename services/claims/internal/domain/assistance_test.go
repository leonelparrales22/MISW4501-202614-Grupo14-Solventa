package domain_test

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
)

// The rules of the request for on-site assistance (HU-M2.3-01 criteria 2, 4
// and 5; assumptions S2 to S6).

var assistanceNow = time.Date(2026, 11, 14, 21, 31, 8, 0, time.UTC)

func aptr[T any](v T) *T { return &v }

// sentLocation is the fix of the mockup M14: Autopista Norte, 8 m.
func sentLocation() *domain.AssistanceLocationInput {
	return &domain.AssistanceLocationInput{
		Status: "sent", Latitude: aptr(4.7712), Longitude: aptr(-74.0452), AccuracyM: aptr(8.0),
		CapturedAt: aptr("2026-11-14T21:31:02Z"),
	}
}

func unavailableLocation(reason string) *domain.AssistanceLocationInput {
	return &domain.AssistanceLocationInput{Status: "unavailable", UnavailableReason: aptr(reason)}
}

func assistanceInput(location *domain.AssistanceLocationInput) domain.AssistanceRequestInput {
	return domain.AssistanceRequestInput{PolicyID: "AU-2025-118204", Location: location,
		Place: aptr("Autopista Norte km 12, Bogotá")}
}

// assistanceFields validates the input and returns the invalid fields, as field:code.
func assistanceFields(t *testing.T, in domain.AssistanceRequestInput) []string {
	t.Helper()
	_, err := in.Validate(assistanceNow)
	if err == nil {
		return nil
	}
	var invalid *domain.ValidationError
	require.ErrorAs(t, err, &invalid)
	out := make([]string, 0, len(invalid.Fields))
	for _, f := range invalid.Fields {
		out = append(out, f.Field+":"+f.Code)
	}
	return out
}

func TestARequestWithALocationKeepsTheFix(t *testing.T) {
	draft, err := assistanceInput(sentLocation()).Validate(assistanceNow)
	require.NoError(t, err)

	assert.Equal(t, "AU-2025-118204", draft.PolicyID)
	assert.Equal(t, domain.LocationSent, draft.Location.Status)
	require.NotNil(t, draft.Location.Coordinates)
	assert.Equal(t, domain.Coordinates{Latitude: 4.7712, Longitude: -74.0452}, *draft.Location.Coordinates)
	assert.Equal(t, 8.0, *draft.Location.AccuracyM)
	assert.Equal(t, time.Date(2026, 11, 14, 21, 31, 2, 0, time.UTC), *draft.Location.CapturedAt)
	assert.Empty(t, draft.Location.UnavailableReason)
	assert.Equal(t, "Autopista Norte km 12, Bogotá", *draft.Place)
}

func TestTheInstantOfTheFixIsKeptInUTC(t *testing.T) {
	location := sentLocation()
	location.CapturedAt = aptr("2026-11-14T16:31:02-05:00")
	draft, err := assistanceInput(location).Validate(assistanceNow)
	require.NoError(t, err)
	assert.Equal(t, time.UTC, draft.Location.CapturedAt.Location())
	assert.Equal(t, time.Date(2026, 11, 14, 21, 31, 2, 0, time.UTC), *draft.Location.CapturedAt)
}

func TestARequestWithoutALocationKeepsItsReason(t *testing.T) {
	for _, reason := range []domain.LocationUnavailableReason{
		domain.UnavailablePermissionDenied, domain.UnavailableLocationDisabled, domain.UnavailableNoFix,
	} {
		t.Run(string(reason), func(t *testing.T) {
			draft, err := assistanceInput(unavailableLocation(string(reason))).Validate(assistanceNow)
			require.NoError(t, err)
			assert.Equal(t, domain.LocationUnavailable, draft.Location.Status)
			assert.Equal(t, reason, draft.Location.UnavailableReason)
			assert.Nil(t, draft.Location.Coordinates)
			assert.Nil(t, draft.Location.AccuracyM)
			assert.Nil(t, draft.Location.CapturedAt)
		})
	}
}

func TestTheCoordinatesAreCheckedAtTheirBounds(t *testing.T) {
	cases := map[string]struct {
		latitude, longitude float64
		want                []string
	}{
		"the origin is a place":     {0, 0, nil},
		"north pole":                {90, 0, nil},
		"south pole":                {-90, 0, nil},
		"east antimeridian":         {0, 180, nil},
		"west antimeridian":         {0, -180, nil},
		"past the north pole":       {90.000001, 0, []string{"location.latitude:out_of_range"}},
		"past the south pole":       {-90.000001, 0, []string{"location.latitude:out_of_range"}},
		"past the east meridian":    {0, 180.000001, []string{"location.longitude:out_of_range"}},
		"past the west meridian":    {0, -180.000001, []string{"location.longitude:out_of_range"}},
		"not a number":              {math.NaN(), math.NaN(), []string{"location.latitude:out_of_range", "location.longitude:out_of_range"}},
		"infinite":                  {math.Inf(1), math.Inf(-1), []string{"location.latitude:out_of_range", "location.longitude:out_of_range"}},
		"both out of range at once": {91, 181, []string{"location.latitude:out_of_range", "location.longitude:out_of_range"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			location := sentLocation()
			location.Latitude, location.Longitude = aptr(c.latitude), aptr(c.longitude)
			assert.Equal(t, c.want, assistanceFields(t, assistanceInput(location)))
		})
	}
}

func TestTheAccuracyIsCheckedAtItsBounds(t *testing.T) {
	cases := map[string]struct {
		accuracy float64
		valid    bool
	}{
		"zero":             {0, false},
		"a tenth":          {0.1, true},
		"the maximum":      {10000, true},
		"past the maximum": {10000.1, false},
		"negative":         {-1, false},
		"not a number":     {math.NaN(), false},
		"infinite":         {math.Inf(1), false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			location := sentLocation()
			location.AccuracyM = aptr(c.accuracy)
			got := assistanceFields(t, assistanceInput(location))
			if c.valid {
				assert.Empty(t, got)
			} else {
				assert.Equal(t, []string{"location.accuracy_m:out_of_range"}, got)
			}
		})
	}
}

func TestTheInstantOfTheFixIsCheckedAgainstTheClock(t *testing.T) {
	cases := map[string]struct {
		capturedAt *string
		want       []string
	}{
		"missing":                     {nil, []string{"location.captured_at:required"}},
		"blank":                       {aptr("  "), []string{"location.captured_at:required"}},
		"not RFC 3339":                {aptr("14/11/2026 16:31"), []string{"location.captured_at:invalid_format"}},
		"now":                         {aptr(assistanceNow.Format(time.RFC3339)), nil},
		"an hour ago is not rejected": {aptr(assistanceNow.Add(-time.Hour).Format(time.RFC3339)), nil},
		"five minutes ahead":          {aptr(assistanceNow.Add(5 * time.Minute).Format(time.RFC3339)), nil},
		"five minutes and one second ahead": {aptr(assistanceNow.Add(5*time.Minute + time.Second).Format(time.RFC3339)),
			[]string{"location.captured_at:out_of_range"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			location := sentLocation()
			location.CapturedAt = c.capturedAt
			assert.Equal(t, c.want, assistanceFields(t, assistanceInput(location)))
		})
	}
}

func TestTheShapeOfTheLocationFollowsItsStatus(t *testing.T) {
	withReason := sentLocation()
	withReason.UnavailableReason = aptr("no_fix")
	withCoordinates := unavailableLocation("no_fix")
	withCoordinates.Latitude, withCoordinates.Longitude = aptr(4.7), aptr(-74.0)
	withEveryFix := unavailableLocation("permission_denied")
	withEveryFix.Latitude, withEveryFix.Longitude, withEveryFix.AccuracyM = aptr(4.7), aptr(-74.0), aptr(8.0)
	withEveryFix.CapturedAt = aptr("2026-11-14T21:31:02Z")

	cases := map[string]struct {
		location *domain.AssistanceLocationInput
		want     []string
	}{
		"no location": {nil, []string{"location:required"}},
		"no status":   {&domain.AssistanceLocationInput{}, []string{"location.status:required"}},
		"an unknown status": {&domain.AssistanceLocationInput{Status: "approximate"},
			[]string{"location.status:invalid_format"}},
		"sent without the fix": {&domain.AssistanceLocationInput{Status: "sent"}, []string{
			"location.latitude:required", "location.longitude:required", "location.accuracy_m:required",
			"location.captured_at:required",
		}},
		"sent with a reason": {withReason, []string{"location.unavailable_reason:not_applicable"}},
		"unavailable without a reason": {&domain.AssistanceLocationInput{Status: "unavailable"},
			[]string{"location.unavailable_reason:required"}},
		"unavailable with a blank reason": {unavailableLocation(" "), []string{"location.unavailable_reason:required"}},
		"an unknown reason":               {unavailableLocation("airplane_mode"), []string{"location.unavailable_reason:invalid_format"}},
		"unavailable with coordinates": {withCoordinates,
			[]string{"location.latitude:not_applicable", "location.longitude:not_applicable"}},
		"unavailable with the whole fix": {withEveryFix, []string{
			"location.latitude:not_applicable", "location.longitude:not_applicable",
			"location.accuracy_m:not_applicable", "location.captured_at:not_applicable",
		}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, c.want, assistanceFields(t, assistanceInput(c.location)))
		})
	}
}

func TestThePlaceIsOptionalAndBounded(t *testing.T) {
	cases := map[string]struct {
		place *string
		want  []string
	}{
		"absent":               {nil, nil},
		"two characters":       {aptr("ab"), []string{"place:out_of_range"}},
		"three characters":     {aptr("abc"), nil},
		"160 characters":       {aptr(strings.Repeat("ñ", 160)), nil},
		"161 characters":       {aptr(strings.Repeat("ñ", 161)), []string{"place:out_of_range"}},
		"only spaces":          {aptr("     "), []string{"place:out_of_range"}},
		"three after trimming": {aptr("  abc  "), nil},
		"two after trimming":   {aptr("   ab   "), []string{"place:out_of_range"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			in := assistanceInput(sentLocation())
			in.Place = c.place
			assert.Equal(t, c.want, assistanceFields(t, in))
		})
	}

	in := assistanceInput(sentLocation())
	in.Place = aptr("  <b>Km 12</b>\nBogotá  ")
	draft, err := in.Validate(assistanceNow)
	require.NoError(t, err)
	assert.Equal(t, "<b>Km 12</b>\nBogotá", *draft.Place, "kept as text, trimmed")
}

func TestThePolicyIDIsRequiredAndWellFormed(t *testing.T) {
	for id, want := range map[string][]string{
		"":                      {"policy_id:required"},
		"  ":                    {"policy_id:required"},
		"AU":                    {"policy_id:invalid_format"},
		"AU 2025":               {"policy_id:invalid_format"},
		strings.Repeat("A", 41): {"policy_id:invalid_format"},
		" AU-2025-118204 ":      nil,
	} {
		in := assistanceInput(sentLocation())
		in.PolicyID = id
		assert.Equal(t, want, assistanceFields(t, in), id)
	}
}

func TestEveryInvalidFieldIsReportedAtOnce(t *testing.T) {
	in := domain.AssistanceRequestInput{PolicyID: "", Location: &domain.AssistanceLocationInput{Status: "sent",
		Latitude: aptr(91.0), Longitude: aptr(-74.0), AccuracyM: aptr(0.0), CapturedAt: aptr("ayer")}, Place: aptr("x")}
	assert.Equal(t, []string{
		"policy_id:required", "location.latitude:out_of_range", "location.accuracy_m:out_of_range",
		"location.captured_at:invalid_format", "place:out_of_range",
	}, assistanceFields(t, in))
}

func TestTheStatusesAndReasonsAreTheOnesOfTheContract(t *testing.T) {
	assert.True(t, domain.LocationSent.Valid())
	assert.True(t, domain.LocationUnavailable.Valid())
	assert.False(t, domain.LocationStatus("").Valid())
	assert.False(t, domain.LocationUnavailableReason("").Valid())
	assert.Equal(t, domain.AssistanceStatus("new"), domain.AssistanceNew)
}

func TestTheNumberOfARequest(t *testing.T) {
	assert.Equal(t, "AST-2026-0912", domain.AssistanceNumber(assistanceNow, 912))
	assert.Equal(t, "AST-2026-0001", domain.AssistanceNumber(assistanceNow, 1))
	assert.Equal(t, "AST-2026-12345", domain.AssistanceNumber(assistanceNow, 12345), "four digits or more")
	newYear := time.Date(2027, 1, 1, 2, 0, 0, 0, time.FixedZone("COT", -5*3600))
	assert.Equal(t, "AST-2027-0007", domain.AssistanceNumber(newYear, 7), "the year of the UTC instant")
}

// --- Fingerprint (assumption S6) ------------------------------------------

func draftOf(t *testing.T, in domain.AssistanceRequestInput) domain.AssistanceDraft {
	t.Helper()
	d, err := in.Validate(assistanceNow)
	require.NoError(t, err)
	return d
}

func TestTheSameRequestHasTheSameFingerprint(t *testing.T) {
	a := draftOf(t, assistanceInput(sentLocation()))
	b := draftOf(t, assistanceInput(sentLocation()))
	assert.Equal(t, a.Fingerprint(), b.Fingerprint())
	assert.Len(t, a.Fingerprint(), 64)

	trimmed := assistanceInput(sentLocation())
	trimmed.PolicyID, trimmed.Place = "  AU-2025-118204 ", aptr(" Autopista Norte km 12, Bogotá ")
	assert.Equal(t, a.Fingerprint(), draftOf(t, trimmed).Fingerprint(), "the spaces at the ends do not count")

	offset := sentLocation()
	offset.CapturedAt = aptr("2026-11-14T16:31:02-05:00")
	assert.Equal(t, a.Fingerprint(), draftOf(t, assistanceInput(offset)).Fingerprint(), "the same instant")
}

func TestEveryFieldChangesTheFingerprint(t *testing.T) {
	base := draftOf(t, assistanceInput(sentLocation())).Fingerprint()
	changes := map[string]func(*domain.AssistanceRequestInput){
		"policy":      func(in *domain.AssistanceRequestInput) { in.PolicyID = "HO-2025-000001" },
		"latitude":    func(in *domain.AssistanceRequestInput) { in.Location.Latitude = aptr(4.7713) },
		"longitude":   func(in *domain.AssistanceRequestInput) { in.Location.Longitude = aptr(-74.0453) },
		"accuracy":    func(in *domain.AssistanceRequestInput) { in.Location.AccuracyM = aptr(9.0) },
		"captured at": func(in *domain.AssistanceRequestInput) { in.Location.CapturedAt = aptr("2026-11-14T21:31:03Z") },
		"place":       func(in *domain.AssistanceRequestInput) { in.Place = aptr("Calle 80, Bogotá") },
		"no place":    func(in *domain.AssistanceRequestInput) { in.Place = nil },
		"unavailable": func(in *domain.AssistanceRequestInput) { in.Location = unavailableLocation("no_fix") },
	}
	for name, change := range changes {
		in := assistanceInput(sentLocation())
		change(&in)
		assert.NotEqual(t, base, draftOf(t, in).Fingerprint(), name)
	}

	reasons := map[string]bool{}
	for _, r := range []string{"permission_denied", "location_disabled", "no_fix"} {
		reasons[draftOf(t, assistanceInput(unavailableLocation(r))).Fingerprint()] = true
	}
	assert.Len(t, reasons, 3, "each reason is another request")
}

// --- Eligibility of the policy (assumption S2) -------------------------------

func TestOnlyAnActivePolicyOfTheInsuredTakesARequest(t *testing.T) {
	validFrom := assistanceNow.AddDate(-1, 0, 0)
	policy := func(id string, status domain.PolicyStatus, from, to time.Time) domain.Policy {
		return domain.Policy{ID: id, Line: domain.LineAuto, Product: "Autos", InsuredObject: "KJT-482",
			Status: status, ValidFrom: from, ValidTo: to, CoveredIncidents: []domain.IncidentType{domain.IncidentCollision}}
	}
	own := []domain.Policy{
		policy("AU-2025-118204", domain.PolicyActive, validFrom, assistanceNow.AddDate(0, 1, 0)),
		policy("AU-2024-000001", domain.PolicyActive, validFrom, assistanceNow.AddDate(0, 0, -1)),
		policy("AU-2026-000002", domain.PolicyActive, assistanceNow.AddDate(0, 0, 1), assistanceNow.AddDate(1, 0, 0)),
		policy("AU-2025-000003", "cancelled", validFrom, assistanceNow.AddDate(0, 1, 0)),
		policy("AU-2025-000004", domain.PolicyActive, validFrom, assistanceNow),
		policy("AU-2025-000005", domain.PolicyActive, assistanceNow, assistanceNow.Add(time.Second)),
		{ID: "VI-2025-000006", Line: domain.LineTravel, Status: domain.PolicyActive, ValidFrom: validFrom,
			ValidTo: assistanceNow.AddDate(0, 1, 0)},
	}

	cases := map[string]struct {
		id   string
		want error
	}{
		"own and active":           {"AU-2025-118204", nil},
		"of someone else":          {"AU-2025-999999", domain.ErrPolicyNotFound},
		"expired yesterday":        {"AU-2024-000001", domain.ErrPolicyNotActive},
		"starts tomorrow":          {"AU-2026-000002", domain.ErrPolicyNotActive},
		"not active in the core":   {"AU-2025-000003", domain.ErrPolicyNotActive},
		"ends right now":           {"AU-2025-000004", domain.ErrPolicyNotActive},
		"starts right now":         {"AU-2025-000005", nil},
		"any line takes a request": {"VI-2025-000006", nil},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			p, err := domain.EligibleForAssistance(own, c.id, assistanceNow)
			if c.want == nil {
				require.NoError(t, err)
				assert.Equal(t, c.id, p.ID)
				return
			}
			require.ErrorIs(t, err, c.want)
			if c.want == domain.ErrPolicyNotActive {
				assert.Equal(t, c.id, p.ID, "the policy comes back to say its validity")
			}
		})
	}

	_, err := domain.EligibleForAssistance(nil, "AU-2025-118204", assistanceNow)
	assert.ErrorIs(t, err, domain.ErrPolicyNotFound, "an insured without policies")
}

func TestTheEligiblePolicyIsACopy(t *testing.T) {
	own := []domain.Policy{{ID: "AU-2025-118204", Status: domain.PolicyActive, ValidFrom: assistanceNow.AddDate(-1, 0, 0),
		ValidTo: assistanceNow.AddDate(0, 1, 0), CoveredIncidents: []domain.IncidentType{domain.IncidentCollision}}}
	p, err := domain.EligibleForAssistance(own, "AU-2025-118204", assistanceNow)
	require.NoError(t, err)
	p.CoveredIncidents[0] = domain.IncidentTheft
	assert.Equal(t, domain.IncidentCollision, own[0].CoveredIncidents[0])
}

func TestThePolicyKeptWithARequest(t *testing.T) {
	p := domain.Policy{ID: "AU-2025-118204", Line: domain.LineAuto, Product: "Autos", InsuredObject: "KJT-482",
		Status: domain.PolicyActive}
	assert.Equal(t, domain.AssistancePolicy{ID: "AU-2025-118204", Line: domain.LineAuto, Product: "Autos",
		InsuredObject: "KJT-482"}, domain.AssistancePolicyOf(p))
}
