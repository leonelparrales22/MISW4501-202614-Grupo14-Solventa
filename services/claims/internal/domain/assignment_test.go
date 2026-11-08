package domain_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
)

// The location of the incident, the search and the assignment of the nearest
// provider (HU-W2.3-01).

var (
	searchedAt = time.Date(2026, 9, 6, 15, 14, 2, 0, time.UTC)
	analyst    = domain.Analyst{Email: "andres.ruiz@solventa.example", Name: "Andrés Ruiz"}
)

// claimOf is a web claim in evaluation, without evidence.
func claimOf(incident domain.IncidentType) domain.ClaimRecord {
	return domain.ClaimRecord{
		Claim: domain.Claim{ID: "8f14e45f-ceea-467a-9f3a-5a2d0b1c7e90", FilingNumber: "SIN-2026-01655",
			IncidentType: incident, Status: domain.StatusInEvaluation},
		Channel: domain.ChannelWeb, CatalogVersion: domain.CatalogVersionV1,
	}
}

// capture is a photograph at the point with the precision.
func capture(at domain.Coordinates, accuracy float64, trusted bool, minute int) domain.Evidence {
	return domain.Evidence{Kind: domain.KindPhoto, Latitude: at.Latitude, Longitude: at.Longitude,
		AccuracyM: accuracy, LocationTrusted: trusted, CapturedAt: time.Date(2026, 9, 6, 14, minute, 0, 0, time.UTC)}
}

// provider is an available provider km kilometers north of the incident.
func provider(id string, km float64, specialties ...domain.Specialty) domain.Provider {
	return domain.Provider{ID: id, Name: id, Specialties: specialties, Location: north(km),
		Availability: domain.AvailabilityAvailable, ContactEmail: id + "@example.com"}
}

func unavailable(p domain.Provider) domain.Provider {
	p.Availability = domain.AvailabilityUnavailable
	return p
}

func analystAt(point domain.Coordinates) *domain.IncidentLocation {
	return &domain.IncidentLocation{Source: domain.LocationFromAnalyst, Point: point, Address: "Carrera 15 # 93-40",
		Seq: 1, RecordedBy: &analyst, RecordedAt: searchedAt}
}

// The mockup "Asignar prestador": water damage, plumbing, three providers.
func mockupProviders() []domain.Provider {
	return []domain.Provider{
		provider("prv-tecnicos-unidos", 6.1, domain.SpecialtyPlumbing, domain.SpecialtyDampProofing),
		provider("prv-servicios-hogar-norte", 2.3, domain.SpecialtyPlumbing, domain.SpecialtyDampProofing),
		unavailable(provider("prv-reparaciones-chapinero", 3.8, domain.SpecialtyPlumbing)),
		provider("prv-taller-vehicular", 1.0, domain.SpecialtyVehicleAssessment),
	}
}

func TestTheOnlyClaimsAssignableAreInEvaluation(t *testing.T) {
	assert.True(t, domain.Assignable(domain.StatusInEvaluation))
	for _, s := range []domain.Status{domain.StatusActive, domain.StatusReturned, domain.StatusApproved, domain.StatusPaid,
		domain.StatusRejected, domain.StatusPaymentException, "unknown"} {
		assert.False(t, domain.Assignable(s), s)
	}
}

func TestTheLocationOfTheAppIsItsMostPreciseTrustedEvidence(t *testing.T) {
	_, ok := domain.AppLocation(nil)
	assert.False(t, ok, "no evidence")

	_, ok = domain.AppLocation([]domain.Evidence{capture(north(1), 8, false, 1), capture(north(2), 60, true, 2)})
	assert.False(t, ok, "neither untrusted nor beyond 50 m counts")

	l, ok := domain.AppLocation([]domain.Evidence{
		capture(north(1), 20, true, 1),
		capture(incident, 8, true, 2),
		capture(north(3), 8, true, 3),
		capture(north(4), 50, true, 4),
	})
	require.True(t, ok)
	assert.Equal(t, domain.LocationFromApp, l.Source)
	assert.Equal(t, incident, l.Point, "the best precision; the first captured on a tie")
	assert.Equal(t, 8.0, *l.AccuracyM)
	assert.Equal(t, time.Date(2026, 9, 6, 14, 2, 0, 0, time.UTC), l.RecordedAt)
	assert.Nil(t, l.RecordedBy)
	assert.Empty(t, l.Address)

	l, ok = domain.AppLocation([]domain.Evidence{capture(north(4), 50, true, 4)})
	require.True(t, ok, "50 m is still trusted")
	assert.Equal(t, 50.0, *l.AccuracyM)
}

