package assessors_test

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/assessors"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// The location, the search and the assignment of the provider of a claim
// (HU-W2.3-01), over the stores in memory.

type fixedClock struct {
	mu sync.Mutex
	at time.Time
}

func (c *fixedClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *fixedClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

var (
	start    = time.Date(2026, 9, 6, 15, 14, 2, 0, time.UTC)
	incident = domain.Coordinates{Latitude: 4.6771, Longitude: -74.0483}
	analyst  = domain.Analyst{Email: "andres.ruiz@solventa.example", Name: "Andrés Ruiz"}
)

// north is the point km kilometers north of the incident.
func north(km float64) domain.Coordinates {
	return domain.Coordinates{Latitude: incident.Latitude + km/(2*math.Pi*domain.EarthRadiusKm/360),
		Longitude: incident.Longitude}
}

func provider(id string, km float64, specialties ...domain.Specialty) domain.Provider {
	if len(specialties) == 0 {
		specialties = []domain.Specialty{domain.SpecialtyPlumbing}
	}
	return domain.Provider{ID: id, Name: "Prestador " + id, Specialties: specialties, Location: north(km),
		Address: "Calle 100 # 15-20, Bogotá", ContactEmail: id + "@example.com", Availability: domain.AvailabilityAvailable,
		UpdatedBy: domain.ReferenceDataAuthor, UpdatedAt: start}
}

func unavailable(p domain.Provider) domain.Provider {
	p.Availability = domain.AvailabilityUnavailable
	return p
}

// mockup is the registry of the screen "Asignar prestador": the nearest is
// available, the second unavailable and the third out of the initial radius.
func mockup() []domain.Provider {
	return []domain.Provider{
		provider("prv-servicios-hogar-norte", 2.3, domain.SpecialtyPlumbing, domain.SpecialtyDampProofing),
		unavailable(provider("prv-reparaciones-chapinero", 3.8)),
		provider("prv-tecnicos-unidos", 6.1),
		provider("prv-vidrios-capital", 1.0, domain.SpecialtyAutoGlass),
	}
}

func fromApp(accuracyM float64) domain.Evidence {
	return domain.Evidence{ObjectKey: "evidence/photo-1.jpg", Kind: domain.KindPhoto, CapturedAt: start.Add(-time.Hour),
		Latitude: incident.Latitude, Longitude: incident.Longitude, AccuracyM: accuracyM, LocationTrusted: true,
		SHA256: "ab", ByteSize: 10}
}

type scene struct {
	cases     *memory.CaseStore
	providers *memory.ProviderStore
	store     *memory.AssignmentStore
	clock     *fixedClock
	svc       *assessors.Service
	woken     int
}

func newScene(t *testing.T, providers ...domain.Provider) *scene {
	t.Helper()
	s := &scene{cases: memory.NewCaseStore(), providers: memory.NewProviderStore(providers...),
		clock: &fixedClock{at: start}}
	s.store = memory.NewAssignmentStore(s.cases, s.providers)
	s.svc = assessors.NewService(s.store, s.providers, s.clock, func() { s.woken++ })
	return s
}

// file files a claim of the type in the status, with the evidence given.
func (s *scene) file(t *testing.T, id string, incidentType domain.IncidentType, status domain.Status,
	evidence ...domain.Evidence) {
	t.Helper()
	_, _, err := s.cases.CreateWeb(context.Background(), domain.ClaimRecord{
		Claim: domain.Claim{ID: id, Subject: "sub-carolina", FilingKey: "key-" + id, PolicyID: "HG-2025-880231",
			IncidentType: incidentType, OccurredAt: start.Add(-48 * time.Hour), Place: "Carrera 15 # 93-40",
			Description: "Filtración.", Status: status, FiledAt: start.Add(-24 * time.Hour), Evidence: evidence},
		CatalogVersion: domain.CatalogVersionV1,
	})
	require.NoError(t, err)
}

func (s *scene) view(t *testing.T, id string) assessors.View {
	t.Helper()
	v, err := s.svc.View(context.Background(), id)
	require.NoError(t, err)
	return v
}

func (s *scene) search(t *testing.T, id string) assessors.View {
	t.Helper()
	v, err := s.svc.Search(context.Background(), id, nil, analyst)
	require.NoError(t, err)
	return v
}

func (s *scene) locate(t *testing.T, id string, at domain.Coordinates) assessors.View {
	t.Helper()
	v, err := s.svc.RecordLocation(context.Background(), id, domain.LocationRequest{
		Address: "Carrera 15 # 93-40, apto 402, Bogotá", Latitude: &at.Latitude, Longitude: &at.Longitude}, analyst)
	require.NoError(t, err)
	return v
}

func radius(km float64) *float64 { return &km }

func TestOnlyAClaimInEvaluationIsAssigned(t *testing.T) {
	s := newScene(t, mockup()...)
	for _, status := range []domain.Status{domain.StatusActive, domain.StatusReturned, domain.StatusApproved,
		domain.StatusRejected, domain.StatusPaid} {
		id := "cl-" + string(status)
		s.file(t, id, domain.IncidentWaterDamage, status, fromApp(8))
		assert.Equal(t, domain.AssignmentNotApplicable, s.view(t, id).State, status)

		_, err := s.svc.Search(context.Background(), id, nil, analyst)
		assert.ErrorIs(t, err, domain.ErrClaimNotAssignable, status)
		_, err = s.svc.Assign(context.Background(), id, "prv-servicios-hogar-norte", 1, analyst)
		assert.ErrorIs(t, err, domain.ErrClaimNotAssignable, status)
	}
	s.file(t, "cl-web", domain.IncidentWaterDamage, domain.StatusActive)
	_, err := s.svc.RecordLocation(context.Background(), "cl-web", domain.LocationRequest{}, analyst)
	assert.ErrorIs(t, err, domain.ErrClaimNotAssignable)
}

func TestATheftNeedsNoVisit(t *testing.T) {
	s := newScene(t, mockup()...)
	s.file(t, "cl-1", domain.IncidentTheft, domain.StatusInEvaluation, fromApp(8))
	v := s.view(t, "cl-1")
	assert.Equal(t, domain.AssignmentNotRequired, v.State)
	assert.Empty(t, v.Specialty)
	assert.Equal(t, domain.LineAuto, v.Line)
	_, err := s.svc.Search(context.Background(), "cl-1", nil, analyst)
	assert.ErrorIs(t, err, domain.ErrClaimNotAssignable)
}

func TestAClaimTypeThePolicyDoesNotKnowNeedsNoVisit(t *testing.T) {
	s := newScene(t, mockup()...)
	s.file(t, "cl-1", "fire", domain.StatusInEvaluation, fromApp(8))
	assert.Equal(t, domain.AssignmentNotRequired, s.view(t, "cl-1").State)
}

func TestAClaimWithoutLocationIsNotSearched(t *testing.T) {
	s := newScene(t, mockup()...)
	s.file(t, "cl-1", domain.IncidentWaterDamage, domain.StatusInEvaluation, fromApp(65))
	v := s.view(t, "cl-1")
	assert.Equal(t, domain.AssignmentLocationMissing, v.State, "a capture of 65 m is not trusted")
	assert.Nil(t, v.Location)

	_, err := s.svc.Search(context.Background(), "cl-1", nil, analyst)
	assert.ErrorIs(t, err, domain.ErrLocationMissing)
}

func TestTheLocationOfTheAppIsTheMostPreciseTrustedCapture(t *testing.T) {
	s := newScene(t, mockup()...)
	s.file(t, "cl-1", domain.IncidentWaterDamage, domain.StatusInEvaluation, fromApp(30), fromApp(8), fromApp(12))
	v := s.view(t, "cl-1")
	assert.Equal(t, domain.AssignmentReady, v.State)
	require.NotNil(t, v.Location)
	assert.Equal(t, domain.LocationFromApp, v.Location.Source)
	assert.Equal(t, 8.0, *v.Location.AccuracyM)
	assert.Equal(t, domain.SpecialtyPlumbing, v.Specialty)
	assert.Equal(t, domain.AssignmentPolicyVersionV1, v.PolicyVersion)

	_, err := s.svc.RecordLocation(context.Background(), "cl-1", domain.LocationRequest{
		Address: "Carrera 15 # 93-40", Latitude: &incident.Latitude, Longitude: &incident.Longitude}, analyst)
	assert.ErrorIs(t, err, domain.ErrLocationFromApp)
}

func TestTheAnalystRecordsTheLocationOfAClaimWithoutCoordinates(t *testing.T) {
	s := newScene(t, mockup()...)
	s.file(t, "cl-1", domain.IncidentWaterDamage, domain.StatusInEvaluation)
	v := s.locate(t, "cl-1", north(30))
	assert.Equal(t, domain.AssignmentReady, v.State)
	require.NotNil(t, v.Location)
	assert.Equal(t, domain.LocationFromAnalyst, v.Location.Source)
	assert.Equal(t, 1, v.Location.Seq)
	assert.Equal(t, analyst, *v.Location.RecordedBy)
	assert.Equal(t, start, v.Location.RecordedAt)
	assert.Nil(t, v.Location.AccuracyM)

	// The analyst corrects the pin before assigning: the last one counts.
	v = s.locate(t, "cl-1", incident)
	assert.Equal(t, 2, v.Location.Seq)
	assert.Equal(t, incident, v.Location.Point)

	_, err := s.svc.RecordLocation(context.Background(), "cl-1", domain.LocationRequest{Address: "x"}, analyst)
	var invalid *domain.ValidationError
	require.ErrorAs(t, err, &invalid)
	assert.Len(t, invalid.Fields, 3)
}

func TestASearchProposesTheNearestAvailableProviderOfTheSpecialty(t *testing.T) {
	s := newScene(t, mockup()...)
	s.file(t, "cl-1", domain.IncidentWaterDamage, domain.StatusInEvaluation, fromApp(8))
	v := s.search(t, "cl-1")

	assert.Equal(t, domain.AssignmentProposed, v.State)
	r := v.LastSearch
	require.NotNil(t, r)
	assert.Equal(t, 1, r.Seq)
	assert.Equal(t, domain.SearchByAnalyst, r.Trigger)
	assert.Equal(t, analyst, *r.SearchedBy)
	assert.Equal(t, domain.SpecialtyPlumbing, r.Specialty)
	assert.Equal(t, domain.SearchFound, r.Result.Outcome)
	assert.Equal(t, 5.0, r.Result.RadiusKm)
	assert.Equal(t, []domain.SearchAttempt{{RadiusKm: 5, EligibleCount: 1, AttemptedAt: start}}, r.Result.Attempts)

	require.Len(t, r.Result.Candidates, 3, "only the providers of the specialty")
	ids, eligibility := []string{}, []domain.Eligibility{}
	for _, c := range r.Result.Candidates {
		ids, eligibility = append(ids, c.ProviderID), append(eligibility, c.Eligibility)
	}
	assert.Equal(t, []string{"prv-servicios-hogar-norte", "prv-reparaciones-chapinero", "prv-tecnicos-unidos"}, ids)
	assert.Equal(t, []domain.Eligibility{domain.Eligible, domain.NotEligibleUnavailable, domain.NotEligibleOutOfRadius},
		eligibility)
	suggested, ok := r.Result.Suggested()
	require.True(t, ok)
	assert.Equal(t, "prv-servicios-hogar-norte", suggested.ProviderID)
	assert.Equal(t, 2.3, domain.RoundKm(suggested.DistanceKm))
}

func TestASearchWidensTheRadiusAndRecordsEachAttempt(t *testing.T) {
	s := newScene(t, provider("prv-lejano", 7))
	s.file(t, "cl-1", domain.IncidentWaterDamage, domain.StatusInEvaluation, fromApp(8))
	r := s.search(t, "cl-1").LastSearch
	assert.Equal(t, domain.SearchFound, r.Result.Outcome)
	assert.Equal(t, 10.0, r.Result.RadiusKm)
	assert.Equal(t, []domain.SearchAttempt{{RadiusKm: 5, AttemptedAt: start},
		{RadiusKm: 10, EligibleCount: 1, AttemptedAt: start}}, r.Result.Attempts)
}

func TestWithoutEligibleProvidersTheClaimIsPendingAssignment(t *testing.T) {
	s := newScene(t, unavailable(provider("prv-cerca", 1)), provider("prv-muy-lejos", 25))
	s.file(t, "cl-1", domain.IncidentWaterDamage, domain.StatusInEvaluation, fromApp(8))
	v := s.search(t, "cl-1")

	assert.Equal(t, domain.AssignmentPending, v.State)
	r := v.LastSearch.Result
	assert.Equal(t, domain.SearchExhausted, r.Outcome)
	assert.Equal(t, domain.ReasonNoEligibleProvider, r.Reason)
	assert.Equal(t, 20.0, r.RadiusKm)
	assert.Len(t, r.Attempts, 3)
	_, suggested := r.Suggested()
	assert.False(t, suggested, "nobody who does not qualify is proposed")

	_, err := s.svc.Assign(context.Background(), "cl-1", "prv-muy-lejos", 1, analyst)
	assert.ErrorIs(t, err, domain.ErrProviderNotEligible)
	_, err = s.svc.Assign(context.Background(), "cl-1", "prv-cerca", 1, analyst)
	assert.ErrorIs(t, err, domain.ErrProviderNotEligible)
}

func TestTheAnalystWidensTheRadiusFromTheNextOne(t *testing.T) {
	s := newScene(t, mockup()...)
	s.file(t, "cl-1", domain.IncidentWaterDamage, domain.StatusInEvaluation, fromApp(8))
	s.search(t, "cl-1")

	v, err := s.svc.Search(context.Background(), "cl-1", radius(10), analyst)
	require.NoError(t, err)
	r := v.LastSearch
	assert.Equal(t, 2, r.Seq)
	assert.Equal(t, 10.0, r.Result.FromRadiusKm)
	assert.Equal(t, []domain.SearchAttempt{{RadiusKm: 10, EligibleCount: 2, AttemptedAt: start}}, r.Result.Attempts)
	assert.Equal(t, domain.Eligible, r.Result.Candidates[2].Eligibility, "Técnicos Unidos is within 10 km")

	_, err = s.svc.Search(context.Background(), "cl-1", radius(7.5), analyst)
	var invalid *domain.ValidationError
	require.ErrorAs(t, err, &invalid)
	assert.Equal(t, []domain.FieldError{{Field: "from_radius_km", Code: domain.CodeOutOfRange}}, invalid.Fields)
}

func TestTheAnalystAssignsTheSuggestedProvider(t *testing.T) {
	s := newScene(t, mockup()...)
	s.file(t, "cl-1", domain.IncidentWaterDamage, domain.StatusInEvaluation, fromApp(8))
	s.search(t, "cl-1")
	s.clock.advance(time.Minute)

	v, err := s.svc.Assign(context.Background(), "cl-1", "prv-servicios-hogar-norte", 1, analyst)
	require.NoError(t, err)
	assert.Equal(t, domain.AssignmentAssigned, v.State)
	a := v.Assignment
	require.NotNil(t, a)
	assert.Equal(t, "prv-servicios-hogar-norte", a.ProviderID)
	assert.Equal(t, "prv-servicios-hogar-norte@example.com", a.ContactEmail)
	assert.Equal(t, analyst, a.AssignedBy)
	assert.Equal(t, start.Add(time.Minute), a.AssignedAt)
	assert.Equal(t, 2.3, domain.RoundKm(a.DistanceKm))
	assert.Equal(t, 1, a.SearchSeq)
	assert.Equal(t, domain.LocationFromApp, a.Location.Source)
	assert.Equal(t, 8.0, *a.Location.AccuracyM)

	require.NotNil(t, v.Notice)
	assert.Equal(t, ports.NoticePending, v.Notice.Status)
	assert.Equal(t, a.ContactEmail, v.Notice.To)
	assert.Contains(t, v.Notice.Subject, v.Claim.FilingNumber)
	assert.Equal(t, 1, s.woken, "the worker sends the notice at once")

	p, err := s.providers.Get(context.Background(), "prv-servicios-hogar-norte")
	require.NoError(t, err)
	assert.Equal(t, domain.AvailabilityBusy, p.Availability)
	assert.Equal(t, "cl-1", *p.BusyClaimID)

	c, err := s.cases.Get(context.Background(), "cl-1")
	require.NoError(t, err)
	assert.Equal(t, domain.StatusInEvaluation, c.Status, "the status of the claim does not change")
}

func TestTheAnalystMayAssignAnotherEligibleProvider(t *testing.T) {
	s := newScene(t, mockup()...)
	s.file(t, "cl-1", domain.IncidentWaterDamage, domain.StatusInEvaluation, fromApp(8))
	s.search(t, "cl-1")
	_, err := s.svc.Search(context.Background(), "cl-1", radius(10), analyst)
	require.NoError(t, err)

	v, err := s.svc.Assign(context.Background(), "cl-1", "prv-tecnicos-unidos", 2, analyst)
	require.NoError(t, err)
	assert.Equal(t, "prv-tecnicos-unidos", v.Assignment.ProviderID)
	assert.Nil(t, v.Assignment.ContactPhone)
}

func TestAnAssignmentFromAnOutdatedSearchIsRefused(t *testing.T) {
	s := newScene(t, mockup()...)
	s.file(t, "cl-1", domain.IncidentWaterDamage, domain.StatusInEvaluation)
	s.locate(t, "cl-1", incident)
	s.search(t, "cl-1")
	s.search(t, "cl-1")

	_, err := s.svc.Assign(context.Background(), "cl-1", "prv-servicios-hogar-norte", 1, analyst)
	assert.ErrorIs(t, err, domain.ErrSearchOutdated, "not the last search")

	v := s.locate(t, "cl-1", north(0.5))
	assert.Equal(t, domain.AssignmentReady, v.State, "the location changed after the search")
	_, err = s.svc.Assign(context.Background(), "cl-1", "prv-servicios-hogar-norte", 2, analyst)
	assert.ErrorIs(t, err, domain.ErrSearchOutdated)
}

func TestAProviderThatStoppedBeingAvailableIsNotAssigned(t *testing.T) {
	s := newScene(t, mockup()...)
	s.file(t, "cl-1", domain.IncidentWaterDamage, domain.StatusInEvaluation, fromApp(8))
	s.file(t, "cl-2", domain.IncidentWaterDamage, domain.StatusInEvaluation, fromApp(8))
	s.search(t, "cl-1")
	s.search(t, "cl-2")

	_, err := s.svc.Assign(context.Background(), "cl-1", "prv-servicios-hogar-norte", 1, analyst)
	require.NoError(t, err)
	_, err = s.svc.Assign(context.Background(), "cl-2", "prv-servicios-hogar-norte", 1, analyst)
	assert.ErrorIs(t, err, domain.ErrProviderUnavailable, "it is busy with the other claim")
	assert.Equal(t, domain.AssignmentProposed, s.view(t, "cl-2").State)

	// A search after it does not propose it any more.
	v := s.search(t, "cl-2")
	assert.Equal(t, domain.NotEligibleUnavailable, v.LastSearch.Result.Candidates[0].Eligibility)
	assert.Equal(t, domain.AvailabilityBusy, v.LastSearch.Result.Candidates[0].Availability)
}

func TestAClaimHasOneAssignment(t *testing.T) {
	s := newScene(t, mockup()...)
	s.file(t, "cl-1", domain.IncidentWaterDamage, domain.StatusInEvaluation)
	s.locate(t, "cl-1", incident)
	s.search(t, "cl-1")
	_, err := s.svc.Assign(context.Background(), "cl-1", "prv-servicios-hogar-norte", 1, analyst)
	require.NoError(t, err)

	_, err = s.svc.Assign(context.Background(), "cl-1", "prv-servicios-hogar-norte", 1, analyst)
	assert.ErrorIs(t, err, domain.ErrAssignmentExists)
	_, err = s.svc.Search(context.Background(), "cl-1", nil, analyst)
	assert.ErrorIs(t, err, domain.ErrAssignmentExists)
	_, err = s.svc.RecordLocation(context.Background(), "cl-1", domain.LocationRequest{}, analyst)
	assert.ErrorIs(t, err, domain.ErrAssignmentExists)
}

func TestAnAssignmentStaysVisibleAfterTheClaimLeavesEvaluation(t *testing.T) {
	s := newScene(t, mockup()...)
	s.file(t, "cl-1", domain.IncidentWaterDamage, domain.StatusInEvaluation, fromApp(8))
	s.search(t, "cl-1")
	_, err := s.svc.Assign(context.Background(), "cl-1", "prv-servicios-hogar-norte", 1, analyst)
	require.NoError(t, err)
	_, err = memory.NewDecisionStore(s.cases).Decide(context.Background(), domain.Decision{ClaimID: "cl-1",
		Outcome: domain.OutcomeRejected, DecidedBy: analyst.Email, DecidedAt: start, Reason: "Sin cobertura.",
		Notice: &domain.Notice{To: "carolina@example.com"}})
	require.NoError(t, err)
	assert.Equal(t, domain.AssignmentAssigned, s.view(t, "cl-1").State)
}

func TestTheSummariesFollowTheOrderOfTheIds(t *testing.T) {
	s := newScene(t, mockup()...)
	s.file(t, "cl-assigned", domain.IncidentWaterDamage, domain.StatusInEvaluation, fromApp(8))
	s.file(t, "cl-pending", domain.IncidentGlass, domain.StatusInEvaluation, fromApp(8))
	s.file(t, "cl-theft", domain.IncidentTheft, domain.StatusInEvaluation)
	s.search(t, "cl-assigned")
	_, err := s.svc.Assign(context.Background(), "cl-assigned", "prv-servicios-hogar-norte", 1, analyst)
	require.NoError(t, err)
	require.NoError(t, s.providers.Create(context.Background(), unavailable(provider("prv-vidrios-otro", 3,
		domain.SpecialtyAutoGlass))))
	_, err = s.providers.SetAvailability(context.Background(), "prv-vidrios-capital", domain.AvailabilityUnavailable,
		nil, analyst.Email, start)
	require.NoError(t, err)
	s.search(t, "cl-pending")

	got, err := s.svc.Summaries(context.Background(), []string{"cl-pending", "cl-missing", "cl-assigned", "cl-theft"})
	require.NoError(t, err)
	require.Len(t, got, 4)
	assert.Equal(t, assessors.Summary{ClaimID: "cl-pending", State: domain.AssignmentPending,
		Specialty: domain.SpecialtyAutoGlass, Reason: domain.ReasonNoEligibleProvider}, got[0])
	assert.Equal(t, assessors.Summary{ClaimID: "cl-missing", State: domain.AssignmentNotFound}, got[1])
	assert.Equal(t, domain.AssignmentAssigned, got[2].State)
	assert.Equal(t, "Prestador prv-servicios-hogar-norte", got[2].ProviderName)
	assert.Equal(t, 2.3, domain.RoundKm(*got[2].DistanceKm))
	assert.Equal(t, start, got[2].AssignedAt)
	assert.Equal(t, assessors.Summary{ClaimID: "cl-theft", State: domain.AssignmentNotRequired}, got[3])
}

func TestAClaimThatDoesNotExist(t *testing.T) {
	s := newScene(t)
	_, err := s.svc.View(context.Background(), "cl-x")
	assert.ErrorIs(t, err, ports.ErrClaimNotFound)
	_, err = s.svc.Search(context.Background(), "cl-x", nil, analyst)
	assert.ErrorIs(t, err, ports.ErrClaimNotFound)
	_, err = s.svc.Assign(context.Background(), "cl-x", "prv-x", 1, analyst)
	assert.ErrorIs(t, err, ports.ErrClaimNotFound)
	_, err = s.svc.RecordLocation(context.Background(), "cl-x", domain.LocationRequest{}, analyst)
	assert.ErrorIs(t, err, ports.ErrClaimNotFound)
	_, err = s.svc.Retry(context.Background(), "cl-x")
	assert.ErrorIs(t, err, ports.ErrClaimNotFound)
}

func TestTheRetrySearchesOnlyAClaimPendingAssignmentAndNeverAssigns(t *testing.T) {
	s := newScene(t, unavailable(provider("prv-cerca", 1)))
	s.file(t, "cl-1", domain.IncidentWaterDamage, domain.StatusInEvaluation, fromApp(8))
	s.search(t, "cl-1")

	state, err := s.svc.Retry(context.Background(), "cl-1")
	require.NoError(t, err)
	assert.Equal(t, domain.AssignmentPending, state, "still nobody")

	_, err = s.providers.SetAvailability(context.Background(), "prv-cerca", domain.AvailabilityAvailable, nil,
		analyst.Email, start)
	require.NoError(t, err)
	state, err = s.svc.Retry(context.Background(), "cl-1")
	require.NoError(t, err)
	assert.Equal(t, domain.AssignmentProposed, state)

	v := s.view(t, "cl-1")
	r := v.LastSearch
	assert.Equal(t, 3, r.Seq)
	assert.Equal(t, domain.SearchByRetry, r.Trigger)
	assert.Nil(t, r.SearchedBy)
	assert.Nil(t, v.Assignment, "the analyst confirms")

	state, err = s.svc.Retry(context.Background(), "cl-1")
	require.NoError(t, err)
	assert.Equal(t, domain.AssignmentProposed, state)
	assert.Equal(t, 3, s.view(t, "cl-1").LastSearch.Seq, "a proposed claim is not searched again")
}

func TestSuggestFindsWhatTheSearchOfAClaimFinds(t *testing.T) {
	s := newScene(t, mockup()...)
	s.file(t, "cl-1", domain.IncidentWaterDamage, domain.StatusInEvaluation, fromApp(8))
	r := s.search(t, "cl-1").LastSearch.Result

	got, err := assessors.Suggest(context.Background(), s.providers, incident, domain.SpecialtyPlumbing,
		s.svc.Policy(), start)
	require.NoError(t, err)
	assert.Equal(t, r, got)

	_, err = assessors.Suggest(context.Background(), failingProviders{s.providers}, incident,
		domain.SpecialtyPlumbing, s.svc.Policy(), start)
	assert.ErrorIs(t, err, errBoom)
}

func TestThePolicyIsACopy(t *testing.T) {
	s := newScene(t)
	p := s.svc.Policy()
	p.RadiiKm[0] = 1
	assert.Equal(t, domain.AssignmentPolicyV1(), s.svc.Policy())
}

var errBoom = errors.New("boom")

// failingProviders is a registry that cannot be read.
type failingProviders struct{ ports.ProviderStore }

func (failingProviders) WithSpecialty(context.Context, domain.Specialty) ([]domain.Provider, error) {
	return nil, errBoom
}

func (failingProviders) Get(context.Context, string) (domain.Provider, error) {
	return domain.Provider{}, errBoom
}

// racingStore is a store where another task records first: the next
// location or search of the claim loses its place once.
type racingStore struct {
	ports.AssignmentStore
	locations, searches int
	failAssign          error
}

func (r *racingStore) RecordLocation(ctx context.Context, claimID string, l domain.IncidentLocation) error {
	if r.locations > 0 {
		r.locations--
		other := l
		other.Address = "Otra dirección del otro analista"
		if err := r.AssignmentStore.RecordLocation(ctx, claimID, other); err != nil {
			return err
		}
		return ports.ErrLocationConflict
	}
	return r.AssignmentStore.RecordLocation(ctx, claimID, l)
}

func (r *racingStore) RecordSearch(ctx context.Context, s domain.ProviderSearch) error {
	if r.searches > 0 {
		r.searches--
		other := s
		other.Trigger, other.SearchedBy = domain.SearchByRetry, nil
		if err := r.AssignmentStore.RecordSearch(ctx, other); err != nil {
			return err
		}
		return ports.ErrSearchConflict
	}
	return r.AssignmentStore.RecordSearch(ctx, s)
}

func (r *racingStore) Assign(ctx context.Context, a domain.Assignment, n ports.ProviderNotice) error {
	if r.failAssign != nil {
		return r.failAssign
	}
	return r.AssignmentStore.Assign(ctx, a, n)
}

func TestAWriteThatLosesItsPlaceToAnotherTask(t *testing.T) {
	s := newScene(t, mockup()...)
	s.file(t, "cl-1", domain.IncidentWaterDamage, domain.StatusInEvaluation)
	racing := &racingStore{AssignmentStore: s.store, locations: 1, searches: 1}
	svc := assessors.NewService(racing, s.providers, s.clock, nil)

	v, err := svc.RecordLocation(context.Background(), "cl-1", domain.LocationRequest{Address: "Carrera 15 # 93-40",
		Latitude: &incident.Latitude, Longitude: &incident.Longitude}, analyst)
	require.NoError(t, err)
	assert.Equal(t, 2, v.Location.Seq, "this location goes after the other one")
	assert.Equal(t, "Carrera 15 # 93-40", v.Location.Address)

	v, err = svc.Search(context.Background(), "cl-1", nil, analyst)
	require.NoError(t, err)
	assert.Equal(t, 1, v.LastSearch.Seq)
	assert.Equal(t, domain.SearchByRetry, v.LastSearch.Trigger, "the view shows the search that won")

	racing.locations = maxConflicts
	_, err = svc.RecordLocation(context.Background(), "cl-1", domain.LocationRequest{Address: "Carrera 15 # 93-40",
		Latitude: &incident.Latitude, Longitude: &incident.Longitude}, analyst)
	assert.ErrorIs(t, err, ports.ErrLocationConflict, "it gives up after a few tries")
}

// maxConflicts is how many times the service makes a write again.
const maxConflicts = 3

func TestTheErrorsOfTheStoresReachTheCaller(t *testing.T) {
	s := newScene(t, mockup()...)
	s.file(t, "cl-1", domain.IncidentWaterDamage, domain.StatusInEvaluation, fromApp(8))
	s.search(t, "cl-1")

	broken := assessors.NewService(s.store, failingProviders{s.providers}, s.clock, nil)
	_, err := broken.Search(context.Background(), "cl-1", nil, analyst)
	assert.ErrorIs(t, err, errBoom)
	_, err = broken.Assign(context.Background(), "cl-1", "prv-servicios-hogar-norte", 1, analyst)
	assert.ErrorIs(t, err, errBoom)

	failing := assessors.NewService(&racingStore{AssignmentStore: s.store, failAssign: errBoom}, s.providers, s.clock, nil)
	_, err = failing.Assign(context.Background(), "cl-1", "prv-servicios-hogar-norte", 1, analyst)
	assert.ErrorIs(t, err, errBoom)
	assert.Nil(t, s.view(t, "cl-1").Assignment)

	// A provider that left the registry is not available.
	_, err = s.svc.Assign(context.Background(), "cl-1", "prv-no-existe", 1, analyst)
	assert.ErrorIs(t, err, domain.ErrProviderNotEligible)
}
