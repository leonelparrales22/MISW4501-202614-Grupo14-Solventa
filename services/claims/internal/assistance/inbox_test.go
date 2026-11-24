package assistance_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/assessors"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/assistance"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// The inbox of operations (HU-W2.3-03), over the stores in memory with the
// reference providers of 0009 and 0015.

var (
	// inboxNow is 16:31 in Bogotá, as AST-2026-0912 of the mockup.
	inboxNow  = time.Date(2026, 11, 15, 21, 31, 4, 0, time.UTC)
	analyst   = domain.Analyst{Email: "andres.ruiz@solventa.example", Name: "Andrés Ruiz"}
	colleague = domain.Analyst{Email: "laura.gomez@solventa.example", Name: "Laura Gómez"}
	// autopista is the place of AST-2026-0912; palmira the one told by voice
	// of AST-2026-0905.
	autopista = domain.Coordinates{Latitude: 4.7712, Longitude: -74.0452}
	palmira   = domain.Coordinates{Latitude: 3.4921, Longitude: -76.4203}
)

const (
	withGPS    = "3b8e2f6a-1c4d-4e7f-9a2b-6c5d8e0f1a2b"
	withoutGPS = "9d0c6a14-7b3e-4f21-8c5a-2e1f0b9d7a63"
	atHome     = "5e7a9c1b-2d4f-4a6b-8c0d-1e3f5a7b9c2d"
)

type inboxFixture struct {
	requests  *memory.AssistanceStore
	inbox     *memory.AssistanceInboxStore
	providers *memory.ProviderStore
	clock     *clock
	svc       *assistance.InboxService
	woken     int
}

func newInbox(t *testing.T) *inboxFixture {
	t.Helper()
	f := &inboxFixture{requests: memory.NewAssistanceStore(),
		providers: memory.NewProviderStore(memory.ReferenceProviders()...), clock: &clock{now: inboxNow}}
	f.inbox = memory.NewAssistanceInboxStore(f.requests)
	f.svc = assistance.NewInboxService(f.inbox, f.providers, f.clock, func() { f.woken++ })
	return f
}

// request records a request sent by the phone at the given time.
func (f *inboxFixture) request(t *testing.T, id string, line domain.PolicyLine, at *domain.Coordinates,
	requestedAt time.Time) {
	t.Helper()
	r := domain.AssistanceRequest{ID: id, Subject: "sub-" + id, RequesterName: "Carolina Pérez Ruiz", Key: "k-" + id,
		Fingerprint: "f-" + id, Status: domain.AssistanceNew, RequestedAt: requestedAt,
		Policy: domain.AssistancePolicy{ID: "AU-2025-118204", Line: line, Product: "Autos", InsuredObject: "KJT-482"}}
	if at != nil {
		point, accuracy, captured := *at, 8.0, requestedAt.Add(-12*time.Second)
		r.Location = domain.AssistanceLocation{Status: domain.LocationSent, Coordinates: &point, AccuracyM: &accuracy,
			CapturedAt: &captured}
		r.Place = ptr("Autopista Norte km 12, Bogotá")
	} else {
		r.Location = domain.AssistanceLocation{Status: domain.LocationUnavailable,
			UnavailableReason: domain.UnavailableNoFix}
	}
	_, _, err := f.requests.Create(context.Background(), r)
	require.NoError(t, err)
}

// towing is the request of the mockup attended with the need of a tow truck.
func (f *inboxFixture) towing(t *testing.T) assistance.CaseView {
	t.Helper()
	f.request(t, withGPS, domain.LineAuto, &autopista, inboxNow)
	_, err := f.svc.Attend(context.Background(), withGPS, analyst)
	require.NoError(t, err)
	view, err := f.svc.RecordNeed(context.Background(), withGPS, "towing", analyst)
	require.NoError(t, err)
	return view
}

func (f *inboxFixture) unavailable(t *testing.T, ids ...string) {
	t.Helper()
	for _, id := range ids {
		_, err := f.providers.SetAvailability(context.Background(), id, domain.AvailabilityUnavailable, nil,
			analyst.Email, inboxNow)
		require.NoError(t, err)
	}
}

