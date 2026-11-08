package domain

import (
	"errors"
	"net/mail"
	"regexp"
	"strings"
	"time"
)

// Specialty is the trade of a provider (HU-W2.3-01). A claim type needs one
// (see AssignmentPolicy) and a provider may have several.
type Specialty string

const (
	SpecialtyVehicleAssessment Specialty = "vehicle_assessment"
	SpecialtyAutoGlass         Specialty = "auto_glass"
	// SpecialtyPlumbing is plumbing and damp ("plomería y humedades").
	SpecialtyPlumbing     Specialty = "plumbing"
	SpecialtyDampProofing Specialty = "damp_proofing"
	SpecialtyPainting     Specialty = "painting"
	SpecialtyBodywork     Specialty = "bodywork"
)

// Specialties is every specialty of the registry. The assistance of
// HU-W2.3-03 adds its own here.
func Specialties() []Specialty {
	return []Specialty{SpecialtyVehicleAssessment, SpecialtyAutoGlass, SpecialtyPlumbing, SpecialtyDampProofing,
		SpecialtyPainting, SpecialtyBodywork}
}

// Valid reports whether s is a specialty of the registry.
func (s Specialty) Valid() bool { return contains(Specialties(), s) }

// Availability is whether a provider can take a claim.
type Availability string

const (
	// AvailabilityAvailable can take a claim today.
	AvailabilityAvailable Availability = "available"
	// AvailabilityUnavailable is set by operations, optionally until a date
	// ("disponible mañana" is unavailable until tomorrow).
	AvailabilityUnavailable Availability = "unavailable"
	// AvailabilityBusy is a provider assigned to a claim. Only the assignment
	// sets it (decision D13).
	AvailabilityBusy Availability = "busy"
)

// Valid reports whether a is one of the three.
func (a Availability) Valid() bool {
	switch a {
	case AvailabilityAvailable, AvailabilityUnavailable, AvailabilityBusy:
		return true
	}
	return false
}

// Limits of the registry of providers.
const (
	MinProviderName     = 3
	MaxProviderName     = 120
	MaxProviderSpecs    = 6
	MinAddress          = 5
	MaxAddress          = 200
	MaxContactEmail     = 254
	ProviderIDPrefix    = "prv-"
	ReferenceDataAuthor = "reference-data"
)

var (
	providerIDPattern = regexp.MustCompile(`^prv-[a-z0-9-]{3,40}$`)
	// E.164: a plus sign, the country code and up to 15 digits in all.
	phonePattern = regexp.MustCompile(`^\+[1-9][0-9]{6,14}$`)
)

// ValidProviderID reports whether id has the form of the id of a provider.
func ValidProviderID(id string) bool { return providerIDPattern.MatchString(id) }

// ErrBusyIsSetByAssignment is a change of availability to busy, which
// only the assignment makes.
var ErrBusyIsSetByAssignment = errors.New("claims: only the assignment makes a provider busy")

// Provider is a provider of the registry (criterion 2): who it is, what it
// does, where it is, whether it can take a claim and how it is reached.
type Provider struct {
	ID           string
	Name         string
	Specialties  []Specialty
	Location     Coordinates
	Address      string
	ContactEmail string
	// ContactPhone is informative; nil when there is none.
	ContactPhone *string
	Availability Availability
	// UnavailableUntil is the day (in Colombia) an unavailable provider
	// expects to be back; informative, nil when unknown.
	UnavailableUntil *time.Time
	// BusyClaimID is the claim a busy provider is assigned to.
	BusyClaimID *string
	UpdatedBy   string
	UpdatedAt   time.Time
}

// Has tells whether the provider has the specialty.
func (p Provider) Has(s Specialty) bool { return contains(p.Specialties, s) }

// Available tells whether the provider can take a claim today.
func (p Provider) Available() bool { return p.Availability == AvailabilityAvailable }

// ProviderProfile is the data of a provider that operations writes: on the
// registration and on every change of its profile. The coordinates are
// pointers so a missing one is told from zero.
type ProviderProfile struct {
	Name         string
	Specialties  []Specialty
	Latitude     *float64
	Longitude    *float64
	Address      string
	ContactEmail string
	ContactPhone *string
}