func TestTheClaimIsSearchedFromTheAppOrElseFromTheAnalyst(t *testing.T) {
	c := claimOf(domain.IncidentWaterDamage)
	assert.Nil(t, domain.LocationOf(c, nil), "a web claim with nothing recorded")

	recorded := analystAt(north(1))
	l := domain.LocationOf(c, recorded)
	require.NotNil(t, l)
	assert.Equal(t, domain.LocationFromAnalyst, l.Source)

	c.Evidence = []domain.Evidence{capture(incident, 8, true, 1)}
	l = domain.LocationOf(c, recorded)
	require.NotNil(t, l)
	assert.Equal(t, domain.LocationFromApp, l.Source, "the app wins")
}

func TestTheAnalystRecordsTheLocationOfAClaimWithoutCoordinates(t *testing.T) {
	c, p := claimOf(domain.IncidentWaterDamage), domain.AssignmentPolicyV1()
	in := domain.LocationRequest{Address: "  Carrera 15 # 93-40, apto 402, Bogotá ", Latitude: float(4.6771),
		Longitude: float(-74.0483)}

	l, err := domain.RecordLocation(c, p, nil, nil, in, analyst, searchedAt)
	require.NoError(t, err)
	assert.Equal(t, domain.LocationFromAnalyst, l.Source)
	assert.Equal(t, incident, l.Point)
	assert.Equal(t, "Carrera 15 # 93-40, apto 402, Bogotá", l.Address)
	assert.Equal(t, 1, l.Seq)
	assert.Equal(t, &analyst, l.RecordedBy)
	assert.Nil(t, l.AccuracyM)

	corrected, err := domain.RecordLocation(c, p, &l, nil, in, analyst, searchedAt)
	require.NoError(t, err)
	assert.Equal(t, 2, corrected.Seq, "it can be corrected until the claim is assigned")
}

func TestTheLocationOfTheAnalystIsRefusedWhenItDoesNotApply(t *testing.T) {
	p := domain.AssignmentPolicyV1()
	in := domain.LocationRequest{Address: "Carrera 15 # 93-40", Latitude: float(4.6771), Longitude: float(-74.0483)}

	fromApp := claimOf(domain.IncidentWaterDamage)
	fromApp.Evidence = []domain.Evidence{capture(incident, 8, true, 1)}
	_, err := domain.RecordLocation(fromApp, p, nil, nil, in, analyst, searchedAt)
	assert.ErrorIs(t, err, domain.ErrLocationFromApp)

	_, err = domain.RecordLocation(claimOf(domain.IncidentWaterDamage), p, nil, &domain.Assignment{}, in, analyst, searchedAt)
	assert.ErrorIs(t, err, domain.ErrAssignmentExists)

	_, err = domain.RecordLocation(claimOf(domain.IncidentTheft), p, nil, nil, in, analyst, searchedAt)
	assert.ErrorIs(t, err, domain.ErrClaimNotAssignable, "theft needs no visit")

	active := claimOf(domain.IncidentWaterDamage)
	active.Status = domain.StatusActive
	_, err = domain.RecordLocation(active, p, nil, nil, in, analyst, searchedAt)
	assert.ErrorIs(t, err, domain.ErrClaimNotAssignable)
}

func TestEveryInvalidFieldOfTheLocationIsReportedAtOnce(t *testing.T) {
	c, p := claimOf(domain.IncidentWaterDamage), domain.AssignmentPolicyV1()
	_, err := domain.RecordLocation(c, p, nil, nil, domain.LocationRequest{}, analyst, searchedAt)
	assert.Equal(t, map[string]string{"address": "required", "latitude": "required", "longitude": "required"},
		fieldsOf(t, err))

	_, err = domain.RecordLocation(c, p, nil, nil, domain.LocationRequest{Address: "Cra", Latitude: float(-90.5),
		Longitude: float(180.5)}, analyst, searchedAt)
	assert.Equal(t, map[string]string{"address": "out_of_range", "latitude": "out_of_range", "longitude": "out_of_range"},
		fieldsOf(t, err))
}