func candidates(view assistance.CaseView) map[string]domain.AssistanceCandidate {
	out := map[string]domain.AssistanceCandidate{}
	for _, c := range view.Suggestion.Candidates {
		out[c.ProviderID] = c
	}
	return out
}

func actions(view assistance.CaseView) []domain.AssistanceActionCode {
	var out []domain.AssistanceActionCode
	for _, a := range view.Case.History {
		out = append(out, a.Code)
	}
	return out
}

func TestTheMockupRequestSuggestsTheNearestTowTruckOfTheSeed(t *testing.T) {
	f := newInbox(t)
	view := f.towing(t)

	s := view.Suggestion
	assert.Equal(t, domain.SuggestionFound, s.State)
	assert.Equal(t, domain.SpecialtyTowing, s.Specialty)
	assert.Equal(t, domain.SharedFromApp, s.Source)
	assert.Equal(t, "assignment-policy-v1", s.AssignmentPolicyVersion)
	require.Len(t, s.Result.Attempts, 1, "found within 5 km")

	got := candidates(view)
	require.Len(t, got, 4, "every provider of the registry that tows")
	near := got["prv-gruas-autopista-norte"]
	assert.True(t, near.Suggested)
	assert.Equal(t, 0.9, domain.RoundKm(near.DistanceKm))
	assert.Equal(t, 12, near.EtaMinutes)
	assert.Equal(t, 2.2, domain.RoundKm(got["prv-auxilio-vial-norte"].DistanceKm))
	assert.Equal(t, 15, got["prv-auxilio-vial-norte"].EtaMinutes)
	assert.Equal(t, domain.Eligible, got["prv-auxilio-vial-norte"].Eligibility)
	assert.Equal(t, 9.9, domain.RoundKm(got["prv-gruas-calle-80"].DistanceKm))
	assert.Equal(t, domain.NotEligibleOutOfRadius, got["prv-gruas-calle-80"].Eligibility)
	assert.Equal(t, domain.NotEligibleOutOfRadius, got["prv-gruas-del-valle"].Eligibility)
}

func TestTheSuggestionIsTheSearchOfTheAssignment(t *testing.T) {
	f := newInbox(t)
	view := f.towing(t)

	direct, err := assessors.Suggest(context.Background(), f.providers, autopista, domain.SpecialtyTowing,
		domain.AssignmentPolicyV1(), inboxNow)
	require.NoError(t, err)
	assert.Equal(t, direct, view.Suggestion.Result, "the same distance, availability and specialty of HU-W2.3-01")
}

func TestTheSuggestionWidensTheRadius(t *testing.T) {
	f := newInbox(t)
	f.unavailable(t, "prv-gruas-autopista-norte", "prv-auxilio-vial-norte")
	view := f.towing(t)

	assert.Equal(t, domain.SuggestionFound, view.Suggestion.State)
	assert.Len(t, view.Suggestion.Result.Attempts, 2, "5 km empty, then 10 km")
	assert.Equal(t, 10.0, view.Suggestion.Result.RadiusKm)
	suggested, ok := view.Suggestion.Result.Suggested()
	require.True(t, ok)
	assert.Equal(t, "prv-gruas-calle-80", suggested.ProviderID)
}

func TestWithoutEligibleProvidersTheRequestIsClosedWithItsReason(t *testing.T) {
	f := newInbox(t)
	f.unavailable(t, "prv-gruas-autopista-norte", "prv-auxilio-vial-norte", "prv-gruas-calle-80",
		"prv-gruas-del-valle")
	view := f.towing(t)
	assert.Equal(t, domain.SuggestionExhausted, view.Suggestion.State)
	assert.Len(t, view.Suggestion.Result.Attempts, 3)
	assert.Equal(t, domain.ReasonNoEligibleProvider, view.Suggestion.Result.Reason)

	_, err := f.svc.Dispatch(context.Background(), withGPS, "prv-gruas-autopista-norte", analyst)
	assert.ErrorIs(t, err, domain.ErrProviderNotEligible, "nobody who does not meet the criteria is dispatched")

	view, err = f.svc.Close(context.Background(), withGPS, domain.CloseInput{Reason: "no_provider_available"}, analyst)
	require.NoError(t, err)
	assert.Equal(t, domain.AssistanceClosed, view.Case.Request.Status)
	assert.Nil(t, view.Case.Dispatch)
	assert.Nil(t, view.Notice, "no provider is notified")
	assert.Zero(t, f.woken)
}

