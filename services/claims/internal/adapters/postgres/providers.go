package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// ProviderStore is the registry of providers over the provider table
// (HU-W2.3-01).
type ProviderStore struct {
	db *sql.DB
}

// NewProviderStore builds the store over an open, migrated database.
func NewProviderStore(db *sql.DB) *ProviderStore { return &ProviderStore{db: db} }

// ProviderStoreOf builds the store over the database of the claims, so the
// composition root does not open a second pool.
func ProviderStoreOf(records *ClaimRecordStore) *ProviderStore { return &ProviderStore{db: records.db} }

var _ ports.ProviderStore = (*ProviderStore)(nil)

// providerColumns reads the specialties as JSON, which database/sql scans.
const providerColumns = `id, name, to_json(specialties), latitude, longitude, address, contact_email,
	contact_phone, availability, to_char(unavailable_until, 'YYYY-MM-DD'), busy_claim_id, updated_by, updated_at`

// WithSpecialty returns the providers with the specialty, by id.
func (s *ProviderStore) WithSpecialty(ctx context.Context, sp domain.Specialty) ([]domain.Provider, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+providerColumns+` FROM provider
		WHERE specialties @> ARRAY[$1]::text[] ORDER BY id`, string(sp))
	if err != nil {
		return nil, fmt.Errorf("read the providers of %s: %w", sp, err)
	}
	return scanProviders(rows)
}

// List returns the providers of the filter by name.
func (s *ProviderStore) List(ctx context.Context, f ports.ProviderFilter, limit int) ([]domain.Provider, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+providerColumns+` FROM provider
		WHERE ($1 = '' OR specialties @> ARRAY[$1]::text[]) AND ($2 = '' OR availability = $2)
		ORDER BY name, id LIMIT $3`, string(f.Specialty), string(f.Availability), limit)
	if err != nil {
		return nil, fmt.Errorf("list the providers: %w", err)
	}
	return scanProviders(rows)
}

// Get returns the provider.
func (s *ProviderStore) Get(ctx context.Context, id string) (domain.Provider, error) {
	return s.one(ctx, `SELECT `+providerColumns+` FROM provider WHERE id = $1`, id)
}

// Create registers the provider.
func (s *ProviderStore) Create(ctx context.Context, p domain.Provider) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO provider (id, name, specialties, latitude, longitude, address,
			contact_email, contact_phone, availability, unavailable_until, busy_claim_id, updated_by, updated_at)
		VALUES ($1, $2, $3::text[], $4, $5, $6, $7, $8, $9, $10::date, $11, $12, $13)`,
		p.ID, p.Name, specialtyNames(p.Specialties), p.Location.Latitude, p.Location.Longitude, p.Address,
		p.ContactEmail, nullText(p.ContactPhone), string(p.Availability), day(p.UnavailableUntil),
		nullText(p.BusyClaimID), p.UpdatedBy, p.UpdatedAt.UTC())
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && pgErr.ConstraintName == "provider_pkey" {
		return ports.ErrProviderExists
	}
	if err != nil {
		return fmt.Errorf("register the provider %s: %w", p.ID, err)
	}
	return nil
}

// UpdateProfile replaces the profile of the provider.
func (s *ProviderStore) UpdateProfile(ctx context.Context, id string, p domain.ProviderProfile, by string,
	at time.Time) (domain.Provider, error) {
	return s.one(ctx, `UPDATE provider SET name = $2, specialties = $3::text[], latitude = $4, longitude = $5,
			address = $6, contact_email = $7, contact_phone = $8, updated_by = $9, updated_at = $10
		WHERE id = $1 RETURNING `+providerColumns,
		id, p.Name, specialtyNames(p.Specialties), *p.Latitude, *p.Longitude, p.Address, p.ContactEmail,
		nullText(p.ContactPhone), by, at.UTC())
}

// SetAvailability sets the availability of the provider and releases it from
// the claim it was busy with.
func (s *ProviderStore) SetAvailability(ctx context.Context, id string, a domain.Availability, until *time.Time,
	by string, at time.Time) (domain.Provider, error) {
	if a == domain.AvailabilityBusy {
		return domain.Provider{}, domain.ErrBusyIsSetByAssignment
	}
	if a == domain.AvailabilityAvailable {
		until = nil
	}
	return s.one(ctx, `UPDATE provider SET availability = $2, unavailable_until = $3::date, busy_claim_id = NULL,
			updated_by = $4, updated_at = $5
		WHERE id = $1 RETURNING `+providerColumns, id, string(a), day(until), by, at.UTC())
}

// one runs a statement that returns at most one provider, or
// ErrProviderNotFound.
func (s *ProviderStore) one(ctx context.Context, query string, args ...any) (domain.Provider, error) {
	p, err := scanProvider(s.db.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Provider{}, ports.ErrProviderNotFound
	}
	if err != nil {
		return domain.Provider{}, fmt.Errorf("read the provider: %w", err)
	}
	return p, nil
}

func scanProviders(rows *sql.Rows) ([]domain.Provider, error) {
	defer func() { _ = rows.Close() }()
	var out []domain.Provider
	for rows.Next() {
		p, err := scanProvider(rows)
		if err != nil {
			return nil, fmt.Errorf("read the providers: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the providers: %w", err)
	}
	return out, nil
}

// scanProvider reads the columns of providerColumns, in that order.
func scanProvider(row scanner) (domain.Provider, error) {
	var (
		p                  domain.Provider
		specialties        []byte
		availability       string
		phone, until, busy sql.NullString
		names              []string
	)
	if err := row.Scan(&p.ID, &p.Name, &specialties, &p.Location.Latitude, &p.Location.Longitude, &p.Address,
		&p.ContactEmail, &phone, &availability, &until, &busy, &p.UpdatedBy, &p.UpdatedAt); err != nil {
		return domain.Provider{}, err
	}
	if err := json.Unmarshal(specialties, &names); err != nil {
		return domain.Provider{}, fmt.Errorf("decode the specialties of the provider %s: %w", p.ID, err)
	}
	for _, n := range names {
		p.Specialties = append(p.Specialties, domain.Specialty(n))
	}
	p.Availability, p.UpdatedAt = domain.Availability(availability), p.UpdatedAt.UTC()
	p.ContactPhone, p.BusyClaimID = stringOf(phone), stringOf(busy)
	if until.Valid {
		d, err := time.ParseInLocation(time.DateOnly, until.String, domain.ColombiaTime)
		if err != nil {
			return domain.Provider{}, fmt.Errorf("decode the availability of the provider %s: %w", p.ID, err)
		}
		p.UnavailableUntil = &d
	}
	return p, nil
}

func specialtyNames(specialties []domain.Specialty) []string {
	out := make([]string, len(specialties))
	for i, s := range specialties {
		out[i] = string(s)
	}
	return out
}

// day is the date (in Colombia) of t, or NULL.
func day(t *time.Time) sql.NullString {
	if t == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: t.In(domain.ColombiaTime).Format(time.DateOnly), Valid: true}
}

func nullText(s *string) sql.NullString {
	if s == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *s, Valid: true}
}

func stringOf(s sql.NullString) *string {
	if !s.Valid {
		return nil
	}
	v := s.String
	return &v
}
