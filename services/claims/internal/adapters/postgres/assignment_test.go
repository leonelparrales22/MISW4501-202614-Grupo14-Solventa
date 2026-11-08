package postgres_test

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/assessors"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// The assignment of the provider in PostgreSQL (HU-W2.3-01): what is written
// reads back the same, the history only grows, and of two analysts or two
// claims racing for the same thing only one wins, with nothing half written.

type clockAt struct {
	mu sync.Mutex
	at time.Time
}

func (c *clockAt) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *clockAt) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

var (
	// incidentAt is the place of the claim of the mockup, near the reference
	// providers of 0009_provider_seed.
	incidentAt  = domain.Coordinates{Latitude: 4.6771, Longitude: -74.0483}
	assignedBy  = domain.Analyst{Email: "andres.ruiz@solventa.example", Name: "Andrés Ruiz"}
	searchStart = filedAt.Add(48 * time.Hour)
)

type assignmentStores struct {
	db        *sql.DB
	claims    *postgres.ClaimRecordStore
	store     *postgres.AssignmentStore
	providers *postgres.ProviderStore
	clock     *clockAt
	svc       *assessors.Service
}

func newAssignmentStores(t *testing.T) assignmentStores {
	t.Helper()
	requireDocker(t)
	db := newDB(t)
	records := postgres.NewClaimRecordStore(db)
	s := assignmentStores{db: db, claims: records, store: postgres.AssignmentStoreOf(records),
		providers: postgres.ProviderStoreOf(records), clock: &clockAt{at: searchStart}}
	s.svc = assessors.NewService(s.store, s.providers, s.clock, nil)
	return s
}

func capture(position int, accuracyM float64, trusted bool) domain.Evidence {
	return domain.Evidence{ObjectKey: "evidence/" + string(rune('a'+position)) + ".jpg", Kind: domain.KindPhoto,
		CapturedAt: filedAt.Add(-time.Duration(position) * time.Minute), Latitude: incidentAt.Latitude,
		Longitude: incidentAt.Longitude, AccuracyM: accuracyM, LocationTrusted: trusted, SHA256: "ab", ByteSize: 10}
}

// file stores a claim of water damage filed from the phone with the evidence
// given, and puts it in evaluation.
func (s assignmentStores) file(t *testing.T, id string, evidence ...domain.Evidence) {
	t.Helper()
	c := claim(id, "sub-carolina", "key-"+id)
	c.IncidentType, c.Evidence = domain.IncidentWaterDamage, evidence
	_, _, err := s.claims.Create(context.Background(), c)
	require.NoError(t, err)
	s.move(t, id, domain.StatusInEvaluation)
}

func (s assignmentStores) move(t *testing.T, id string, status domain.Status) {
	t.Helper()
	_, err := s.db.Exec(`UPDATE claim SET status = $2 WHERE id = $1`, id, string(status))
	require.NoError(t, err)
}

func (s assignmentStores) search(t *testing.T, id string) assessors.View {
	t.Helper()
	v, err := s.svc.Search(context.Background(), id, nil, assignedBy)
	require.NoError(t, err)
	return v
}

func (s assignmentStores) setAvailability(t *testing.T, id string, a domain.Availability, until *time.Time) {
	t.Helper()
	_, err := s.providers.SetAvailability(context.Background(), id, a, until, assignedBy.Email, searchStart)
	require.NoError(t, err)
}

func TestTheLocationOfTheAppIsReadFromTheEvidence(t *testing.T) {
	s := newAssignmentStores(t)
	s.file(t, "cl-1", capture(0, 30, true), capture(1, 8, true), capture(2, 3, false))

	k, err := s.store.Case(context.Background(), "cl-1")
	require.NoError(t, err)
	assert.Len(t, k.Claim.Evidence, 3)
	l := domain.LocationOf(k.Claim, k.Location)
	require.NotNil(t, l)
	assert.Equal(t, domain.LocationFromApp, l.Source)
	assert.Equal(t, 8.0, *l.AccuracyM, "the most precise trusted capture")
	assert.Equal(t, filedAt.Add(-time.Minute), l.RecordedAt)
	assert.Nil(t, k.Location)
	assert.Nil(t, k.LastSearch)
	assert.Nil(t, k.Assignment)
}

