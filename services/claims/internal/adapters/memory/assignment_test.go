package memory_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// The assignment of the provider in memory (HU-W2.3-01), with the rules of
// PostgreSQL.

var (
	assignedAt = time.Date(2026, 9, 6, 15, 14, 2, 0, time.UTC)
	analyst    = domain.Analyst{Email: "andres.ruiz@solventa.example", Name: "Andrés Ruiz"}
	spot       = domain.Coordinates{Latitude: 4.6771, Longitude: -74.0483}
)

type assignmentScene struct {
	cases     *memory.CaseStore
	providers *memory.ProviderStore
	store     *memory.AssignmentStore
}

func newAssignmentScene(t *testing.T) assignmentScene {
	t.Helper()
	cases := memory.NewCaseStore()
	providers := memory.NewProviderStore(memory.ReferenceProviders()...)
	s := assignmentScene{cases: cases, providers: providers, store: memory.NewAssignmentStore(cases, providers)}
	for _, id := range []string{"cl-1", "cl-2"} {
		_, _, err := cases.CreateWeb(context.Background(), domain.ClaimRecord{
			Claim: domain.Claim{ID: id, Subject: "sub-carolina", FilingKey: "key-" + id, PolicyID: "HG-2025-880231",
				IncidentType: domain.IncidentWaterDamage, Status: domain.StatusInEvaluation, FiledAt: assignedAt},
			CatalogVersion: domain.CatalogVersionV1,
		})
		require.NoError(t, err)
	}
	return s
}

func located(seq int) domain.IncidentLocation {
	by := analyst
	return domain.IncidentLocation{Source: domain.LocationFromAnalyst, Point: spot, Address: "Carrera 15 # 93-40",
		Seq: seq, RecordedBy: &by, RecordedAt: assignedAt}
}

// searched records a search of the claim from its last location.
func (s assignmentScene) searched(t *testing.T, claimID string) domain.ProviderSearch {
	t.Helper()
	k, err := s.store.Case(context.Background(), claimID)
	require.NoError(t, err)
	providers, err := s.providers.WithSpecialty(context.Background(), domain.SpecialtyPlumbing)
	require.NoError(t, err)
	r, err := domain.NewSearch(k.Claim, domain.AssignmentPolicyV1(), domain.LocationOf(k.Claim, k.Location),
		k.LastSearch, k.Assignment, providers, domain.SearchRequest{Trigger: domain.SearchByAnalyst, By: &analyst},
		assignedAt)
	require.NoError(t, err)
	require.NoError(t, s.store.RecordSearch(context.Background(), r))
	return r
}

func (s assignmentScene) assignment(t *testing.T, claimID, providerID string) domain.Assignment {
	t.Helper()
	k, err := s.store.Case(context.Background(), claimID)
	require.NoError(t, err)
	p, err := s.providers.Get(context.Background(), providerID)
	require.NoError(t, err)
	a, err := domain.Assign(k.Claim, domain.AssignmentPolicyV1(), domain.LocationOf(k.Claim, k.Location),
		k.LastSearch, nil, providerID, k.LastSearch.Seq, &p, analyst, assignedAt)
	require.NoError(t, err)
	return a
}

func noticeOf(a domain.Assignment) ports.ProviderNotice {
	return ports.ProviderNotice{ClaimID: a.ClaimID, To: a.ContactEmail, Subject: "Asignación", Body: "Ubicación",
		NextAttemptAt: assignedAt}
}

func TestTheLocationsAndSearchesTakeTheirPlaces(t *testing.T) {
	s := newAssignmentScene(t)
	require.NoError(t, s.store.RecordLocation(context.Background(), "cl-1", located(1)))
	assert.ErrorIs(t, s.store.RecordLocation(context.Background(), "cl-1", located(1)), ports.ErrLocationConflict)
	r := s.searched(t, "cl-1")
	assert.ErrorIs(t, s.store.RecordSearch(context.Background(), r), ports.ErrSearchConflict)

	k, err := s.store.Case(context.Background(), "cl-1")
	require.NoError(t, err)
	assert.Equal(t, located(1), *k.Location)
	assert.Equal(t, r, *k.LastSearch)

	// What the caller gets is a copy.
	k.Location.RecordedBy.Name = "Otro"
	k.LastSearch.Result.Candidates[0].Specialties[0] = domain.SpecialtyPainting
	again, err := s.store.Case(context.Background(), "cl-1")
	require.NoError(t, err)
	assert.Equal(t, located(1), *again.Location)
	assert.Equal(t, r, *again.LastSearch)

	_, err = s.store.Case(context.Background(), "cl-x")
	assert.ErrorIs(t, err, ports.ErrClaimNotFound)
	cases, err := s.store.Cases(context.Background(), []string{"cl-1", "cl-x", "cl-2"})
	require.NoError(t, err)
	assert.Len(t, cases, 2)
}

