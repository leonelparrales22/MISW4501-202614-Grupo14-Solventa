package httpadapter_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/assessors"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// The routes of the assignment of the provider (HU-W2.3-01).

var (
	searchedAt = time.Date(2026, 9, 6, 15, 14, 2, 0, time.UTC)
	// analysts are the callers of the assignment: the analyst of operations
	// has an e-mail and a name in the token.
	analysts = tokens{
		"operations": {Subject: "sub-ops", Email: "andres.ruiz@solventa.example", Name: "Andrés Ruiz",
			Roles: []security.Role{security.RoleOperations}},
		"insured": callers["insured"], "advisor": callers["advisor"], "partner": callers["partner"],
	}
	andres = domain.Analyst{Email: "andres.ruiz@solventa.example", Name: "Andrés Ruiz"}
)

type fakeAssigner struct {
	view      assessors.View
	summaries []assessors.Summary
	err       error
	calls     int

	gotClaim, gotProvider string
	gotSeq                int
	gotIDs                []string
	gotBy                 domain.Analyst
	gotLocation           domain.LocationRequest
	gotFrom               *float64
}

func (f *fakeAssigner) View(_ context.Context, id string) (assessors.View, error) {
	f.calls++
	f.gotClaim = id
	return f.view, f.err
}

func (f *fakeAssigner) Summaries(_ context.Context, ids []string) ([]assessors.Summary, error) {
	f.calls++
	f.gotIDs = ids
	return f.summaries, f.err
}

func (f *fakeAssigner) RecordLocation(_ context.Context, id string, in domain.LocationRequest,
	by domain.Analyst) (assessors.View, error) {
	f.calls++
	f.gotClaim, f.gotLocation, f.gotBy = id, in, by
	return f.view, f.err
}

func (f *fakeAssigner) Search(_ context.Context, id string, from *float64, by domain.Analyst) (assessors.View, error) {
	f.calls++
	f.gotClaim, f.gotFrom, f.gotBy = id, from, by
	return f.view, f.err
}

func (f *fakeAssigner) Assign(_ context.Context, id, providerID string, seq int, by domain.Analyst) (assessors.View, error) {
	f.calls++
	f.gotClaim, f.gotProvider, f.gotSeq, f.gotBy = id, providerID, seq, by
	return f.view, f.err
}

func (f *fakeAssigner) Policy() domain.AssignmentPolicy {
	f.calls++
	return domain.AssignmentPolicyV1()
}

func assignmentRouter(f *fakeAssigner) http.Handler {
	return httpadapter.NewRouter(httpadapter.WithAssignment(f, 300*time.Second, analysts))
}

func waterDamage() domain.ClaimRecord {
	return domain.ClaimRecord{Claim: domain.Claim{ID: claimID, FilingNumber: "SIN-2026-01655",
		IncidentType: domain.IncidentWaterDamage, Status: domain.StatusInEvaluation}, CatalogVersion: domain.CatalogVersionV1}
}

