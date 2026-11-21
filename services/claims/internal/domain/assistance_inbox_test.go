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

// The inbox of the requests for assistance (HU-W2.3-03).

var (
	// requestedAt is AST-2026-0912 of the mockup: 16:31 in Bogotá.
	requestedAt = time.Date(2026, 11, 15, 21, 31, 4, 0, time.UTC)
	attendedAt  = requestedAt.Add(40 * time.Second)
	colleague   = domain.Analyst{Email: "laura.gomez@solventa.example", Name: "Laura Gómez"}
)

// assistanceRequest is a request of the line; with GPS it was sent from the
// place of the mockup of HU-W2.3-01 (incident), so north(km) is km away.
func assistanceRequest(status domain.AssistanceStatus, line domain.PolicyLine, gps bool) domain.AssistanceRequest {
	r := domain.AssistanceRequest{ID: "3b8e2f6a-1c4d-4e7f-9a2b-6c5d8e0f1a2b", Number: "AST-2026-0912",
		Subject: "sub-carolina", RequesterName: "Carolina Pérez Ruiz", Status: status, RequestedAt: requestedAt,
		Policy: domain.AssistancePolicy{ID: "AU-2025-118204", Line: line, Product: "Autos", InsuredObject: "KJT-482"},
		Place:  text("Autopista Norte km 12, Bogotá")}
	if gps {
		point, accuracy, captured := incident, 8.0, requestedAt.Add(-12*time.Second)
		r.Location = domain.AssistanceLocation{Status: domain.LocationSent, Coordinates: &point, AccuracyM: &accuracy,
			CapturedAt: &captured}
	} else {
		r.Location = domain.AssistanceLocation{Status: domain.LocationUnavailable,
			UnavailableReason: domain.UnavailableNoFix}
	}
	return r
}

// inCall is an auto request with GPS attended by the analyst.
func inCall() domain.AssistanceCase {
	return domain.AssistanceCase{Request: assistanceRequest(domain.AssistanceInCall, domain.LineAuto, true),
		History: []domain.AssistanceAction{{Seq: 1, Code: domain.ActionAttended, By: analyst, At: attendedAt}}}
}

// withNeed is the case after the analyst recorded the need.
func withNeed(t *testing.T, c domain.AssistanceCase, need domain.AssistanceNeed) domain.AssistanceCase {
	t.Helper()
	n, _, err := domain.RecordNeed(c, domain.AssistanceInboxPolicyV1(), string(need), analyst, attendedAt)
	require.NoError(t, err)
	c.Need = &n
	return c
}

func voiceAt(address string, lat, lon float64) domain.VoiceLocationInput {
	return domain.VoiceLocationInput{Address: address, Latitude: &lat, Longitude: &lon}
}

// towTrucks are three tow trucks around the incident: one at 0.9 km, one
// unavailable at 2.2 km and one at 9.9 km, out of the initial radius; and a
// workshop that does not tow.
func towTrucks() []domain.Provider {
	return []domain.Provider{
		provider("prv-gruas-autopista-norte", 0.9, domain.SpecialtyTowing),
		unavailable(provider("prv-auxilio-vial-norte", 2.2, domain.SpecialtyRoadsideAssistance, domain.SpecialtyTowing)),
		provider("prv-gruas-calle-80", 9.9, domain.SpecialtyTowing),
		provider("prv-taller-cercano", 0.2, domain.SpecialtyBodywork),
	}
}

func searchTowing(providers []domain.Provider) domain.SearchResult {
	return domain.Search(incident, domain.SpecialtyTowing, providers, domain.AssignmentPolicyV1().RadiiKm, attendedAt)
}

func TestTheStatusesOfTheInbox(t *testing.T) {
	open := map[domain.AssistanceStatus]bool{domain.AssistanceNew: true, domain.AssistanceInCall: true,
		domain.AssistanceDispatched: false, domain.AssistanceClosed: false}
	for s, isOpen := range open {
		assert.True(t, s.Valid(), s)
		assert.Equal(t, isOpen, s.Open(), s)
	}
	assert.False(t, domain.AssistanceStatus("cancelled").Valid())
	assert.False(t, domain.AssistanceStatus("").Valid())
}

