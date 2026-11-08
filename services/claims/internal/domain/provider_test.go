package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
)

// The registry of providers (HU-W2.3-01, criterion 2, assumption S12).

func float(v float64) *float64 { return &v }
func text(v string) *string    { return &v }

func profile() domain.ProviderProfile {
	return domain.ProviderProfile{
		Name:         "Servicios Hogar Norte",
		Specialties:  []domain.Specialty{domain.SpecialtyPlumbing, domain.SpecialtyDampProofing},
		Latitude:     float(4.6912),
		Longitude:    float(-74.0391),
		Address:      "Calle 106 # 18-35, Bogotá",
		ContactEmail: "despachos@hogarnorte.example.com",
		ContactPhone: text("+576015550102"),
	}
}

// fieldsOf returns the field codes of a *ValidationError, or fails.
func fieldsOf(t *testing.T, err error) map[string]string {
	t.Helper()
	var v *domain.ValidationError
	require.True(t, errors.As(err, &v), "want a validation error, got %v", err)
	out := map[string]string{}
	for _, f := range v.Fields {
		out[f.Field] = f.Code
	}
	return out
}

func TestEverySpecialtyOfTheRegistryIsValid(t *testing.T) {
	for _, s := range domain.Specialties() {
		assert.True(t, s.Valid(), s)
	}
	assert.False(t, domain.Specialty("towing").Valid(), "the trades of the assistance come with HU-W2.3-03")
	assert.False(t, domain.Specialty("").Valid())
}

func TestTheAvailabilitiesOfAProvider(t *testing.T) {
	for _, a := range []domain.Availability{domain.AvailabilityAvailable, domain.AvailabilityUnavailable,
		domain.AvailabilityBusy} {
		assert.True(t, a.Valid(), a)
	}
	assert.False(t, domain.Availability("tomorrow").Valid())
	assert.True(t, domain.Provider{Availability: domain.AvailabilityAvailable}.Available())
	assert.False(t, domain.Provider{Availability: domain.AvailabilityBusy}.Available())
	assert.False(t, domain.Provider{Availability: domain.AvailabilityUnavailable}.Available())
}

func TestTheIdOfAProvider(t *testing.T) {
	for _, id := range []string{"prv-servicios-hogar-norte", "prv-abc", "prv-" + strings.Repeat("a", 40),
		"prv-8f14e45f-ceea-467a-9f3a-5a2d0b1c7e90"} {
		assert.True(t, domain.ValidProviderID(id), id)
	}
	for _, id := range []string{"", "prv-ab", "prv-" + strings.Repeat("a", 41), "PRV-abc", "prv-ABC", "abc",
		"prv-../x", "prv-a b"} {
		assert.False(t, domain.ValidProviderID(id), id)
	}
}

func TestAValidProfileIsNormalized(t *testing.T) {
	in := profile()
	in.Name, in.ContactEmail = "  Servicios Hogar Norte ", " Despachos@HogarNorte.example.com "
	out, err := in.Validate()
	require.NoError(t, err)
	assert.Equal(t, "Servicios Hogar Norte", out.Name)
	assert.Equal(t, "despachos@hogarnorte.example.com", out.ContactEmail)
	assert.Equal(t, 4.6912, *out.Latitude)

	in.ContactPhone = nil
	out, err = in.Validate()
	require.NoError(t, err)
	assert.Nil(t, out.ContactPhone, "the phone is optional")
}

func TestEveryInvalidFieldOfAProfileIsReportedAtOnce(t *testing.T) {
	_, err := domain.ProviderProfile{}.Validate()
	fields := fieldsOf(t, err)
	assert.Equal(t, map[string]string{"name": "required", "address": "required", "specialties": "required",
		"latitude": "required", "longitude": "required", "contact_email": "required"}, fields)

	in := profile()
	in.Name, in.Address = "ab", strings.Repeat("a", 201)
	in.Latitude, in.Longitude = float(90.01), float(-180.01)
	in.ContactEmail, in.ContactPhone = "Hogar Norte <despachos@hogarnorte.example.com>", text("3015550102")
	in.Specialties = []domain.Specialty{domain.SpecialtyPlumbing, domain.SpecialtyPlumbing}
	_, err = in.Validate()
	fields = fieldsOf(t, err)
	assert.Equal(t, map[string]string{"name": "out_of_range", "address": "out_of_range", "specialties": "invalid_format",
		"latitude": "out_of_range", "longitude": "out_of_range", "contact_email": "invalid_format",
		"contact_phone": "invalid_format"}, fields)
}