func TestAnAssignmentMakesItsProviderBusyAllOrNothing(t *testing.T) {
	s := newAssignmentScene(t)
	require.NoError(t, s.store.RecordLocation(context.Background(), "cl-1", located(1)))
	require.NoError(t, s.store.RecordLocation(context.Background(), "cl-2", located(1)))
	s.searched(t, "cl-1")
	s.searched(t, "cl-2")
	one, two := s.assignment(t, "cl-1", "prv-servicios-hogar-norte"), s.assignment(t, "cl-2", "prv-servicios-hogar-norte")

	require.NoError(t, s.store.Assign(context.Background(), one, noticeOf(one)))
	assert.ErrorIs(t, s.store.Assign(context.Background(), one, noticeOf(one)), domain.ErrAssignmentExists)
	assert.ErrorIs(t, s.store.Assign(context.Background(), two, noticeOf(two)), domain.ErrProviderUnavailable)
	k, err := s.store.Case(context.Background(), "cl-2")
	require.NoError(t, err)
	assert.Nil(t, k.Assignment, "nothing of the loser is written")

	p, err := s.providers.Get(context.Background(), "prv-servicios-hogar-norte")
	require.NoError(t, err)
	assert.Equal(t, domain.AvailabilityBusy, p.Availability)
	assert.Equal(t, "cl-1", *p.BusyClaimID)

	k, err = s.store.Case(context.Background(), "cl-1")
	require.NoError(t, err)
	assert.Equal(t, one, *k.Assignment)
	assert.Equal(t, ports.NoticePending, k.Notice.Status)
	assert.ErrorIs(t, s.store.RecordLocation(context.Background(), "cl-1", located(2)), domain.ErrAssignmentExists)
	assert.ErrorIs(t, s.store.RecordSearch(context.Background(), *k.LastSearch), domain.ErrAssignmentExists)
}

func TestAnAssignmentIsRefusedWhenTheClaimMovedOnInMemory(t *testing.T) {
	s := newAssignmentScene(t)
	require.NoError(t, s.store.RecordLocation(context.Background(), "cl-1", located(1)))
	s.searched(t, "cl-1")
	a := s.assignment(t, "cl-1", "prv-servicios-hogar-norte")
	s.searched(t, "cl-1")
	assert.ErrorIs(t, s.store.Assign(context.Background(), a, noticeOf(a)), domain.ErrSearchOutdated)

	_, err := memory.NewDecisionStore(s.cases).Decide(context.Background(), domain.Decision{ClaimID: "cl-1",
		Outcome: domain.OutcomeRejected, DecidedBy: analyst.Email, DecidedAt: assignedAt, Reason: "Sin cobertura.",
		Notice: &domain.Notice{To: "carolina@example.com"}})
	require.NoError(t, err)
	a.SearchSeq = 2
	assert.ErrorIs(t, s.store.Assign(context.Background(), a, noticeOf(a)), domain.ErrClaimNotAssignable)
	a.ClaimID = "cl-x"
	assert.ErrorIs(t, s.store.Assign(context.Background(), a, noticeOf(a)), ports.ErrClaimNotFound)
}

func TestTheNoticesInMemory(t *testing.T) {
	s := newAssignmentScene(t)
	for _, id := range []string{"cl-2", "cl-1"} {
		require.NoError(t, s.store.RecordLocation(context.Background(), id, located(1)))
		s.searched(t, id)
	}
	one := s.assignment(t, "cl-1", "prv-servicios-hogar-norte")
	require.NoError(t, s.store.Assign(context.Background(), one, noticeOf(one)))
	_, err := s.providers.SetAvailability(context.Background(), "prv-servicios-hogar-norte",
		domain.AvailabilityAvailable, nil, analyst.Email, assignedAt)
	require.NoError(t, err)
	two := s.assignment(t, "cl-2", "prv-servicios-hogar-norte")
	require.NoError(t, s.store.Assign(context.Background(), two, noticeOf(two)))

	due, err := s.store.ClaimNoticesDue(context.Background(), assignedAt, time.Minute, 1)
	require.NoError(t, err)
	require.Len(t, due, 1)
	assert.Equal(t, "cl-1", due[0].ClaimID, "by id on a tie")
	assert.Equal(t, assignedAt.Add(time.Minute), due[0].NextAttemptAt, "held for the lease")

	require.NoError(t, s.store.NoticeFailed(context.Background(), "cl-2", assignedAt.Add(time.Hour), "caído"))
	require.NoError(t, s.store.NoticeSent(context.Background(), "cl-1", assignedAt))
	require.NoError(t, s.store.NoticeSent(context.Background(), "cl-x", assignedAt))
	require.NoError(t, s.store.NoticeFailed(context.Background(), "cl-1", assignedAt, "after sent"))

	k, err := s.store.Case(context.Background(), "cl-1")
	require.NoError(t, err)
	assert.Equal(t, ports.ProviderNotice{ClaimID: "cl-1", To: one.ContactEmail, Subject: "Asignación",
		Body: "Ubicación", Status: ports.NoticeSent, Attempts: 1, SentAt: assignedAt}, *k.Notice)
	k, err = s.store.Case(context.Background(), "cl-2")
	require.NoError(t, err)
	assert.Equal(t, 1, k.Notice.Attempts)
	assert.Equal(t, "caído", k.Notice.LastError)

	due, err = s.store.ClaimNoticesDue(context.Background(), assignedAt.Add(time.Hour), time.Minute, 10)
	require.NoError(t, err)
	require.Len(t, due, 1)
	assert.Equal(t, "cl-2", due[0].ClaimID)
}