func TestAnAnalystAttendsANewRequest(t *testing.T) {
	c := domain.AssistanceCase{Request: assistanceRequest(domain.AssistanceNew, domain.LineAuto, true)}
	assert.Nil(t, c.HandledBy(), "nobody handles a new request")

	action, changed, err := domain.Attend(c, analyst, attendedAt)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, domain.AssistanceAction{Code: domain.ActionAttended, By: analyst, At: attendedAt}, action)
}

func TestAnotherAnalystTakesOverARequestInCall(t *testing.T) {
	c := inCall()
	assert.Equal(t, &analyst, c.HandledBy())

	_, changed, err := domain.Attend(c, analyst, attendedAt.Add(time.Minute))
	require.NoError(t, err)
	assert.False(t, changed, "the same analyst continues: nothing is recorded")

	action, changed, err := domain.Attend(c, colleague, attendedAt.Add(time.Minute))
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, domain.ActionTakenOver, action.Code)
	assert.Equal(t, analyst.Email, action.From)

	c.History = append(c.History, action)
	assert.Equal(t, &colleague, c.HandledBy(), "the last one who attended handles it")

	orphan := domain.AssistanceCase{Request: assistanceRequest(domain.AssistanceInCall, domain.LineAuto, true)}
	action, changed, err = domain.Attend(orphan, colleague, attendedAt)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, "", action.From, "a request in call with no history is taken from nobody")
}

func TestEveryDecisionNeedsTheRequestInCall(t *testing.T) {
	p := domain.AssistanceInboxPolicyV1()
	cases := map[domain.AssistanceStatus]error{
		domain.AssistanceNew:        domain.ErrAssistanceNotAttended,
		domain.AssistanceDispatched: domain.ErrAssistanceNotOpen,
		domain.AssistanceClosed:     domain.ErrAssistanceNotOpen,
	}
	for status, want := range cases {
		c := domain.AssistanceCase{Request: assistanceRequest(status, domain.LineAuto, false)}
		_, _, err := domain.RecordNeed(c, p, "towing", analyst, attendedAt)
		assert.ErrorIs(t, err, want, "need on %s", status)
		_, _, err = domain.RecordVoiceLocation(c, voiceAt("Calle 26 # 68-40", 4.65, -74.1), analyst, attendedAt)
		assert.ErrorIs(t, err, want, "voice on %s", status)
		_, _, err = domain.Dispatch(c, p, domain.AssignmentPolicyVersionV1, domain.SearchResult{}, nil, analyst, attendedAt)
		assert.ErrorIs(t, err, want, "dispatch on %s", status)
	}
	for _, status := range []domain.AssistanceStatus{domain.AssistanceDispatched, domain.AssistanceClosed} {
		c := domain.AssistanceCase{Request: assistanceRequest(status, domain.LineAuto, true)}
		_, _, err := domain.Attend(c, analyst, attendedAt)
		assert.ErrorIs(t, err, domain.ErrAssistanceNotOpen, "attend on %s", status)
		_, _, err = domain.Close(c, p, domain.CloseInput{Reason: "duplicate"}, analyst, attendedAt)
		assert.ErrorIs(t, err, domain.ErrAssistanceNotOpen, "close on %s", status)
	}
}

