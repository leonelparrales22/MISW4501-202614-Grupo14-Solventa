package domain

import (
	"errors"
	"sort"
	"strings"
	"time"
)

// Assignable tells whether a claim in the status can be assigned a provider:
// it is in evaluation, approved by the advisor (HU-W2.1-03, assumption S1).
// The status of the claim does not change with the assignment (decision D2).
func Assignable(s Status) bool { return s == StatusInEvaluation }

// Errors of the assignment of a provider (HU-W2.3-01).
var (
	// ErrClaimNotAssignable is a claim that is not in evaluation, or whose
	// type needs no on-site visit.
	ErrClaimNotAssignable = errors.New("claims: the claim is not in evaluation or needs no on-site visit")
	// ErrAssignmentExists is a claim that already has its provider.
	ErrAssignmentExists = errors.New("claims: the claim already has its provider")
	// ErrLocationMissing is a claim with no coordinates from the app and no
	// location recorded by the analyst.
	ErrLocationMissing = errors.New("claims: the claim has no location to search from")
	// ErrLocationFromApp is a location recorded for a claim that has trusted
	// coordinates from the app: the alternative is only for when it has none.
	ErrLocationFromApp = errors.New("claims: the claim has trusted coordinates from the app")
	// ErrSearchOutdated is an assignment that names a search that is not the
	// last one of the claim, or whose location is no longer the claim's.
	ErrSearchOutdated = errors.New("claims: the search is not the last one of the claim")
	// ErrProviderNotEligible is a provider that is not among the eligible of
	// the search.
	ErrProviderNotEligible = errors.New("claims: the provider is not eligible in that search")
	// ErrProviderUnavailable is a provider that stopped being available after
	// the search.
	ErrProviderUnavailable = errors.New("claims: the provider is no longer available")
)

// Analyst is the analyst of operations who acts, from the token: the e-mail
// identifies them and the name is what the screens show.
type Analyst struct {
	Email string
	Name  string
}

// LocationSource is where the location of the incident came from.
type LocationSource string

const (
	// LocationFromApp is the most precise trusted evidence of the phone.
	LocationFromApp LocationSource = "app"
	// LocationFromAnalyst is the address and pin recorded by operations.
	LocationFromAnalyst LocationSource = "analyst"
)

// IncidentLocation is where the incident happened, as the search measures
// from it (criterion 1).
type IncidentLocation struct {
	Source LocationSource
	Point  Coordinates
	// AccuracyM is the precision of the capture; nil for the analyst.
	AccuracyM *float64
	// Address is the one the analyst recorded; empty for the app.
	Address string
	// Seq numbers the locations the analyst recorded for the claim; 0 for
	// the app.
	Seq        int
	RecordedBy *Analyst
	// RecordedAt is when the analyst recorded it, or when the phone captured
	// the evidence.
	RecordedAt time.Time
}

// SameAs tells whether two locations are the same point from the same source:
// a search made from another location is outdated.
func (l IncidentLocation) SameAs(o IncidentLocation) bool {
	return l.Source == o.Source && l.Point == o.Point
}

// AppLocation is the location the phone gave the claim: the trusted evidence
// (precision of 50 m or better) with the best precision; the first captured on
// a tie (assumption S3). false when the claim has none.
func AppLocation(evidence []Evidence) (IncidentLocation, bool) {
	best := -1
	for i, e := range evidence {
		if !e.LocationTrusted || e.AccuracyM <= 0 || e.AccuracyM > TrustedAccuracyM {
			continue
		}
		if best < 0 || e.AccuracyM < evidence[best].AccuracyM {
			best = i
		}
	}
	if best < 0 {
		return IncidentLocation{}, false
	}
	e := evidence[best]
	accuracy := e.AccuracyM
	return IncidentLocation{Source: LocationFromApp, Point: Coordinates{Latitude: e.Latitude, Longitude: e.Longitude},
		AccuracyM: &accuracy, RecordedAt: e.CapturedAt.UTC()}, true
}

// LocationOf is the location the claim is searched from: the one of the app
// or, without it, the last one the analyst recorded (nil when none). nil when
// the claim has neither.
func LocationOf(c ClaimRecord, recorded *IncidentLocation) *IncidentLocation {
	if l, ok := AppLocation(c.Evidence); ok {
		return &l
	}
	if recorded != nil {
		l := *recorded
		return &l
	}
	return nil
}