func TestTheMockupSuggestsTheNearestAvailableProviderWithTheSpecialty(t *testing.T) {
	r := domain.Search(incident, domain.SpecialtyPlumbing, mockupProviders(), []float64{5, 10, 20}, searchedAt)

	assert.Equal(t, domain.SearchFound, r.Outcome)
	assert.Empty(t, r.Reason)
	assert.Equal(t, 5.0, r.FromRadiusKm)
	assert.Equal(t, 5.0, r.RadiusKm)
	assert.Equal(t, []domain.SearchAttempt{{RadiusKm: 5, EligibleCount: 1, AttemptedAt: searchedAt}}, r.Attempts)

	require.Len(t, r.Candidates, 3, "only the providers of the specialty")
	got := map[string]domain.Eligibility{}
	for _, c := range r.Candidates {
		got[c.ProviderID] = c.Eligibility
	}
	assert.Equal(t, map[string]domain.Eligibility{
		"prv-servicios-hogar-norte":  domain.Eligible,
		"prv-reparaciones-chapinero": domain.NotEligibleUnavailable,
		"prv-tecnicos-unidos":        domain.NotEligibleOutOfRadius,
	}, got)
	assert.Equal(t, "prv-servicios-hogar-norte", r.Candidates[0].ProviderID, "nearest first")
	assert.Equal(t, "prv-tecnicos-unidos", r.Candidates[2].ProviderID)
	assert.Equal(t, 2.3, domain.RoundKm(r.Candidates[0].DistanceKm))

	suggested, ok := r.Suggested()
	require.True(t, ok)
	assert.Equal(t, "prv-servicios-hogar-norte", suggested.ProviderID)
}

func TestTheMockupWithNobodyAvailableIsPendingAssignment(t *testing.T) {
	providers := mockupProviders()
	for i := range providers {
		providers[i] = unavailable(providers[i])
	}
	r := domain.Search(incident, domain.SpecialtyPlumbing, providers, []float64{5, 10, 20}, searchedAt)

	assert.Equal(t, domain.SearchExhausted, r.Outcome)
	assert.Equal(t, domain.ReasonNoEligibleProvider, r.Reason)
	assert.Equal(t, 20.0, r.RadiusKm)
	assert.Equal(t, []domain.SearchAttempt{
		{RadiusKm: 5, AttemptedAt: searchedAt}, {RadiusKm: 10, AttemptedAt: searchedAt}, {RadiusKm: 20, AttemptedAt: searchedAt},
	}, r.Attempts, "every radius tried is recorded")
	for _, c := range r.Candidates {
		assert.Equal(t, domain.NotEligibleUnavailable, c.Eligibility, c.ProviderID)
		assert.False(t, c.Suggested)
	}
	_, ok := r.Suggested()
	assert.False(t, ok)
}

func TestTheSearchWidensTheRadiusUntilItFindsAnEligibleProvider(t *testing.T) {
	providers := []domain.Provider{
		unavailable(provider("prv-near", 1, domain.SpecialtyPlumbing)),
		provider("prv-eight", 8, domain.SpecialtyPlumbing),
		provider("prv-fifteen", 15, domain.SpecialtyPlumbing),
	}
	r := domain.Search(incident, domain.SpecialtyPlumbing, providers, []float64{5, 10, 20}, searchedAt)
	assert.Equal(t, domain.SearchFound, r.Outcome)
	assert.Equal(t, 10.0, r.RadiusKm)
	assert.Equal(t, []domain.SearchAttempt{{RadiusKm: 5, AttemptedAt: searchedAt},
		{RadiusKm: 10, EligibleCount: 1, AttemptedAt: searchedAt}}, r.Attempts)
	assert.Equal(t, domain.NotEligibleOutOfRadius, r.Candidates[2].Eligibility, "15 km is beyond the radius that found")

	r = domain.Search(incident, domain.SpecialtyPlumbing, providers, []float64{20}, searchedAt)
	assert.Equal(t, 20.0, r.FromRadiusKm, "widening starts from the radius asked")
	assert.Equal(t, 2, r.Attempts[0].EligibleCount)
}