func TestTheAnalystRecordsTheNeedOfTheLine(t *testing.T) {
	p := domain.AssistanceInboxPolicyV1()
	c := inCall()

	need, action, err := domain.RecordNeed(c, p, " towing ", analyst, attendedAt)
	require.NoError(t, err)
	assert.Equal(t, domain.RecordedNeed{Seq: 1, Need: domain.NeedTowing, Specialty: domain.SpecialtyTowing,
		PolicyVersion: "assistance-policy-v1", By: analyst, At: attendedAt}, need)
	assert.Equal(t, domain.AssistanceAction{Code: domain.ActionNeedRecorded, By: analyst, At: attendedAt,
		Need: domain.NeedTowing}, action)

	c.Need = &need
	again, _, err := domain.RecordNeed(c, p, "battery", colleague, attendedAt.Add(time.Minute))
	require.NoError(t, err)
	assert.Equal(t, 2, again.Seq, "the need may change until the dispatch; the last one is in force")
	assert.Equal(t, domain.SpecialtyRoadsideAssistance, again.Specialty)

	home := domain.AssistanceCase{Request: assistanceRequest(domain.AssistanceInCall, domain.LineHome, false)}
	need, _, err = domain.RecordNeed(home, p, "plumbing_emergency", analyst, attendedAt)
	require.NoError(t, err)
	assert.Equal(t, domain.SpecialtyPlumbing, need.Specialty)
	_, _, err = domain.RecordNeed(home, p, "towing", analyst, attendedAt)
	assert.ErrorIs(t, err, domain.ErrNeedNotForLine, "a home policy does not take a tow truck")

	travel := domain.AssistanceCase{Request: assistanceRequest(domain.AssistanceInCall, domain.LineTravel, false)}
	_, _, err = domain.RecordNeed(travel, p, "battery", analyst, attendedAt)
	assert.ErrorIs(t, err, domain.ErrNeedNotForLine, "travel has no on-site need")
}

func TestANeedThatIsNotOfTheCatalogIsRefused(t *testing.T) {
	p := domain.AssistanceInboxPolicyV1()
	for need, code := range map[string]string{"": "required", "   ": "required", "ambulance": "invalid_format",
		"TOWING": "invalid_format"} {
		_, _, err := domain.RecordNeed(inCall(), p, need, analyst, attendedAt)
		assert.Equal(t, map[string]string{"need": code}, fieldsOf(t, err), "%q", need)
	}
}

func TestTheAnalystRecordsTheLocationGivenByVoice(t *testing.T) {
	c := domain.AssistanceCase{Request: assistanceRequest(domain.AssistanceInCall, domain.LineAuto, false),
		History: inCall().History}
	_, ok := c.Location()
	assert.False(t, ok, "without GPS and without voice there is no location")

	voice, action, err := domain.RecordVoiceLocation(c, voiceAt("  Vía Cali–Palmira km 4 ", 3.4921, -76.4203), analyst,
		attendedAt)
	require.NoError(t, err)
	assert.Equal(t, domain.VoiceLocation{Seq: 1, Address: "Vía Cali–Palmira km 4",
		Point: domain.Coordinates{Latitude: 3.4921, Longitude: -76.4203}, By: analyst, At: attendedAt}, voice)
	assert.Equal(t, domain.ActionVoiceLocationRecorded, action.Code)

	c.Voice = &voice
	loc, ok := c.Location()
	require.True(t, ok)
	assert.Equal(t, domain.SharedLocation{Source: domain.SharedFromVoice, Point: voice.Point,
		Address: "Vía Cali–Palmira km 4", Place: c.Request.Place}, loc)

	corrected, _, err := domain.RecordVoiceLocation(c, voiceAt("Vía Cali–Palmira km 5", 3.49, -76.41), colleague,
		attendedAt.Add(time.Minute))
	require.NoError(t, err)
	assert.Equal(t, 2, corrected.Seq, "it may be corrected; the last one is in force")
}

func TestTheLocationOfThePhoneIsNotReplacedByVoice(t *testing.T) {
	c := inCall()
	_, _, err := domain.RecordVoiceLocation(c, voiceAt("Autopista Norte km 12", 4.7712, -74.0452), analyst, attendedAt)
	assert.ErrorIs(t, err, domain.ErrLocationFromApp)

	loc, ok := c.Location()
	require.True(t, ok)
	assert.Equal(t, domain.SharedFromApp, loc.Source)
	assert.Equal(t, incident, loc.Point)
	assert.Equal(t, 8.0, *loc.AccuracyM)
	assert.Equal(t, "Autopista Norte km 12, Bogotá", *loc.Place)
	assert.Empty(t, loc.Address)
}

