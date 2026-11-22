package domain

import (
	"errors"
	"math"
	"strings"
	"time"
)

// The inbox of operations that attends the requests for assistance
// (HU-W2.3-03). A request goes new → in_call → dispatched or closed, and new
// → closed (decision D6). Every decision keeps its analyst and its time
// (criterion 5, decision D7); the suggestion of provider is the search of
// HU-W2.3-01, which this file does not repeat (decision D5).

// The statuses the inbox writes. AssistanceNew is the one of HU-M2.3-01.
const (
	AssistanceInCall     AssistanceStatus = "in_call"
	AssistanceDispatched AssistanceStatus = "dispatched"
	AssistanceClosed     AssistanceStatus = "closed"
)

// Valid reports whether the status is one of the four of the inbox.
func (s AssistanceStatus) Valid() bool {
	switch s {
	case AssistanceNew, AssistanceInCall, AssistanceDispatched, AssistanceClosed:
		return true
	}
	return false
}

// Open reports whether the request can still be attended: new or in_call.
func (s AssistanceStatus) Open() bool { return s == AssistanceNew || s == AssistanceInCall }

var (
	// ErrAssistanceNotOpen is a request already dispatched or closed.
	ErrAssistanceNotOpen = errors.New("claims: the request for assistance is already dispatched or closed")
	// ErrAssistanceNotAttended is a request still new: an analyst must
	// attend it first (assumption S3).
	ErrAssistanceNotAttended = errors.New("claims: the request for assistance has not been attended")
	// ErrNeedMissing is a dispatch with no need recorded.
	ErrNeedMissing = errors.New("claims: the request for assistance has no need")
)

// Limits of what the analyst writes on a request.
const (
	MinCloseNote = 3
	MaxCloseNote = 500
)

// AssistanceActionCode names a decision on a request (criterion 5).
type AssistanceActionCode string

const (
	ActionAttended              AssistanceActionCode = "attended"
	ActionTakenOver             AssistanceActionCode = "taken_over"
	ActionNeedRecorded          AssistanceActionCode = "need_recorded"
	ActionVoiceLocationRecorded AssistanceActionCode = "voice_location_recorded"
	ActionDispatched            AssistanceActionCode = "dispatched"
	ActionClosed                AssistanceActionCode = "closed"
)

// AssistanceAction is one decision on a request: who, when and what. The
// store numbers them (Seq) in the order they are recorded.
type AssistanceAction struct {
	Seq  int
	Code AssistanceActionCode
	By   Analyst
	At   time.Time
	// The detail of the action; empty when it does not apply.
	Need       AssistanceNeed
	ProviderID string
	Reason     CloseReason
	// From is the e-mail of the analyst a request was taken over from.
	From string
}

// RecordedNeed is a need the analyst recorded; the last one is in force.
type RecordedNeed struct {
	Seq           int
	Need          AssistanceNeed
	Specialty     Specialty
	PolicyVersion string
	By            Analyst
	At            time.Time
}

// VoiceLocation is the address the insured gave by voice with the pin the
// analyst put on the map (decision D3); the last one is in force.
type VoiceLocation struct {
	Seq     int
	Address string
	Point   Coordinates
	By      Analyst
	At      time.Time
}

// VoiceLocationInput is the location by voice as it came. The pointers tell
// a coordinate that was not sent from a zero.
type VoiceLocationInput struct {
	Address   string
	Latitude  *float64
	Longitude *float64
}

// SharedSource is where the location of a request came from.
type SharedSource string

const (
	SharedFromApp   SharedSource = "app"
	SharedFromVoice SharedSource = "voice"
)

// SharedLocation is the location a request is served at: the fix of the
// phone or the pin of the analyst.
type SharedLocation struct {
	Source SharedSource
	Point  Coordinates
	// AccuracyM is the accuracy of the phone; nil by voice.
	AccuracyM *float64
	// Address is the address given by voice; empty for the phone.
	Address string
	// Place is the address the app sent; nil when it sent none.
	Place *string
}

// DispatchedProvider is the provider as it was when dispatched.
type DispatchedProvider struct {
	ID           string
	Name         string
	ContactEmail string
	ContactPhone *string
}

// AssistanceDispatch is the provider sent to a request (criterion 3). It
// keeps what the analyst saw: the suggested one, the candidates and the
// attempts.
type AssistanceDispatch struct {
	Provider                DispatchedProvider
	Need                    AssistanceNeed
	Specialty               Specialty
	DistanceKm              float64
	EtaMinutes              int
	Location                SharedLocation
	SuggestedProviderID     string
	Candidates              []Candidate
	Attempts                []SearchAttempt
	AssistancePolicyVersion string
	AssignmentPolicyVersion string
	By                      Analyst
	At                      time.Time
}

