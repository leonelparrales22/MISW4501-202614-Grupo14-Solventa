package postgres_test

import (
	"context"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/migrations"
)

// The registry of providers in PostgreSQL (HU-W2.3-01): the reference data of
// 0009_provider_seed, and what operations changes in it.

func newProviderStore(t *testing.T) (*postgres.ProviderStore, *assignmentStores) {
	t.Helper()
	s := newAssignmentStores(t)
	return s.providers, &s
}

func TestTheReferenceProvidersAreTheSameWithAndWithoutADatabase(t *testing.T) {
	store, _ := newProviderStore(t)
	got, err := store.List(context.Background(), ports.ProviderFilter{}, 200)
	require.NoError(t, err)

	want := map[string]domain.Provider{}
	for _, p := range memory.ReferenceProviders() {
		want[p.ID] = p
	}
	require.Len(t, got, len(want))
	for _, p := range got {
		assert.Equal(t, want[p.ID], p, p.ID)
	}
}

func TestARegisteredProviderReadsBack(t *testing.T) {
	store, _ := newProviderStore(t)
	phone := "+576015550199"
	p := domain.Provider{ID: "prv-nuevo-prestador", Name: "Nuevo Prestador",
		Specialties: []domain.Specialty{domain.SpecialtyPainting, domain.SpecialtyBodywork},
		Location:    domain.Coordinates{Latitude: 4.70123456, Longitude: -74.04987654}, Address: "Calle 100 # 15-20, Bogotá",
		ContactEmail: "nuevo@example.com", ContactPhone: &phone, Availability: domain.AvailabilityAvailable,
		UpdatedBy: assignedBy.Email, UpdatedAt: searchStart}
	require.NoError(t, store.Create(context.Background(), p))
	got, err := store.Get(context.Background(), p.ID)
	require.NoError(t, err)
	assert.Equal(t, p, got)

	assert.ErrorIs(t, store.Create(context.Background(), p), ports.ErrProviderExists)
	_, err = store.Get(context.Background(), "prv-nadie")
	assert.ErrorIs(t, err, ports.ErrProviderNotFound)
}

func TestOperationsChangesAProviderInPostgreSQL(t *testing.T) {
	store, _ := newProviderStore(t)
	lat, lon := 4.7, -74.05
	at := searchStart.Add(time.Hour)
	p, err := store.UpdateProfile(context.Background(), "prv-tecnicos-unidos", domain.ProviderProfile{
		Name: "Técnicos Unidos SAS", Specialties: []domain.Specialty{domain.SpecialtyPlumbing}, Latitude: &lat,
		Longitude: &lon, Address: "Calle 127 # 7-10, Bogotá", ContactEmail: "turnos@tecnicosunidos.example.com"},
		assignedBy.Email, at)
	require.NoError(t, err)
	assert.Equal(t, "Técnicos Unidos SAS", p.Name)
	assert.Equal(t, []domain.Specialty{domain.SpecialtyPlumbing}, p.Specialties)
	assert.Equal(t, domain.Coordinates{Latitude: lat, Longitude: lon}, p.Location)
	assert.Nil(t, p.ContactPhone)
	assert.Equal(t, domain.AvailabilityAvailable, p.Availability)
	assert.Equal(t, assignedBy.Email, p.UpdatedBy)
	assert.Equal(t, at, p.UpdatedAt)

	until := time.Date(2026, 10, 30, 0, 0, 0, 0, domain.ColombiaTime)
	p, err = store.SetAvailability(context.Background(), "prv-tecnicos-unidos", domain.AvailabilityUnavailable, &until,
		assignedBy.Email, at)
	require.NoError(t, err)
	assert.Equal(t, domain.AvailabilityUnavailable, p.Availability)
	assert.Equal(t, until, *p.UnavailableUntil)

	p, err = store.SetAvailability(context.Background(), "prv-tecnicos-unidos", domain.AvailabilityAvailable, &until,
		assignedBy.Email, at)
	require.NoError(t, err)
	assert.Nil(t, p.UnavailableUntil, "an available provider has no date of return")

	_, err = store.SetAvailability(context.Background(), "prv-tecnicos-unidos", domain.AvailabilityBusy, nil,
		assignedBy.Email, at)
	assert.ErrorIs(t, err, domain.ErrBusyIsSetByAssignment)
	_, err = store.SetAvailability(context.Background(), "prv-nadie", domain.AvailabilityAvailable, nil,
		assignedBy.Email, at)
	assert.ErrorIs(t, err, ports.ErrProviderNotFound)
	_, err = store.UpdateProfile(context.Background(), "prv-nadie", domain.ProviderProfile{Name: "Nadie",
		Specialties: []domain.Specialty{domain.SpecialtyPlumbing}, Latitude: &lat, Longitude: &lon,
		Address: "Calle 1 # 1-1", ContactEmail: "nadie@example.com"}, assignedBy.Email, at)
	assert.ErrorIs(t, err, ports.ErrProviderNotFound)
}

