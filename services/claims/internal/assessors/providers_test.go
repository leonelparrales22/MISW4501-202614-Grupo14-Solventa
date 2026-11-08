package assessors_test

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/assessors"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// The registry of providers that operations keeps (HU-W2.3-01, criterion 2).

func profile() domain.ProviderProfile {
	lat, lon, phone := 4.694, -74.0364, "+576015550102"
	return domain.ProviderProfile{Name: "Servicios Hogar Norte",
		Specialties: []domain.Specialty{domain.SpecialtyPlumbing, domain.SpecialtyDampProofing},
		Latitude:    &lat, Longitude: &lon, Address: "Calle 116 # 9-72, Bogotá",
		ContactEmail: "Despachos@HogarNorte.example.com", ContactPhone: &phone}
}

func newRegistry(t *testing.T, providers ...domain.Provider) (*assessors.ProviderService, *memory.ProviderStore,
	*fixedClock) {
	t.Helper()
	store, clock := memory.NewProviderStore(providers...), &fixedClock{at: start}
	return assessors.NewProviderService(store, clock), store, clock
}

func TestOperationsRegistersAProvider(t *testing.T) {
	svc, _, _ := newRegistry(t)
	p, err := svc.Create(context.Background(), profile(), analyst.Email)
	require.NoError(t, err)
	assert.Regexp(t, regexp.MustCompile(`^prv-[0-9a-f]{12}$`), p.ID)
	assert.True(t, domain.ValidProviderID(p.ID))
	assert.Equal(t, domain.AvailabilityAvailable, p.Availability)
	assert.Equal(t, "despachos@hogarnorte.example.com", p.ContactEmail)
	assert.Equal(t, analyst.Email, p.UpdatedBy)
	assert.Equal(t, start, p.UpdatedAt)

	got, err := svc.Get(context.Background(), p.ID)
	require.NoError(t, err)
	assert.Equal(t, p, got)

	_, err = svc.Create(context.Background(), domain.ProviderProfile{Name: "X"}, analyst.Email)
	var invalid *domain.ValidationError
	assert.ErrorAs(t, err, &invalid)
}

func TestTheIdOfANewProviderIsDrawnAgainWhenTaken(t *testing.T) {
	svc, _, _ := newRegistry(t, provider("prv-ocupado", 1))
	assessors.WithIDs(svc, "prv-ocupado", "prv-libre")
	p, err := svc.Create(context.Background(), profile(), analyst.Email)
	require.NoError(t, err)
	assert.Equal(t, "prv-libre", p.ID)

	assessors.WithIDs(svc, "prv-ocupado", "prv-ocupado", "prv-ocupado")
	_, err = svc.Create(context.Background(), profile(), analyst.Email)
	assert.ErrorIs(t, err, ports.ErrProviderExists)
}

func TestOperationsChangesTheProfileOfAProvider(t *testing.T) {
	svc, _, clock := newRegistry(t, unavailable(provider("prv-uno", 1)))
	clock.advance(time.Hour)
	p, err := svc.UpdateProfile(context.Background(), "prv-uno", profile(), analyst.Email)
	require.NoError(t, err)
	assert.Equal(t, "Servicios Hogar Norte", p.Name)
	assert.Equal(t, domain.Coordinates{Latitude: 4.694, Longitude: -74.0364}, p.Location)
	assert.Equal(t, domain.AvailabilityUnavailable, p.Availability, "the profile does not change the availability")
	assert.Equal(t, start.Add(time.Hour), p.UpdatedAt)

	_, err = svc.UpdateProfile(context.Background(), "prv-nadie", profile(), analyst.Email)
	assert.ErrorIs(t, err, ports.ErrProviderNotFound)
	_, err = svc.UpdateProfile(context.Background(), "prv-uno", domain.ProviderProfile{}, analyst.Email)
	var invalid *domain.ValidationError
	assert.ErrorAs(t, err, &invalid)
}