func TestEveryInvalidFieldOfTheVoiceLocationIsReportedAtOnce(t *testing.T) {
	c := domain.AssistanceCase{Request: assistanceRequest(domain.AssistanceInCall, domain.LineAuto, false)}
	_, _, err := domain.RecordVoiceLocation(c, domain.VoiceLocationInput{Address: "  "}, analyst, attendedAt)
	assert.Equal(t, map[string]string{"address": "required", "latitude": "required", "longitude": "required"},
		fieldsOf(t, err))

	cases := []struct {
		name     string
		in       domain.VoiceLocationInput
		failures map[string]string
	}{
		{"address of 4", voiceAt("abcd", 0, 0), map[string]string{"address": "out_of_range"}},
		{"address of 5", voiceAt("abcde", 0, 0), nil},
		{"address of 200", voiceAt(strings.Repeat("ñ", 200), 0, 0), nil},
		{"address of 201", voiceAt(strings.Repeat("ñ", 201), 0, 0), map[string]string{"address": "out_of_range"}},
		{"address with a NUL", voiceAt("Vía Cali\x00Palmira", 0, 0), map[string]string{"address": "invalid_format"}},
		{"poles and antimeridian", voiceAt("Polo Sur", -90, 180), nil},
		{"latitude past the pole", voiceAt("Polo Norte", 90.0001, 0), map[string]string{"latitude": "out_of_range"}},
		{"longitude past the antimeridian", voiceAt("Pacífico", 0, -180.0001),
			map[string]string{"longitude": "out_of_range"}},
		{"not a number", voiceAt("Ninguna parte", math.NaN(), math.NaN()),
			map[string]string{"latitude": "out_of_range", "longitude": "out_of_range"}},
	}
	for _, tc := range cases {
		_, _, err := domain.RecordVoiceLocation(c, tc.in, analyst, attendedAt)
		if tc.failures == nil {
			assert.NoError(t, err, tc.name)
			continue
		}
		assert.Equal(t, tc.failures, fieldsOf(t, err), tc.name)
	}
}

func TestWhatTheSuggestionSearchesFor(t *testing.T) {
	closed := domain.AssistanceCase{Request: assistanceRequest(domain.AssistanceClosed, domain.LineAuto, true)}
	_, _, state := closed.Searchable()
	assert.Equal(t, domain.SuggestionNotApplicable, state)

	noGPS := domain.AssistanceCase{Request: assistanceRequest(domain.AssistanceNew, domain.LineAuto, false)}
	_, _, state = noGPS.Searchable()
	assert.Equal(t, domain.SuggestionLocationMissing, state, "without a location there is nothing to search from")

	c := inCall()
	_, loc, state := c.Searchable()
	assert.Equal(t, domain.SuggestionNeedMissing, state)
	assert.Equal(t, domain.SharedFromApp, loc.Source)

	c = withNeed(t, c, domain.NeedTowing)
	specialty, loc, state := c.Searchable()
	assert.Equal(t, domain.SuggestionState(""), state, "searchable")
	assert.Equal(t, domain.SpecialtyTowing, specialty)
	assert.Equal(t, incident, loc.Point)
}

