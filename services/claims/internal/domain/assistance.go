package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// The request for on-site assistance from the phone (HU-M2.3-01). It is not a
// claim: it has its own number, its own table and it never enters the review
// nor the triage (decision D6). The inbox of operations that attends it is
// HU-W2.3-03.

// LocationStatus is the result of the location the phone reports: it shared
// a fix, or it could not.
type LocationStatus string

const (
	LocationSent        LocationStatus = "sent"
	LocationUnavailable LocationStatus = "unavailable"
)

// Valid reports whether the status is one of the two the contract names.
func (s LocationStatus) Valid() bool { return s == LocationSent || s == LocationUnavailable }

// LocationUnavailableReason is why the phone shared no location (criterion 4).
type LocationUnavailableReason string

const (
	// UnavailablePermissionDenied is the insured denying the permission.
	UnavailablePermissionDenied LocationUnavailableReason = "permission_denied"
	// UnavailableLocationDisabled is the location service of the phone off.
	UnavailableLocationDisabled LocationUnavailableReason = "location_disabled"
	// UnavailableNoFix is no fix in time, or only one older than 60 seconds.
	UnavailableNoFix LocationUnavailableReason = "no_fix"
)

// Valid reports whether the reason is one of the three of criterion 4.
func (r LocationUnavailableReason) Valid() bool {
	switch r {
	case UnavailablePermissionDenied, UnavailableLocationDisabled, UnavailableNoFix:
		return true
	}
	return false
}

// AssistanceStatus is where a request is in the inbox of operations. This
// story only writes AssistanceNew; in_call, dispatched and closed come with
// HU-W2.3-03 (assumption S8).
type AssistanceStatus string

const AssistanceNew AssistanceStatus = "new"

// Limits of a request for assistance.
const (
	// MaxAccuracyM is the widest radius of a fix the request takes, in meters.
	MaxAccuracyM = 10000
	// MaxFixClockSkew is how far in the future the instant of a fix may be:
	// the clock of a phone drifts (assumption S4).
	MaxFixClockSkew = 5 * time.Minute
)

// Why a policy does not take a request for assistance (assumption S2). A
// policy that is found but not in force is ErrPolicyNotActive.
var ErrPolicyNotFound = errors.New("claims: the policy is not one of the insured's")

// AssistanceLocation is the result of the location as it is recorded. With
// LocationSent, Coordinates, AccuracyM and CapturedAt are set and
// UnavailableReason is empty; with LocationUnavailable it is the other way
// round.
type AssistanceLocation struct {
	Status            LocationStatus
	Coordinates       *Coordinates
	AccuracyM         *float64
	CapturedAt        *time.Time
	UnavailableReason LocationUnavailableReason
}

// AssistanceLocationInput is the location as it came in the request. The
// pointers tell a field that was not sent from a zero: latitude 0 is a place.
type AssistanceLocationInput struct {
	Status            string
	Latitude          *float64
	Longitude         *float64
	AccuracyM         *float64
	CapturedAt        *string
	UnavailableReason *string
}

// AssistanceRequestInput is what the phone sends. Who requests is the
// subject of the token, never the body.
type AssistanceRequestInput struct {
	PolicyID string
	Location *AssistanceLocationInput
	Place    *string
}

// AssistanceDraft is a valid request before the policy is checked.
type AssistanceDraft struct {
	PolicyID string
	Location AssistanceLocation
	// Place is the readable address the phone sent, trimmed; nil when it sent
	// none.
	Place *string
}

// AssistancePolicy is the copy of the policy kept with the request, as the
// policy core described it when the request was recorded (assumption S7).
type AssistancePolicy struct {
	ID            string
	Line          PolicyLine
	Product       string
	InsuredObject string
}

// AssistancePolicyOf takes the copy of the policy a request keeps.
func AssistancePolicyOf(p Policy) AssistancePolicy {
	return AssistancePolicy{ID: p.ID, Line: p.Line, Product: p.Product, InsuredObject: p.InsuredObject}
}