func TestTheClaimsPendingAssignmentInMemory(t *testing.T) {
	s := newAssignmentScene(t)
	for _, id := range []string{"prv-servicios-hogar-norte", "prv-reparaciones-chapinero", "prv-tecnicos-unidos"} {
		_, err := s.providers.SetAvailability(context.Background(), id, domain.AvailabilityUnavailable, nil,
			analyst.Email, assignedAt)
		require.NoError(t, err)
	}
	require.NoError(t, s.store.RecordLocation(context.Background(), "cl-1", located(1)))
	require.NoError(t, s.store.RecordLocation(context.Background(), "cl-2", located(1)))
	s.searched(t, "cl-2")
	s.searched(t, "cl-1")

	ids, err := s.store.PendingAssignment(context.Background(), 10)
	require.NoError(t, err)
	assert.Equal(t, []string{"cl-1", "cl-2"}, ids, "by id when searched at the same time")
	ids, err = s.store.PendingAssignment(context.Background(), 1)
	require.NoError(t, err)
	assert.Equal(t, []string{"cl-1"}, ids)

	require.NoError(t, s.store.RecordLocation(context.Background(), "cl-1", located(2)))
	ids, err = s.store.PendingAssignment(context.Background(), 10)
	require.NoError(t, err)
	assert.Equal(t, []string{"cl-2"}, ids, "cl-1 has a new location to search from")
}

func TestTheRegistryInMemory(t *testing.T) {
	providers := memory.NewProviderStore(memory.ReferenceProviders()...)
	p, err := providers.Get(context.Background(), "prv-tecnicos-unidos")
	require.NoError(t, err)
	p.Specialties[0] = domain.SpecialtyBodywork
	again, err := providers.Get(context.Background(), "prv-tecnicos-unidos")
	require.NoError(t, err)
	assert.Equal(t, domain.SpecialtyPlumbing, again.Specialties[0], "the caller gets a copy")

	_, err = providers.Get(context.Background(), "prv-nadie")
	assert.ErrorIs(t, err, ports.ErrProviderNotFound)
	assert.ErrorIs(t, providers.Create(context.Background(), again), ports.ErrProviderExists)
	_, err = providers.SetAvailability(context.Background(), "prv-tecnicos-unidos", domain.AvailabilityBusy, nil,
		analyst.Email, assignedAt)
	assert.ErrorIs(t, err, domain.ErrBusyIsSetByAssignment)

	until := time.Date(2026, 9, 7, 0, 0, 0, 0, domain.ColombiaTime)
	p, err = providers.SetAvailability(context.Background(), "prv-tecnicos-unidos", domain.AvailabilityUnavailable,
		&until, analyst.Email, assignedAt)
	require.NoError(t, err)
	assert.Equal(t, until, *p.UnavailableUntil)

	lat, lon := 4.7, -74.05
	p, err = providers.UpdateProfile(context.Background(), "prv-tecnicos-unidos", domain.ProviderProfile{
		Name: "Técnicos Unidos SAS", Specialties: []domain.Specialty{domain.SpecialtyPlumbing}, Latitude: &lat,
		Longitude: &lon, Address: "Calle 127 # 7-10", ContactEmail: "turnos@example.com"}, analyst.Email, assignedAt)
	require.NoError(t, err)
	assert.Equal(t, "Técnicos Unidos SAS", p.Name)
	assert.Equal(t, domain.AvailabilityUnavailable, p.Availability)

	list, err := providers.List(context.Background(), ports.ProviderFilter{Availability: domain.AvailabilityUnavailable}, 0)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "prv-tecnicos-unidos", list[0].ID)
}