// proposed is the screen "Asignar prestador" of the mockup.
func proposed() assessors.View {
	accuracy := 8.0
	until := time.Date(2026, 9, 7, 0, 0, 0, 0, domain.ColombiaTime)
	location := domain.IncidentLocation{Source: domain.LocationFromApp,
		Point: domain.Coordinates{Latitude: 4.6771, Longitude: -74.0483}, AccuracyM: &accuracy,
		RecordedAt: searchedAt.Add(-22 * time.Minute)}
	by := andres
	return assessors.View{Claim: waterDamage(), State: domain.AssignmentProposed, Line: domain.LineHome,
		Specialty: domain.SpecialtyPlumbing, PolicyVersion: domain.AssignmentPolicyVersionV1, Location: &location,
		LastSearch: &domain.ProviderSearch{ClaimID: claimID, Seq: 1, Trigger: domain.SearchByAnalyst, SearchedBy: &by,
			Specialty: domain.SpecialtyPlumbing, Location: location, PolicyVersion: domain.AssignmentPolicyVersionV1,
			SearchedAt: searchedAt, Result: domain.SearchResult{FromRadiusKm: 5, RadiusKm: 5, Outcome: domain.SearchFound,
				Attempts: []domain.SearchAttempt{{RadiusKm: 5, EligibleCount: 1, AttemptedAt: searchedAt}},
				Candidates: []domain.Candidate{
					{ProviderID: "prv-servicios-hogar-norte", Name: "Servicios Hogar Norte",
						Specialties: []domain.Specialty{domain.SpecialtyPlumbing, domain.SpecialtyDampProofing},
						DistanceKm:  2.2961, Availability: domain.AvailabilityAvailable, Eligibility: domain.Eligible,
						Suggested: true},
					{ProviderID: "prv-reparaciones-chapinero", Name: "Reparaciones Chapinero",
						Specialties: []domain.Specialty{domain.SpecialtyPlumbing}, DistanceKm: 3.798,
						Availability: domain.AvailabilityUnavailable, UnavailableUntil: &until,
						Eligibility: domain.NotEligibleUnavailable},
				}}}}
}

// assigned is the claim once Servicios Hogar Norte was confirmed.
func assigned() assessors.View {
	v := proposed()
	v.State = domain.AssignmentAssigned
	v.Assignment = &domain.Assignment{ClaimID: claimID, ProviderID: "prv-servicios-hogar-norte",
		ProviderName: "Servicios Hogar Norte", ContactEmail: "despachos@hogarnorte.example.com", AssignedBy: andres,
		AssignedAt: searchedAt.Add(time.Minute), DistanceKm: 2.2961, SearchSeq: 1, Location: *v.Location,
		PolicyVersion: domain.AssignmentPolicyVersionV1}
	v.Notice = &ports.ProviderNotice{ClaimID: claimID, Status: ports.NoticeSent, Attempts: 2,
		SentAt: searchedAt.Add(2 * time.Minute)}
	return v
}

func send(h http.Handler, method, path, caller, body string) (int, map[string]any, http.Header) {
	rec := do(h, method, path, caller, strings.NewReader(body), map[string]string{"Content-Type": "application/json"})
	var out map[string]any
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
	}
	return rec.Code, out, rec.Header()
}

func TestTheAssignmentOfAClaimAsTheScreenShowsIt(t *testing.T) {
	f := &fakeAssigner{view: proposed()}
	rec := do(assignmentRouter(f), http.MethodGet, "/v1/claims/"+claimID+"/assignment", "operations", nil, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, claimID, f.gotClaim)
	assert.JSONEq(t, `{
		"claim_id": "`+claimID+`", "filing_number": "SIN-2026-01655", "claim_status": "in_evaluation",
		"incident_type": "water_damage", "line": "home", "state": "proposed", "specialty": "plumbing",
		"policy_version": "assignment-policy-v1",
		"location": {"source": "app", "latitude": 4.6771, "longitude": -74.0483, "accuracy_m": 8, "address": null,
			"recorded_by": null, "recorded_at": "2026-09-06T14:52:02Z"},
		"last_search": {"seq": 1, "trigger": "analyst",
			"searched_by": {"email": "andres.ruiz@solventa.example", "name": "Andrés Ruiz"},
			"specialty": "plumbing", "location": {"source": "app", "latitude": 4.6771, "longitude": -74.0483},
			"from_radius_km": 5, "outcome": "found", "radius_km": 5, "reason": null,
			"policy_version": "assignment-policy-v1", "searched_at": "2026-09-06T15:14:02Z",
			"attempts": [{"radius_km": 5, "eligible_count": 1, "attempted_at": "2026-09-06T15:14:02Z"}],
			"candidates": [
				{"provider_id": "prv-servicios-hogar-norte", "name": "Servicios Hogar Norte",
				 "specialties": ["plumbing", "damp_proofing"], "distance_km": 2.3, "availability": "available",
				 "unavailable_until": null, "eligibility": "eligible", "suggested": true},
				{"provider_id": "prv-reparaciones-chapinero", "name": "Reparaciones Chapinero",
				 "specialties": ["plumbing"], "distance_km": 3.8, "availability": "unavailable",
				 "unavailable_until": "2026-09-07", "eligibility": "unavailable", "suggested": false}]},
		"assignment": null}`, rec.Body.String())
}