func TestARequestWithoutGPSIsServedFromTheLocationGivenByVoice(t *testing.T) {
	f := newInbox(t)
	f.request(t, withoutGPS, domain.LineAuto, nil, inboxNow)
	ctx := context.Background()

	view, err := f.svc.Attend(ctx, withoutGPS, analyst)
	require.NoError(t, err)
	assert.Equal(t, domain.SuggestionLocationMissing, view.Suggestion.State)
	view, err = f.svc.RecordNeed(ctx, withoutGPS, "towing", analyst)
	require.NoError(t, err)
	assert.Equal(t, domain.SuggestionLocationMissing, view.Suggestion.State, "the need alone is not enough")

	lat, lon := palmira.Latitude, palmira.Longitude
	view, err = f.svc.RecordVoiceLocation(ctx, withoutGPS,
		domain.VoiceLocationInput{Address: "Vía Cali–Palmira km 4", Latitude: &lat, Longitude: &lon}, analyst)
	require.NoError(t, err)
	assert.Equal(t, domain.SuggestionFound, view.Suggestion.State)
	assert.Equal(t, domain.SharedFromVoice, view.Suggestion.Source)
	suggested, _ := view.Suggestion.Result.Suggested()
	assert.Equal(t, "prv-gruas-del-valle", suggested.ProviderID)

	view, err = f.svc.Dispatch(ctx, withoutGPS, "prv-gruas-del-valle", analyst)
	require.NoError(t, err)
	assert.Equal(t, domain.SharedLocation{Source: domain.SharedFromVoice, Point: palmira,
		Address: "Vía Cali–Palmira km 4"}, view.Case.Dispatch.Location)
	assert.Contains(t, view.Notice.Body, "indicada por voz")
}

func TestTheLocationOfThePhoneIsNotReplacedByVoice(t *testing.T) {
	f := newInbox(t)
	f.towing(t)
	lat, lon := palmira.Latitude, palmira.Longitude
	_, err := f.svc.RecordVoiceLocation(context.Background(), withGPS,
		domain.VoiceLocationInput{Address: "Otra parte", Latitude: &lat, Longitude: &lon}, analyst)
	assert.ErrorIs(t, err, domain.ErrLocationFromApp)
}

func TestTheDispatchIsRecordedWithItsNoticeAndWakesTheWorker(t *testing.T) {
	f := newInbox(t)
	f.towing(t)
	f.clock.now = inboxNow.Add(3*time.Minute + 40*time.Second)

	view, err := f.svc.Dispatch(context.Background(), withGPS, "prv-gruas-autopista-norte", analyst)
	require.NoError(t, err)
	assert.Equal(t, 1, f.woken, "the worker sends the notice at once")
	assert.Equal(t, domain.AssistanceDispatched, view.Case.Request.Status)
	assert.Equal(t, domain.SuggestionNotApplicable, view.Suggestion.State)

	d := view.Case.Dispatch
	require.NotNil(t, d)
	assert.Equal(t, "prv-gruas-autopista-norte", d.Provider.ID)
	assert.Equal(t, "despachos@gruasautopistanorte.example.com", d.Provider.ContactEmail)
	assert.Equal(t, "prv-gruas-autopista-norte", d.SuggestedProviderID)
	assert.Equal(t, domain.NeedTowing, d.Need)
	assert.Equal(t, 12, d.EtaMinutes)
	assert.Equal(t, f.clock.now, d.At)
	assert.Equal(t, analyst, d.By)
	assert.Equal(t, "assistance-policy-v1", d.AssistancePolicyVersion)

	require.NotNil(t, view.Notice)
	assert.Equal(t, ports.NoticePending, view.Notice.Status)
	assert.Equal(t, "despachos@gruasautopistanorte.example.com", view.Notice.To)
	assert.Equal(t, "Asistencia AST-2026-0001 · Grúa", view.Notice.Subject)
	assert.Equal(t, []domain.AssistanceActionCode{domain.ActionAttended, domain.ActionNeedRecorded,
		domain.ActionDispatched}, actions(view))

	inbox, err := f.svc.Inbox(context.Background(), ports.InboxFilter{})
	require.NoError(t, err)
	require.NotNil(t, inbox.Summary.MeanDispatchSeconds)
	assert.Equal(t, 220, *inbox.Summary.MeanDispatchSeconds, "3 min 40 s from the request to the dispatch")
	assert.Equal(t, 1, inbox.Summary.DispatchedToday)

	f.request(t, atHome, domain.LineAuto, &autopista, f.clock.now)
	_, err = f.svc.Attend(context.Background(), atHome, colleague)
	require.NoError(t, err)
	next, err := f.svc.RecordNeed(context.Background(), atHome, "towing", colleague)
	require.NoError(t, err)
	assert.Equal(t, 1, candidates(next)["prv-gruas-autopista-norte"].DispatchedToday,
		"the provider is still eligible: a dispatch does not make it busy")
	assert.Equal(t, domain.Eligible, candidates(next)["prv-gruas-autopista-norte"].Eligibility)
}