func TestTheRadiusIncludesItsEdge(t *testing.T) {
	cases := []struct {
		km   float64
		want domain.SearchOutcome
	}{
		{4.99, domain.SearchFound},
		{5.01, domain.SearchExhausted},
	}
	for _, c := range cases {
		r := domain.Search(incident, domain.SpecialtyPlumbing, []domain.Provider{provider("prv-edge", c.km,
			domain.SpecialtyPlumbing)}, []float64{5}, searchedAt)
		assert.Equal(t, c.want, r.Outcome, c.km)
	}

	at := north(5)
	exact := domain.Distance(incident, at)
	p := provider("prv-edge", 5, domain.SpecialtyPlumbing)
	r := domain.Search(incident, domain.SpecialtyPlumbing, []domain.Provider{p}, []float64{exact}, searchedAt)
	assert.Equal(t, domain.SearchFound, r.Outcome, "a provider exactly on the radius is within it")

	for _, c := range []struct {
		km   float64
		want domain.SearchOutcome
	}{{20.0 - 1e-9, domain.SearchFound}, {20.01, domain.SearchExhausted}} {
		r := domain.Search(incident, domain.SpecialtyPlumbing, []domain.Provider{provider("prv-far", c.km,
			domain.SpecialtyPlumbing)}, []float64{5, 10, 20}, searchedAt)
		assert.Equal(t, c.want, r.Outcome, c.km)
	}
}

func TestATieOfDistanceGoesToTheLowestId(t *testing.T) {
	r := domain.Search(incident, domain.SpecialtyPlumbing, []domain.Provider{
		provider("prv-zeta", 2, domain.SpecialtyPlumbing), provider("prv-alfa", 2, domain.SpecialtyPlumbing),
	}, []float64{5}, searchedAt)
	suggested, ok := r.Suggested()
	require.True(t, ok)
	assert.Equal(t, "prv-alfa", suggested.ProviderID)
	assert.Equal(t, domain.Eligible, r.Candidates[1].Eligibility, "the other one is still eligible")
	assert.False(t, r.Candidates[1].Suggested)
}

func TestTheSearchShowsTheTwentyNearest(t *testing.T) {
	var providers []domain.Provider
	for i := 0; i < 25; i++ {
		providers = append(providers, provider("prv-"+string(rune('a'+i))+"xx", float64(25-i), domain.SpecialtyPlumbing))
	}
	r := domain.Search(incident, domain.SpecialtyPlumbing, providers, []float64{5, 10, 20}, searchedAt)
	require.Len(t, r.Candidates, domain.MaxCandidates)
	assert.InDelta(t, 1, r.Candidates[0].DistanceKm, 1e-9)
	assert.InDelta(t, 20, r.Candidates[19].DistanceKm, 1e-9)
}

func TestASearchWithoutRadiiFindsNobody(t *testing.T) {
	r := domain.Search(incident, domain.SpecialtyPlumbing, mockupProviders(), nil, searchedAt)
	assert.Equal(t, domain.SearchExhausted, r.Outcome)
	assert.Empty(t, r.Attempts)
}

func TestASearchIsTheNextOfTheClaim(t *testing.T) {
	c, p := claimOf(domain.IncidentWaterDamage), domain.AssignmentPolicyV1()
	location := analystAt(incident)

	s, err := domain.NewSearch(c, p, location, nil, nil, mockupProviders(),
		domain.SearchRequest{Trigger: domain.SearchByAnalyst, By: &analyst}, searchedAt)
	require.NoError(t, err)
	assert.Equal(t, 1, s.Seq)
	assert.Equal(t, c.ID, s.ClaimID)
	assert.Equal(t, domain.SpecialtyPlumbing, s.Specialty)
	assert.Equal(t, *location, s.Location)
	assert.Equal(t, domain.AssignmentPolicyVersionV1, s.PolicyVersion)
	assert.Equal(t, &analyst, s.SearchedBy)
	assert.Equal(t, 5.0, s.Result.FromRadiusKm)

	retry, err := domain.NewSearch(c, p, location, &s, nil, mockupProviders(),
		domain.SearchRequest{Trigger: domain.SearchByRetry, FromRadiusKm: float(10)}, searchedAt)
	require.NoError(t, err)
	assert.Equal(t, 2, retry.Seq)
	assert.Nil(t, retry.SearchedBy, "the automatic retry has no analyst")
	assert.Equal(t, domain.SearchByRetry, retry.Trigger)
	assert.Equal(t, 10.0, retry.Result.FromRadiusKm)
}

