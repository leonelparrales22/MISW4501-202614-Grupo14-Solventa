package memory

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// ProviderStore is the registry of providers in memory (HU-W2.3-01), with the
// same rules as PostgreSQL. Everything is lost when the service restarts.
type ProviderStore struct {
	mu   sync.Mutex
	byID map[string]domain.Provider
}

// NewProviderStore builds the registry with the providers given.
func NewProviderStore(providers ...domain.Provider) *ProviderStore {
	s := &ProviderStore{byID: map[string]domain.Provider{}}
	for _, p := range providers {
		s.byID[p.ID] = copyProvider(p)
	}
	return s
}

var _ ports.ProviderStore = (*ProviderStore)(nil)

// referenceDataLoadedAt is when the reference providers were loaded.
var referenceDataLoadedAt = time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)

// ReferenceProviders are the providers of the migration 0009_provider_seed
// (assumption S13), so the service without a database has the same registry.
func ReferenceProviders() []domain.Provider {
	phone := func(s string) *string { return &s }
	provider := func(id, name string, lat, lon float64, address, email string, tel *string,
		specialties ...domain.Specialty) domain.Provider {
		return domain.Provider{ID: id, Name: name, Specialties: specialties,
			Location: domain.Coordinates{Latitude: lat, Longitude: lon}, Address: address, ContactEmail: email,
			ContactPhone: tel, Availability: domain.AvailabilityAvailable, UpdatedBy: domain.ReferenceDataAuthor,
			UpdatedAt: referenceDataLoadedAt}
	}
	return []domain.Provider{
		provider("prv-servicios-hogar-norte", "Servicios Hogar Norte", 4.694, -74.0364, "Calle 116 # 9-72, Bogotá",
			"despachos@hogarnorte.example.com", phone("+576015550102"),
			domain.SpecialtyPlumbing, domain.SpecialtyDampProofing),
		provider("prv-reparaciones-chapinero", "Reparaciones Chapinero", 4.645, -74.06, "Calle 63 # 13-20, Bogotá",
			"servicio@reparacioneschapinero.example.com", phone("+576015550103"), domain.SpecialtyPlumbing),
		provider("prv-tecnicos-unidos", "Técnicos Unidos", 4.7246, -74.0758, "Avenida Suba # 115-58, Bogotá",
			"operaciones@tecnicosunidos.example.com", nil,
			domain.SpecialtyPlumbing, domain.SpecialtyDampProofing, domain.SpecialtyPainting),
		provider("prv-peritos-autos-andinos", "Peritos Autos Andinos", 4.665, -74.07, "Carrera 30 # 75-40, Bogotá",
			"peritajes@autosandinos.example.com", phone("+576015550104"),
			domain.SpecialtyVehicleAssessment, domain.SpecialtyBodywork),
		provider("prv-peritaje-express-sur", "Peritaje Express Sur", 4.61, -74.145,
			"Avenida Boyacá # 38-20 Sur, Bogotá", "citas@peritajeexpress.example.com", phone("+576015550105"),
			domain.SpecialtyVehicleAssessment),
		provider("prv-vidrios-capital", "Vidrios Capital", 4.639, -74.083, "Calle 45 # 20-15, Bogotá",
			"servicio@vidrioscapital.example.com", phone("+576015550106"), domain.SpecialtyAutoGlass),
		provider("prv-latoneria-pintura-la-80", "Latonería y Pintura La 80", 4.69, -74.087, "Calle 80 # 68-30, Bogotá",
			"taller@latonerialaochenta.example.com", phone("+576015550107"),
			domain.SpecialtyBodywork, domain.SpecialtyPainting),
		provider("prv-humedades-y-acabados", "Humedades y Acabados", 4.71, -74.03, "Calle 140 # 11-45, Bogotá",
			"contacto@humedadesyacabados.example.com", phone("+576015550108"),
			domain.SpecialtyDampProofing, domain.SpecialtyPainting),
	}
}