func TestASearchReadsBackAsItWasMade(t *testing.T) {
	s := newAssignmentStores(t)
	s.file(t, "cl-1", capture(0, 8, true))
	back := time.Date(2026, 10, 27, 0, 0, 0, 0, domain.ColombiaTime)
	s.setAvailability(t, "prv-reparaciones-chapinero", domain.AvailabilityUnavailable, &back)
	want, err := assessors.Suggest(context.Background(), s.providers, incidentAt, domain.SpecialtyPlumbing,
		domain.AssignmentPolicyV1(), searchStart)
	require.NoError(t, err)

	v := s.search(t, "cl-1")
	assert.Equal(t, domain.AssignmentProposed, v.State)
	r := v.LastSearch
	require.NotNil(t, r)
	assert.Equal(t, want, r.Result)
	assert.Equal(t, 1, r.Seq)
	assert.Equal(t, domain.SearchByAnalyst, r.Trigger)
	assert.Equal(t, assignedBy, *r.SearchedBy)
	assert.Equal(t, domain.SpecialtyPlumbing, r.Specialty)
	assert.Equal(t, domain.AssignmentPolicyVersionV1, r.PolicyVersion)
	assert.Equal(t, searchStart, r.SearchedAt)
	assert.Equal(t, domain.LocationFromApp, r.Location.Source)
	assert.Equal(t, incidentAt, r.Location.Point)
	assert.Equal(t, 8.0, *r.Location.AccuracyM)

	require.Len(t, r.Result.Candidates, 3)
	assert.Equal(t, "prv-servicios-hogar-norte", r.Result.Candidates[0].ProviderID)
	assert.True(t, r.Result.Candidates[0].Suggested)
	assert.Equal(t, back, *r.Result.Candidates[1].UnavailableUntil)
	assert.Equal(t, domain.NotEligibleOutOfRadius, r.Result.Candidates[2].Eligibility)
}

func TestAnExhaustedSearchAndAnAutomaticRetryReadBack(t *testing.T) {
	s := newAssignmentStores(t)
	s.file(t, "cl-1", capture(0, 8, true))
	for _, id := range []string{"prv-servicios-hogar-norte", "prv-reparaciones-chapinero", "prv-tecnicos-unidos"} {
		s.setAvailability(t, id, domain.AvailabilityUnavailable, nil)
	}
	v := s.search(t, "cl-1")
	assert.Equal(t, domain.AssignmentPending, v.State)
	assert.Equal(t, domain.ReasonNoEligibleProvider, v.LastSearch.Result.Reason)
	assert.Equal(t, []float64{5, 10, 20}, radii(v.LastSearch.Result.Attempts))

	state, err := s.svc.Retry(context.Background(), "cl-1")
	require.NoError(t, err)
	assert.Equal(t, domain.AssignmentPending, state)
	r, err := s.svc.View(context.Background(), "cl-1")
	require.NoError(t, err)
	assert.Equal(t, 2, r.LastSearch.Seq)
	assert.Equal(t, domain.SearchByRetry, r.LastSearch.Trigger)
	assert.Nil(t, r.LastSearch.SearchedBy)
	assert.Len(t, r.LastSearch.Result.Attempts, 3, "only the attempts of the last search")
}

func radii(attempts []domain.SearchAttempt) []float64 {
	var out []float64
	for _, a := range attempts {
		out = append(out, a.RadiusKm)
	}
	return out
}

