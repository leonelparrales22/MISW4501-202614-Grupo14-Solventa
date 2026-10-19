package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/ports"
)

// ConsentStore implements ports.ConsentStore over the consent_record table
// (HU-W1.2-01). The table is append-only: its triggers reject UPDATE, DELETE
// and TRUNCATE, and this adapter has no statement that issues them.
type ConsentStore struct {
	db *sql.DB
}

// NewConsentStore builds the store over an opened and migrated database.
func NewConsentStore(db *sql.DB) *ConsentStore { return &ConsentStore{db: db} }

var _ ports.ConsentStore = (*ConsentStore)(nil)

const consentColumns = `consent_id, customer_id, holder_document_id, holder_full_name, terms_version,
	scope, purpose, channel, partner_id, application_id, user_agent, granted_at`

// scopeEntry is the JSON shape of one source in the stored scope.
type scopeEntry struct {
	Code           string          `json:"code"`
	DataCategories []string        `json:"data_categories"`
	Validity       domain.Validity `json:"validity"`
}

// Create inserts the consent, or returns the one already stored for the same
// (customer_id, application_id, terms_version) with created=false.
func (s *ConsentStore) Create(ctx context.Context, c domain.Consent) (domain.Consent, bool, error) {
	scope := make([]scopeEntry, len(c.Scope))
	for i, src := range c.Scope {
		scope[i] = scopeEntry{Code: src.Code, DataCategories: src.DataCategories, Validity: src.Validity}
	}
	rawScope, err := json.Marshal(scope)
	if err != nil {
		return domain.Consent{}, false, fmt.Errorf("encode the consent scope: %w", err)
	}

	res, err := s.db.ExecContext(ctx, `INSERT INTO consent_record (`+consentColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT ON CONSTRAINT consent_record_idempotency DO NOTHING`,
		c.ID, c.CustomerID, c.Holder.DocumentID, c.Holder.FullName, string(c.TermsVersion),
		rawScope, c.Purpose, string(c.Channel), c.PartnerID, c.ApplicationID, c.UserAgent, c.GrantedAt)
	if err != nil {
		return domain.Consent{}, false, fmt.Errorf("insert the consent: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 1 {
		return c, true, nil
	}

	existing, err := scanConsent(s.db.QueryRowContext(ctx, `SELECT `+consentColumns+` FROM consent_record
		WHERE customer_id = $1 AND application_id = $2 AND terms_version = $3`,
		c.CustomerID, c.ApplicationID, string(c.TermsVersion)))
	if err != nil {
		return domain.Consent{}, false, fmt.Errorf("read the existing consent: %w", err)
	}
	return existing, false, nil
}

// Get returns the consent with the id, or ports.ErrConsentNotFound.
func (s *ConsentStore) Get(ctx context.Context, id string) (domain.Consent, error) {
	c, err := scanConsent(s.db.QueryRowContext(ctx, `SELECT `+consentColumns+` FROM consent_record WHERE consent_id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Consent{}, ports.ErrConsentNotFound
	}
	return c, err
}

// LatestByCustomer returns the most recently granted consent of the customer,
// or ports.ErrConsentNotFound.
func (s *ConsentStore) LatestByCustomer(ctx context.Context, customerID string) (domain.Consent, error) {
	c, err := scanConsent(s.db.QueryRowContext(ctx, `SELECT `+consentColumns+` FROM consent_record
		WHERE customer_id = $1 ORDER BY granted_at DESC, recorded_at DESC LIMIT 1`, customerID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Consent{}, ports.ErrConsentNotFound
	}
	return c, err
}

func scanConsent(row *sql.Row) (domain.Consent, error) {
	var (
		c                      domain.Consent
		terms, channel         string
		rawScope               []byte
		documentID, holderName string
	)
	if err := row.Scan(&c.ID, &c.CustomerID, &documentID, &holderName, &terms,
		&rawScope, &c.Purpose, &channel, &c.PartnerID, &c.ApplicationID, &c.UserAgent, &c.GrantedAt); err != nil {
		return domain.Consent{}, err
	}
	var scope []scopeEntry
	if err := json.Unmarshal(rawScope, &scope); err != nil {
		return domain.Consent{}, fmt.Errorf("decode the consent scope: %w", err)
	}
	c.Holder = domain.Holder{DocumentID: documentID, FullName: holderName}
	c.TermsVersion = domain.TermsVersion(terms)
	c.Channel = domain.Channel(channel)
	c.GrantedAt = c.GrantedAt.UTC()
	c.Scope = make([]domain.SourceTerm, len(scope))
	for i, e := range scope {
		c.Scope[i] = domain.SourceTerm{Code: e.Code, DataCategories: e.DataCategories, Validity: e.Validity}
	}
	return c, nil
}