func TestTheLimitsOfAProfile(t *testing.T) {
	cases := []struct {
		name   string
		change func(*domain.ProviderProfile)
		field  string
		code   string
	}{
		{"name of 3", func(p *domain.ProviderProfile) { p.Name = "abc" }, "", ""},
		{"name of 120", func(p *domain.ProviderProfile) { p.Name = strings.Repeat("ñ", 120) }, "", ""},
		{"name of 121", func(p *domain.ProviderProfile) { p.Name = strings.Repeat("ñ", 121) }, "name", "out_of_range"},
		{"address of 4", func(p *domain.ProviderProfile) { p.Address = "abcd" }, "address", "out_of_range"},
		{"address of 5", func(p *domain.ProviderProfile) { p.Address = "abcde" }, "", ""},
		{"six specialties", func(p *domain.ProviderProfile) { p.Specialties = domain.Specialties() }, "", ""},
		{"seven specialties", func(p *domain.ProviderProfile) {
			p.Specialties = append(domain.Specialties(), domain.SpecialtyPlumbing)
		}, "specialties", "out_of_range"},
		{"unknown specialty", func(p *domain.ProviderProfile) { p.Specialties = []domain.Specialty{"towing"} },
			"specialties", "invalid_format"},
		{"poles", func(p *domain.ProviderProfile) { p.Latitude, p.Longitude = float(-90), float(180) }, "", ""},
		{"e-mail of 254", func(p *domain.ProviderProfile) {
			p.ContactEmail = strings.Repeat("a", 64) + "@" + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." +
				strings.Repeat("d", 61)
		}, "", ""},
		{"e-mail without domain", func(p *domain.ProviderProfile) { p.ContactEmail = "despachos" }, "contact_email",
			"invalid_format"},
		{"phone of 7 digits", func(p *domain.ProviderProfile) { p.ContactPhone = text("+5712345") }, "", ""},
		{"phone of 16 digits", func(p *domain.ProviderProfile) { p.ContactPhone = text("+5760155501020000") },
			"contact_phone", "invalid_format"},
	}
	for _, c := range cases {
		in := profile()
		c.change(&in)
		_, err := in.Validate()
		if c.field == "" {
			assert.NoError(t, err, c.name)
			continue
		}
		assert.Equal(t, map[string]string{c.field: c.code}, fieldsOf(t, err), c.name)
	}
}

func TestANewProviderIsAvailable(t *testing.T) {
	at := time.Date(2026, 11, 7, 15, 0, 0, 0, time.FixedZone("UTC-5", -5*3600))
	in, err := profile().Validate()
	require.NoError(t, err)
	p := domain.NewProvider("prv-servicios-hogar-norte", in, "andres.ruiz@solventa.example", at)
	assert.Equal(t, domain.AvailabilityAvailable, p.Availability)
	assert.Equal(t, domain.Coordinates{Latitude: 4.6912, Longitude: -74.0391}, p.Location)
	assert.Equal(t, "andres.ruiz@solventa.example", p.UpdatedBy)
	assert.Equal(t, at.UTC(), p.UpdatedAt)

	in.Specialties[0] = domain.SpecialtyPainting
	assert.Equal(t, domain.SpecialtyPlumbing, p.Specialties[0], "the provider keeps its own copy")
}

func TestAChangeOfProfileKeepsTheAvailability(t *testing.T) {
	in, err := profile().Validate()
	require.NoError(t, err)
	claim := "8f14e45f-ceea-467a-9f3a-5a2d0b1c7e90"
	p := domain.Provider{ID: "prv-servicios-hogar-norte", Availability: domain.AvailabilityBusy, BusyClaimID: &claim}
	changed := p.WithProfile(in, "andres.ruiz@solventa.example", time.Now())
	assert.Equal(t, domain.AvailabilityBusy, changed.Availability)
	assert.Equal(t, &claim, changed.BusyClaimID)
	assert.Equal(t, "Servicios Hogar Norte", changed.Name)
}