func TestTheLocationOfTheAnalystReadsBack(t *testing.T) {
	s := newAssignmentStores(t)
	s.file(t, "cl-1")
	for _, lat := range []float64{4.7, incidentAt.Latitude} {
		_, err := s.svc.RecordLocation(context.Background(), "cl-1", domain.LocationRequest{
			Address: "Carrera 15 # 93-40, apto 402, Bogotá", Latitude: &lat, Longitude: &incidentAt.Longitude},
			assignedBy)
		require.NoError(t, err)
	}
	k, err := s.store.Case(context.Background(), "cl-1")
	require.NoError(t, err)
	require.NotNil(t, k.Location)
	assert.Equal(t, domain.IncidentLocation{Source: domain.LocationFromAnalyst, Point: incidentAt,
		Address: "Carrera 15 # 93-40, apto 402, Bogotá", Seq: 2, RecordedBy: &assignedBy, RecordedAt: searchStart},
		*k.Location)

	v := s.search(t, "cl-1")
	assert.Equal(t, domain.LocationFromAnalyst, v.LastSearch.Location.Source)
	assert.Equal(t, 2, v.LastSearch.Location.Seq)
	assert.Equal(t, "Carrera 15 # 93-40, apto 402, Bogotá", v.LastSearch.Location.Address)
	assert.Nil(t, v.LastSearch.Location.AccuracyM)

	err = s.store.RecordLocation(context.Background(), "cl-1", *k.Location)
	assert.ErrorIs(t, err, ports.ErrLocationConflict, "the place 2 is taken")
}

func TestAnAssignmentReadsBackWithItsNoticeAndMakesTheProviderBusy(t *testing.T) {
	s := newAssignmentStores(t)
	s.file(t, "cl-1", capture(0, 8, true))
	s.search(t, "cl-1")
	s.clock.advance(time.Minute)

	v, err := s.svc.Assign(context.Background(), "cl-1", "prv-servicios-hogar-norte", 1, assignedBy)
	require.NoError(t, err)
	assert.Equal(t, domain.AssignmentAssigned, v.State)
	phone := "+576015550102"
	accuracy := 8.0
	want := domain.Assignment{ClaimID: "cl-1", ProviderID: "prv-servicios-hogar-norte",
		ProviderName: "Servicios Hogar Norte", ContactEmail: "despachos@hogarnorte.example.com", ContactPhone: &phone,
		AssignedBy: assignedBy, AssignedAt: searchStart.Add(time.Minute), DistanceKm: v.Assignment.DistanceKm,
		SearchSeq: 1, Location: domain.IncidentLocation{Source: domain.LocationFromApp, Point: incidentAt,
			AccuracyM: &accuracy, RecordedAt: filedAt}, PolicyVersion: domain.AssignmentPolicyVersionV1}
	assert.Equal(t, want, *v.Assignment)
	assert.Equal(t, 2.3, domain.RoundKm(v.Assignment.DistanceKm))

	n := v.Notice
	require.NotNil(t, n)
	assert.Equal(t, ports.NoticePending, n.Status)
	assert.Equal(t, "despachos@hogarnorte.example.com", n.To)
	assert.Equal(t, "Asignación de siniestro "+v.Claim.FilingNumber, n.Subject)
	assert.Contains(t, n.Body, "precisión 8 m")
	assert.Equal(t, searchStart.Add(time.Minute), n.NextAttemptAt)

	p, err := s.providers.Get(context.Background(), "prv-servicios-hogar-norte")
	require.NoError(t, err)
	assert.Equal(t, domain.AvailabilityBusy, p.Availability)
	assert.Equal(t, "cl-1", *p.BusyClaimID)
	assert.Equal(t, assignedBy.Email, p.UpdatedBy)

	// Operations releases it (decision D13).
	s.setAvailability(t, "prv-servicios-hogar-norte", domain.AvailabilityAvailable, nil)
	p, err = s.providers.Get(context.Background(), "prv-servicios-hogar-norte")
	require.NoError(t, err)
	assert.True(t, p.Available())
	assert.Nil(t, p.BusyClaimID)

	var status string
	require.NoError(t, s.db.QueryRow(`SELECT status FROM claim WHERE id = 'cl-1'`).Scan(&status))
	assert.Equal(t, string(domain.StatusInEvaluation), status, "the status of the claim does not change")
}

