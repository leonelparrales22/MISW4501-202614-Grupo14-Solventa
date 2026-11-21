package httpadapter

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/assistance"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// AssistanceInbox is what the routes of the inbox of assistance use
// (HU-W2.3-03).
type AssistanceInbox interface {
	Inbox(ctx context.Context, f ports.InboxFilter) (assistance.Inbox, error)
	Case(ctx context.Context, id string) (assistance.CaseView, error)
	Attend(ctx context.Context, id string, by domain.Analyst) (assistance.CaseView, error)
	RecordNeed(ctx context.Context, id, need string, by domain.Analyst) (assistance.CaseView, error)
	RecordVoiceLocation(ctx context.Context, id string, in domain.VoiceLocationInput,
		by domain.Analyst) (assistance.CaseView, error)
	Dispatch(ctx context.Context, id, providerID string, by domain.Analyst) (assistance.CaseView, error)
	Close(ctx context.Context, id string, in domain.CloseInput, by domain.Analyst) (assistance.CaseView, error)
	Policy() (domain.AssistanceInboxPolicy, domain.AssignmentPolicy)
}

// WithAssistanceInbox mounts the inbox of operations of the requests for
// assistance (HU-W2.3-03): the inbox, one request with its suggestion, the
// decisions of the analyst and the policy. Only operations (assumption S1);
// the insured reads their own request by the routes of HU-M2.3-01.
func WithAssistanceInbox(svc AssistanceInbox, verifier security.Verifier) Option {
	return func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(noStore, security.Authenticate(verifier), security.RequireRole(security.RoleOperations))
			r.Get("/v1/claims/assistance-inbox", assistanceInbox(svc))
			r.Get("/v1/claims/assistance-policy", assistanceInboxPolicy(svc))
			r.Get("/v1/claims/assistance-inbox/{request_id}", assistanceCase(svc))
			r.Post("/v1/claims/assistance-inbox/{request_id}/attend", attendAssistance(svc))
			r.Post("/v1/claims/assistance-inbox/{request_id}/need", recordAssistanceNeed(svc))
			r.Post("/v1/claims/assistance-inbox/{request_id}/voice-location", recordVoiceLocation(svc))
			r.Post("/v1/claims/assistance-inbox/{request_id}/dispatch", dispatchAssistance(svc))
			r.Post("/v1/claims/assistance-inbox/{request_id}/close", closeAssistance(svc))
		})
	}
}

type voiceLocationBody struct {
	Address    string    `json:"address"`
	Latitude   float64   `json:"latitude"`
	Longitude  float64   `json:"longitude"`
	RecordedBy actorBody `json:"recorded_by"`
	RecordedAt time.Time `json:"recorded_at"`
}

type inboxLocationBody struct {
	assistanceLocationResponse
	Place *string            `json:"place"`
	Voice *voiceLocationBody `json:"voice"`
}

type inboxItemBody struct {
	RequestID     string                   `json:"request_id"`
	RequestNumber string                   `json:"request_number"`
	Status        string                   `json:"status"`
	RequesterName *string                  `json:"requester_name"`
	Policy        assistancePolicyResponse `json:"policy"`
	Location      inboxLocationBody        `json:"location"`
	RequestedAt   time.Time                `json:"requested_at"`
	HandledBy     *actorBody               `json:"handled_by"`
	DispatchedAt  *time.Time               `json:"dispatched_at"`
	ClosedAt      *time.Time               `json:"closed_at"`
}

type inboxSummaryBody struct {
	NewCount                 int  `json:"new_count"`
	InCallCount              int  `json:"in_call_count"`
	DispatchedToday          int  `json:"dispatched_today"`
	MeanDispatchSecondsToday *int `json:"mean_dispatch_seconds_today"`
	DispatchTargetSeconds    int  `json:"dispatch_target_seconds"`
}

type inboxBody struct {
	Summary inboxSummaryBody `json:"summary"`
	Items   []inboxItemBody  `json:"items"`
}

type recordedNeedBody struct {
	Code       string    `json:"code"`
	Specialty  string    `json:"specialty"`
	RecordedBy actorBody `json:"recorded_by"`
	RecordedAt time.Time `json:"recorded_at"`
}

type assistanceCandidateBody struct {
	candidateBody
	EtaMinutes      int `json:"eta_minutes"`
	DispatchedToday int `json:"dispatched_today"`
}

