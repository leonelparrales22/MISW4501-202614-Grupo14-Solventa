package httpadapter_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/assistance"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// The routes of the inbox of assistance (HU-W2.3-03).

const inboxPath = "/v1/claims/assistance-inbox"

var inboxAt = time.Date(2026, 11, 15, 21, 31, 4, 0, time.UTC)

type fakeInbox struct {
	view  assistance.CaseView
	inbox assistance.Inbox
	err   error
	calls int

	gotID, gotNeed, gotProvider string
	gotFilter                   ports.InboxFilter
	gotVoice                    domain.VoiceLocationInput
	gotClose                    domain.CloseInput
	gotBy                       domain.Analyst
}

func (f *fakeInbox) Inbox(_ context.Context, filter ports.InboxFilter) (assistance.Inbox, error) {
	f.calls++
	f.gotFilter = filter
	return f.inbox, f.err
}

func (f *fakeInbox) Case(_ context.Context, id string) (assistance.CaseView, error) {
	f.calls++
	f.gotID = id
	return f.view, f.err
}

func (f *fakeInbox) Attend(_ context.Context, id string, by domain.Analyst) (assistance.CaseView, error) {
	f.calls++
	f.gotID, f.gotBy = id, by
	return f.view, f.err
}

func (f *fakeInbox) RecordNeed(_ context.Context, id, need string, by domain.Analyst) (assistance.CaseView, error) {
	f.calls++
	f.gotID, f.gotNeed, f.gotBy = id, need, by
	return f.view, f.err
}

func (f *fakeInbox) RecordVoiceLocation(_ context.Context, id string, in domain.VoiceLocationInput,
	by domain.Analyst) (assistance.CaseView, error) {
	f.calls++
	f.gotID, f.gotVoice, f.gotBy = id, in, by
	return f.view, f.err
}

func (f *fakeInbox) Dispatch(_ context.Context, id, providerID string, by domain.Analyst) (assistance.CaseView, error) {
	f.calls++
	f.gotID, f.gotProvider, f.gotBy = id, providerID, by
	return f.view, f.err
}

func (f *fakeInbox) Close(_ context.Context, id string, in domain.CloseInput, by domain.Analyst) (assistance.CaseView,
	error) {
	f.calls++
	f.gotID, f.gotClose, f.gotBy = id, in, by
	return f.view, f.err
}

func (f *fakeInbox) Policy() (domain.AssistanceInboxPolicy, domain.AssignmentPolicy) {
	f.calls++
	return domain.AssistanceInboxPolicyV1(), domain.AssignmentPolicyV1()
}

func inboxRouter(f *fakeInbox) http.Handler {
	return httpadapter.NewRouter(httpadapter.WithAssistanceInbox(f, analysts))
}

// inCallRequest is AST-2026-0912 of the mockup, attended by Andrés.
func inCallRequest() domain.AssistanceCase {
	point, accuracy, captured, place := domain.Coordinates{Latitude: 4.7712, Longitude: -74.0452}, 8.0,
		inboxAt.Add(-12*time.Second), "Autopista Norte km 12, Bogotá"
	return domain.AssistanceCase{
		Request: domain.AssistanceRequest{ID: requestID, Number: "AST-2026-0912", RequesterName: "Carolina Pérez Ruiz",
			Status: domain.AssistanceInCall, RequestedAt: inboxAt, Place: &place,
			Policy: domain.AssistancePolicy{ID: "AU-2025-118204", Line: domain.LineAuto, Product: "Autos",
				InsuredObject: "KJT-482"},
			Location: domain.AssistanceLocation{Status: domain.LocationSent, Coordinates: &point, AccuracyM: &accuracy,
				CapturedAt: &captured}},
		History: []domain.AssistanceAction{{Seq: 1, Code: domain.ActionAttended, By: andres,
			At: inboxAt.Add(36 * time.Second)}},
	}
}