func TestTheAnalystMayDispatchAnotherEligibleProvider(t *testing.T) {
	f := newInbox(t)
	f.towing(t)
	view, err := f.svc.Dispatch(context.Background(), withGPS, "prv-auxilio-vial-norte", colleague)
	require.NoError(t, err)
	assert.Equal(t, "prv-auxilio-vial-norte", view.Case.Dispatch.Provider.ID)
	assert.Equal(t, "prv-gruas-autopista-norte", view.Case.Dispatch.SuggestedProviderID)
	assert.Equal(t, colleague, view.Case.Dispatch.By)
}

func TestAProviderThatIsNotEligibleNowIsNotDispatched(t *testing.T) {
	f := newInbox(t)
	f.towing(t)
	f.unavailable(t, "prv-gruas-autopista-norte")

	for _, id := range []string{"prv-gruas-autopista-norte", "prv-gruas-calle-80", "prv-servicios-hogar-norte",
		"prv-no-existe"} {
		_, err := f.svc.Dispatch(context.Background(), withGPS, id, analyst)
		assert.ErrorIs(t, err, domain.ErrProviderNotEligible, id)
	}
	view, err := f.svc.Case(context.Background(), withGPS)
	require.NoError(t, err)
	assert.Equal(t, domain.AssistanceInCall, view.Case.Request.Status, "nothing was dispatched")
	assert.Zero(t, f.woken)
}

func TestARequestIsDecidedOnlyWhileOpen(t *testing.T) {
	f := newInbox(t)
	f.request(t, withGPS, domain.LineAuto, &autopista, inboxNow)
	ctx := context.Background()

	_, err := f.svc.RecordNeed(ctx, withGPS, "towing", analyst)
	assert.ErrorIs(t, err, domain.ErrAssistanceNotAttended)
	_, err = f.svc.Dispatch(ctx, withGPS, "prv-gruas-autopista-norte", analyst)
	assert.ErrorIs(t, err, domain.ErrAssistanceNotAttended)

	view, err := f.svc.Close(ctx, withGPS, domain.CloseInput{Reason: "duplicate"}, analyst)
	require.NoError(t, err, "a repeated request is closed without attending it")
	assert.Equal(t, domain.AssistanceClosed, view.Case.Request.Status)
	assert.Equal(t, domain.CloseDuplicate, view.Case.Closure.Reason)

	_, err = f.svc.Attend(ctx, withGPS, analyst)
	assert.ErrorIs(t, err, domain.ErrAssistanceNotOpen)
	_, err = f.svc.Close(ctx, withGPS, domain.CloseInput{Reason: "duplicate"}, analyst)
	assert.ErrorIs(t, err, domain.ErrAssistanceNotOpen)

	_, err = f.svc.Attend(ctx, "no-existe", analyst)
	assert.ErrorIs(t, err, ports.ErrAssistanceRequestNotFound)
	_, err = f.svc.Case(ctx, "no-existe")
	assert.ErrorIs(t, err, ports.ErrAssistanceRequestNotFound)
}

