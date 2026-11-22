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

// The inbox of assistance without a database (HU-W2.3-03): the same rules as
// PostgreSQL over the requests of the phone.

// inboxScene is two requests of the phone: id-1 first, id-2 a minute later.
func inboxScene(t *testing.T) (*memory.AssistanceStore, *memory.AssistanceInboxStore) {
	t.Helper()
	requests := memory.NewAssistanceStore()
	for i, id := range []string{"id-1", "id-2"} {
		r := assistanceRequest(id, "sub-"+id, "k-"+id, "f-"+id)
		r.RequestedAt = requestedAt.Add(time.Duration(i) * time.Minute)
		_, _, err := requests.Create(context.Background(), r)
		require.NoError(t, err)
	}
	return requests, memory.NewAssistanceInboxStore(requests)
}

func act(code domain.AssistanceActionCode, minute int) domain.AssistanceAction {
	return domain.AssistanceAction{Code: code, By: analyst, At: requestedAt.Add(time.Duration(minute) * time.Minute)}
}

func dispatchOf(minute int) domain.AssistanceDispatch {
	accuracy := 8.0
	return domain.AssistanceDispatch{
		Provider: domain.DispatchedProvider{ID: "prv-gruas-autopista-norte", Name: "Grúas Autopista Norte",
			ContactEmail: "despachos@gruasautopistanorte.example.com"},
		Need: domain.NeedTowing, Specialty: domain.SpecialtyTowing, DistanceKm: 0.9, EtaMinutes: 12,
		Location: domain.SharedLocation{Source: domain.SharedFromApp, Point: domain.Coordinates{Latitude: 4.7712,
			Longitude: -74.0452}, AccuracyM: &accuracy},
		SuggestedProviderID: "prv-gruas-autopista-norte",
		Candidates:          []domain.Candidate{{ProviderID: "prv-gruas-autopista-norte", Eligibility: domain.Eligible}},
		Attempts:            []domain.SearchAttempt{{RadiusKm: 5, EligibleCount: 1}},
		By:                  analyst, At: requestedAt.Add(time.Duration(minute) * time.Minute),
	}
}

func TestTheMemoryInboxRecordsEveryDecisionInOrder(t *testing.T) {
	_, s := inboxScene(t)
	ctx := context.Background()

	require.NoError(t, s.Attend(ctx, "id-1", 0, act(domain.ActionAttended, 1)))
	need := domain.RecordedNeed{Seq: 1, Need: domain.NeedTowing, Specialty: domain.SpecialtyTowing, By: analyst}
	require.NoError(t, s.RecordNeed(ctx, "id-1", 1, need, act(domain.ActionNeedRecorded, 2)))
	voice := domain.VoiceLocation{Seq: 1, Address: "Vía Cali–Palmira km 4", By: analyst}
	require.NoError(t, s.RecordVoiceLocation(ctx, "id-1", 2, voice, act(domain.ActionVoiceLocationRecorded, 3)))
	require.NoError(t, s.Dispatch(ctx, "id-1", 3, dispatchOf(4), act(domain.ActionDispatched, 4),
		ports.AssistanceNotice{To: "despachos@gruasautopistanorte.example.com", Subject: "s", Body: "b",
			NextAttemptAt: requestedAt.Add(4 * time.Minute)}))

	r, err := s.Case(ctx, "id-1")
	require.NoError(t, err)
	assert.Equal(t, domain.AssistanceDispatched, r.Case.Request.Status)
	assert.Equal(t, 4, r.Version())
	for i, a := range r.Case.History {
		assert.Equal(t, i+1, a.Seq, "numbered in order")
	}
	assert.Equal(t, need, *r.Case.Need)
	assert.Equal(t, voice, *r.Case.Voice)
	assert.Equal(t, dispatchOf(4), *r.Case.Dispatch)
	assert.Equal(t, ports.AssistanceNotice{RequestID: "id-1", To: "despachos@gruasautopistanorte.example.com",
		Subject: "s", Body: "b", Status: ports.NoticePending, NextAttemptAt: requestedAt.Add(4 * time.Minute)},
		*r.Notice)

	r.Case.Dispatch.Candidates[0].ProviderID = "changed"
	r.Case.History[0].By.Email = "changed"
	again, err := s.Case(ctx, "id-1")
	require.NoError(t, err)
	assert.Equal(t, "prv-gruas-autopista-norte", again.Case.Dispatch.Candidates[0].ProviderID,
		"what the store keeps is not shared")
	assert.Equal(t, analyst.Email, again.Case.History[0].By.Email)

	require.NoError(t, s.Close(ctx, "id-2", 0, domain.AssistanceClosure{Reason: domain.CloseDuplicate, By: analyst},
		act(domain.ActionClosed, 5)))
	r, err = s.Case(ctx, "id-2")
	require.NoError(t, err)
	assert.Equal(t, domain.AssistanceClosed, r.Case.Request.Status)
	assert.Equal(t, domain.CloseDuplicate, r.Case.Closure.Reason)
}