// AssistanceClosure is a request closed without a dispatch (criterion 4).
type AssistanceClosure struct {
	Reason CloseReason
	// Note is required with CloseOther; nil when none was written.
	Note *string
	By   Analyst
	At   time.Time
}

// CloseInput is the closing as it came.
type CloseInput struct {
	Reason string
	Note   *string
}

// AssistanceCase is a request with everything the inbox decided on it.
type AssistanceCase struct {
	Request  AssistanceRequest
	Need     *RecordedNeed
	Voice    *VoiceLocation
	Dispatch *AssistanceDispatch
	Closure  *AssistanceClosure
	// History is every decision, oldest first.
	History []AssistanceAction
}

// HandledBy is the last analyst who attended the request; nil while nobody
// did.
func (c AssistanceCase) HandledBy() *Analyst {
	for i := len(c.History) - 1; i >= 0; i-- {
		if a := c.History[i]; a.Code == ActionAttended || a.Code == ActionTakenOver {
			by := a.By
			return &by
		}
	}
	return nil
}

// Location is the location in force: the fix of the phone or, without it,
// the last one given by voice. false when there is none yet.
func (c AssistanceCase) Location() (SharedLocation, bool) {
	r := c.Request
	if r.Location.Status == LocationSent && r.Location.Coordinates != nil {
		return SharedLocation{Source: SharedFromApp, Point: *r.Location.Coordinates, AccuracyM: r.Location.AccuracyM,
			Place: r.Place}, true
	}
	if c.Voice != nil {
		return SharedLocation{Source: SharedFromVoice, Point: c.Voice.Point, Address: c.Voice.Address, Place: r.Place},
			true
	}
	return SharedLocation{}, false
}

// inCall checks that the request is attended and still open (assumption S3).
func (c AssistanceCase) inCall() error {
	switch c.Request.Status {
	case AssistanceNew:
		return ErrAssistanceNotAttended
	case AssistanceInCall:
		return nil
	}
	return ErrAssistanceNotOpen
}

// Attend is the analyst attending the request (assumption S2): a new one
// becomes in_call; one in_call of another analyst is taken over; one of the
// same analyst does not change, and changed is false.
func Attend(c AssistanceCase, by Analyst, at time.Time) (action AssistanceAction, changed bool, err error) {
	switch c.Request.Status {
	case AssistanceNew:
		return AssistanceAction{Code: ActionAttended, By: by, At: at.UTC()}, true, nil
	case AssistanceInCall:
		handler := c.HandledBy()
		if handler != nil && handler.Email == by.Email {
			return AssistanceAction{}, false, nil
		}
		action = AssistanceAction{Code: ActionTakenOver, By: by, At: at.UTC()}
		if handler != nil {
			action.From = handler.Email
		}
		return action, true, nil
	}
	return AssistanceAction{}, false, ErrAssistanceNotOpen
}

// RecordNeed is the analyst choosing what the insured needs (assumption S4).
// The need must be one of the line of the policy; it may change until the
// request is dispatched.
func RecordNeed(c AssistanceCase, p AssistanceInboxPolicy, need string, by Analyst,
	at time.Time) (RecordedNeed, AssistanceAction, error) {
	if err := c.inCall(); err != nil {
		return RecordedNeed{}, AssistanceAction{}, err
	}
	n := AssistanceNeed(strings.TrimSpace(need))
	switch {
	case n == "":
		return RecordedNeed{}, AssistanceAction{}, &ValidationError{Fields: []FieldError{{Field: "need", Code: CodeRequired}}}
	case !p.Known(n):
		return RecordedNeed{}, AssistanceAction{}, &ValidationError{Fields: []FieldError{{Field: "need",
			Code: CodeInvalidFormat}}}
	}
	specialty, err := p.SpecialtyOf(c.Request.Policy.Line, n)
	if err != nil {
		return RecordedNeed{}, AssistanceAction{}, err
	}
	seq := 1
	if c.Need != nil {
		seq = c.Need.Seq + 1
	}
	at = at.UTC()
	return RecordedNeed{Seq: seq, Need: n, Specialty: specialty, PolicyVersion: p.Version, By: by, At: at},
		AssistanceAction{Code: ActionNeedRecorded, By: by, At: at, Need: n}, nil
}