func TestNothingIsWrittenOverAnAssignment(t *testing.T) {
	s := newAssignmentStores(t)
	s.file(t, "cl-1")
	_, err := s.svc.RecordLocation(context.Background(), "cl-1", domain.LocationRequest{Address: "Carrera 15 # 93-40",
		Latitude: &incidentAt.Latitude, Longitude: &incidentAt.Longitude}, assignedBy)
	require.NoError(t, err)
	v := s.search(t, "cl-1")
	assert.ErrorIs(t, s.store.RecordSearch(context.Background(), *v.LastSearch), ports.ErrSearchConflict)
	_, err = s.svc.Assign(context.Background(), "cl-1", "prv-servicios-hogar-norte", 1, assignedBy)
	require.NoError(t, err)

	next := *v.Location
	next.Seq = 2
	assert.ErrorIs(t, s.store.RecordLocation(context.Background(), "cl-1", next), domain.ErrAssignmentExists)
	again := *v.LastSearch
	again.Seq = 2
	assert.ErrorIs(t, s.store.RecordSearch(context.Background(), again), domain.ErrAssignmentExists)
}

// assignment is the assignment of the claim to the provider from its last
// search, as the service would build it.
func (s assignmentStores) assignment(t *testing.T, claimID, providerID string) domain.Assignment {
	t.Helper()
	k, err := s.store.Case(context.Background(), claimID)
	require.NoError(t, err)
	p, err := s.providers.Get(context.Background(), providerID)
	require.NoError(t, err)
	a, err := domain.Assign(k.Claim, domain.AssignmentPolicyV1(), domain.LocationOf(k.Claim, k.Location),
		k.LastSearch, k.Assignment, providerID, k.LastSearch.Seq, &p, assignedBy, searchStart)
	require.NoError(t, err)
	return a
}

func notice(a domain.Assignment) ports.ProviderNotice {
	return ports.ProviderNotice{ClaimID: a.ClaimID, To: a.ContactEmail, Subject: "Asignación", Body: "Ubicación",
		NextAttemptAt: a.AssignedAt}
}

// race runs the calls at once and returns their errors.
func race(calls ...func() error) []error {
	var wg sync.WaitGroup
	errs := make([]error, len(calls))
	ready := make(chan struct{})
	for i, call := range calls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-ready
			errs[i] = call()
		}()
	}
	close(ready)
	wg.Wait()
	return errs
}

func (s assignmentStores) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, s.db.QueryRow(query, args...).Scan(&n))
	return n
}

func TestOfTwoAnalystsAssigningTheSameClaimOneWins(t *testing.T) {
	s := newAssignmentStores(t)
	s.file(t, "cl-1", capture(0, 8, true))
	_, err := s.svc.Search(context.Background(), "cl-1", ptr(10.0), assignedBy)
	require.NoError(t, err)
	first := s.assignment(t, "cl-1", "prv-servicios-hogar-norte")
	second := s.assignment(t, "cl-1", "prv-tecnicos-unidos")

	errs := race(func() error { return s.store.Assign(context.Background(), first, notice(first)) },
		func() error { return s.store.Assign(context.Background(), second, notice(second)) })
	won := 0
	for _, err := range errs {
		if err == nil {
			won++
			continue
		}
		assert.ErrorIs(t, err, domain.ErrAssignmentExists)
	}
	assert.Equal(t, 1, won)
	assert.Equal(t, 1, s.count(t, `SELECT count(*) FROM provider WHERE availability = 'busy'`),
		"only the provider of the winner is busy")
	assert.Equal(t, 1, s.count(t, `SELECT count(*) FROM provider_notice`))
}

func TestOfTwoClaimsTakingTheSameProviderOneWinsAndTheOtherWritesNothing(t *testing.T) {
	s := newAssignmentStores(t)
	s.file(t, "cl-1", capture(0, 8, true))
	s.file(t, "cl-2", capture(0, 8, true))
	s.search(t, "cl-1")
	s.search(t, "cl-2")
	one, two := s.assignment(t, "cl-1", "prv-servicios-hogar-norte"), s.assignment(t, "cl-2", "prv-servicios-hogar-norte")

	errs := race(func() error { return s.store.Assign(context.Background(), one, notice(one)) },
		func() error { return s.store.Assign(context.Background(), two, notice(two)) })
	var loser string
	for i, err := range errs {
		if err != nil {
			assert.ErrorIs(t, err, domain.ErrProviderUnavailable)
			loser = []string{"cl-1", "cl-2"}[i]
		}
	}
	require.NotEmpty(t, loser, "one of them lost")
	assert.Zero(t, s.count(t, `SELECT count(*) FROM claim_assignment WHERE claim_id = $1`, loser))
	assert.Zero(t, s.count(t, `SELECT count(*) FROM provider_notice WHERE claim_id = $1`, loser))
	assert.Equal(t, 1, s.count(t, `SELECT count(*) FROM claim_assignment`))
}

