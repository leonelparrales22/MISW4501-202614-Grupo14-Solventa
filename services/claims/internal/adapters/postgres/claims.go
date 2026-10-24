package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// ClaimStore keeps the filed cases in PostgreSQL. It is what makes a filing
// survive a redeploy of the service.
type ClaimStore struct {
	db *sql.DB
}

// NewClaimStore builds the store over an open database.
func NewClaimStore(db *sql.DB) *ClaimStore { return &ClaimStore{db: db} }

const claimColumns = `id, filing_number, cognito_sub, filing_key, fingerprint, policy_id,
	incident_type, occurred_at, place, description, details, status, filed_at`

// Create stores the claim with a fresh filing number.
//
// The filing key is read first: a retry after a lost response is the common
// case, and answering it must not burn a filing number. The unique index is
// what settles the race between two retries that arrive at the same time, and
// then the row that won is read back.
func (s *ClaimStore) Create(ctx context.Context, c domain.Claim) (domain.Claim, bool, error) {
	switch stored, filed, err := s.byFilingKey(ctx, c.Subject, c.FilingKey); {
	case err == nil:
		return sameFiling(stored, filed, c.Fingerprint())
	case !errors.Is(err, sql.ErrNoRows):
		return domain.Claim{}, false, fmt.Errorf("read the filing by its key: %w", err)
	}

	var sequence int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT nextval('claim_filing_number_seq')`).Scan(&sequence); err != nil {
		return domain.Claim{}, false, fmt.Errorf("take the next filing number: %w", err)
	}
	c.FilingNumber = domain.FilingNumber(c.FiledAt, sequence)

	details, err := json.Marshal(c.Details)
	if err != nil {
		return domain.Claim{}, false, fmt.Errorf("encode the details of the filing: %w", err)
	}

	res, err := s.db.ExecContext(ctx, `INSERT INTO claim (`+claimColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (cognito_sub, filing_key) DO NOTHING`,
		c.ID, c.FilingNumber, c.Subject, c.FilingKey, c.Fingerprint(), c.PolicyID,
		string(c.IncidentType), c.OccurredAt, c.Place, c.Description, details,
		string(c.Status), c.FiledAt)
	if err != nil {
		return domain.Claim{}, false, fmt.Errorf("store the filing: %w", err)
	}
	if rows, err := res.RowsAffected(); err == nil && rows == 0 {
		// Another retry of the same filing won the race.
		stored, filed, err := s.byFilingKey(ctx, c.Subject, c.FilingKey)
		if err != nil {
			return domain.Claim{}, false, fmt.Errorf("read the filing that won the race: %w", err)
		}
		return sameFiling(stored, filed, c.Fingerprint())
	}
	return c, true, nil
}

// sameFiling answers a repeated filing key: the case that already exists when
// the filing is the same one, and ErrFilingKeyReused when it is another.
func sameFiling(stored domain.Claim, filed, incoming string) (domain.Claim, bool, error) {
	if filed != incoming {
		return domain.Claim{}, false, ports.ErrFilingKeyReused
	}
	return stored, false, nil
}

// byFilingKey returns the case and the fingerprint of what was filed with it.
func (s *ClaimStore) byFilingKey(ctx context.Context, subject, key string) (domain.Claim, string, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+claimColumns+`
		FROM claim WHERE cognito_sub = $1 AND filing_key = $2`, subject, key)
	return scanClaim(row)
}

func scanClaim(row *sql.Row) (domain.Claim, string, error) {
	var (
		c           domain.Claim
		incident    string
		status      string
		details     []byte
		fingerprint string
	)
	if err := row.Scan(&c.ID, &c.FilingNumber, &c.Subject, &c.FilingKey, &fingerprint, &c.PolicyID,
		&incident, &c.OccurredAt, &c.Place, &c.Description, &details, &status, &c.FiledAt); err != nil {
		return domain.Claim{}, "", err
	}
	c.IncidentType = domain.IncidentType(incident)
	c.Status = domain.Status(status)
	c.OccurredAt = c.OccurredAt.UTC()
	c.FiledAt = c.FiledAt.UTC()
	if len(details) > 0 {
		if err := json.Unmarshal(details, &c.Details); err != nil {
			return domain.Claim{}, "", fmt.Errorf("decode the details of the filing: %w", err)
		}
	}
	if c.Details == nil {
		c.Details = map[string]string{}
	}
	return c, fingerprint, nil
}