// LocationRequest is the address and the pin the analyst records when the
// claim has no coordinates from the app. The coordinates are pointers so a
// missing one is told from zero.
type LocationRequest struct {
	Address   string
	Latitude  *float64
	Longitude *float64
}

// RecordLocation is the analyst recording the location of the incident of a
// claim with no coordinates from the app. last is the location they recorded
// before (nil when none): it can be corrected until the claim is assigned.
func RecordLocation(c ClaimRecord, p AssignmentPolicy, last *IncidentLocation, assigned *Assignment,
	in LocationRequest, by Analyst, at time.Time) (IncidentLocation, error) {
	if err := assignable(c, p, assigned); err != nil {
		return IncidentLocation{}, err
	}
	if _, fromApp := AppLocation(c.Evidence); fromApp {
		return IncidentLocation{}, ErrLocationFromApp
	}

	var fields []FieldError
	bad := func(field, code string) { fields = append(fields, FieldError{Field: field, Code: code}) }
	address := strings.TrimSpace(in.Address)
	lengthOf("address", address, MinAddress, MaxAddress, bad)
	lat := coordinate("latitude", in.Latitude, validLatitude, bad)
	lon := coordinate("longitude", in.Longitude, validLongitude, bad)
	if len(fields) > 0 {
		return IncidentLocation{}, &ValidationError{Fields: fields}
	}

	seq := 1
	if last != nil {
		seq = last.Seq + 1
	}
	analyst := by
	return IncidentLocation{Source: LocationFromAnalyst, Point: Coordinates{Latitude: *lat, Longitude: *lon},
		Address: address, Seq: seq, RecordedBy: &analyst, RecordedAt: at.UTC()}, nil
}

// assignable checks that the claim can still be searched for and assigned.
func assignable(c ClaimRecord, p AssignmentPolicy, assigned *Assignment) error {
	if assigned != nil {
		return ErrAssignmentExists
	}
	if _, s, _ := p.SpecialtyFor(c); s == "" || !Assignable(c.Status) {
		return ErrClaimNotAssignable
	}
	return nil
}

// Eligibility is why a provider of the specialty can or cannot be assigned.
type Eligibility string

const (
	// Eligible is available and within the radius of the last attempt.
	Eligible Eligibility = "eligible"
	// NotEligibleUnavailable is unavailable or busy.
	NotEligibleUnavailable Eligibility = "unavailable"
	// NotEligibleOutOfRadius is available but farther than the radius.
	NotEligibleOutOfRadius Eligibility = "out_of_radius"
)

// Candidate is a provider of the specialty as the search found it: a copy, so
// the search keeps what the analyst saw.
type Candidate struct {
	ProviderID       string
	Name             string
	Specialties      []Specialty
	DistanceKm       float64
	Availability     Availability
	UnavailableUntil *time.Time
	Eligibility      Eligibility
	Suggested        bool
}

// SearchAttempt is one radius tried (criterion 4).
type SearchAttempt struct {
	RadiusKm      float64
	EligibleCount int
	AttemptedAt   time.Time
}

// SearchOutcome is how a search ended.
type SearchOutcome string

const (
	// SearchFound is a radius with eligible providers.
	SearchFound SearchOutcome = "found"
	// SearchExhausted is no eligible provider up to the maximum radius: the
	// claim is pending assignment (criterion 6).
	SearchExhausted SearchOutcome = "exhausted"
)

// ReasonNoEligibleProvider is why a claim is pending assignment.
const ReasonNoEligibleProvider = "no_eligible_provider_within_max_radius"

// MaxCandidates is how many providers a search shows, nearest first.
const MaxCandidates = 20

// SearchResult is what a search found.
type SearchResult struct {
	FromRadiusKm float64
	// RadiusKm is the radius of the last attempt: where it found, or the
	// maximum.
	RadiusKm   float64
	Outcome    SearchOutcome
	Reason     string
	Attempts   []SearchAttempt
	Candidates []Candidate
}

// Suggested is the suggested candidate; false when the search found none.
func (r SearchResult) Suggested() (Candidate, bool) {
	for _, c := range r.Candidates {
		if c.Suggested {
			return c, true
		}
	}
	return Candidate{}, false
}