// AssistanceRequest is a recorded request for assistance. The store assigns
// Number.
type AssistanceRequest struct {
	ID     string
	Number string
	// Subject is the "sub" of the token: who requested it.
	Subject string
	// RequesterName is the name of the token, empty when it has none. Only
	// the inbox of operations reads it (HU-W2.3-03).
	RequesterName string
	// Key is the Idempotency-Key and Fingerprint what was sent with it, so a
	// retry is told from another request (assumption S6).
	Key         string
	Fingerprint string
	Policy      AssistancePolicy
	Location    AssistanceLocation
	Place       *string
	Status      AssistanceStatus
	// RequestedAt is the clock of the server when it was recorded, in UTC.
	RequestedAt time.Time
}

// AssistanceNumber is the number the insured sees: AST-<year>-<4 digits or
// more>, from a sequence of its own.
func AssistanceNumber(at time.Time, sequence int64) string {
	return fmt.Sprintf("AST-%d-%04d", at.UTC().Year(), sequence)
}

// Validate checks the request at the given time and returns the draft it
// describes, or a *ValidationError naming every invalid field at once.
func (in AssistanceRequestInput) Validate(now time.Time) (AssistanceDraft, error) {
	var fields []FieldError
	bad := func(field, code string) { fields = append(fields, FieldError{Field: field, Code: code}) }

	policyID := strings.TrimSpace(in.PolicyID)
	switch {
	case policyID == "":
		bad("policy_id", CodeRequired)
	case !policyIDPattern.MatchString(policyID):
		bad("policy_id", CodeInvalidFormat)
	}

	var location AssistanceLocation
	if in.Location == nil {
		bad("location", CodeRequired)
	} else {
		location = in.Location.validate(now, bad)
	}

	place := assistancePlace(in.Place, bad)

	if len(fields) > 0 {
		return AssistanceDraft{}, &ValidationError{Fields: fields}
	}
	return AssistanceDraft{PolicyID: policyID, Location: location, Place: place}, nil
}

func (in AssistanceLocationInput) validate(now time.Time, bad func(field, code string)) AssistanceLocation {
	status := LocationStatus(strings.TrimSpace(in.Status))
	switch {
	case status == "":
		bad("location.status", CodeRequired)
		return AssistanceLocation{}
	case !status.Valid():
		bad("location.status", CodeInvalidFormat)
		return AssistanceLocation{}
	case status == LocationUnavailable:
		return in.unavailable(bad)
	}
	return in.sent(now, bad)
}

// sent checks a location the phone shared: every field of the fix, and no
// reason.
func (in AssistanceLocationInput) sent(now time.Time, bad func(field, code string)) AssistanceLocation {
	coordinates := Coordinates{}
	switch {
	case in.Latitude == nil:
		bad("location.latitude", CodeRequired)
	case !validLatitude(*in.Latitude): // NaN and infinities too
		bad("location.latitude", CodeOutOfRange)
	default:
		coordinates.Latitude = *in.Latitude
	}
	switch {
	case in.Longitude == nil:
		bad("location.longitude", CodeRequired)
	case !validLongitude(*in.Longitude):
		bad("location.longitude", CodeOutOfRange)
	default:
		coordinates.Longitude = *in.Longitude
	}

	var accuracy float64
	switch {
	case in.AccuracyM == nil:
		bad("location.accuracy_m", CodeRequired)
	case !(*in.AccuracyM > 0 && *in.AccuracyM <= MaxAccuracyM):
		// NaN fails both comparisons, so it lands here too.
		bad("location.accuracy_m", CodeOutOfRange)
	default:
		accuracy = *in.AccuracyM
	}

	captured := fixInstant(in.CapturedAt, now, bad)

	if in.UnavailableReason != nil {
		bad("location.unavailable_reason", CodeNotApplicable)
	}
	return AssistanceLocation{Status: LocationSent, Coordinates: &coordinates, AccuracyM: &accuracy, CapturedAt: &captured}
}