func TestEveryDecisionKeepsItsAnalystAndItsTime(t *testing.T) {
	f := newInbox(t)
	f.request(t, withGPS, domain.LineAuto, &autopista, inboxNow)
	ctx := context.Background()

	_, err := f.svc.Attend(ctx, withGPS, analyst)
	require.NoError(t, err)
	view, err := f.svc.Attend(ctx, withGPS, analyst)
	require.NoError(t, err)
	assert.Len(t, view.Case.History, 1, "continuing a request of one's own records nothing")

	f.clock.now = inboxNow.Add(time.Minute)
	view, err = f.svc.Attend(ctx, withGPS, colleague)
	require.NoError(t, err)
	assert.Equal(t, &colleague, view.Case.HandledBy())

	f.clock.now = inboxNow.Add(2 * time.Minute)
	view, err = f.svc.Close(ctx, withGPS, domain.CloseInput{Reason: "other", Note: ptr("Le pasaron corriente")},
		colleague)
	require.NoError(t, err)

	h := view.Case.History
	require.Len(t, h, 3)
	assert.Equal(t, domain.AssistanceAction{Seq: 1, Code: domain.ActionAttended, By: analyst, At: inboxNow}, h[0])
	assert.Equal(t, domain.AssistanceAction{Seq: 2, Code: domain.ActionTakenOver, By: colleague,
		At: inboxNow.Add(time.Minute), From: analyst.Email}, h[1])
	assert.Equal(t, domain.AssistanceAction{Seq: 3, Code: domain.ActionClosed, By: colleague,
		At: inboxNow.Add(2 * time.Minute), Reason: domain.CloseOther}, h[2])
	assert.Equal(t, "Le pasaron corriente", *view.Case.Closure.Note)
}

func TestTheInboxListsTheLatestFirstWithItsIndicators(t *testing.T) {
	f := newInbox(t)
	ctx := context.Background()
	f.request(t, withoutGPS, domain.LineAuto, nil, inboxNow.Add(-20*time.Minute))
	f.request(t, atHome, domain.LineHome, nil, inboxNow.Add(-10*time.Minute))
	f.request(t, withGPS, domain.LineAuto, &autopista, inboxNow)
	_, err := f.svc.Attend(ctx, atHome, analyst)
	require.NoError(t, err)

	inbox, err := f.svc.Inbox(ctx, ports.InboxFilter{})
	require.NoError(t, err)
	var ids []string
	for _, r := range inbox.Items {
		ids = append(ids, r.Case.Request.ID)
	}
	assert.Equal(t, []string{withGPS, atHome, withoutGPS}, ids, "the latest first")
	assert.Equal(t, domain.InboxSummary{New: 2, InCall: 1, TargetSeconds: 300}, inbox.Summary)
	assert.Equal(t, &analyst, inbox.Items[1].Case.HandledBy())

	inbox, err = f.svc.Inbox(ctx, ports.InboxFilter{Statuses: []domain.AssistanceStatus{domain.AssistanceInCall}})
	require.NoError(t, err)
	require.Len(t, inbox.Items, 1)
	assert.Equal(t, atHome, inbox.Items[0].Case.Request.ID)
	assert.Equal(t, 2, inbox.Summary.New, "the indicators do not depend on the filter")

	inbox, err = f.svc.Inbox(ctx, ports.InboxFilter{Limit: 1})
	require.NoError(t, err)
	assert.Len(t, inbox.Items, 1)
}

func TestTheInboxAsksForADefaultAndABoundedPage(t *testing.T) {
	f := newInbox(t)
	spy := &listSpy{AssistanceInboxStore: f.inbox}
	svc := assistance.NewInboxService(spy, f.providers, f.clock, nil)
	for limit, want := range map[int]int{0: 50, -1: 50, 1: 1, 100: 100, 101: 100} {
		_, err := svc.Inbox(context.Background(), ports.InboxFilter{Limit: limit})
		require.NoError(t, err)
		assert.Equal(t, want, spy.limit, "limit %d", limit)
	}
}

type listSpy struct {
	*memory.AssistanceInboxStore
	limit int
}

func (s *listSpy) List(ctx context.Context, f ports.InboxFilter) ([]ports.AssistanceRecord, error) {
	s.limit = f.Limit
	return s.AssistanceInboxStore.List(ctx, f)
}