func TestTheMemoryInboxRefusesADecisionOnARequestThatChanged(t *testing.T) {
	_, s := inboxScene(t)
	ctx := context.Background()
	require.NoError(t, s.Attend(ctx, "id-1", 0, act(domain.ActionAttended, 1)))
	assert.ErrorIs(t, s.Attend(ctx, "id-1", 0, act(domain.ActionTakenOver, 1)), ports.ErrAssistanceChanged)
	assert.ErrorIs(t, s.Close(ctx, "id-1", 2, domain.AssistanceClosure{}, act(domain.ActionClosed, 1)),
		ports.ErrAssistanceChanged)
	assert.ErrorIs(t, s.Attend(ctx, "no-existe", 0, act(domain.ActionAttended, 1)), ports.ErrAssistanceRequestNotFound)
	_, err := s.Case(ctx, "no-existe")
	assert.ErrorIs(t, err, ports.ErrAssistanceRequestNotFound)
}

func TestTheMemoryInboxListsAndCounts(t *testing.T) {
	_, s := inboxScene(t)
	ctx := context.Background()
	require.NoError(t, s.Attend(ctx, "id-1", 0, act(domain.ActionAttended, 1)))
	require.NoError(t, s.Dispatch(ctx, "id-1", 1, dispatchOf(3), act(domain.ActionDispatched, 3),
		ports.AssistanceNotice{NextAttemptAt: requestedAt}))

	list, err := s.List(ctx, ports.InboxFilter{})
	require.NoError(t, err)
	require.Len(t, list, 2)
	assert.Equal(t, "id-2", list[0].Case.Request.ID, "the latest first")

	list, err = s.List(ctx, ports.InboxFilter{Statuses: []domain.AssistanceStatus{domain.AssistanceDispatched},
		Limit: 5})
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "id-1", list[0].Case.Request.ID)
	list, err = s.List(ctx, ports.InboxFilter{Limit: 1})
	require.NoError(t, err)
	assert.Len(t, list, 1)

	newCount, inCall, took, err := s.Counts(ctx, requestedAt)
	require.NoError(t, err)
	assert.Equal(t, 1, newCount)
	assert.Zero(t, inCall)
	assert.Equal(t, []time.Duration{3 * time.Minute}, took)
	_, _, took, err = s.Counts(ctx, requestedAt.Add(time.Hour))
	require.NoError(t, err)
	assert.Empty(t, took, "a dispatch before the day does not count")

	today, err := s.DispatchesSince(ctx, []string{"prv-gruas-autopista-norte", "prv-gruas-calle-80"}, requestedAt)
	require.NoError(t, err)
	assert.Equal(t, map[string]int{"prv-gruas-autopista-norte": 1}, today)
	today, err = s.DispatchesSince(ctx, []string{"prv-gruas-calle-80"}, requestedAt)
	require.NoError(t, err)
	assert.Empty(t, today)
}

func TestTheMemoryInboxHoldsTheDueNotices(t *testing.T) {
	_, s := inboxScene(t)
	ctx := context.Background()
	for i, id := range []string{"id-1", "id-2"} {
		require.NoError(t, s.Attend(ctx, id, 0, act(domain.ActionAttended, 1)))
		require.NoError(t, s.Dispatch(ctx, id, 1, dispatchOf(2), act(domain.ActionDispatched, 2),
			ports.AssistanceNotice{To: id + "@example.com", NextAttemptAt: requestedAt.Add(time.Duration(i) * time.Second)}))
	}

	due, err := s.ClaimNoticesDue(ctx, requestedAt.Add(time.Second), time.Minute, 1)
	require.NoError(t, err)
	require.Len(t, due, 1, "up to the limit, the oldest first")
	assert.Equal(t, "id-1", due[0].RequestID)
	due, err = s.ClaimNoticesDue(ctx, requestedAt.Add(time.Second), time.Minute, 10)
	require.NoError(t, err)
	require.Len(t, due, 1, "the one held is not taken again")
	assert.Equal(t, "id-2", due[0].RequestID)

	require.NoError(t, s.NoticeFailed(ctx, "id-2", requestedAt.Add(time.Hour), "down"))
	require.NoError(t, s.NoticeSent(ctx, "id-1", requestedAt.Add(2*time.Second)))
	require.NoError(t, s.NoticeSent(ctx, "id-1", requestedAt.Add(time.Hour)), "sent twice records it once")
	require.NoError(t, s.NoticeFailed(ctx, "id-1", requestedAt, "late"), "a sent notice does not fail")
	require.NoError(t, s.NoticeSent(ctx, "no-existe", requestedAt))

	one, err := s.Case(ctx, "id-1")
	require.NoError(t, err)
	assert.Equal(t, ports.NoticeSent, one.Notice.Status)
	assert.Equal(t, requestedAt.Add(2*time.Second), one.Notice.SentAt)
	assert.Equal(t, 1, one.Notice.Attempts)
	two, err := s.Case(ctx, "id-2")
	require.NoError(t, err)
	assert.Equal(t, ports.NoticePending, two.Notice.Status)
	assert.Equal(t, 1, two.Notice.Attempts)
	assert.Equal(t, "down", two.Notice.LastError)
}