// dispatchedView is AST-2026-0912 dispatched to the tow truck it suggested.
func dispatchedView() assistance.CaseView {
	c := inCallRequest()
	c.Request.Status = domain.AssistanceDispatched
	c.Need = &domain.RecordedNeed{Seq: 1, Need: domain.NeedTowing, Specialty: domain.SpecialtyTowing, By: andres,
		At: inboxAt.Add(66 * time.Second)}
	accuracy := 8.0
	c.Dispatch = &domain.AssistanceDispatch{
		Provider: domain.DispatchedProvider{ID: "prv-gruas-autopista-norte", Name: "Grúas Autopista Norte",
			ContactEmail: "despachos@gruasautopistanorte.example.com", ContactPhone: ptrTo("+576015550301")},
		Need: domain.NeedTowing, Specialty: domain.SpecialtyTowing, DistanceKm: 0.8807, EtaMinutes: 12,
		Location: domain.SharedLocation{Source: domain.SharedFromApp, Point: *c.Request.Location.Coordinates,
			AccuracyM: &accuracy, Place: c.Request.Place},
		SuggestedProviderID: "prv-gruas-autopista-norte", AssistancePolicyVersion: "assistance-policy-v1",
		AssignmentPolicyVersion: "assignment-policy-v1", By: andres, At: inboxAt.Add(220 * time.Second)}
	c.History = append(c.History,
		domain.AssistanceAction{Seq: 2, Code: domain.ActionNeedRecorded, By: andres, At: inboxAt.Add(66 * time.Second),
			Need: domain.NeedTowing},
		domain.AssistanceAction{Seq: 3, Code: domain.ActionDispatched, By: andres, At: inboxAt.Add(220 * time.Second),
			ProviderID: "prv-gruas-autopista-norte"})
	return assistance.CaseView{
		AssistanceRecord: ports.AssistanceRecord{Case: c, Notice: &ports.AssistanceNotice{RequestID: requestID,
			Status: ports.NoticeSent, Attempts: 1, SentAt: inboxAt.Add(221 * time.Second)}},
		Suggestion: domain.NoSuggestion(domain.SuggestionNotApplicable, domain.AssignmentPolicyVersionV1),
	}
}

// foundView is AST-2026-0912 in call with its suggestion.
func foundView() assistance.CaseView {
	c := inCallRequest()
	c.Need = &domain.RecordedNeed{Seq: 1, Need: domain.NeedTowing, Specialty: domain.SpecialtyTowing, By: andres,
		At: inboxAt.Add(66 * time.Second)}
	until := time.Date(2026, 11, 16, 0, 0, 0, 0, domain.ColombiaTime)
	result := domain.SearchResult{FromRadiusKm: 5, RadiusKm: 5, Outcome: domain.SearchFound,
		Attempts: []domain.SearchAttempt{{RadiusKm: 5, EligibleCount: 1, AttemptedAt: inboxAt.Add(66 * time.Second)}},
		Candidates: []domain.Candidate{
			{ProviderID: "prv-gruas-autopista-norte", Name: "Grúas Autopista Norte",
				Specialties: []domain.Specialty{domain.SpecialtyTowing}, DistanceKm: 0.8807,
				Availability: domain.AvailabilityAvailable, Eligibility: domain.Eligible, Suggested: true},
			{ProviderID: "prv-auxilio-vial-norte", Name: "Auxilio Vial Norte",
				Specialties: []domain.Specialty{domain.SpecialtyRoadsideAssistance, domain.SpecialtyTowing},
				DistanceKm:  2.2017, Availability: domain.AvailabilityUnavailable, UnavailableUntil: &until,
				Eligibility: domain.NotEligibleUnavailable},
		}}
	loc, _ := c.Location()
	return assistance.CaseView{AssistanceRecord: ports.AssistanceRecord{Case: c},
		Suggestion: domain.NewSuggestion(domain.SpecialtyTowing, loc, result, domain.AssistanceInboxPolicyV1(),
			domain.AssignmentPolicyVersionV1, map[string]int{"prv-gruas-autopista-norte": 2})}
}