func TestASearchIsRefusedWhenItDoesNotApply(t *testing.T) {
	p := domain.AssignmentPolicyV1()
	in := domain.SearchRequest{Trigger: domain.SearchByAnalyst, By: &analyst}
	location := analystAt(incident)

	_, err := domain.NewSearch(claimOf(domain.IncidentWaterDamage), p, nil, nil, nil, nil, in, searchedAt)
	assert.ErrorIs(t, err, domain.ErrLocationMissing)

	_, err = domain.NewSearch(claimOf(domain.IncidentTheft), p, location, nil, nil, nil, in, searchedAt)
	assert.ErrorIs(t, err, domain.ErrClaimNotAssignable)

	paid := claimOf(domain.IncidentWaterDamage)
	paid.Status = domain.StatusPaid
	_, err = domain.NewSearch(paid, p, location, nil, nil, nil, in, searchedAt)
	assert.ErrorIs(t, err, domain.ErrClaimNotAssignable)

	_, err = domain.NewSearch(claimOf(domain.IncidentWaterDamage), p, location, nil, &domain.Assignment{}, nil, in,
		searchedAt)
	assert.ErrorIs(t, err, domain.ErrAssignmentExists)

	in.FromRadiusKm = float(7)
	_, err = domain.NewSearch(claimOf(domain.IncidentWaterDamage), p, location, nil, nil, nil, in, searchedAt)
	assert.Equal(t, map[string]string{"from_radius_km": "out_of_range"}, fieldsOf(t, err))
}

// searched is the claim of the mockup searched once from the analyst's pin.
func searched(t *testing.T) (domain.ClaimRecord, *domain.IncidentLocation, *domain.ProviderSearch) {
	t.Helper()
	c, location := claimOf(domain.IncidentWaterDamage), analystAt(incident)
	s, err := domain.NewSearch(c, domain.AssignmentPolicyV1(), location, nil, nil, mockupProviders(),
		domain.SearchRequest{Trigger: domain.SearchByAnalyst, By: &analyst}, searchedAt)
	require.NoError(t, err)
	return c, location, &s
}

func TestTheAnalystAssignsTheSuggestedProvider(t *testing.T) {
	c, location, s := searched(t)
	current := mockupProviders()[1]
	at := searchedAt.Add(time.Minute)

	a, err := domain.Assign(c, domain.AssignmentPolicyV1(), location, s, nil, "prv-servicios-hogar-norte", 1, &current,
		analyst, at)
	require.NoError(t, err)
	assert.Equal(t, c.ID, a.ClaimID)
	assert.Equal(t, "prv-servicios-hogar-norte", a.ProviderID)
	assert.Equal(t, "prv-servicios-hogar-norte@example.com", a.ContactEmail)
	assert.Equal(t, analyst, a.AssignedBy)
	assert.Equal(t, at, a.AssignedAt)
	assert.Equal(t, 2.3, domain.RoundKm(a.DistanceKm))
	assert.Equal(t, 1, a.SearchSeq)
	assert.Equal(t, *location, a.Location, "the location shared is the one searched from")
	assert.Equal(t, domain.AssignmentPolicyVersionV1, a.PolicyVersion)
	assert.Equal(t, "asignacion-"+c.ID, domain.AssignmentNoticeKey(c.ID))
}

func TestTheAnalystMayAssignAnotherEligibleProvider(t *testing.T) {
	c, location := claimOf(domain.IncidentWaterDamage), analystAt(incident)
	providers := []domain.Provider{provider("prv-near", 1, domain.SpecialtyPlumbing),
		provider("prv-farther", 4, domain.SpecialtyPlumbing)}
	s, err := domain.NewSearch(c, domain.AssignmentPolicyV1(), location, nil, nil, providers,
		domain.SearchRequest{Trigger: domain.SearchByAnalyst, By: &analyst}, searchedAt)
	require.NoError(t, err)

	a, err := domain.Assign(c, domain.AssignmentPolicyV1(), location, &s, nil, "prv-farther", 1, &providers[1], analyst,
		searchedAt)
	require.NoError(t, err)
	assert.InDelta(t, 4, a.DistanceKm, 1e-9)
}