func TestTheNeedMustBeOfTheLineOfThePolicy(t *testing.T) {
	f := newInbox(t)
	f.request(t, atHome, domain.LineHome, nil, inboxNow)
	_, err := f.svc.Attend(context.Background(), atHome, analyst)
	require.NoError(t, err)
	_, err = f.svc.RecordNeed(context.Background(), atHome, "towing", analyst)
	assert.ErrorIs(t, err, domain.ErrNeedNotForLine)
	view, err := f.svc.RecordNeed(context.Background(), atHome, "plumbing_emergency", analyst)
	require.NoError(t, err)
	assert.Equal(t, domain.SpecialtyPlumbing, view.Case.Need.Specialty)
}

// racer decides on the request as another analyst right before the first
// write of the service, so the write finds the request changed.
type racer struct {
	*memory.AssistanceInboxStore
	once   sync.Once
	before func()
}

func (r *racer) Dispatch(ctx context.Context, id string, version int, d domain.AssistanceDispatch,
	a domain.AssistanceAction, n ports.AssistanceNotice) error {
	r.once.Do(r.before)
	return r.AssistanceInboxStore.Dispatch(ctx, id, version, d, a, n)
}

func TestOfTwoAnalystsDecidingAtOnceTheSecondReadsTheFirst(t *testing.T) {
	f := newInbox(t)
	f.towing(t)
	r := &racer{AssistanceInboxStore: f.inbox, before: func() {
		_, err := f.svc.Close(context.Background(), withGPS, domain.CloseInput{Reason: "resolved_by_phone"}, colleague)
		require.NoError(t, err)
	}}
	svc := assistance.NewInboxService(r, f.providers, f.clock, nil)

	_, err := svc.Dispatch(context.Background(), withGPS, "prv-gruas-autopista-norte", analyst)
	assert.ErrorIs(t, err, domain.ErrAssistanceNotOpen, "it read again and found the request closed")
	view, err := f.svc.Case(context.Background(), withGPS)
	require.NoError(t, err)
	assert.Nil(t, view.Case.Dispatch)
	assert.Equal(t, domain.CloseResolvedByPhone, view.Case.Closure.Reason)
}

// alwaysChanged is a store whose request changes before every write.
type alwaysChanged struct {
	*memory.AssistanceInboxStore
	writes int
}

func (s *alwaysChanged) Attend(context.Context, string, int, domain.AssistanceAction) error {
	s.writes++
	return ports.ErrAssistanceChanged
}

func TestADecisionGivesUpAfterThreeAttempts(t *testing.T) {
	f := newInbox(t)
	f.request(t, withGPS, domain.LineAuto, &autopista, inboxNow)
	s := &alwaysChanged{AssistanceInboxStore: f.inbox}
	svc := assistance.NewInboxService(s, f.providers, f.clock, nil)
	_, err := svc.Attend(context.Background(), withGPS, analyst)
	assert.ErrorIs(t, err, ports.ErrAssistanceChanged)
	assert.Equal(t, 3, s.writes)
}

// failing fails the reads of the inbox it is told to.
type failing struct {
	*memory.AssistanceInboxStore
	list, counts, dispatches error
}

func (s *failing) List(ctx context.Context, f ports.InboxFilter) ([]ports.AssistanceRecord, error) {
	if s.list != nil {
		return nil, s.list
	}
	return s.AssistanceInboxStore.List(ctx, f)
}

func (s *failing) Counts(ctx context.Context, day time.Time) (int, int, []time.Duration, error) {
	if s.counts != nil {
		return 0, 0, nil, s.counts
	}
	return s.AssistanceInboxStore.Counts(ctx, day)
}

func (s *failing) DispatchesSince(ctx context.Context, ids []string, day time.Time) (map[string]int, error) {
	if s.dispatches != nil {
		return nil, s.dispatches
	}
	return s.AssistanceInboxStore.DispatchesSince(ctx, ids, day)
}

// brokenRegistry is a registry of providers that does not answer.
type brokenRegistry struct {
	*memory.ProviderStore
	search, get error
}