// RecordVoiceLocation is the analyst recording the address the insured gives
// by voice and the pin of the map (decision D3, assumption S5). Only for a
// request without coordinates from the phone; it may be corrected until the
// request is dispatched.
func RecordVoiceLocation(c AssistanceCase, in VoiceLocationInput, by Analyst,
	at time.Time) (VoiceLocation, AssistanceAction, error) {
	if err := c.inCall(); err != nil {
		return VoiceLocation{}, AssistanceAction{}, err
	}
	if c.Request.Location.Status == LocationSent {
		return VoiceLocation{}, AssistanceAction{}, ErrLocationFromApp
	}

	var fields []FieldError
	bad := func(field, code string) { fields = append(fields, FieldError{Field: field, Code: code}) }
	address := strings.TrimSpace(in.Address)
	lengthOf("address", address, MinAddress, MaxAddress, bad)
	lat := coordinate("latitude", in.Latitude, validLatitude, bad)
	lon := coordinate("longitude", in.Longitude, validLongitude, bad)
	if len(fields) > 0 {
		return VoiceLocation{}, AssistanceAction{}, &ValidationError{Fields: fields}
	}

	seq := 1
	if c.Voice != nil {
		seq = c.Voice.Seq + 1
	}
	at = at.UTC()
	return VoiceLocation{Seq: seq, Address: address, Point: Coordinates{Latitude: *lat, Longitude: *lon}, By: by, At: at},
		AssistanceAction{Code: ActionVoiceLocationRecorded, By: by, At: at}, nil
}

// SuggestionState is whether a request can be searched for and what the
// search found (assumption S6).
type SuggestionState string

const (
	SuggestionLocationMissing SuggestionState = "location_missing"
	SuggestionNeedMissing     SuggestionState = "need_missing"
	SuggestionFound           SuggestionState = "found"
	SuggestionExhausted       SuggestionState = "exhausted"
	// SuggestionNotApplicable is a request dispatched or closed: there is
	// nothing to search for.
	SuggestionNotApplicable SuggestionState = "not_applicable"
)

// Searchable tells what the suggestion of the request searches for: the
// specialty and the location, and an empty state. When it cannot be searched
// the state says why: closed or dispatched, then no location, then no need.
func (c AssistanceCase) Searchable() (Specialty, SharedLocation, SuggestionState) {
	if !c.Request.Status.Open() {
		return "", SharedLocation{}, SuggestionNotApplicable
	}
	loc, ok := c.Location()
	if !ok {
		return "", SharedLocation{}, SuggestionLocationMissing
	}
	if c.Need == nil {
		return "", loc, SuggestionNeedMissing
	}
	return c.Need.Specialty, loc, ""
}

// AssistanceCandidate is a candidate of the search with its estimated arrival
// and the assistances it was sent to today (assumption S7).
type AssistanceCandidate struct {
	Candidate
	EtaMinutes      int
	DispatchedToday int
}

// AssistanceSuggestion is the suggestion of provider of a request, computed
// on every read and never recorded (decision D8).
type AssistanceSuggestion struct {
	State     SuggestionState
	Specialty Specialty
	// Source is empty when there was no search.
	Source SharedSource
	// Result is the search; zero when there was none.
	Result                  SearchResult
	Candidates              []AssistanceCandidate
	AssignmentPolicyVersion string
}

// NoSuggestion is the suggestion of a request that cannot be searched.
func NoSuggestion(state SuggestionState, assignmentVersion string) AssistanceSuggestion {
	return AssistanceSuggestion{State: state, AssignmentPolicyVersion: assignmentVersion}
}

// NewSuggestion completes the result of the search with the estimated arrival
// and the dispatches of today of every candidate.
func NewSuggestion(specialty Specialty, loc SharedLocation, result SearchResult, p AssistanceInboxPolicy,
	assignmentVersion string, dispatchedToday map[string]int) AssistanceSuggestion {
	s := AssistanceSuggestion{State: SuggestionExhausted, Specialty: specialty, Source: loc.Source, Result: result,
		AssignmentPolicyVersion: assignmentVersion}
	if result.Outcome == SearchFound {
		s.State = SuggestionFound
	}
	for _, c := range result.Candidates {
		s.Candidates = append(s.Candidates, AssistanceCandidate{Candidate: c, EtaMinutes: p.EtaMinutes(c.DistanceKm),
			DispatchedToday: dispatchedToday[c.ProviderID]})
	}
	return s
}