func TestTheSuggestionCarriesTheArrivalAndTheDispatchesOfToday(t *testing.T) {
	p := domain.AssistanceInboxPolicyV1()
	_, loc, _ := withNeed(t, inCall(), domain.NeedTowing).Searchable()

	s := domain.NewSuggestion(domain.SpecialtyTowing, loc, searchTowing(towTrucks()), p,
		domain.AssignmentPolicyVersionV1, map[string]int{"prv-gruas-autopista-norte": 2})
	assert.Equal(t, domain.SuggestionFound, s.State)
	assert.Equal(t, domain.SharedFromApp, s.Source)
	assert.Equal(t, "assignment-policy-v1", s.AssignmentPolicyVersion)
	require.Len(t, s.Candidates, 3, "the workshop does not tow")

	got := map[string]domain.AssistanceCandidate{}
	for _, c := range s.Candidates {
		got[c.ProviderID] = c
	}
	assert.True(t, got["prv-gruas-autopista-norte"].Suggested)
	assert.Equal(t, 12, got["prv-gruas-autopista-norte"].EtaMinutes)
	assert.Equal(t, 2, got["prv-gruas-autopista-norte"].DispatchedToday)
	assert.Equal(t, domain.NotEligibleUnavailable, got["prv-auxilio-vial-norte"].Eligibility)
	assert.Equal(t, 15, got["prv-auxilio-vial-norte"].EtaMinutes)
	assert.Equal(t, domain.NotEligibleOutOfRadius, got["prv-gruas-calle-80"].Eligibility)
	assert.Equal(t, 30, got["prv-gruas-calle-80"].EtaMinutes)
	assert.Zero(t, got["prv-gruas-calle-80"].DispatchedToday)

	nobody := []domain.Provider{unavailable(provider("prv-gruas-autopista-norte", 0.9, domain.SpecialtyTowing))}
	s = domain.NewSuggestion(domain.SpecialtyTowing, loc, searchTowing(nobody), p, domain.AssignmentPolicyVersionV1, nil)
	assert.Equal(t, domain.SuggestionExhausted, s.State)
	assert.Len(t, s.Result.Attempts, 3, "5, 10 and 20 km")
	assert.Equal(t, domain.ReasonNoEligibleProvider, s.Result.Reason)

	none := domain.NoSuggestion(domain.SuggestionNeedMissing, domain.AssignmentPolicyVersionV1)
	assert.Equal(t, domain.SuggestionNeedMissing, none.State)
	assert.Empty(t, none.Candidates)
}

func TestTheAnalystDispatchesTheSuggestedProvider(t *testing.T) {
	p := domain.AssistanceInboxPolicyV1()
	c := withNeed(t, inCall(), domain.NeedTowing)
	trucks := towTrucks()
	result := searchTowing(trucks)
	at := attendedAt.Add(3 * time.Minute)

	d, action, err := domain.Dispatch(c, p, domain.AssignmentPolicyVersionV1, result, &trucks[0], analyst, at)
	require.NoError(t, err)
	assert.Equal(t, domain.DispatchedProvider{ID: "prv-gruas-autopista-norte", Name: "prv-gruas-autopista-norte",
		ContactEmail: "prv-gruas-autopista-norte@example.com"}, d.Provider)
	assert.Equal(t, domain.NeedTowing, d.Need)
	assert.Equal(t, domain.SpecialtyTowing, d.Specialty)
	assert.InDelta(t, 0.9, d.DistanceKm, 1e-9)
	assert.Equal(t, 12, d.EtaMinutes)
	assert.Equal(t, domain.SharedFromApp, d.Location.Source)
	assert.Equal(t, incident, d.Location.Point)
	assert.Equal(t, "prv-gruas-autopista-norte", d.SuggestedProviderID)
	assert.Equal(t, result.Candidates, d.Candidates, "what the analyst saw is kept")
	assert.Equal(t, result.Attempts, d.Attempts)
	assert.Equal(t, "assistance-policy-v1", d.AssistancePolicyVersion)
	assert.Equal(t, "assignment-policy-v1", d.AssignmentPolicyVersion)
	assert.Equal(t, analyst, d.By)
	assert.Equal(t, at, d.At)
	assert.Equal(t, domain.AssistanceAction{Code: domain.ActionDispatched, By: analyst, At: at,
		ProviderID: "prv-gruas-autopista-norte"}, action)
}

func TestTheAnalystMayDispatchAnotherEligibleProvider(t *testing.T) {
	p := domain.AssistanceInboxPolicyV1()
	c := withNeed(t, inCall(), domain.NeedTowing)
	trucks := append(towTrucks(), provider("prv-gruas-norte-dos", 3.1, domain.SpecialtyTowing))
	result := searchTowing(trucks)

	d, _, err := domain.Dispatch(c, p, domain.AssignmentPolicyVersionV1, result, &trucks[4], colleague, attendedAt)
	require.NoError(t, err)
	assert.Equal(t, "prv-gruas-norte-dos", d.Provider.ID)
	assert.Equal(t, "prv-gruas-autopista-norte", d.SuggestedProviderID, "the suggestion is kept as it was")
	assert.Equal(t, 17, d.EtaMinutes)
}