func TestAnAssignedClaimShowsTheProviderAndTheNotice(t *testing.T) {
	f := &fakeAssigner{view: assigned()}
	rec := do(assignmentRouter(f), http.MethodGet, "/v1/claims/"+claimID+"/assignment", "operations", nil, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	body := decode(t, rec)
	assert.Equal(t, "assigned", body["state"])
	assert.JSONEq(t, `{
		"provider": {"id": "prv-servicios-hogar-norte", "name": "Servicios Hogar Norte",
			"contact_email": "despachos@hogarnorte.example.com", "contact_phone": null},
		"assigned_by": {"email": "andres.ruiz@solventa.example", "name": "Andrés Ruiz"},
		"assigned_at": "2026-09-06T15:15:02Z", "distance_km": 2.3, "search_seq": 1,
		"location_shared": {"source": "app", "latitude": 4.6771, "longitude": -74.0483, "accuracy_m": 8, "address": null},
		"policy_version": "assignment-policy-v1",
		"notice": {"status": "sent", "attempts": 2, "sent_at": "2026-09-06T15:16:02Z"}}`, mustJSON(t, body["assignment"]))

	v := assigned()
	v.Notice = nil
	f.view = v
	rec = do(assignmentRouter(f), http.MethodGet, "/v1/claims/"+claimID+"/assignment", "operations", nil, nil)
	notice := decode(t, rec)["assignment"].(map[string]any)["notice"]
	assert.Equal(t, map[string]any{"status": "pending", "attempts": float64(0), "sent_at": nil}, notice)
}

func TestAClaimWithoutLocationNorSearch(t *testing.T) {
	f := &fakeAssigner{view: assessors.View{Claim: waterDamage(), State: domain.AssignmentNotRequired,
		Line: domain.LineAuto, PolicyVersion: domain.AssignmentPolicyVersionV1}}
	rec := do(assignmentRouter(f), http.MethodGet, "/v1/claims/"+claimID+"/assignment", "operations", nil, nil)
	body := decode(t, rec)
	assert.Nil(t, body["specialty"])
	assert.Nil(t, body["location"])
	assert.Nil(t, body["last_search"])
	assert.Nil(t, body["assignment"])
}

func TestTheAnalystLocationOfAClaim(t *testing.T) {
	v := proposed()
	location := domain.IncidentLocation{Source: domain.LocationFromAnalyst, Point: v.Location.Point, Seq: 1,
		Address: "Carrera 15 # 93-40, apto 402, Bogotá", RecordedBy: &andres, RecordedAt: searchedAt}
	v.State, v.Location, v.LastSearch = domain.AssignmentReady, &location, nil
	f := &fakeAssigner{view: v}

	status, body, header := send(assignmentRouter(f), http.MethodPost, "/v1/claims/"+claimID+"/incident-location",
		"operations", `{"address": "Carrera 15 # 93-40, apto 402, Bogotá", "latitude": 4.6771, "longitude": -74.0483}`)
	require.Equal(t, http.StatusCreated, status)
	assert.Equal(t, "no-store", header.Get("Cache-Control"))
	assert.Equal(t, "ready", body["state"])
	assert.Equal(t, map[string]any{"source": "analyst", "latitude": 4.6771, "longitude": -74.0483, "accuracy_m": nil,
		"address":     "Carrera 15 # 93-40, apto 402, Bogotá",
		"recorded_by": map[string]any{"email": "andres.ruiz@solventa.example", "name": "Andrés Ruiz"},
		"recorded_at": "2026-09-06T15:14:02Z"}, body["location"])
	assert.Equal(t, "Carrera 15 # 93-40, apto 402, Bogotá", f.gotLocation.Address)
	assert.Equal(t, 4.6771, *f.gotLocation.Latitude)
	assert.Equal(t, -74.0483, *f.gotLocation.Longitude)
	assert.Equal(t, andres, f.gotBy, "who records it comes from the token")

	f = &fakeAssigner{err: &domain.ValidationError{Fields: []domain.FieldError{
		{Field: "address", Code: domain.CodeRequired}, {Field: "latitude", Code: domain.CodeRequired}}}}
	status, body, _ = send(assignmentRouter(f), http.MethodPost, "/v1/claims/"+claimID+"/incident-location",
		"operations", `{"longitude": -74.0483}`)
	assert.Equal(t, http.StatusUnprocessableEntity, status)
	assert.Equal(t, "validation_failed", body["error"])
	assert.Len(t, body["fields"], 2)
	assert.Nil(t, f.gotLocation.Latitude, "a missing coordinate is told from zero")
}

func TestABodyThatIsNotAnObjectOfTheContractIsRefused(t *testing.T) {
	for path, bodies := range map[string][]string{
		"/incident-location": {`{"address": "Calle 1", "latitude": "4.6", "longitude": 1}`, `{"extra": true}`, `[]`, `{`,
			`{"address": "x"} {}`},
		"/provider-searches": {`{"from_radius_km": "10"}`, `{"radius": 10}`, `[]`, `5`,
			`{"from_radius_km": ` + strings.Repeat("1", 1<<16) + `}`},
		"/assignment": {`{"provider_id": 1, "search_seq": 1}`, `{"provider_id": "prv-abc", "search_seq": 1.5}`,
			`{"provider_id": "prv-abc", "search_seq": 1, "x": 1}`, `"prv-abc"`},
	} {
		for _, body := range bodies {
			f := &fakeAssigner{view: proposed()}
			status, out, _ := send(assignmentRouter(f), http.MethodPost, "/v1/claims/"+claimID+path, "operations", body)
			assert.Equal(t, http.StatusBadRequest, status, "%s %.40s", path, body)
			assert.Equal(t, "invalid_request", out["error"], "%s %.40s", path, body)
			assert.Zero(t, f.calls, "%s %.40s", path, body)
		}
	}
}

func TestTheSearchStartsFromTheRadiusAsked(t *testing.T) {
	for body, want := range map[string]*float64{"": nil, "  ": nil, "{}": nil, `{"from_radius_km": 10}`: ptrTo(10.0),
		`{"from_radius_km": null}`: nil} {
		f := &fakeAssigner{view: proposed()}
		status, out, _ := send(assignmentRouter(f), http.MethodPost, "/v1/claims/"+claimID+"/provider-searches",
			"operations", body)
		require.Equal(t, http.StatusCreated, status, body)
		assert.Equal(t, "proposed", out["state"], body)
		assert.Equal(t, want, f.gotFrom, body)
		assert.Equal(t, andres, f.gotBy, body)
	}
}

func TestTheConfirmationOfAProvider(t *testing.T) {
	f := &fakeAssigner{view: assigned()}
	status, out, _ := send(assignmentRouter(f), http.MethodPost, "/v1/claims/"+claimID+"/assignment", "operations",
		`{"provider_id": "prv-servicios-hogar-norte", "search_seq": 1}`)
	require.Equal(t, http.StatusCreated, status)
	assert.Equal(t, "assigned", out["state"])
	assert.Equal(t, "prv-servicios-hogar-norte", f.gotProvider)
	assert.Equal(t, 1, f.gotSeq)
	assert.Equal(t, andres, f.gotBy)

	for body, fields := range map[string][]any{
		`{}`: {map[string]any{"field": "provider_id", "code": "required"},
			map[string]any{"field": "search_seq", "code": "required"}},
		`{"provider_id": "PRV-1", "search_seq": 0}`: {map[string]any{"field": "provider_id", "code": "invalid_format"},
			map[string]any{"field": "search_seq", "code": "out_of_range"}},
	} {
		f := &fakeAssigner{}
		status, out, _ := send(assignmentRouter(f), http.MethodPost, "/v1/claims/"+claimID+"/assignment", "operations", body)
		assert.Equal(t, http.StatusUnprocessableEntity, status, body)
		assert.Equal(t, "validation_failed", out["error"], body)
		assert.Equal(t, fields, out["fields"], body)
		assert.Zero(t, f.calls, body)
	}
}

func TestEveryErrorOfTheAssignmentHasItsCode(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{ports.ErrClaimNotFound, http.StatusNotFound, "claim_not_found"},
		{domain.ErrLocationFromApp, http.StatusConflict, "location_from_app"},
		{domain.ErrAssignmentExists, http.StatusConflict, "assignment_exists"},
		{domain.ErrClaimNotAssignable, http.StatusConflict, "claim_not_assignable"},
		{domain.ErrProviderUnavailable, http.StatusConflict, "provider_unavailable"},
		{domain.ErrSearchOutdated, http.StatusConflict, "search_outdated"},
		{domain.ErrLocationMissing, http.StatusUnprocessableEntity, "location_missing"},
		{domain.ErrProviderNotEligible, http.StatusUnprocessableEntity, "provider_not_eligible"},
		{&domain.ValidationError{Fields: []domain.FieldError{{Field: "from_radius_km", Code: domain.CodeOutOfRange}}},
			http.StatusUnprocessableEntity, "validation_failed"},
		{errors.New("connection refused"), http.StatusServiceUnavailable, "claims_unavailable"},
	}
	requests := []struct{ method, path, body string }{
		{http.MethodGet, "/assignment", ""},
		{http.MethodPost, "/incident-location", `{"address": "Calle 1 # 2-3", "latitude": 4, "longitude": -74}`},
		{http.MethodPost, "/provider-searches", ``},
		{http.MethodPost, "/assignment", `{"provider_id": "prv-servicios-hogar-norte", "search_seq": 1}`},
	}
	for _, c := range cases {
		for _, r := range requests {
			f := &fakeAssigner{err: c.err}
			status, out, _ := send(assignmentRouter(f), r.method, "/v1/claims/"+claimID+r.path, "operations", r.body)
			assert.Equal(t, c.status, status, "%s %s: %v", r.method, r.path, c.err)
			assert.Equal(t, c.code, out["error"], "%s %s: %v", r.method, r.path, c.err)
		}
	}
}

