package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// AssistanceStore keeps the requests for on-site assistance (HU-M2.3-01) in
// assistance_request, whose triggers keep what was sent from being rewritten
// (decision D7).
type AssistanceStore struct {
	db *sql.DB
}

// NewAssistanceStore builds the store over an open, migrated database.
func NewAssistanceStore(db *sql.DB) *AssistanceStore { return &AssistanceStore{db: db} }

// AssistanceStoreOf builds the store over the database of the claims, so the
// composition root does not open a second pool.
func AssistanceStoreOf(records *ClaimRecordStore) *AssistanceStore {
	return &AssistanceStore{db: records.db}
}

var _ ports.AssistanceStore = (*AssistanceStore)(nil)

const assistanceColumns = `id, request_number, cognito_sub, request_key, fingerprint, requester_name, policy_id,
	policy_snapshot, location_status, latitude, longitude, accuracy_m, location_captured_at, unavailable_reason,
	place, status, requested_at`

// assistancePolicyJSON is the copy of the policy as policy_snapshot keeps it.
type assistancePolicyJSON struct {
	PolicyID      string `json:"policy_id"`
	Line          string `json:"line"`
	Product       string `json:"product"`
	InsuredObject string `json:"insured_object"`
}

// Create stores the request with the next number. The key is read first: a
// retry after a lost response is the common case, and answering it must not
// burn a number. The unique index settles the race between two retries that
// arrive at the same time, and then the row that won is read back.
func (s *AssistanceStore) Create(ctx context.Context, r domain.AssistanceRequest) (domain.AssistanceRequest, bool, error) {
	switch stored, err := s.byKey(ctx, r.Subject, r.Key); {
	case err == nil:
		return sameAssistance(stored, r.Fingerprint)
	case !errors.Is(err, sql.ErrNoRows):
		return domain.AssistanceRequest{}, false, fmt.Errorf("read the request for assistance by its key: %w", err)
	}

	snapshot, err := json.Marshal(assistancePolicyJSON{PolicyID: r.Policy.ID, Line: string(r.Policy.Line),
		Product: r.Policy.Product, InsuredObject: r.Policy.InsuredObject})
	if err != nil {
		return domain.AssistanceRequest{}, false, fmt.Errorf("encode the policy of the request: %w", err)
	}

	var sequence int64
	if err := s.db.QueryRowContext(ctx, `SELECT nextval('assistance_request_number_seq')`).Scan(&sequence); err != nil {
		return domain.AssistanceRequest{}, false, fmt.Errorf("take the next number of a request: %w", err)
	}
	r.Number = domain.AssistanceNumber(r.RequestedAt, sequence)
	r.RequestedAt = r.RequestedAt.UTC().Truncate(time.Microsecond)

	l := r.Location
	var latitude, longitude any
	if l.Coordinates != nil {
		latitude, longitude = l.Coordinates.Latitude, l.Coordinates.Longitude
	}
	var capturedAt any
	if l.CapturedAt != nil {
		capturedAt = l.CapturedAt.UTC()
	}
	var place any
	if r.Place != nil {
		place = *r.Place
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO assistance_request (`+assistanceColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
		ON CONFLICT (cognito_sub, request_key) DO NOTHING`,
		r.ID, r.Number, r.Subject, r.Key, r.Fingerprint, r.RequesterName, r.Policy.ID, snapshot,
		string(l.Status), latitude, longitude, nullFloat(l.AccuracyM), capturedAt,
		nullString(string(l.UnavailableReason)), place, string(r.Status), r.RequestedAt)
	if err != nil {
		return domain.AssistanceRequest{}, false, fmt.Errorf("store the request for assistance: %w", err)
	}
	if rows, err := res.RowsAffected(); err == nil && rows == 0 {
		// Another retry of the same request won the race.
		stored, err := s.byKey(ctx, r.Subject, r.Key)
		if err != nil {
			return domain.AssistanceRequest{}, false, fmt.Errorf("read the request that won the race: %w", err)
		}
		return sameAssistance(stored, r.Fingerprint)
	}
	return r, true, nil
}

// sameAssistance answers a retry: the stored request when it is the same one,
// ports.ErrAssistanceKeyReused when the key came with another one.
func sameAssistance(stored domain.AssistanceRequest, fingerprint string) (domain.AssistanceRequest, bool, error) {
	if stored.Fingerprint != fingerprint {
		return domain.AssistanceRequest{}, false, ports.ErrAssistanceKeyReused
	}
	return stored, false, nil
}

// Get returns the request with the id, whoever owns it.
func (s *AssistanceStore) Get(ctx context.Context, id string) (domain.AssistanceRequest, error) {
	r, err := scanAssistance(s.db.QueryRowContext(ctx,
		`SELECT `+assistanceColumns+` FROM assistance_request WHERE id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AssistanceRequest{}, ports.ErrAssistanceRequestNotFound
	}
	if err != nil {
		return domain.AssistanceRequest{}, fmt.Errorf("read the request for assistance: %w", err)
	}
	return r, nil
}

func (s *AssistanceStore) byKey(ctx context.Context, subject, key string) (domain.AssistanceRequest, error) {
	return scanAssistance(s.db.QueryRowContext(ctx,
		`SELECT `+assistanceColumns+` FROM assistance_request WHERE cognito_sub = $1 AND request_key = $2`,
		subject, key))
}

func scanAssistance(row *sql.Row) (domain.AssistanceRequest, error) {
	var (
		r                        domain.AssistanceRequest
		snapshot                 []byte
		status, locationStatus   string
		latitude, longitude      sql.NullFloat64
		accuracy                 sql.NullFloat64
		capturedAt               sql.NullTime
		unavailableReason, place sql.NullString
		requestedAt              time.Time
	)
	if err := row.Scan(&r.ID, &r.Number, &r.Subject, &r.Key, &r.Fingerprint, &r.RequesterName, &r.Policy.ID,
		&snapshot, &locationStatus, &latitude, &longitude, &accuracy, &capturedAt, &unavailableReason, &place,
		&status, &requestedAt); err != nil {
		return domain.AssistanceRequest{}, err
	}
	var p assistancePolicyJSON
	if err := json.Unmarshal(snapshot, &p); err != nil {
		return domain.AssistanceRequest{}, fmt.Errorf("decode the policy of the request %s: %w", r.ID, err)
	}
	r.Policy = domain.AssistancePolicy{ID: p.PolicyID, Line: domain.PolicyLine(p.Line), Product: p.Product,
		InsuredObject: p.InsuredObject}
	r.Status = domain.AssistanceStatus(status)
	r.RequestedAt = requestedAt.UTC()

	r.Location = domain.AssistanceLocation{Status: domain.LocationStatus(locationStatus),
		UnavailableReason: domain.LocationUnavailableReason(unavailableReason.String)}
	if latitude.Valid && longitude.Valid {
		r.Location.Coordinates = &domain.Coordinates{Latitude: latitude.Float64, Longitude: longitude.Float64}
	}
	if accuracy.Valid {
		r.Location.AccuracyM = &accuracy.Float64
	}
	if capturedAt.Valid {
		at := capturedAt.Time.UTC()
		r.Location.CapturedAt = &at
	}
	if place.Valid {
		r.Place = &place.String
	}
	return r, nil
}