type suggestionBody struct {
	State          string                    `json:"state"`
	Specialty      *string                   `json:"specialty"`
	LocationSource *string                   `json:"location_source"`
	FromRadiusKm   *float64                  `json:"from_radius_km"`
	RadiusKm       *float64                  `json:"radius_km"`
	Reason         *string                   `json:"reason"`
	PolicyVersion  string                    `json:"policy_version"`
	Attempts       []searchAttemptBody       `json:"attempts"`
	Candidates     []assistanceCandidateBody `json:"candidates"`
}

type dispatchedLocationBody struct {
	Source    string   `json:"source"`
	Latitude  float64  `json:"latitude"`
	Longitude float64  `json:"longitude"`
	AccuracyM *float64 `json:"accuracy_m"`
	Address   *string  `json:"address"`
	Place     *string  `json:"place"`
}

type policyVersionsBody struct {
	Assistance string `json:"assistance"`
	Assignment string `json:"assignment"`
}

type dispatchBody struct {
	Provider            assignedProviderBody   `json:"provider"`
	Need                string                 `json:"need"`
	Specialty           string                 `json:"specialty"`
	DistanceKm          float64                `json:"distance_km"`
	EtaMinutes          int                    `json:"eta_minutes"`
	LocationShared      dispatchedLocationBody `json:"location_shared"`
	SuggestedProviderID string                 `json:"suggested_provider_id"`
	DispatchedBy        actorBody              `json:"dispatched_by"`
	DispatchedAt        time.Time              `json:"dispatched_at"`
	PolicyVersions      policyVersionsBody     `json:"policy_versions"`
	Notice              noticeBody             `json:"notice"`
}

type closureBody struct {
	Reason   string    `json:"reason"`
	Note     *string   `json:"note"`
	ClosedBy actorBody `json:"closed_by"`
	ClosedAt time.Time `json:"closed_at"`
}

type historyDetailBody struct {
	Need       string `json:"need,omitempty"`
	ProviderID string `json:"provider_id,omitempty"`
	Reason     string `json:"reason,omitempty"`
	From       string `json:"from,omitempty"`
}

type historyEntryBody struct {
	Action string             `json:"action"`
	By     actorBody          `json:"by"`
	At     time.Time          `json:"at"`
	Detail *historyDetailBody `json:"detail"`
}

type assistanceCaseBody struct {
	inboxItemBody
	Need       *recordedNeedBody  `json:"need"`
	Suggestion suggestionBody     `json:"suggestion"`
	Dispatch   *dispatchBody      `json:"dispatch"`
	Closure    *closureBody       `json:"closure"`
	History    []historyEntryBody `json:"history"`
}

func inboxItemOf(r ports.AssistanceRecord) inboxItemBody {
	c := r.Case
	out := inboxItemBody{RequestID: c.Request.ID, RequestNumber: c.Request.Number, Status: string(c.Request.Status),
		RequesterName: nullable(c.Request.RequesterName), RequestedAt: c.Request.RequestedAt.UTC(),
		HandledBy: optionalActor(c.HandledBy()),
		Location: inboxLocationBody{assistanceLocationResponse: assistanceBodyOf(c.Request).Location,
			Place: c.Request.Place},
		Policy: assistancePolicyResponse{PolicyID: c.Request.Policy.ID, Line: string(c.Request.Policy.Line),
			Product: c.Request.Policy.Product, InsuredObject: c.Request.Policy.InsuredObject}}
	if v := c.Voice; v != nil {
		out.Location.Voice = &voiceLocationBody{Address: v.Address, Latitude: v.Point.Latitude,
			Longitude: v.Point.Longitude, RecordedBy: actorOf(v.By), RecordedAt: v.At.UTC()}
	}
	if d := c.Dispatch; d != nil {
		at := d.At.UTC()
		out.DispatchedAt = &at
	}
	if cl := c.Closure; cl != nil {
		at := cl.At.UTC()
		out.ClosedAt = &at
	}
	return out
}