// Search looks for the providers of the specialty around the point, through
// the radii of the ladder, and stops at the first radius with eligible
// providers (criteria 2 to 4, assumptions S5 and S6). A provider is eligible
// when it is available today, has the specialty and is within the radius; the
// suggested one is the nearest eligible, the lowest id on a tie. It is pure:
// the same providers give the same result, and HU-W2.3-03 uses it as it is.
func Search(at Coordinates, specialty Specialty, providers []Provider, ladder []float64, now time.Time) SearchResult {
	type measured struct {
		p Provider
		d float64
	}
	var near []measured
	for _, p := range providers {
		if p.Has(specialty) {
			near = append(near, measured{p: p, d: Distance(at, p.Location)})
		}
	}
	sort.Slice(near, func(i, j int) bool {
		if near[i].d != near[j].d {
			return near[i].d < near[j].d
		}
		return near[i].p.ID < near[j].p.ID
	})

	r := SearchResult{Outcome: SearchExhausted, Reason: ReasonNoEligibleProvider}
	if len(ladder) > 0 {
		r.FromRadiusKm = ladder[0]
	}
	for _, radius := range ladder {
		count := 0
		for _, m := range near {
			if m.p.Available() && m.d <= radius {
				count++
			}
		}
		r.RadiusKm = radius
		r.Attempts = append(r.Attempts, SearchAttempt{RadiusKm: radius, EligibleCount: count, AttemptedAt: now.UTC()})
		if count > 0 {
			r.Outcome, r.Reason = SearchFound, ""
			break
		}
	}

	suggested := false
	for _, m := range near {
		if len(r.Candidates) == MaxCandidates {
			break
		}
		c := Candidate{ProviderID: m.p.ID, Name: m.p.Name, Specialties: append([]Specialty(nil), m.p.Specialties...),
			DistanceKm: m.d, Availability: m.p.Availability, UnavailableUntil: m.p.UnavailableUntil, Eligibility: Eligible}
		switch {
		case !m.p.Available():
			c.Eligibility = NotEligibleUnavailable
		case m.d > r.RadiusKm:
			c.Eligibility = NotEligibleOutOfRadius
		case !suggested:
			c.Suggested, suggested = true, true
		}
		r.Candidates = append(r.Candidates, c)
	}
	return r
}

// SearchTrigger is who started a search.
type SearchTrigger string

const (
	SearchByAnalyst SearchTrigger = "analyst"
	// SearchByRetry is the automatic search of a claim pending assignment.
	SearchByRetry SearchTrigger = "automatic_retry"
)

// ProviderSearch is one search for the provider of a claim. Seq numbers the
// searches of the claim from 1, so two searches cannot take the same place.
type ProviderSearch struct {
	ClaimID    string
	Seq        int
	Trigger    SearchTrigger
	SearchedBy *Analyst
	Specialty  Specialty
	// Location is the location the search measured from.
	Location      IncidentLocation
	Result        SearchResult
	PolicyVersion string
	SearchedAt    time.Time
}

// SearchRequest is a search: from a radius of the policy (nil: the initial
// one), by an analyst or by the automatic retry (By nil).
type SearchRequest struct {
	FromRadiusKm *float64
	Trigger      SearchTrigger
	By           *Analyst
}

// NewSearch searches the provider of a claim at the given time. location is
// the one the claim is searched from (LocationOf), last the last search of
// the claim (nil when none) and providers the providers of its specialty.
func NewSearch(c ClaimRecord, p AssignmentPolicy, location *IncidentLocation, last *ProviderSearch,
	assigned *Assignment, providers []Provider, in SearchRequest, at time.Time) (ProviderSearch, error) {
	if err := assignable(c, p, assigned); err != nil {
		return ProviderSearch{}, err
	}
	from := p.InitialRadiusKm()
	if in.FromRadiusKm != nil {
		from = *in.FromRadiusKm
	}
	ladder, ok := p.Ladder(from)
	if !ok {
		return ProviderSearch{}, &ValidationError{Fields: []FieldError{{Field: "from_radius_km", Code: CodeOutOfRange}}}
	}
	if location == nil {
		return ProviderSearch{}, ErrLocationMissing
	}
	_, specialty, _ := p.SpecialtyFor(c)
	s := ProviderSearch{ClaimID: c.ID, Seq: 1, Trigger: in.Trigger, Specialty: specialty, Location: *location,
		Result: Search(location.Point, specialty, providers, ladder, at), PolicyVersion: p.Version,
		SearchedAt: at.UTC()}
	if in.By != nil {
		by := *in.By
		s.SearchedBy = &by
	}
	if last != nil {
		s.Seq = last.Seq + 1
	}
	return s, nil
}