func TestTheInboxAsTheScreenShowsIt(t *testing.T) {
	voiceless := inCallRequest()
	voiceless.Request.ID, voiceless.Request.Number = "9d0c6a14-7b3e-4f21-8c5a-2e1f0b9d7a63", "AST-2026-0905"
	voiceless.Request.Status, voiceless.Request.RequesterName, voiceless.Request.Place = domain.AssistanceClosed, "", nil
	voiceless.Request.Location = domain.AssistanceLocation{Status: domain.LocationUnavailable,
		UnavailableReason: domain.UnavailableNoFix}
	voiceless.Voice = &domain.VoiceLocation{Seq: 1, Address: "Vía Cali–Palmira km 4",
		Point: domain.Coordinates{Latitude: 3.4921, Longitude: -76.4203}, By: andres, At: inboxAt.Add(time.Minute)}
	voiceless.Closure = &domain.AssistanceClosure{Reason: domain.CloseResolvedByPhone, By: andres,
		At: inboxAt.Add(2 * time.Minute)}
	mean := 220
	f := &fakeInbox{inbox: assistance.Inbox{
		Summary: domain.InboxSummary{New: 2, InCall: 1, DispatchedToday: 6, MeanDispatchSeconds: &mean,
			TargetSeconds: 300},
		Items: []ports.AssistanceRecord{{Case: inCallRequest()}, {Case: voiceless}},
	}}

	rec := do(inboxRouter(f), http.MethodGet, inboxPath, "operations", nil, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, ports.InboxFilter{}, f.gotFilter, "every status, the default page")
	assert.JSONEq(t, `{
		"summary": {"new_count": 2, "in_call_count": 1, "dispatched_today": 6, "mean_dispatch_seconds_today": 220,
			"dispatch_target_seconds": 300},
		"items": [
			{"request_id": "`+requestID+`", "request_number": "AST-2026-0912", "status": "in_call",
			 "requester_name": "Carolina Pérez Ruiz",
			 "policy": {"policy_id": "AU-2025-118204", "line": "auto", "product": "Autos", "insured_object": "KJT-482"},
			 "location": {"status": "sent", "latitude": 4.7712, "longitude": -74.0452, "accuracy_m": 8,
				"captured_at": "2026-11-15T21:30:52Z", "unavailable_reason": null,
				"place": "Autopista Norte km 12, Bogotá", "voice": null},
			 "requested_at": "2026-11-15T21:31:04Z",
			 "handled_by": {"email": "andres.ruiz@solventa.example", "name": "Andrés Ruiz"},
			 "dispatched_at": null, "closed_at": null},
			{"request_id": "9d0c6a14-7b3e-4f21-8c5a-2e1f0b9d7a63", "request_number": "AST-2026-0905",
			 "status": "closed", "requester_name": null,
			 "policy": {"policy_id": "AU-2025-118204", "line": "auto", "product": "Autos", "insured_object": "KJT-482"},
			 "location": {"status": "unavailable", "latitude": null, "longitude": null, "accuracy_m": null,
				"captured_at": null, "unavailable_reason": "no_fix", "place": null,
				"voice": {"address": "Vía Cali–Palmira km 4", "latitude": 3.4921, "longitude": -76.4203,
					"recorded_by": {"email": "andres.ruiz@solventa.example", "name": "Andrés Ruiz"},
					"recorded_at": "2026-11-15T21:32:04Z"}},
			 "requested_at": "2026-11-15T21:31:04Z",
			 "handled_by": {"email": "andres.ruiz@solventa.example", "name": "Andrés Ruiz"},
			 "dispatched_at": null, "closed_at": "2026-11-15T21:33:04Z"}
		]}`, rec.Body.String())

	f = &fakeInbox{}
	rec = do(inboxRouter(f), http.MethodGet, inboxPath, "operations", nil, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"summary": {"new_count": 0, "in_call_count": 0, "dispatched_today": 0,
		"mean_dispatch_seconds_today": null, "dispatch_target_seconds": 0}, "items": []}`, rec.Body.String())
}

func TestTheFilterOfTheInbox(t *testing.T) {
	f := &fakeInbox{}
	rec := do(inboxRouter(f), http.MethodGet, inboxPath+"?status=new&status=in_call&limit=100", "operations", nil, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, ports.InboxFilter{Statuses: []domain.AssistanceStatus{domain.AssistanceNew,
		domain.AssistanceInCall}, Limit: 100}, f.gotFilter)
	rec = do(inboxRouter(f), http.MethodGet, inboxPath+"?limit=1&status=dispatched&status=closed&status=new"+
		"&status=in_call", "operations", nil, nil)
	require.Equal(t, http.StatusOK, rec.Code, "the four statuses at once")
	assert.Equal(t, 1, f.gotFilter.Limit)

	for _, query := range []string{"?status=cancelled", "?status=new&status=new", "?status=", "?limit=0",
		"?limit=101", "?limit=diez", "?limit=1&limit=2", "?limit=-1", "?status=NEW",
		"?status=new&status=in_call&status=dispatched&status=closed&status=new"} {
		f := &fakeInbox{}
		rec := do(inboxRouter(f), http.MethodGet, inboxPath+query, "operations", nil, nil)
		assert.Equal(t, http.StatusBadRequest, rec.Code, query)
		assert.Contains(t, rec.Body.String(), "invalid_request", query)
		assert.Zero(t, f.calls, "%s reached the service", query)
	}
}

func TestARequestAsTheDetailShowsIt(t *testing.T) {
	f := &fakeInbox{view: foundView()}
	rec := do(inboxRouter(f), http.MethodGet, inboxPath+"/"+requestID, "operations", nil, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, requestID, f.gotID)
	out := decode(t, rec)
	assert.Equal(t, map[string]any{"code": "towing", "specialty": "towing",
		"recorded_by": map[string]any{"email": "andres.ruiz@solventa.example", "name": "Andrés Ruiz"},
		"recorded_at": "2026-11-15T21:32:10Z"}, out["need"])
	assert.Nil(t, out["dispatch"])
	assert.Nil(t, out["closure"])
	suggestion := asJSON(t, out["suggestion"])
	assert.JSONEq(t, `{"state": "found", "specialty": "towing", "location_source": "app", "from_radius_km": 5,
		"radius_km": 5, "reason": null, "policy_version": "assignment-policy-v1",
		"attempts": [{"radius_km": 5, "eligible_count": 1, "attempted_at": "2026-11-15T21:32:10Z"}],
		"candidates": [
			{"provider_id": "prv-gruas-autopista-norte", "name": "Grúas Autopista Norte", "specialties": ["towing"],
			 "distance_km": 0.9, "availability": "available", "unavailable_until": null, "eligibility": "eligible",
			 "suggested": true, "eta_minutes": 12, "dispatched_today": 2},
			{"provider_id": "prv-auxilio-vial-norte", "name": "Auxilio Vial Norte",
			 "specialties": ["roadside_assistance", "towing"], "distance_km": 2.2, "availability": "unavailable",
			 "unavailable_until": "2026-11-16", "eligibility": "unavailable", "suggested": false, "eta_minutes": 15,
			 "dispatched_today": 0}]}`, suggestion)
	history := asJSON(t, out["history"])
	assert.JSONEq(t, `[{"action": "attended", "by": {"email": "andres.ruiz@solventa.example", "name": "Andrés Ruiz"},
		"at": "2026-11-15T21:31:40Z", "detail": null}]`, history)
}

func TestADispatchedRequestAsTheDetailShowsIt(t *testing.T) {
	f := &fakeInbox{view: dispatchedView()}
	rec := do(inboxRouter(f), http.MethodGet, inboxPath+"/"+requestID, "operations", nil, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	out := decode(t, rec)
	assert.Equal(t, "dispatched", out["status"])
	assert.Equal(t, "2026-11-15T21:34:44Z", out["dispatched_at"])
	dispatch := asJSON(t, out["dispatch"])
	assert.JSONEq(t, `{
		"provider": {"id": "prv-gruas-autopista-norte", "name": "Grúas Autopista Norte",
			"contact_email": "despachos@gruasautopistanorte.example.com", "contact_phone": "+576015550301"},
		"need": "towing", "specialty": "towing", "distance_km": 0.9, "eta_minutes": 12,
		"location_shared": {"source": "app", "latitude": 4.7712, "longitude": -74.0452, "accuracy_m": 8,
			"address": null, "place": "Autopista Norte km 12, Bogotá"},
		"suggested_provider_id": "prv-gruas-autopista-norte",
		"dispatched_by": {"email": "andres.ruiz@solventa.example", "name": "Andrés Ruiz"},
		"dispatched_at": "2026-11-15T21:34:44Z",
		"policy_versions": {"assistance": "assistance-policy-v1", "assignment": "assignment-policy-v1"},
		"notice": {"status": "sent", "attempts": 1, "sent_at": "2026-11-15T21:34:45Z"}}`, dispatch)
	suggestion := asJSON(t, out["suggestion"])
	assert.JSONEq(t, `{"state": "not_applicable", "specialty": null, "location_source": null, "from_radius_km": null,
		"radius_km": null, "reason": null, "policy_version": "assignment-policy-v1", "attempts": [],
		"candidates": []}`, suggestion)
	history := asJSON(t, out["history"])
	assert.Contains(t, history, `"detail":{"need":"towing"}`)
	assert.Contains(t, history, `"detail":{"provider_id":"prv-gruas-autopista-norte"}`)

	v := dispatchedView()
	v.Notice = nil
	v.Case.Closure = &domain.AssistanceClosure{Reason: domain.CloseOther, Note: ptrTo("Le pasaron corriente"),
		By: andres, At: inboxAt}
	v.Case.History = append(v.Case.History, domain.AssistanceAction{Seq: 4, Code: domain.ActionTakenOver,
		By: andres, At: inboxAt, From: "laura.gomez@solventa.example"})
	f = &fakeInbox{view: v}
	out = decode(t, do(inboxRouter(f), http.MethodGet, inboxPath+"/"+requestID, "operations", nil, nil))
	assert.Equal(t, map[string]any{"status": "pending", "attempts": float64(0), "sent_at": nil},
		out["dispatch"].(map[string]any)["notice"], "a dispatch whose notice was not read yet")
	assert.Equal(t, map[string]any{"reason": "other", "note": "Le pasaron corriente",
		"closed_by": map[string]any{"email": "andres.ruiz@solventa.example", "name": "Andrés Ruiz"},
		"closed_at": "2026-11-15T21:31:04Z"}, out["closure"])
	history = asJSON(t, out["history"])
	assert.Contains(t, history, `"detail":{"from":"laura.gomez@solventa.example"}`)
}

// asJSON re-encodes a part of a decoded answer to compare it as JSON.
func asJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

func TestTheDecisionsOfTheAnalystReachTheService(t *testing.T) {
	cases := []struct {
		name, path, body string
		check            func(t *testing.T, f *fakeInbox)
	}{
		{"attend without a body", "/attend", "", func(t *testing.T, f *fakeInbox) {}},
		{"attend with an empty object", "/attend", "{}", func(t *testing.T, f *fakeInbox) {}},
		{"need", "/need", `{"need": "towing"}`, func(t *testing.T, f *fakeInbox) {
			assert.Equal(t, "towing", f.gotNeed)
		}},
		{"voice", "/voice-location", `{"address": "Vía Cali–Palmira km 4", "latitude": 3.4921, "longitude": -76.4203}`,
			func(t *testing.T, f *fakeInbox) {
				assert.Equal(t, "Vía Cali–Palmira km 4", f.gotVoice.Address)
				assert.Equal(t, 3.4921, *f.gotVoice.Latitude)
				assert.Equal(t, -76.4203, *f.gotVoice.Longitude)
			}},
		{"voice without coordinates", "/voice-location", `{"address": "Vía Cali–Palmira km 4"}`,
			func(t *testing.T, f *fakeInbox) {
				assert.Nil(t, f.gotVoice.Latitude, "a missing coordinate is told from zero")
			}},
		{"dispatch", "/dispatch", `{"provider_id": "prv-gruas-autopista-norte"}`, func(t *testing.T, f *fakeInbox) {
			assert.Equal(t, "prv-gruas-autopista-norte", f.gotProvider)
		}},
		{"close", "/close", `{"reason": "other", "note": "Le pasaron corriente"}`, func(t *testing.T, f *fakeInbox) {
			assert.Equal(t, "other", f.gotClose.Reason)
			assert.Equal(t, "Le pasaron corriente", *f.gotClose.Note)
		}},
		{"close without a note", "/close", `{"reason": "duplicate"}`, func(t *testing.T, f *fakeInbox) {
			assert.Nil(t, f.gotClose.Note)
		}},
	}
	for _, c := range cases {
		f := &fakeInbox{view: dispatchedView()}
		code, out, header := send(inboxRouter(f), http.MethodPost, inboxPath+"/"+requestID+c.path, "operations", c.body)
		require.Equal(t, http.StatusOK, code, c.name)
		assert.Equal(t, "no-store", header.Get("Cache-Control"), c.name)
		assert.Equal(t, "AST-2026-0912", out["request_number"], "%s answers the whole request", c.name)
		assert.Equal(t, 1, f.calls, c.name)
		assert.Equal(t, requestID, f.gotID, c.name)
		assert.Equal(t, andres, f.gotBy, "%s: the analyst of the token", c.name)
		c.check(t, f)
	}
}

func TestADispatchNeedsTheIdOfAProvider(t *testing.T) {
	for body, code := range map[string]string{`{}`: "required", `{"provider_id": null}`: "required",
		`{"provider_id": "Grúas"}`: "invalid_format", `{"provider_id": "prv-../x"}`: "invalid_format"} {
		f := &fakeInbox{}
		status, out, _ := send(inboxRouter(f), http.MethodPost, inboxPath+"/"+requestID+"/dispatch", "operations", body)
		assert.Equal(t, http.StatusUnprocessableEntity, status, body)
		assert.Equal(t, "validation_failed", out["error"], body)
		assert.Equal(t, []any{map[string]any{"field": "provider_id", "code": code}}, out["fields"], body)
		assert.Zero(t, f.calls, body)
	}
}

func TestABodyThatIsNotOfTheContractIsRefusedByTheInbox(t *testing.T) {
	for _, c := range []struct{ path, body string }{
		{"/attend", `{"provider_id": "prv-abc"}`},
		{"/attend", `[]`},
		{"/need", ``},
		{"/need", `{"need": "towing", "specialty": "towing"}`},
		{"/need", `{"need": 3}`},
		{"/voice-location", `{"address": "x", "latitude": "4.7"}`},
		{"/voice-location", `{"address": "x", "latitude": 1e400, "longitude": 0}`},
		{"/voice-location", `{"address": "x", "latitude": NaN, "longitude": 0}`},
		{"/voice-location", `{"address": "x", "latitude": 0, "longitude": -Infinity}`},
		{"/voice-location", `{"address": "x", "latitude": 0, "longitude": 0, "accuracy_m": 5}`},
		{"/dispatch", `{"provider_id": "prv-abc"} {}`},
		{"/close", `"duplicate"`},
		{"/close", `{"reason": "other", "note": "` + strings.Repeat("a", 17000) + `"}`},
	} {
		f := &fakeInbox{}
		status, _, _ := send(inboxRouter(f), http.MethodPost, inboxPath+"/"+requestID+c.path, "operations", c.body)
		assert.Equal(t, http.StatusBadRequest, status, "%s %.60s", c.path, c.body)
		assert.Zero(t, f.calls, "%s %.60s reached the service", c.path, c.body)
	}
}

func TestEveryErrorOfTheInboxHasItsCode(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{ports.ErrAssistanceRequestNotFound, http.StatusNotFound, "assistance_request_not_found"},
		{domain.ErrAssistanceNotOpen, http.StatusConflict, "assistance_not_open"},
		{domain.ErrAssistanceNotAttended, http.StatusConflict, "assistance_not_attended"},
		{domain.ErrLocationFromApp, http.StatusConflict, "location_from_app"},
		{domain.ErrNeedNotForLine, http.StatusUnprocessableEntity, "need_not_for_line"},
		{domain.ErrNeedMissing, http.StatusUnprocessableEntity, "need_missing"},
		{domain.ErrLocationMissing, http.StatusUnprocessableEntity, "location_missing"},
		{domain.ErrProviderNotEligible, http.StatusUnprocessableEntity, "provider_not_eligible"},
		{fmt.Errorf("wrapped: %w", domain.ErrAssistanceNotOpen), http.StatusConflict, "assistance_not_open"},
		{ports.ErrAssistanceChanged, http.StatusServiceUnavailable, "assistance_unavailable"},
		{errors.New("the database is down"), http.StatusServiceUnavailable, "assistance_unavailable"},
	}
	for _, c := range cases {
		for _, route := range []struct{ method, path, body string }{
			{http.MethodGet, inboxPath, ""},
			{http.MethodGet, inboxPath + "/" + requestID, ""},
			{http.MethodPost, inboxPath + "/" + requestID + "/dispatch", `{"provider_id": "prv-abc"}`},
		} {
			f := &fakeInbox{err: c.err}
			status, out, _ := send(inboxRouter(f), route.method, route.path, "operations", route.body)
			assert.Equal(t, c.status, status, "%v on %s", c.err, route.path)
			assert.Equal(t, c.code, out["error"], "%v on %s", c.err, route.path)
		}
	}

	f := &fakeInbox{err: &domain.ValidationError{Fields: []domain.FieldError{{Field: "note", Code: "required"}}}}
	status, out, _ := send(inboxRouter(f), http.MethodPost, inboxPath+"/"+requestID+"/close", "operations",
		`{"reason": "other"}`)
	assert.Equal(t, http.StatusUnprocessableEntity, status)
	assert.Equal(t, []any{map[string]any{"field": "note", "code": "required"}}, out["fields"])
}

func TestTheLogOfTheInboxNamesTheRequestAndNothingOfThePerson(t *testing.T) {
	logs := captureLogs(t)
	f := &fakeInbox{err: errors.New("the database is down")}
	status, _, _ := send(inboxRouter(f), http.MethodPost, inboxPath+"/"+requestID+"/voice-location", "operations",
		`{"address": "Calle 80 # 15-20, Bogotá", "latitude": 4.6912, "longitude": -74.0451}`)
	require.Equal(t, http.StatusServiceUnavailable, status)
	out := logs.String()
	assert.Contains(t, out, requestID)
	for _, secret := range []string{"Calle 80", "4.6912", "-74.0451", "andres.ruiz", "Andrés"} {
		assert.NotContains(t, out, secret)
	}
}

func TestAnIdThatIsNotOfARequestNeverReachesTheInbox(t *testing.T) {
	for _, id := range []string{"..%2F..%2Fsecret", "3B8E2F6A-1C4D-4E7F-9A2B-6C5D8E0F1A2B", requestID + "0",
		"3b8e2f6a-1c4d-1e7f-9a2b-6c5d8e0f1a2b", "%00", strings.Repeat("a", 10240), "AST-2026-0912"} {
		for _, path := range []string{"", "/attend", "/need", "/dispatch"} {
			f := &fakeInbox{}
			method := http.MethodPost
			if path == "" {
				method = http.MethodGet
			}
			status, out, _ := send(inboxRouter(f), method, inboxPath+"/"+id+path, "operations", `{"need": "towing"}`)
			assert.Equal(t, http.StatusNotFound, status, "%.40s%s", id, path)
			if out != nil {
				assert.Equal(t, "assistance_request_not_found", out["error"], "%.40s%s", id, path)
			}
			assert.Zero(t, f.calls, "%.40s%s reached the service", id, path)
		}
	}
}

func TestThePolicyOfTheInboxOverHTTP(t *testing.T) {
	f := &fakeInbox{}
	rec := do(inboxRouter(f), http.MethodGet, "/v1/claims/assistance-policy", "operations", nil, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"version": "assistance-policy-v1",
		"needs": [
			{"line": "auto", "code": "towing", "specialty": "towing"},
			{"line": "auto", "code": "battery", "specialty": "roadside_assistance"},
			{"line": "auto", "code": "flat_tire", "specialty": "roadside_assistance"},
			{"line": "auto", "code": "fuel", "specialty": "roadside_assistance"},
			{"line": "auto", "code": "vehicle_lockout", "specialty": "roadside_assistance"},
			{"line": "home", "code": "plumbing_emergency", "specialty": "plumbing"}],
		"radii_km": [5, 10, 20], "assignment_policy_version": "assignment-policy-v1",
		"eta": {"preparation_minutes": 10, "average_speed_kmh": 30},
		"close_reasons": ["resolved_by_phone", "duplicate", "cancelled_by_insured", "not_covered",
			"no_provider_available", "other"],
		"dispatch_target_seconds": 300}`, rec.Body.String())
}