func TestOperationsSetsTheAvailabilityOfAProvider(t *testing.T) {
	busy := provider("prv-ocupado", 1)
	claimID := "cl-1"
	busy.Availability, busy.BusyClaimID = domain.AvailabilityBusy, &claimID
	svc, _, _ := newRegistry(t, busy)

	tomorrow := "2026-09-07"
	p, err := svc.SetAvailability(context.Background(), "prv-ocupado",
		domain.AvailabilityRequest{Availability: domain.AvailabilityUnavailable, UnavailableUntil: &tomorrow},
		analyst.Email)
	require.NoError(t, err)
	assert.Equal(t, domain.AvailabilityUnavailable, p.Availability)
	assert.Equal(t, tomorrow, p.UnavailableUntil.Format(time.DateOnly))
	assert.Nil(t, p.BusyClaimID)

	p, err = svc.SetAvailability(context.Background(), "prv-ocupado",
		domain.AvailabilityRequest{Availability: domain.AvailabilityAvailable}, analyst.Email)
	require.NoError(t, err)
	assert.True(t, p.Available())
	assert.Nil(t, p.UnavailableUntil)

	yesterday := "2026-09-05"
	_, err = svc.SetAvailability(context.Background(), "prv-ocupado",
		domain.AvailabilityRequest{Availability: domain.AvailabilityUnavailable, UnavailableUntil: &yesterday},
		analyst.Email)
	var invalid *domain.ValidationError
	require.ErrorAs(t, err, &invalid)
	assert.Equal(t, []domain.FieldError{{Field: "unavailable_until", Code: domain.CodeOutOfRange}}, invalid.Fields)

	_, err = svc.SetAvailability(context.Background(), "prv-ocupado",
		domain.AvailabilityRequest{Availability: domain.AvailabilityBusy}, analyst.Email)
	assert.ErrorAs(t, err, &invalid, "only the assignment makes a provider busy")

	_, err = svc.SetAvailability(context.Background(), "prv-nadie",
		domain.AvailabilityRequest{Availability: domain.AvailabilityAvailable}, analyst.Email)
	assert.ErrorIs(t, err, ports.ErrProviderNotFound)
}

func TestTheListOfProvidersIsFilteredAndByName(t *testing.T) {
	svc, _, _ := newRegistry(t, memory.ReferenceProviders()...)
	all, err := svc.List(context.Background(), ports.ProviderFilter{})
	require.NoError(t, err)
	require.Len(t, all, 8)
	for i := 1; i < len(all); i++ {
		assert.Less(t, all[i-1].Name, all[i].Name)
	}

	_, err = svc.SetAvailability(context.Background(), "prv-reparaciones-chapinero",
		domain.AvailabilityRequest{Availability: domain.AvailabilityUnavailable}, analyst.Email)
	require.NoError(t, err)
	plumbing, err := svc.List(context.Background(), ports.ProviderFilter{Specialty: domain.SpecialtyPlumbing,
		Availability: domain.AvailabilityAvailable})
	require.NoError(t, err)
	var ids []string
	for _, p := range plumbing {
		ids = append(ids, p.ID)
	}
	assert.Equal(t, []string{"prv-servicios-hogar-norte", "prv-tecnicos-unidos"}, ids)
}

func TestTheReferenceProvidersCoverEverySpecialtyWithValidProfiles(t *testing.T) {
	covered := map[domain.Specialty]bool{}
	for _, p := range memory.ReferenceProviders() {
		assert.True(t, domain.ValidProviderID(p.ID), p.ID)
		lat, lon := p.Location.Latitude, p.Location.Longitude
		in := domain.ProviderProfile{Name: p.Name, Specialties: p.Specialties, Latitude: &lat, Longitude: &lon,
			Address: p.Address, ContactEmail: p.ContactEmail, ContactPhone: p.ContactPhone}
		valid, err := in.Validate()
		require.NoError(t, err, p.ID)
		assert.Equal(t, in, valid, "%s is already normalized", p.ID)
		for _, s := range p.Specialties {
			covered[s] = true
		}
	}
	for _, s := range domain.Specialties() {
		assert.True(t, covered[s], s)
	}

	// The three of the mockup, at the distances of the screen.
	at := map[string]float64{}
	for _, p := range memory.ReferenceProviders() {
		at[p.ID] = domain.RoundKm(domain.Distance(incident, p.Location))
	}
	assert.Equal(t, 2.3, at["prv-servicios-hogar-norte"])
	assert.Equal(t, 3.8, at["prv-reparaciones-chapinero"])
	assert.Equal(t, 6.1, at["prv-tecnicos-unidos"])
}