func TestAProviderThatIsNotEligibleNowIsNotDispatched(t *testing.T) {
	p := domain.AssistanceInboxPolicyV1()
	c := withNeed(t, inCall(), domain.NeedTowing)
	trucks := towTrucks()
	result := searchTowing(trucks)

	busyNow := trucks[0]
	busyNow.Availability = domain.AvailabilityUnavailable
	noLongerTows := trucks[0]
	noLongerTows.Specialties = []domain.Specialty{domain.SpecialtyBodywork}
	stranger := provider("prv-otra-grua", 0.1, domain.SpecialtyTowing)

	cases := map[string]*domain.Provider{
		"unknown to the registry":          nil,
		"unavailable in the search":        &trucks[1],
		"out of the radius":                &trucks[2],
		"without the specialty":            &trucks[3],
		"unavailable since the search":     &busyNow,
		"without the specialty any longer": &noLongerTows,
		"not among the candidates":         &stranger,
	}
	for name, current := range cases {
		_, _, err := domain.Dispatch(c, p, domain.AssignmentPolicyVersionV1, result, current, analyst, attendedAt)
		assert.ErrorIs(t, err, domain.ErrProviderNotEligible, name)
	}
}

func TestADispatchNeedsALocationAndANeed(t *testing.T) {
	p := domain.AssistanceInboxPolicyV1()
	trucks := towTrucks()
	result := searchTowing(trucks)

	noNeed := inCall()
	_, _, err := domain.Dispatch(noNeed, p, domain.AssignmentPolicyVersionV1, result, &trucks[0], analyst, attendedAt)
	assert.ErrorIs(t, err, domain.ErrNeedMissing)

	noLocation := withNeed(t, domain.AssistanceCase{
		Request: assistanceRequest(domain.AssistanceInCall, domain.LineAuto, false)}, domain.NeedTowing)
	_, _, err = domain.Dispatch(noLocation, p, domain.AssignmentPolicyVersionV1, result, &trucks[0], analyst, attendedAt)
	assert.ErrorIs(t, err, domain.ErrLocationMissing)

	voice, _, err := domain.RecordVoiceLocation(noLocation, voiceAt("Calle 170 con Autopista", incident.Latitude,
		incident.Longitude), analyst, attendedAt)
	require.NoError(t, err)
	noLocation.Voice = &voice
	d, _, err := domain.Dispatch(noLocation, p, domain.AssignmentPolicyVersionV1, result, &trucks[0], analyst, attendedAt)
	require.NoError(t, err)
	assert.Equal(t, domain.SharedLocation{Source: domain.SharedFromVoice, Point: incident,
		Address: "Calle 170 con Autopista", Place: noLocation.Request.Place}, d.Location, "the pin of the analyst")
}

func TestARequestIsClosedWithItsReason(t *testing.T) {
	p := domain.AssistanceInboxPolicyV1()
	newOne := domain.AssistanceCase{Request: assistanceRequest(domain.AssistanceNew, domain.LineAuto, true)}

	closure, action, err := domain.Close(newOne, p, domain.CloseInput{Reason: "duplicate"}, analyst, attendedAt)
	require.NoError(t, err, "a repeated request is closed without attending it")
	assert.Equal(t, domain.AssistanceClosure{Reason: domain.CloseDuplicate, By: analyst, At: attendedAt}, closure)
	assert.Equal(t, domain.AssistanceAction{Code: domain.ActionClosed, By: analyst, At: attendedAt,
		Reason: domain.CloseDuplicate}, action)

	for _, r := range p.CloseReasons {
		in := domain.CloseInput{Reason: string(r), Note: text("  Se resolvió en la llamada  ")}
		closure, _, err = domain.Close(inCall(), p, in, colleague, attendedAt)
		require.NoError(t, err, r)
		assert.Equal(t, "Se resolvió en la llamada", *closure.Note, r)
	}
}