// Assignment is the provider confirmed for a claim (criterion 5): who, when,
// at what distance and the location it received, as they were then.
type Assignment struct {
	ClaimID      string
	ProviderID   string
	ProviderName string
	ContactEmail string
	ContactPhone *string
	AssignedBy   Analyst
	AssignedAt   time.Time
	DistanceKm   float64
	SearchSeq    int
	// Location is the location shared with the provider.
	Location      IncidentLocation
	PolicyVersion string
}

// Assign is the analyst confirming a provider of the last search of the
// claim. current is the provider as it is now (nil when it no longer exists):
// it has to be available still. The store checks it again when it writes, so
// two claims cannot take the same provider.
func Assign(c ClaimRecord, p AssignmentPolicy, location *IncidentLocation, last *ProviderSearch,
	assigned *Assignment, providerID string, searchSeq int, current *Provider, by Analyst,
	at time.Time) (Assignment, error) {
	if err := assignable(c, p, assigned); err != nil {
		return Assignment{}, err
	}
	if last == nil || last.Seq != searchSeq || location == nil || !location.SameAs(last.Location) {
		return Assignment{}, ErrSearchOutdated
	}
	var chosen *Candidate
	for i, candidate := range last.Result.Candidates {
		if candidate.ProviderID == providerID && candidate.Eligibility == Eligible {
			chosen = &last.Result.Candidates[i]
		}
	}
	if chosen == nil {
		return Assignment{}, ErrProviderNotEligible
	}
	if current == nil || current.ID != providerID || !current.Available() {
		return Assignment{}, ErrProviderUnavailable
	}
	return Assignment{ClaimID: c.ID, ProviderID: current.ID, ProviderName: current.Name,
		ContactEmail: current.ContactEmail, ContactPhone: current.ContactPhone, AssignedBy: by, AssignedAt: at.UTC(),
		DistanceKm: chosen.DistanceKm, SearchSeq: last.Seq, Location: last.Location, PolicyVersion: last.PolicyVersion}, nil
}

// AssignmentNoticeKey is the idempotency key of the e-mail to the provider:
// one per claim (decision D12).
func AssignmentNoticeKey(claimID string) string { return "asignacion-" + claimID }

// AssignmentState is where the assignment of a claim stands.
type AssignmentState string

const (
	AssignmentNotRequired     AssignmentState = "not_required"
	AssignmentNotApplicable   AssignmentState = "not_applicable"
	AssignmentLocationMissing AssignmentState = "location_missing"
	AssignmentReady           AssignmentState = "ready"
	AssignmentProposed        AssignmentState = "proposed"
	AssignmentPending         AssignmentState = "pending_assignment"
	AssignmentAssigned        AssignmentState = "assigned"
	// AssignmentNotFound is a claim that does not exist, in a batch read.
	AssignmentNotFound AssignmentState = "not_found"
)

// AssignmentStateOf tells where the assignment of the claim stands. An
// assignment stays visible after the claim leaves evaluation; a search made
// from a location that is no longer the claim's leaves it ready.
func AssignmentStateOf(c ClaimRecord, p AssignmentPolicy, location *IncidentLocation, last *ProviderSearch,
	assigned *Assignment) AssignmentState {
	_, specialty, _ := p.SpecialtyFor(c)
	switch {
	case assigned != nil:
		return AssignmentAssigned
	case specialty == "":
		return AssignmentNotRequired
	case !Assignable(c.Status):
		return AssignmentNotApplicable
	case location == nil:
		return AssignmentLocationMissing
	case last == nil || !location.SameAs(last.Location):
		return AssignmentReady
	case last.Result.Outcome == SearchExhausted:
		return AssignmentPending
	}
	return AssignmentProposed
}