// WithSpecialty returns the providers with the specialty, by id.
func (s *ProviderStore) WithSpecialty(_ context.Context, sp domain.Specialty) ([]domain.Provider, error) {
	return s.matching(func(p domain.Provider) bool { return p.Has(sp) }, byID, 0), nil
}

// List returns the providers of the filter by name.
func (s *ProviderStore) List(_ context.Context, f ports.ProviderFilter, limit int) ([]domain.Provider, error) {
	return s.matching(func(p domain.Provider) bool {
		return (f.Specialty == "" || p.Has(f.Specialty)) && (f.Availability == "" || p.Availability == f.Availability)
	}, byName, limit), nil
}

func byID(a, b domain.Provider) bool { return a.ID < b.ID }

func byName(a, b domain.Provider) bool {
	if a.Name != b.Name {
		return a.Name < b.Name
	}
	return a.ID < b.ID
}

// matching returns copies of the providers that match, in order, up to limit
// (0: all).
func (s *ProviderStore) matching(match func(domain.Provider) bool, less func(a, b domain.Provider) bool,
	limit int) []domain.Provider {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.Provider
	for _, p := range s.byID {
		if match(p) {
			out = append(out, copyProvider(p))
		}
	}
	sort.Slice(out, func(i, j int) bool { return less(out[i], out[j]) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Get returns the provider.
func (s *ProviderStore) Get(_ context.Context, id string) (domain.Provider, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.byID[id]
	if !ok {
		return domain.Provider{}, ports.ErrProviderNotFound
	}
	return copyProvider(p), nil
}

// Create registers the provider.
func (s *ProviderStore) Create(_ context.Context, p domain.Provider) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, taken := s.byID[p.ID]; taken {
		return ports.ErrProviderExists
	}
	s.byID[p.ID] = copyProvider(p)
	return nil
}

// UpdateProfile replaces the profile of the provider.
func (s *ProviderStore) UpdateProfile(_ context.Context, id string, profile domain.ProviderProfile, by string,
	at time.Time) (domain.Provider, error) {
	return s.change(id, func(p domain.Provider) (domain.Provider, error) { return p.WithProfile(profile, by, at), nil })
}

// SetAvailability sets the availability of the provider.
func (s *ProviderStore) SetAvailability(_ context.Context, id string, a domain.Availability, until *time.Time,
	by string, at time.Time) (domain.Provider, error) {
	return s.change(id, func(p domain.Provider) (domain.Provider, error) { return p.WithAvailability(a, until, by, at) })
}

func (s *ProviderStore) change(id string, apply func(domain.Provider) (domain.Provider, error)) (domain.Provider, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.byID[id]
	if !ok {
		return domain.Provider{}, ports.ErrProviderNotFound
	}
	p, err := apply(copyProvider(p))
	if err != nil {
		return domain.Provider{}, err
	}
	s.byID[id] = copyProvider(p)
	return copyProvider(p), nil
}

// occupy makes the provider busy with the claim if it is available. The
// caller holds no lock of s.
func (s *ProviderStore) occupy(id, claimID, by string, at time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.byID[id]
	if !ok || !p.Available() {
		return false
	}
	p.Availability, p.UnavailableUntil, p.BusyClaimID = domain.AvailabilityBusy, nil, &claimID
	p.UpdatedBy, p.UpdatedAt = by, at.UTC()
	s.byID[id] = p
	return true
}

// copyProvider returns a provider that shares no slice nor pointer with p.
func copyProvider(p domain.Provider) domain.Provider {
	p.Specialties = append([]domain.Specialty(nil), p.Specialties...)
	p.ContactPhone = copyString(p.ContactPhone)
	p.BusyClaimID = copyString(p.BusyClaimID)
	if p.UnavailableUntil != nil {
		t := *p.UnavailableUntil
		p.UnavailableUntil = &t
	}
	return p
}

func copyString(s *string) *string {
	if s == nil {
		return nil
	}
	v := *s
	return &v
}