func TestTheChangeOfAvailability(t *testing.T) {
	// 23:30 in Colombia is already the 8th in UTC: today is the 7th.
	now := time.Date(2026, 11, 8, 4, 30, 0, 0, time.UTC)
	cases := []struct {
		name  string
		in    domain.AvailabilityRequest
		want  domain.Availability
		until string
		field string
		code  string
	}{
		{"available", domain.AvailabilityRequest{Availability: domain.AvailabilityAvailable}, domain.AvailabilityAvailable,
			"", "", ""},
		{"unavailable", domain.AvailabilityRequest{Availability: domain.AvailabilityUnavailable},
			domain.AvailabilityUnavailable, "", "", ""},
		{"until today", domain.AvailabilityRequest{Availability: domain.AvailabilityUnavailable,
			UnavailableUntil: text("2026-11-07")}, domain.AvailabilityUnavailable, "2026-11-07", "", ""},
		{"until yesterday", domain.AvailabilityRequest{Availability: domain.AvailabilityUnavailable,
			UnavailableUntil: text("2026-11-06")}, "", "", "unavailable_until", "out_of_range"},
		{"a date that is not one", domain.AvailabilityRequest{Availability: domain.AvailabilityUnavailable,
			UnavailableUntil: text("22/09/2026")}, "", "", "unavailable_until", "invalid_format"},
		{"a date with available", domain.AvailabilityRequest{Availability: domain.AvailabilityAvailable,
			UnavailableUntil: text("2026-11-09")}, "", "", "unavailable_until", "invalid_format"},
		{"busy", domain.AvailabilityRequest{Availability: domain.AvailabilityBusy}, "", "", "availability",
			"invalid_format"},
		{"none", domain.AvailabilityRequest{}, "", "", "availability", "required"},
	}
	for _, c := range cases {
		got, until, err := c.in.Validate(now)
		if c.field != "" {
			assert.Equal(t, map[string]string{c.field: c.code}, fieldsOf(t, err), c.name)
			continue
		}
		require.NoError(t, err, c.name)
		assert.Equal(t, c.want, got, c.name)
		if c.until == "" {
			assert.Nil(t, until, c.name)
			continue
		}
		assert.Equal(t, c.until, until.Format(time.DateOnly), c.name)
	}
}

func TestSettingABusyProviderAvailableReleasesIt(t *testing.T) {
	claim := "8f14e45f-ceea-467a-9f3a-5a2d0b1c7e90"
	at := time.Date(2026, 11, 7, 20, 0, 0, 0, time.UTC)
	busy := domain.Provider{ID: "prv-servicios-hogar-norte", Availability: domain.AvailabilityBusy, BusyClaimID: &claim}

	p, err := busy.WithAvailability(domain.AvailabilityAvailable, nil, "andres.ruiz@solventa.example", at)
	require.NoError(t, err)
	assert.Equal(t, domain.AvailabilityAvailable, p.Availability)
	assert.Nil(t, p.BusyClaimID)
	assert.Equal(t, at, p.UpdatedAt)

	tomorrow := time.Date(2026, 11, 8, 0, 0, 0, 0, domain.ColombiaTime)
	p, err = p.WithAvailability(domain.AvailabilityUnavailable, &tomorrow, "andres.ruiz@solventa.example", at)
	require.NoError(t, err)
	assert.Equal(t, &tomorrow, p.UnavailableUntil)

	p, err = p.WithAvailability(domain.AvailabilityAvailable, &tomorrow, "andres.ruiz@solventa.example", at)
	require.NoError(t, err)
	assert.Nil(t, p.UnavailableUntil, "an available provider has no date of return")

	_, err = p.WithAvailability(domain.AvailabilityBusy, nil, "andres.ruiz@solventa.example", at)
	assert.ErrorIs(t, err, domain.ErrBusyIsSetByAssignment)
}