func suggestionBodyOf(s domain.AssistanceSuggestion) suggestionBody {
	out := suggestionBody{State: string(s.State), Specialty: nullable(string(s.Specialty)),
		LocationSource: nullable(string(s.Source)), Reason: nullable(s.Result.Reason),
		PolicyVersion: s.AssignmentPolicyVersion, Attempts: make([]searchAttemptBody, 0, len(s.Result.Attempts)),
		Candidates: make([]assistanceCandidateBody, 0, len(s.Candidates))}
	if s.State == domain.SuggestionFound || s.State == domain.SuggestionExhausted {
		from, radius := s.Result.FromRadiusKm, s.Result.RadiusKm
		out.FromRadiusKm, out.RadiusKm = &from, &radius
	}
	for _, a := range s.Result.Attempts {
		out.Attempts = append(out.Attempts, searchAttemptBody{RadiusKm: a.RadiusKm, EligibleCount: a.EligibleCount,
			AttemptedAt: a.AttemptedAt.UTC()})
	}
	for _, c := range s.Candidates {
		out.Candidates = append(out.Candidates, assistanceCandidateBody{candidateBody: candidateBody{
			ProviderID: c.ProviderID, Name: c.Name, Specialties: specialtyNames(c.Specialties),
			DistanceKm: domain.RoundKm(c.DistanceKm), Availability: string(c.Availability),
			UnavailableUntil: day(c.UnavailableUntil), Eligibility: string(c.Eligibility), Suggested: c.Suggested},
			EtaMinutes: c.EtaMinutes, DispatchedToday: c.DispatchedToday})
	}
	return out
}

func dispatchBodyOf(d domain.AssistanceDispatch, n *ports.AssistanceNotice) *dispatchBody {
	l := d.Location
	out := &dispatchBody{
		Provider: assignedProviderBody{ID: d.Provider.ID, Name: d.Provider.Name, ContactEmail: d.Provider.ContactEmail,
			ContactPhone: d.Provider.ContactPhone},
		Need: string(d.Need), Specialty: string(d.Specialty), DistanceKm: domain.RoundKm(d.DistanceKm),
		EtaMinutes: d.EtaMinutes, SuggestedProviderID: d.SuggestedProviderID, DispatchedBy: actorOf(d.By),
		DispatchedAt: d.At.UTC(),
		LocationShared: dispatchedLocationBody{Source: string(l.Source), Latitude: l.Point.Latitude,
			Longitude: l.Point.Longitude, AccuracyM: l.AccuracyM, Address: nullable(l.Address), Place: l.Place},
		PolicyVersions: policyVersionsBody{Assistance: d.AssistancePolicyVersion, Assignment: d.AssignmentPolicyVersion},
		Notice:         noticeBody{Status: string(ports.NoticePending)},
	}
	if n != nil {
		out.Notice = noticeBody{Status: string(n.Status), Attempts: n.Attempts, SentAt: optionalTime(n.SentAt)}
	}
	return out
}

func historyOf(actions []domain.AssistanceAction) []historyEntryBody {
	out := make([]historyEntryBody, 0, len(actions))
	for _, a := range actions {
		entry := historyEntryBody{Action: string(a.Code), By: actorOf(a.By), At: a.At.UTC()}
		detail := historyDetailBody{Need: string(a.Need), ProviderID: a.ProviderID, Reason: string(a.Reason),
			From: a.From}
		if detail != (historyDetailBody{}) {
			entry.Detail = &detail
		}
		out = append(out, entry)
	}
	return out
}

func assistanceCaseBodyOf(v assistance.CaseView) assistanceCaseBody {
	c := v.Case
	out := assistanceCaseBody{inboxItemBody: inboxItemOf(v.AssistanceRecord),
		Suggestion: suggestionBodyOf(v.Suggestion), History: historyOf(c.History)}
	if n := c.Need; n != nil {
		out.Need = &recordedNeedBody{Code: string(n.Need), Specialty: string(n.Specialty), RecordedBy: actorOf(n.By),
			RecordedAt: n.At.UTC()}
	}
	if d := c.Dispatch; d != nil {
		out.Dispatch = dispatchBodyOf(*d, v.Notice)
	}
	if cl := c.Closure; cl != nil {
		out.Closure = &closureBody{Reason: string(cl.Reason), Note: cl.Note, ClosedBy: actorOf(cl.By),
			ClosedAt: cl.At.UTC()}
	}
	return out
}