func TestAnAssignmentIsRefusedWhenTheClaimMovedOn(t *testing.T) {
	s := newAssignmentStores(t)
	s.file(t, "cl-1", capture(0, 8, true))
	s.search(t, "cl-1")
	a := s.assignment(t, "cl-1", "prv-servicios-hogar-norte")

	s.search(t, "cl-1")
	assert.ErrorIs(t, s.store.Assign(context.Background(), a, notice(a)), domain.ErrSearchOutdated)

	a.SearchSeq = 2
	s.move(t, "cl-1", domain.StatusApproved)
	assert.ErrorIs(t, s.store.Assign(context.Background(), a, notice(a)), domain.ErrClaimNotAssignable)

	a.ClaimID = "cl-x"
	assert.ErrorIs(t, s.store.Assign(context.Background(), a, notice(a)), ports.ErrClaimNotFound)
	assert.Zero(t, s.count(t, `SELECT count(*) FROM provider WHERE availability = 'busy'`))
}

func TestTheClaimsPendingAssignment(t *testing.T) {
	s := newAssignmentStores(t)
	for _, id := range []string{"prv-servicios-hogar-norte", "prv-reparaciones-chapinero", "prv-tecnicos-unidos"} {
		s.setAvailability(t, id, domain.AvailabilityUnavailable, nil)
	}
	s.file(t, "cl-left", capture(0, 8, true))
	s.file(t, "cl-pending", capture(0, 8, true))
	s.file(t, "cl-moved", capture(0, 8, true))
	s.file(t, "cl-relocated")
	_, err := s.svc.RecordLocation(context.Background(), "cl-relocated", domain.LocationRequest{
		Address: "Carrera 15 # 93-40", Latitude: &incidentAt.Latitude, Longitude: &incidentAt.Longitude}, assignedBy)
	require.NoError(t, err)
	for _, id := range []string{"cl-left", "cl-pending", "cl-moved", "cl-relocated"} {
		s.search(t, id)
		s.clock.advance(time.Second)
	}
	pending := func() []string {
		ids, err := s.store.PendingAssignment(context.Background(), 10)
		require.NoError(t, err)
		return ids
	}
	assert.Equal(t, []string{"cl-left", "cl-pending", "cl-moved", "cl-relocated"}, pending(), "oldest searched first")

	s.move(t, "cl-left", domain.StatusRejected)
	_, err = s.svc.RecordLocation(context.Background(), "cl-relocated", domain.LocationRequest{
		Address: "Carrera 15 # 93-40", Latitude: ptr(4.68), Longitude: &incidentAt.Longitude}, assignedBy)
	require.NoError(t, err)
	s.setAvailability(t, "prv-tecnicos-unidos", domain.AvailabilityAvailable, nil)
	s.search(t, "cl-moved")
	assert.Equal(t, []string{"cl-pending"}, pending(),
		"left evaluation, located again, or found a provider: none is pending any more")

	ids, err := s.store.PendingAssignment(context.Background(), 0)
	require.NoError(t, err)
	assert.Empty(t, ids)
}