// unavailable checks a location the phone could not share: its reason, and
// none of the fields of a fix.
func (in AssistanceLocationInput) unavailable(bad func(field, code string)) AssistanceLocation {
	var reason LocationUnavailableReason
	if in.UnavailableReason != nil {
		reason = LocationUnavailableReason(strings.TrimSpace(*in.UnavailableReason))
	}
	switch {
	case reason == "":
		bad("location.unavailable_reason", CodeRequired)
	case !reason.Valid():
		bad("location.unavailable_reason", CodeInvalidFormat)
	}
	for _, f := range []struct {
		name string
		sent bool
	}{
		{"location.latitude", in.Latitude != nil},
		{"location.longitude", in.Longitude != nil},
		{"location.accuracy_m", in.AccuracyM != nil},
		{"location.captured_at", in.CapturedAt != nil},
	} {
		if f.sent {
			bad(f.name, CodeNotApplicable)
		}
	}
	return AssistanceLocation{Status: LocationUnavailable, UnavailableReason: reason}
}

// fixInstant parses the instant of the fix. It may not be more than
// MaxFixClockSkew in the future; an old fix is not rejected here, the app
// sends no_fix instead (assumption S4).
func fixInstant(value *string, now time.Time, bad func(field, code string)) time.Time {
	if value == nil || strings.TrimSpace(*value) == "" {
		bad("location.captured_at", CodeRequired)
		return time.Time{}
	}
	at, err := time.Parse(time.RFC3339, strings.TrimSpace(*value))
	if err != nil {
		bad("location.captured_at", CodeInvalidFormat)
		return time.Time{}
	}
	// The database keeps microseconds: the answer of the request and its
	// later reads say the same instant.
	at = at.UTC().Truncate(time.Microsecond)
	if at.After(now.Add(MaxFixClockSkew)) {
		bad("location.captured_at", CodeOutOfRange)
	}
	return at
}

// assistancePlace trims the optional address. Sent, it must have between
// MinPlace and MaxPlace characters: only spaces is too short.
func assistancePlace(value *string, bad func(field, code string)) *string {
	if value == nil {
		return nil
	}
	place := strings.TrimSpace(*value)
	if n := len([]rune(place)); n < MinPlace || n > MaxPlace {
		bad("place", CodeOutOfRange)
		return nil
	}
	return &place
}

// Fingerprint summarizes what was sent. Two requests with the same key are
// the same request only when their fingerprints match (assumption S6).
func (d AssistanceDraft) Fingerprint() string {
	var b strings.Builder
	b.WriteString("policy_id=" + d.PolicyID + "\n")
	b.WriteString("location.status=" + string(d.Location.Status) + "\n")
	if c := d.Location.Coordinates; c != nil {
		b.WriteString("location.latitude=" + formatFloat(c.Latitude) + "\n")
		b.WriteString("location.longitude=" + formatFloat(c.Longitude) + "\n")
	}
	if a := d.Location.AccuracyM; a != nil {
		b.WriteString("location.accuracy_m=" + formatFloat(*a) + "\n")
	}
	if at := d.Location.CapturedAt; at != nil {
		b.WriteString("location.captured_at=" + at.UTC().Format(time.RFC3339Nano) + "\n")
	}
	if d.Location.UnavailableReason != "" {
		b.WriteString("location.unavailable_reason=" + string(d.Location.UnavailableReason) + "\n")
	}
	if d.Place != nil {
		b.WriteString("place=" + *d.Place + "\n")
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

func formatFloat(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }

// EligibleForAssistance finds the policy among the insured's own and checks
// that it is in force now (assumption S2). Any line takes a request: the core
// does not say which products include assistance, and the call confirms it.
// A policy of someone else is ErrPolicyNotFound, as one that does not exist;
// one that is found but not in force is returned with ErrPolicyNotActive, so
// the answer can say its validity.
func EligibleForAssistance(policies []Policy, policyID string, now time.Time) (Policy, error) {
	for _, p := range policies {
		if p.ID != policyID {
			continue
		}
		p = p.Copy()
		if !p.ActiveAt(now) {
			return p, ErrPolicyNotActive
		}
		return p, nil
	}
	return Policy{}, ErrPolicyNotFound
}