func (b *brokenRegistry) WithSpecialty(ctx context.Context, s domain.Specialty) ([]domain.Provider, error) {
	if b.search != nil {
		return nil, b.search
	}
	return b.ProviderStore.WithSpecialty(ctx, s)
}

func (b *brokenRegistry) Get(ctx context.Context, id string) (domain.Provider, error) {
	if b.get != nil {
		return domain.Provider{}, b.get
	}
	return b.ProviderStore.Get(ctx, id)
}

func TestTheFailuresOfTheStoresAreReturned(t *testing.T) {
	down := errors.New("down")
	f := newInbox(t)
	f.towing(t)
	ctx := context.Background()

	svc := assistance.NewInboxService(&failing{AssistanceInboxStore: f.inbox, list: down}, f.providers, f.clock, nil)
	_, err := svc.Inbox(ctx, ports.InboxFilter{})
	assert.ErrorIs(t, err, down)
	svc = assistance.NewInboxService(&failing{AssistanceInboxStore: f.inbox, counts: down}, f.providers, f.clock, nil)
	_, err = svc.Inbox(ctx, ports.InboxFilter{})
	assert.ErrorIs(t, err, down)
	svc = assistance.NewInboxService(&failing{AssistanceInboxStore: f.inbox, dispatches: down}, f.providers, f.clock,
		nil)
	_, err = svc.Case(ctx, withGPS)
	assert.ErrorIs(t, err, down)

	svc = assistance.NewInboxService(f.inbox, &brokenRegistry{ProviderStore: f.providers, search: down}, f.clock, nil)
	_, err = svc.Case(ctx, withGPS)
	assert.ErrorIs(t, err, down)
	_, err = svc.Dispatch(ctx, withGPS, "prv-gruas-autopista-norte", analyst)
	assert.ErrorIs(t, err, down)
	svc = assistance.NewInboxService(f.inbox, &brokenRegistry{ProviderStore: f.providers, get: down}, f.clock, nil)
	_, err = svc.Dispatch(ctx, withGPS, "prv-gruas-autopista-norte", analyst)
	assert.ErrorIs(t, err, down)
}

func TestThePolicyOfTheInbox(t *testing.T) {
	f := newInbox(t)
	p, a := f.svc.Policy()
	assert.Equal(t, "assistance-policy-v1", p.Version)
	assert.Equal(t, []float64{5, 10, 20}, a.RadiiKm)
}

func TestTheNoticeCarriesTheLocationOfThePhone(t *testing.T) {
	f := newInbox(t)
	f.towing(t)
	view, err := f.svc.Dispatch(context.Background(), withGPS, "prv-gruas-autopista-norte", analyst)
	require.NoError(t, err)
	n := view.Notice
	assert.Equal(t, "Solventa le asigna la asistencia AST-2026-0001.\n\n"+
		"Necesidad: Grúa\nLínea: Autos · KJT-482\n"+
		"Ubicación: 4.7712, -74.0452 (precisión 8 m, compartida desde el teléfono)\n"+
		"Dirección: Autopista Norte km 12, Bogotá\n"+
		"Mapa: https://www.google.com/maps/search/?api=1&query=4.7712,-74.0452\n"+
		"Solicitada: 2026-11-15 21:31 UTC\n", n.Body)
	assert.NotContains(t, n.Body, "Carolina", "nothing of the insured")
	assert.NotContains(t, n.Subject, "Carolina")
}

func TestTheNoticeOfAnUnknownNeedOrLineStillReads(t *testing.T) {
	r := domain.AssistanceRequest{Number: "AST-2026-0002", RequestedAt: inboxNow,
		Policy: domain.AssistancePolicy{Line: "marine"}}
	d := domain.AssistanceDispatch{Need: "ambulance", Location: domain.SharedLocation{Source: domain.SharedFromApp,
		Point: autopista}}
	subject, body := assistance.DispatchNotice(r, d)
	assert.Equal(t, "Asistencia AST-2026-0002 · ambulance", subject)
	assert.Contains(t, body, "Línea: marine\n")
	assert.Contains(t, body, "(compartida desde el teléfono)")
	assert.Contains(t, body, "Dirección: (no registrada)")
	assert.False(t, strings.Contains(body, "·  "), "no empty insured object")
}