// Dispatch sends a provider to the request (criterion 3, assumption S9). The
// result is the search made at this moment and current the provider as the
// registry has it now (nil when it does not exist): it must be one of the
// eligible ones of that search, the suggested one or another.
func Dispatch(c AssistanceCase, p AssistanceInboxPolicy, assignmentVersion string, result SearchResult,
	current *Provider, by Analyst, at time.Time) (AssistanceDispatch, AssistanceAction, error) {
	if err := c.inCall(); err != nil {
		return AssistanceDispatch{}, AssistanceAction{}, err
	}
	loc, ok := c.Location()
	if !ok {
		return AssistanceDispatch{}, AssistanceAction{}, ErrLocationMissing
	}
	if c.Need == nil {
		return AssistanceDispatch{}, AssistanceAction{}, ErrNeedMissing
	}
	if current == nil {
		return AssistanceDispatch{}, AssistanceAction{}, ErrProviderNotEligible
	}
	var chosen *Candidate
	for i, cand := range result.Candidates {
		if cand.ProviderID == current.ID && cand.Eligibility == Eligible {
			chosen = &result.Candidates[i]
		}
	}
	if chosen == nil || !current.Available() || !current.Has(c.Need.Specialty) {
		return AssistanceDispatch{}, AssistanceAction{}, ErrProviderNotEligible
	}
	suggested, _ := result.Suggested()

	at = at.UTC()
	return AssistanceDispatch{
			Provider: DispatchedProvider{ID: current.ID, Name: current.Name, ContactEmail: current.ContactEmail,
				ContactPhone: current.ContactPhone},
			Need:                    c.Need.Need,
			Specialty:               c.Need.Specialty,
			DistanceKm:              chosen.DistanceKm,
			EtaMinutes:              p.EtaMinutes(chosen.DistanceKm),
			Location:                loc,
			SuggestedProviderID:     suggested.ProviderID,
			Candidates:              append([]Candidate(nil), result.Candidates...),
			Attempts:                append([]SearchAttempt(nil), result.Attempts...),
			AssistancePolicyVersion: p.Version,
			AssignmentPolicyVersion: assignmentVersion,
			By:                      by,
			At:                      at,
		}, AssistanceAction{Code: ActionDispatched, By: by, At: at, ProviderID: current.ID},
		nil
}

// Close closes the request without a dispatch (criterion 4, assumption S10):
// from new or in_call, with a reason of the policy and a note when the reason
// is other.
func Close(c AssistanceCase, p AssistanceInboxPolicy, in CloseInput, by Analyst,
	at time.Time) (AssistanceClosure, AssistanceAction, error) {
	if !c.Request.Status.Open() {
		return AssistanceClosure{}, AssistanceAction{}, ErrAssistanceNotOpen
	}

	var fields []FieldError
	bad := func(field, code string) { fields = append(fields, FieldError{Field: field, Code: code}) }
	reason := CloseReason(strings.TrimSpace(in.Reason))
	switch {
	case reason == "":
		bad("reason", CodeRequired)
	case !p.Closes(reason):
		bad("reason", CodeInvalidFormat)
	}
	var note *string
	if in.Note != nil {
		text := strings.TrimSpace(*in.Note)
		lengthOf("note", text, MinCloseNote, MaxCloseNote, bad)
		note = &text
	} else if reason == CloseOther {
		bad("note", CodeRequired)
	}
	if len(fields) > 0 {
		return AssistanceClosure{}, AssistanceAction{}, &ValidationError{Fields: fields}
	}

	at = at.UTC()
	return AssistanceClosure{Reason: reason, Note: note, By: by, At: at},
		AssistanceAction{Code: ActionClosed, By: by, At: at, Reason: reason}, nil
}

// AssistanceNoticeKey is the idempotency key of the e-mail to the provider
// of a dispatch (decision D4): a retry never sends it twice.
func AssistanceNoticeKey(requestID string) string { return "asistencia-" + requestID }

// ColombiaDayStart is the instant today began in Colombia, in UTC: "today"
// of the indicators of the inbox.
func ColombiaDayStart(now time.Time) time.Time {
	local := now.In(ColombiaTime)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, ColombiaTime).UTC()
}

// InboxSummary is the indicators of the inbox (assumption S11).
type InboxSummary struct {
	New             int
	InCall          int
	DispatchedToday int
	// MeanDispatchSeconds is the mean from the request to its dispatch of the
	// requests dispatched today; nil when none was.
	MeanDispatchSeconds *int
	TargetSeconds       int
}

// NewInboxSummary takes the counts and how long each dispatch of today took.
func NewInboxSummary(newCount, inCall int, dispatchesToday []time.Duration, p AssistanceInboxPolicy) InboxSummary {
	s := InboxSummary{New: newCount, InCall: inCall, DispatchedToday: len(dispatchesToday),
		TargetSeconds: int(p.DispatchTarget / time.Second)}
	if len(dispatchesToday) > 0 {
		var total time.Duration
		for _, d := range dispatchesToday {
			total += d
		}
		mean := int(math.Round(total.Seconds() / float64(len(dispatchesToday))))
		s.MeanDispatchSeconds = &mean
	}
	return s
}