func TestAProviderThatDoesNotMeetTheCriteriaIsNotAssigned(t *testing.T) {
	c, location, s := searched(t)
	p := domain.AssignmentPolicyV1()
	providers := mockupProviders()

	cases := []struct {
		name     string
		provider string
		current  *domain.Provider
		want     error
	}{
		{"unavailable", "prv-reparaciones-chapinero", &providers[2], domain.ErrProviderNotEligible},
		{"out of the radius", "prv-tecnicos-unidos", &providers[0], domain.ErrProviderNotEligible},
		{"without the specialty", "prv-taller-vehicular", &providers[3], domain.ErrProviderNotEligible},
		{"unknown", "prv-nobody", nil, domain.ErrProviderNotEligible},
	}
	for _, tc := range cases {
		_, err := domain.Assign(c, p, location, s, nil, tc.provider, 1, tc.current, analyst, searchedAt)
		assert.ErrorIs(t, err, tc.want, tc.name)
	}

	busy := providers[1]
	busy.Availability = domain.AvailabilityBusy
	_, err := domain.Assign(c, p, location, s, nil, "prv-servicios-hogar-norte", 1, &busy, analyst, searchedAt)
	assert.ErrorIs(t, err, domain.ErrProviderUnavailable, "it became busy after the search")
	_, err = domain.Assign(c, p, location, s, nil, "prv-servicios-hogar-norte", 1, nil, analyst, searchedAt)
	assert.ErrorIs(t, err, domain.ErrProviderUnavailable, "it no longer exists")
}

func TestAnAssignmentFromAnOutdatedSearchIsRefused(t *testing.T) {
	c, location, s := searched(t)
	p := domain.AssignmentPolicyV1()
	current := mockupProviders()[1]

	_, err := domain.Assign(c, p, location, s, nil, "prv-servicios-hogar-norte", 2, &current, analyst, searchedAt)
	assert.ErrorIs(t, err, domain.ErrSearchOutdated, "not the last search")
	_, err = domain.Assign(c, p, location, nil, nil, "prv-servicios-hogar-norte", 1, &current, analyst, searchedAt)
	assert.ErrorIs(t, err, domain.ErrSearchOutdated, "no search")
	_, err = domain.Assign(c, p, analystAt(north(1)), s, nil, "prv-servicios-hogar-norte", 1, &current, analyst,
		searchedAt)
	assert.ErrorIs(t, err, domain.ErrSearchOutdated, "the location changed after the search")

	_, err = domain.Assign(c, p, location, s, &domain.Assignment{}, "prv-servicios-hogar-norte", 1, &current, analyst,
		searchedAt)
	assert.ErrorIs(t, err, domain.ErrAssignmentExists)
	c.Status = domain.StatusRejected
	_, err = domain.Assign(c, p, location, s, nil, "prv-servicios-hogar-norte", 1, &current, analyst, searchedAt)
	assert.ErrorIs(t, err, domain.ErrClaimNotAssignable)
}

func TestTheStateOfTheAssignmentOfAClaim(t *testing.T) {
	p := domain.AssignmentPolicyV1()
	c, location, found := searched(t)
	exhausted := *found
	exhausted.Result.Outcome = domain.SearchExhausted
	approved := c
	approved.Status = domain.StatusApproved

	cases := []struct {
		name     string
		claim    domain.ClaimRecord
		location *domain.IncidentLocation
		last     *domain.ProviderSearch
		assigned *domain.Assignment
		want     domain.AssignmentState
	}{
		{"theft", claimOf(domain.IncidentTheft), nil, nil, nil, domain.AssignmentNotRequired},
		{"not in evaluation", approved, location, nil, nil, domain.AssignmentNotApplicable},
		{"no location", c, nil, nil, nil, domain.AssignmentLocationMissing},
		{"located", c, location, nil, nil, domain.AssignmentReady},
		{"located elsewhere after the search", c, analystAt(north(1)), found, nil, domain.AssignmentReady},
		{"found", c, location, found, nil, domain.AssignmentProposed},
		{"exhausted", c, location, &exhausted, nil, domain.AssignmentPending},
		{"assigned", c, location, found, &domain.Assignment{}, domain.AssignmentAssigned},
		{"assigned and approved since", approved, location, found, &domain.Assignment{}, domain.AssignmentAssigned},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, domain.AssignmentStateOf(tc.claim, p, tc.location, tc.last, tc.assigned), tc.name)
	}
}