func TestTheProvidersAreListedByFilter(t *testing.T) {
	store, _ := newProviderStore(t)
	until := time.Date(2026, 10, 30, 0, 0, 0, 0, domain.ColombiaTime)
	_, err := store.SetAvailability(context.Background(), "prv-reparaciones-chapinero", domain.AvailabilityUnavailable,
		&until, assignedBy.Email, searchStart)
	require.NoError(t, err)

	ids := func(f ports.ProviderFilter, limit int) []string {
		list, err := store.List(context.Background(), f, limit)
		require.NoError(t, err)
		var out []string
		for _, p := range list {
			out = append(out, p.ID)
		}
		return out
	}
	assert.Equal(t, []string{"prv-servicios-hogar-norte", "prv-tecnicos-unidos"},
		ids(ports.ProviderFilter{Specialty: domain.SpecialtyPlumbing, Availability: domain.AvailabilityAvailable}, 200))
	assert.Equal(t, []string{"prv-reparaciones-chapinero"},
		ids(ports.ProviderFilter{Availability: domain.AvailabilityUnavailable}, 200))
	assert.Equal(t, []string{"prv-humedades-y-acabados", "prv-latoneria-pintura-la-80"},
		ids(ports.ProviderFilter{}, 2), "by name, up to the limit")

	plumbing, err := store.WithSpecialty(context.Background(), domain.SpecialtyPlumbing)
	require.NoError(t, err)
	assert.Len(t, plumbing, 3, "whatever their availability")
}

func TestTheSeedDoesNotOverwriteWhatOperationsEdited(t *testing.T) {
	store, s := newProviderStore(t)
	_, err := store.SetAvailability(context.Background(), "prv-vidrios-capital", domain.AvailabilityUnavailable, nil,
		assignedBy.Email, searchStart)
	require.NoError(t, err)

	_, err = s.db.Exec(`DELETE FROM schema_migrations WHERE version = '0009_provider_seed'`)
	require.NoError(t, err)
	require.NoError(t, postgres.Migrate(context.Background(), s.db))

	p, err := store.Get(context.Background(), "prv-vidrios-capital")
	require.NoError(t, err)
	assert.Equal(t, domain.AvailabilityUnavailable, p.Availability)
	assert.Equal(t, 8, s.count(t, `SELECT count(*) FROM provider`))
}

func TestTheDatabaseRefusesAnInconsistentProvider(t *testing.T) {
	_, s := newProviderStore(t)
	for _, stmt := range []string{
		`UPDATE provider SET availability = 'busy' WHERE id = 'prv-vidrios-capital'`,
		`UPDATE provider SET unavailable_until = '2026-12-01' WHERE id = 'prv-vidrios-capital'`,
		`UPDATE provider SET specialties = '{}' WHERE id = 'prv-vidrios-capital'`,
		`UPDATE provider SET latitude = 91 WHERE id = 'prv-vidrios-capital'`,
		`INSERT INTO provider (id, name, specialties, latitude, longitude, address, contact_email, availability,
			updated_by, updated_at) VALUES ('PRV-1', 'Nombre', '{plumbing}', 0, 0, 'Calle 1 # 1', 'a@b.co', 'available',
			'x', now())`,
	} {
		_, err := s.db.Exec(stmt)
		assert.Error(t, err, stmt)
	}
}

// Another story developed at the same time may also number its migrations
// 0008 and 0009: all are applied, each by its own name.
func TestTheMigrationsOfTheAssignmentLiveWithOthersOfTheSameNumber(t *testing.T) {
	requireDocker(t)
	db := newDB(t)
	files := fstest.MapFS{}
	entries, err := migrations.FS.ReadDir(".")
	require.NoError(t, err)
	for _, e := range entries {
		body, err := migrations.FS.ReadFile(e.Name())
		require.NoError(t, err)
		files[e.Name()] = &fstest.MapFile{Data: body}
	}
	files["0008_claim_followup.sql"] = &fstest.MapFile{Data: []byte(`CREATE TABLE claim_followup (claim_id TEXT PRIMARY KEY)`)}
	files["0009_assistance.sql"] = &fstest.MapFile{Data: []byte(`CREATE TABLE assistance (id TEXT PRIMARY KEY)`)}
	require.NoError(t, postgres.MigrateFS(context.Background(), db, files))

	rows, err := db.Query(`SELECT version FROM schema_migrations WHERE version LIKE '0008_%' OR version LIKE '0009_%'
		ORDER BY version`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var applied []string
	for rows.Next() {
		var v string
		require.NoError(t, rows.Scan(&v))
		applied = append(applied, v)
	}
	assert.Equal(t, []string{"0008_claim_followup", "0008_provider_assignment", "0009_assistance",
		"0009_provider_seed"}, applied)
}