// inboxFilterOf reads the filter of the inbox: status, repeatable and without
// repeats, and limit, 1 to 100 (assumption S11).
func inboxFilterOf(req *http.Request) (ports.InboxFilter, bool) {
	q := req.URL.Query()
	var f ports.InboxFilter
	statuses := q["status"]
	if len(statuses) > 4 {
		return f, false
	}
	for _, s := range statuses {
		st := domain.AssistanceStatus(s)
		for _, seen := range f.Statuses {
			if seen == st {
				return f, false
			}
		}
		if !st.Valid() {
			return f, false
		}
		f.Statuses = append(f.Statuses, st)
	}
	if limits, given := q["limit"]; given {
		if len(limits) != 1 {
			return f, false
		}
		n, err := strconv.Atoi(limits[0])
		if err != nil || n < 1 || n > assistance.MaxInboxLimit {
			return f, false
		}
		f.Limit = n
	}
	return f, true
}

func assistanceInbox(svc AssistanceInbox) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		f, ok := inboxFilterOf(req)
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid_request",
				"status must be among new, in_call, dispatched and closed without repeats, and limit between 1 and 100")
			return
		}
		inbox, err := svc.Inbox(req.Context(), f)
		if writeInboxError(w, req, "", err) {
			return
		}
		s := inbox.Summary
		out := inboxBody{Summary: inboxSummaryBody{NewCount: s.New, InCallCount: s.InCall,
			DispatchedToday: s.DispatchedToday, MeanDispatchSecondsToday: s.MeanDispatchSeconds,
			DispatchTargetSeconds: s.TargetSeconds}, Items: make([]inboxItemBody, 0, len(inbox.Items))}
		for _, r := range inbox.Items {
			out.Items = append(out.Items, inboxItemOf(r))
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// requestIDOf reads the id of the path; one that is not a UUID v4 answers as a
// request that does not exist, without reaching the service.
func requestIDOf(w http.ResponseWriter, req *http.Request) (string, bool) {
	id := chi.URLParam(req, "request_id")
	if !assistanceIDPattern.MatchString(id) {
		writeAssistanceNotFound(w)
		return "", false
	}
	return id, true
}

func assistanceCase(svc AssistanceInbox) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := requestIDOf(w, req)
		if !ok {
			return
		}
		v, err := svc.Case(req.Context(), id)
		if writeInboxError(w, req, id, err) {
			return
		}
		writeJSON(w, http.StatusOK, assistanceCaseBodyOf(v))
	}
}

// decide answers a decision of the analyst with the whole request.
func decide(w http.ResponseWriter, req *http.Request, id string, v assistance.CaseView, err error) {
	if writeInboxError(w, req, id, err) {
		return
	}
	writeJSON(w, http.StatusOK, assistanceCaseBodyOf(v))
}

func attendAssistance(svc AssistanceInbox) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := requestIDOf(w, req)
		if !ok {
			return
		}
		var body struct{}
		if !decodeOptional(w, req, &body) {
			return
		}
		v, err := svc.Attend(req.Context(), id, analystFrom(req))
		decide(w, req, id, v, err)
	}
}

type needRequest struct {
	Need string `json:"need"`
}

func recordAssistanceNeed(svc AssistanceInbox) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := requestIDOf(w, req)
		if !ok {
			return
		}
		var body needRequest
		if !decodeStrict(w, req, &body) {
			return
		}
		v, err := svc.RecordNeed(req.Context(), id, body.Need, analystFrom(req))
		decide(w, req, id, v, err)
	}
}

func recordVoiceLocation(svc AssistanceInbox) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := requestIDOf(w, req)
		if !ok {
			return
		}
		var body incidentLocationRequest
		if !decodeStrict(w, req, &body) {
			return
		}
		v, err := svc.RecordVoiceLocation(req.Context(), id, domain.VoiceLocationInput{Address: body.Address,
			Latitude: body.Latitude, Longitude: body.Longitude}, analystFrom(req))
		decide(w, req, id, v, err)
	}
}

type dispatchRequest struct {
	ProviderID *string `json:"provider_id"`
}

func dispatchAssistance(svc AssistanceInbox) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := requestIDOf(w, req)
		if !ok {
			return
		}
		var body dispatchRequest
		if !decodeStrict(w, req, &body) {
			return
		}
		switch {
		case body.ProviderID == nil:
			writeValidationError(w, &domain.ValidationError{Fields: []domain.FieldError{{Field: "provider_id",
				Code: domain.CodeRequired}}})
			return
		case !domain.ValidProviderID(*body.ProviderID):
			writeValidationError(w, &domain.ValidationError{Fields: []domain.FieldError{{Field: "provider_id",
				Code: domain.CodeInvalidFormat}}})
			return
		}
		v, err := svc.Dispatch(req.Context(), id, *body.ProviderID, analystFrom(req))
		decide(w, req, id, v, err)
	}
}