func TestTheReasonAndTheNoteOfAClosingAtTheirLimits(t *testing.T) {
	p := domain.AssistanceInboxPolicyV1()
	cases := []struct {
		name     string
		in       domain.CloseInput
		failures map[string]string
	}{
		{"no reason", domain.CloseInput{}, map[string]string{"reason": "required"}},
		{"unknown reason", domain.CloseInput{Reason: "dispatched"}, map[string]string{"reason": "invalid_format"}},
		{"other without a note", domain.CloseInput{Reason: "other"}, map[string]string{"note": "required"}},
		{"other with a blank note", domain.CloseInput{Reason: "other", Note: text("   ")},
			map[string]string{"note": "required"}},
		{"note of 2", domain.CloseInput{Reason: "other", Note: text("ok")}, map[string]string{"note": "out_of_range"}},
		{"note of 3", domain.CloseInput{Reason: "other", Note: text("ñam")}, nil},
		{"note of 500", domain.CloseInput{Reason: "other", Note: text(strings.Repeat("ñ", 500))}, nil},
		{"note with a NUL", domain.CloseInput{Reason: "other", Note: text("Le pasaron\x00corriente")},
			map[string]string{"note": "invalid_format"}},
		{"note on two lines", domain.CloseInput{Reason: "other", Note: text("Le pasaron corriente.\r\n\tSigue su viaje")},
			nil},
		{"note of 501", domain.CloseInput{Reason: "duplicate", Note: text(strings.Repeat("ñ", 501))},
			map[string]string{"note": "out_of_range"}},
		{"everything wrong", domain.CloseInput{Reason: "x", Note: text("ab")},
			map[string]string{"reason": "invalid_format", "note": "out_of_range"}},
	}
	for _, tc := range cases {
		_, _, err := domain.Close(inCall(), p, tc.in, analyst, attendedAt)
		if tc.failures == nil {
			assert.NoError(t, err, tc.name)
			continue
		}
		assert.Equal(t, tc.failures, fieldsOf(t, err), tc.name)
	}
}

func TestTheNoticeKeyOfADispatch(t *testing.T) {
	assert.Equal(t, "asistencia-3b8e2f6a-1c4d-4e7f-9a2b-6c5d8e0f1a2b",
		domain.AssistanceNoticeKey("3b8e2f6a-1c4d-4e7f-9a2b-6c5d8e0f1a2b"))
}

func TestTodayOfTheInboxIsTheDayOfColombia(t *testing.T) {
	lastMinute := time.Date(2026, 11, 16, 4, 59, 59, 0, time.UTC) // 23:59:59 of the 15th in Bogotá
	assert.Equal(t, time.Date(2026, 11, 15, 5, 0, 0, 0, time.UTC), domain.ColombiaDayStart(lastMinute))
	midnight := time.Date(2026, 11, 16, 5, 0, 0, 0, time.UTC)
	assert.Equal(t, midnight, domain.ColombiaDayStart(midnight))
	assert.Equal(t, time.UTC, domain.ColombiaDayStart(requestedAt).Location())
}

func TestTheIndicatorsOfTheInbox(t *testing.T) {
	p := domain.AssistanceInboxPolicyV1()
	s := domain.NewInboxSummary(2, 1, nil, p)
	assert.Equal(t, domain.InboxSummary{New: 2, InCall: 1, TargetSeconds: 300}, s, "no dispatch today: no mean")

	s = domain.NewInboxSummary(0, 0, []time.Duration{200 * time.Second, 241 * time.Second}, p)
	assert.Equal(t, 2, s.DispatchedToday)
	require.NotNil(t, s.MeanDispatchSeconds)
	assert.Equal(t, 221, *s.MeanDispatchSeconds, "220.5 s rounds to 221")

	s = domain.NewInboxSummary(0, 0, []time.Duration{3*time.Minute + 40*time.Second}, p)
	assert.Equal(t, 220, *s.MeanDispatchSeconds, "3 min 40 s of the mockup")
}