func TestAnIdThatIsNotOfAClaimIsNotFound(t *testing.T) {
	for _, r := range []struct{ method, path string }{
		{http.MethodGet, "/assignment"}, {http.MethodPost, "/assignment"},
		{http.MethodPost, "/incident-location"}, {http.MethodPost, "/provider-searches"},
	} {
		f := &fakeAssigner{}
		status, out, _ := send(assignmentRouter(f), r.method, "/v1/claims/SIN-2026-01655"+r.path, "operations", `{}`)
		assert.Equal(t, http.StatusNotFound, status, r.path)
		assert.Equal(t, "claim_not_found", out["error"], r.path)
		assert.Zero(t, f.calls, r.path)
	}
}

func assignmentBatch(ids ...string) string {
	return "/v1/claims/assignments?claim_id=" + strings.Join(ids, "&claim_id=")
}

func TestTheAssignmentOfAPageComesInTheOrderAsked(t *testing.T) {
	ids := claimIDs(3)
	distance := 2.2961
	f := &fakeAssigner{summaries: []assessors.Summary{
		{ClaimID: ids[0], State: domain.AssignmentAssigned, Specialty: domain.SpecialtyPlumbing,
			ProviderName: "Servicios Hogar Norte", DistanceKm: &distance, AssignedAt: searchedAt},
		{ClaimID: ids[1], State: domain.AssignmentPending, Specialty: domain.SpecialtyVehicleAssessment,
			Reason: domain.ReasonNoEligibleProvider},
		{ClaimID: ids[2], State: domain.AssignmentNotFound},
	}}
	rec := do(assignmentRouter(f), http.MethodGet, assignmentBatch(ids...), "operations", nil, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, ids, f.gotIDs)
	assert.JSONEq(t, `{"items": [
		{"claim_id": "`+ids[0]+`", "state": "assigned", "specialty": "plumbing", "provider_name": "Servicios Hogar Norte",
		 "distance_km": 2.3, "assigned_at": "2026-09-06T15:14:02Z", "reason": null},
		{"claim_id": "`+ids[1]+`", "state": "pending_assignment", "specialty": "vehicle_assessment",
		 "provider_name": null, "distance_km": null, "assigned_at": null,
		 "reason": "no_eligible_provider_within_max_radius"},
		{"claim_id": "`+ids[2]+`", "state": "not_found", "specialty": null, "provider_name": null, "distance_km": null,
		 "assigned_at": null, "reason": null}]}`, rec.Body.String())

	for _, path := range []string{"/v1/claims/assignments", assignmentBatch(claimIDs(51)...),
		assignmentBatch(ids[0], ids[0]), assignmentBatch("SIN-2026-01655")} {
		f := &fakeAssigner{}
		rec := do(assignmentRouter(f), http.MethodGet, path, "operations", nil, nil)
		assert.Equal(t, http.StatusBadRequest, rec.Code, "%.80s", path)
		assert.Zero(t, f.calls)
	}
	rec = do(assignmentRouter(&fakeAssigner{}), http.MethodGet, assignmentBatch(claimIDs(50)...), "operations", nil, nil)
	assert.Equal(t, http.StatusOK, rec.Code, "50 is the limit")

	rec = do(assignmentRouter(&fakeAssigner{err: errors.New("down")}), http.MethodGet, assignmentBatch(ids[0]),
		"operations", nil, nil)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestTheAssignmentPolicyInForce(t *testing.T) {
	rec := do(assignmentRouter(&fakeAssigner{}), http.MethodGet, "/v1/claims/assignment-policy", "operations", nil, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"version": "assignment-policy-v1", "radii_km": [5, 10, 20], "initial_radius_km": 5,
		"max_radius_km": 20, "retry_interval_seconds": 300, "specialties": [
		{"line": "auto", "incident_type": "collision", "specialty": "vehicle_assessment"},
		{"line": "auto", "incident_type": "theft", "specialty": null},
		{"line": "auto", "incident_type": "third_party_damage", "specialty": "vehicle_assessment"},
		{"line": "auto", "incident_type": "glass", "specialty": "auto_glass"},
		{"line": "auto", "incident_type": "other", "specialty": "vehicle_assessment"},
		{"line": "home", "incident_type": "water_damage", "specialty": "plumbing"}]}`, rec.Body.String())
}

func TestAnAnalystWithoutNameIsNull(t *testing.T) {
	v := proposed()
	v.LastSearch.SearchedBy = &domain.Analyst{Email: "sub-ops"}
	rec := do(assignmentRouter(&fakeAssigner{view: v}), http.MethodGet, "/v1/claims/"+claimID+"/assignment",
		"operations", nil, nil)
	assert.Equal(t, map[string]any{"email": "sub-ops", "name": nil},
		decode(t, rec)["last_search"].(map[string]any)["searched_by"])
}

func ptrTo[T any](v T) *T { return &v }