type closeRequest struct {
	Reason string  `json:"reason"`
	Note   *string `json:"note"`
}

func closeAssistance(svc AssistanceInbox) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := requestIDOf(w, req)
		if !ok {
			return
		}
		var body closeRequest
		if !decodeStrict(w, req, &body) {
			return
		}
		v, err := svc.Close(req.Context(), id, domain.CloseInput{Reason: body.Reason, Note: body.Note}, analystFrom(req))
		decide(w, req, id, v, err)
	}
}

type needRuleBody struct {
	Line      string `json:"line"`
	Code      string `json:"code"`
	Specialty string `json:"specialty"`
}

type etaBody struct {
	PreparationMinutes int     `json:"preparation_minutes"`
	AverageSpeedKmh    float64 `json:"average_speed_kmh"`
}

type assistanceInboxPolicyBody struct {
	Version                 string         `json:"version"`
	Needs                   []needRuleBody `json:"needs"`
	RadiiKm                 []float64      `json:"radii_km"`
	AssignmentPolicyVersion string         `json:"assignment_policy_version"`
	Eta                     etaBody        `json:"eta"`
	CloseReasons            []string       `json:"close_reasons"`
	DispatchTargetSeconds   int            `json:"dispatch_target_seconds"`
}

func assistanceInboxPolicy(svc AssistanceInbox) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		p, a := svc.Policy()
		out := assistanceInboxPolicyBody{Version: p.Version, RadiiKm: a.RadiiKm, AssignmentPolicyVersion: a.Version,
			Eta:                   etaBody{PreparationMinutes: p.PreparationMinutes, AverageSpeedKmh: p.AverageSpeedKmh},
			DispatchTargetSeconds: int(p.DispatchTarget / time.Second),
			Needs:                 make([]needRuleBody, 0, len(p.Needs)), CloseReasons: make([]string, 0, len(p.CloseReasons))}
		for _, n := range p.Needs {
			out.Needs = append(out.Needs, needRuleBody{Line: string(n.Line), Code: string(n.Need),
				Specialty: string(n.Specialty)})
		}
		for _, r := range p.CloseReasons {
			out.CloseReasons = append(out.CloseReasons, string(r))
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// inboxErrors are the errors of the inbox the analyst can act on, with the
// status and the code of the contract.
var inboxErrors = []struct {
	err     error
	status  int
	code    string
	message string
}{
	{ports.ErrAssistanceRequestNotFound, http.StatusNotFound, "assistance_request_not_found",
		"there is no such request for assistance"},
	{domain.ErrAssistanceNotOpen, http.StatusConflict, "assistance_not_open",
		"the request is already dispatched or closed"},
	{domain.ErrAssistanceNotAttended, http.StatusConflict, "assistance_not_attended",
		"the request has not been attended: attend it first"},
	{domain.ErrLocationFromApp, http.StatusConflict, "location_from_app",
		"the request has the coordinates of the phone"},
	{domain.ErrNeedNotForLine, http.StatusUnprocessableEntity, "need_not_for_line",
		"the need is not one of the line of the policy"},
	{domain.ErrNeedMissing, http.StatusUnprocessableEntity, "need_missing", "the request has no need recorded"},
	{domain.ErrLocationMissing, http.StatusUnprocessableEntity, "location_missing",
		"the request has no coordinates from the phone nor by voice"},
	{domain.ErrProviderNotEligible, http.StatusUnprocessableEntity, "provider_not_eligible",
		"the provider is not eligible now"},
}

// writeInboxError answers the error of the inbox and tells whether there was
// one. The log names the request, never the location nor a contact
// (assumption S15).
func writeInboxError(w http.ResponseWriter, req *http.Request, requestID string, err error) bool {
	if err == nil {
		return false
	}
	var invalid *domain.ValidationError
	if errors.As(err, &invalid) {
		writeValidationError(w, invalid)
		return true
	}
	for _, e := range inboxErrors {
		if errors.Is(err, e.err) {
			writeError(w, e.status, e.code, e.message)
			return true
		}
	}
	slog.ErrorContext(req.Context(), "could not serve the inbox of assistance", "request_id", requestID, "error", err)
	writeError(w, http.StatusServiceUnavailable, "assistance_unavailable", "the inbox could not be served; retry")
	return true
}