func TestTheNoticesDueAreHeldByOneTask(t *testing.T) {
	s := newAssignmentStores(t)
	s.file(t, "cl-1", capture(0, 8, true))
	s.search(t, "cl-1")
	_, err := s.svc.Assign(context.Background(), "cl-1", "prv-servicios-hogar-norte", 1, assignedBy)
	require.NoError(t, err)

	var held [2][]ports.ProviderNotice
	errs := race(func() error {
		var err error
		held[0], err = s.store.ClaimNoticesDue(context.Background(), searchStart, time.Minute, 10)
		return err
	}, func() error {
		var err error
		held[1], err = s.store.ClaimNoticesDue(context.Background(), searchStart, time.Minute, 10)
		return err
	})
	require.NoError(t, errs[0])
	require.NoError(t, errs[1])
	assert.Equal(t, 1, len(held[0])+len(held[1]), "only one task holds it")

	later := searchStart.Add(time.Minute)
	require.NoError(t, s.store.NoticeFailed(context.Background(), "cl-1", later, "email provider unavailable"))
	due, err := s.store.ClaimNoticesDue(context.Background(), later, time.Minute, 10)
	require.NoError(t, err)
	require.Len(t, due, 1)
	assert.Equal(t, 1, due[0].Attempts)
	assert.Equal(t, "email provider unavailable", due[0].LastError)
	assert.Equal(t, later.Add(time.Minute), due[0].NextAttemptAt)

	require.NoError(t, s.store.NoticeSent(context.Background(), "cl-1", later))
	v, err := s.svc.View(context.Background(), "cl-1")
	require.NoError(t, err)
	assert.Equal(t, ports.NoticeSent, v.Notice.Status)
	assert.Equal(t, 2, v.Notice.Attempts)
	assert.Equal(t, later, v.Notice.SentAt)
	assert.Empty(t, v.Notice.LastError)
	assert.True(t, v.Notice.NextAttemptAt.IsZero())

	due, err = s.store.ClaimNoticesDue(context.Background(), later.Add(time.Hour), time.Minute, 10)
	require.NoError(t, err)
	assert.Empty(t, due, "a notice sent is not due")
}

func TestTheAssignmentOfSeveralClaimsIsReadAtOnce(t *testing.T) {
	s := newAssignmentStores(t)
	s.file(t, "cl-1", capture(0, 8, true))
	s.file(t, "cl-2")
	s.search(t, "cl-1")
	cases, err := s.store.Cases(context.Background(), []string{"cl-2", "cl-x", "cl-1"})
	require.NoError(t, err)
	assert.Len(t, cases, 2)
	assert.NotNil(t, cases["cl-1"].LastSearch)
	assert.Nil(t, cases["cl-2"].LastSearch)

	_, err = s.store.Case(context.Background(), "cl-x")
	assert.ErrorIs(t, err, ports.ErrClaimNotFound)
}

func TestTheHistoryOfTheAssignmentIsAppendOnly(t *testing.T) {
	s := newAssignmentStores(t)
	s.file(t, "cl-1")
	_, err := s.svc.RecordLocation(context.Background(), "cl-1", domain.LocationRequest{Address: "Carrera 15 # 93-40",
		Latitude: &incidentAt.Latitude, Longitude: &incidentAt.Longitude}, assignedBy)
	require.NoError(t, err)
	s.search(t, "cl-1")
	_, err = s.svc.Assign(context.Background(), "cl-1", "prv-servicios-hogar-norte", 1, assignedBy)
	require.NoError(t, err)

	for _, stmt := range []string{
		`UPDATE claim_incident_location SET address = 'Otra dirección'`,
		`DELETE FROM claim_incident_location`,
		`TRUNCATE claim_incident_location CASCADE`,
		`UPDATE claim_provider_search SET outcome = 'exhausted', reason = 'x'`,
		`DELETE FROM claim_provider_search`,
		`TRUNCATE claim_provider_search CASCADE`,
		`UPDATE claim_provider_search_attempt SET eligible_count = 0`,
		`DELETE FROM claim_provider_search_attempt`,
		`TRUNCATE claim_provider_search_attempt`,
		`UPDATE claim_assignment SET provider_name = 'Otro'`,
		`DELETE FROM claim_assignment`,
		`TRUNCATE claim_assignment CASCADE`,
	} {
		_, err := s.db.Exec(stmt)
		require.Error(t, err, stmt)
		assert.Contains(t, err.Error(), "append-only", stmt)
	}
	assert.Equal(t, 1, s.count(t, `SELECT count(*) FROM claim_assignment`))
}

func ptr[T any](v T) *T { return &v }