// Validate checks the profile and returns it normalized (trimmed, the e-mail
// in lower case), or a *ValidationError naming every invalid field at once.
func (r ProviderProfile) Validate() (ProviderProfile, error) {
	var fields []FieldError
	bad := func(field, code string) { fields = append(fields, FieldError{Field: field, Code: code}) }

	out := ProviderProfile{Name: strings.TrimSpace(r.Name), Address: strings.TrimSpace(r.Address),
		ContactEmail: strings.ToLower(strings.TrimSpace(r.ContactEmail))}
	lengthOf("name", out.Name, MinProviderName, MaxProviderName, bad)
	lengthOf("address", out.Address, MinAddress, MaxAddress, bad)

	switch {
	case len(r.Specialties) == 0:
		bad("specialties", CodeRequired)
	case len(r.Specialties) > MaxProviderSpecs:
		bad("specialties", CodeOutOfRange)
	default:
		for _, s := range r.Specialties {
			if !s.Valid() || contains(out.Specialties, s) {
				bad("specialties", CodeInvalidFormat)
				break
			}
			out.Specialties = append(out.Specialties, s)
		}
	}

	out.Latitude, out.Longitude = coordinate("latitude", r.Latitude, validLatitude, bad),
		coordinate("longitude", r.Longitude, validLongitude, bad)

	switch {
	case out.ContactEmail == "":
		bad("contact_email", CodeRequired)
	case len(out.ContactEmail) > MaxContactEmail || !plainEmail(out.ContactEmail):
		bad("contact_email", CodeInvalidFormat)
	}
	if r.ContactPhone != nil {
		phone := strings.TrimSpace(*r.ContactPhone)
		if !phonePattern.MatchString(phone) {
			bad("contact_phone", CodeInvalidFormat)
		}
		out.ContactPhone = &phone
	}

	if len(fields) > 0 {
		return ProviderProfile{}, &ValidationError{Fields: fields}
	}
	return out, nil
}

// NewProvider registers a provider with a validated profile: available from
// now on.
func NewProvider(id string, p ProviderProfile, by string, at time.Time) Provider {
	return Provider{ID: id, Availability: AvailabilityAvailable, UpdatedBy: by, UpdatedAt: at.UTC()}.WithProfile(p, by, at)
}

// WithProfile is the provider with a validated profile. Its availability does
// not change.
func (p Provider) WithProfile(profile ProviderProfile, by string, at time.Time) Provider {
	p.Name, p.Address, p.ContactEmail, p.ContactPhone = profile.Name, profile.Address, profile.ContactEmail,
		profile.ContactPhone
	p.Specialties = append([]Specialty(nil), profile.Specialties...)
	p.Location = Coordinates{Latitude: *profile.Latitude, Longitude: *profile.Longitude}
	p.UpdatedBy, p.UpdatedAt = by, at.UTC()
	return p
}

// AvailabilityRequest is a change of availability made by operations.
// UnavailableUntil is a date (YYYY-MM-DD), only with unavailable.
type AvailabilityRequest struct {
	Availability     Availability
	UnavailableUntil *string
}

// Validate checks the change at the given time. The date cannot be before
// today in Colombia.
func (r AvailabilityRequest) Validate(now time.Time) (Availability, *time.Time, error) {
	var fields []FieldError
	bad := func(field, code string) { fields = append(fields, FieldError{Field: field, Code: code}) }

	switch r.Availability {
	case "":
		bad("availability", CodeRequired)
	case AvailabilityAvailable, AvailabilityUnavailable:
	default:
		// busy is set only by the assignment.
		bad("availability", CodeInvalidFormat)
	}
	var until *time.Time
	if r.UnavailableUntil != nil {
		day, err := time.ParseInLocation(time.DateOnly, *r.UnavailableUntil, ColombiaTime)
		today := now.In(ColombiaTime).Format(time.DateOnly)
		switch {
		case err != nil || r.Availability == AvailabilityAvailable:
			bad("unavailable_until", CodeInvalidFormat)
		case day.Format(time.DateOnly) < today:
			bad("unavailable_until", CodeOutOfRange)
		default:
			until = &day
		}
	}
	if len(fields) > 0 {
		return "", nil, &ValidationError{Fields: fields}
	}
	return r.Availability, until, nil
}

// WithAvailability is the provider with the availability operations set.
// Setting it available releases it from the claim it was busy with (until the
// closing of the intervention does it, HU-W2.3-02).
func (p Provider) WithAvailability(a Availability, until *time.Time, by string, at time.Time) (Provider, error) {
	if a == AvailabilityBusy {
		return Provider{}, ErrBusyIsSetByAssignment
	}
	p.Availability, p.UnavailableUntil, p.BusyClaimID = a, until, nil
	if a == AvailabilityAvailable {
		p.UnavailableUntil = nil
	}
	p.UpdatedBy, p.UpdatedAt = by, at.UTC()
	return p, nil
}

func lengthOf(field, value string, minLength, maxLength int, bad func(field, code string)) {
	switch n := len([]rune(value)); {
	case n == 0:
		bad(field, CodeRequired)
	case n < minLength, n > maxLength:
		bad(field, CodeOutOfRange)
	}
}

func coordinate(field string, v *float64, valid func(float64) bool, bad func(field, code string)) *float64 {
	switch {
	case v == nil:
		bad(field, CodeRequired)
	case !valid(*v):
		bad(field, CodeOutOfRange)
	default:
		value := *v
		return &value
	}
	return nil
}

// plainEmail accepts a bare address (no display name, no angle brackets).
func plainEmail(s string) bool {
	a, err := mail.ParseAddress(s)
	return err == nil && a.Address == s && a.Name == ""
}
