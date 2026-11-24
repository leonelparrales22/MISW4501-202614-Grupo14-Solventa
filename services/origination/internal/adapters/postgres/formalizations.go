package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// FormalizationStore is the ports.FormalizationStore over the formalization
// table (HU-W1.1-03, migration 0011). The unique quote_id makes two advisors
// formalizing the same quote at once converge on a single winner (CA-6).
type FormalizationStore struct {
	db *sql.DB
}

var _ ports.FormalizationStore = (*FormalizationStore)(nil)

// NewFormalizationStore builds a FormalizationStore over db (already migrated).
func NewFormalizationStore(db *sql.DB) *FormalizationStore { return &FormalizationStore{db: db} }

const formalizationColumns = `formalization_id, quote_id, offer_id, customer_id, advisor_id, advisor_email,
	consent_method, accepted_at, consent_reference, formalized_at`

// Create stores the formalization unless its quote already has one.
func (s *FormalizationStore) Create(ctx context.Context, f domain.Formalization) (domain.Formalization, bool, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO formalization (`+formalizationColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) ON CONFLICT (quote_id) DO NOTHING`,
		f.ID, f.QuoteID, f.OfferID, f.CustomerID, f.AdvisorID, f.AdvisorEmail,
		string(f.Consent.Method), f.Consent.AcceptedAt.UTC(), f.Consent.Reference, f.FormalizedAt.UTC())
	if err != nil {
		return domain.Formalization{}, false, fmt.Errorf("store the formalization: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return domain.Formalization{}, false, err
	}
	stored, err := s.GetByQuote(ctx, f.QuoteID)
	return stored, n == 1, err
}

// GetByQuote returns the formalization of the quote.
func (s *FormalizationStore) GetByQuote(ctx context.Context, quoteID string) (domain.Formalization, error) {
	var f domain.Formalization
	var method string
	err := s.db.QueryRowContext(ctx, `SELECT `+formalizationColumns+` FROM formalization WHERE quote_id = $1`, quoteID).Scan(
		&f.ID, &f.QuoteID, &f.OfferID, &f.CustomerID, &f.AdvisorID, &f.AdvisorEmail,
		&method, &f.Consent.AcceptedAt, &f.Consent.Reference, &f.FormalizedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Formalization{}, ports.ErrFormalizationNotFound
	}
	if err != nil {
		return domain.Formalization{}, fmt.Errorf("read the formalization: %w", err)
	}
	f.Consent.Method, f.Consent.AcceptedAt, f.FormalizedAt = domain.ConsentMethod(method), f.Consent.AcceptedAt.UTC(), f.FormalizedAt.UTC()
	return f, nil
}

// ConsentChallengeStore is the ports.ConsentChallengeStore over the
// consent_challenge table, or over renewal_consent_challenge for the
// renewals (HU-W1.5-03, NewRenewalConsentChallengeStore). Every operation is
// one statement.
type ConsentChallengeStore struct {
	db *sql.DB
	// sql are the statements of the table the store works on, keyed by the
	// quote (HU-W1.1-03) or by the requote of a renewal (HU-W1.5-03).
	sql challengeStatements
}

var _ ports.ConsentChallengeStore = (*ConsentChallengeStore)(nil)

// challengeStatements are the statements of one challenge table. The table and
// its key column are written in each statement, never formatted in at run
// time.
type challengeStatements struct {
	issue, get, attempt string
}

// quoteChallengeStatements work on consent_challenge, keyed by quote_id (0011).
var quoteChallengeStatements = challengeStatements{
	issue: `
		INSERT INTO consent_challenge (quote_id, advisor_id, code_hash, sends, attempts, created_at, expires_at)
		VALUES ($1, $2, $3, 1, 0, $4, $5)
		ON CONFLICT (quote_id) DO UPDATE
		   SET advisor_id = EXCLUDED.advisor_id, code_hash = EXCLUDED.code_hash, created_at = EXCLUDED.created_at,
		       expires_at = EXCLUDED.expires_at, sends = consent_challenge.sends + 1
		 WHERE consent_challenge.sends < $6
		RETURNING sends, attempts`,
	get: `SELECT advisor_id, code_hash, sends, attempts, created_at, expires_at
		FROM consent_challenge WHERE quote_id = $1`,
	attempt: `UPDATE consent_challenge SET attempts = attempts + 1
		WHERE quote_id = $1 RETURNING attempts`,
}

// renewalChallengeStatements work on renewal_consent_challenge, keyed by
// requote_id (0014), with the same rules.
var renewalChallengeStatements = challengeStatements{
	issue: `
		INSERT INTO renewal_consent_challenge (requote_id, advisor_id, code_hash, sends, attempts, created_at, expires_at)
		VALUES ($1, $2, $3, 1, 0, $4, $5)
		ON CONFLICT (requote_id) DO UPDATE
		   SET advisor_id = EXCLUDED.advisor_id, code_hash = EXCLUDED.code_hash, created_at = EXCLUDED.created_at,
		       expires_at = EXCLUDED.expires_at, sends = renewal_consent_challenge.sends + 1
		 WHERE renewal_consent_challenge.sends < $6
		RETURNING sends, attempts`,
	get: `SELECT advisor_id, code_hash, sends, attempts, created_at, expires_at
		FROM renewal_consent_challenge WHERE requote_id = $1`,
	attempt: `UPDATE renewal_consent_challenge SET attempts = attempts + 1
		WHERE requote_id = $1 RETURNING attempts`,
}

// NewConsentChallengeStore builds a ConsentChallengeStore over db, on the codes
// of the quotes (HU-W1.1-03).
func NewConsentChallengeStore(db *sql.DB) *ConsentChallengeStore {
	return &ConsentChallengeStore{db: db, sql: quoteChallengeStatements}
}

// NewRenewalConsentChallengeStore builds a ConsentChallengeStore over db, on
// the codes of the renewals an advisor formalizes (HU-W1.5-03): the challenge
// key (QuoteID of domain.ConsentChallenge) is the id of the requote.
func NewRenewalConsentChallengeStore(db *sql.DB) *ConsentChallengeStore {
	return &ConsentChallengeStore{db: db, sql: renewalChallengeStatements}
}

// Issue stores the challenge as the code of its quote. The upsert only takes
// effect while the quote has codes left, so concurrent requests cannot go past
// the limit.
func (s *ConsentChallengeStore) Issue(ctx context.Context, c domain.ConsentChallenge) (domain.ConsentChallenge, error) {
	err := s.db.QueryRowContext(ctx, s.sql.issue,
		c.QuoteID, c.AdvisorID, c.CodeHash, c.CreatedAt.UTC(), c.ExpiresAt.UTC(), domain.MaxConsentCodeSends).Scan(&c.Sends, &c.Attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ConsentChallenge{}, ports.ErrConsentCodeLimit
	}
	if err != nil {
		return domain.ConsentChallenge{}, fmt.Errorf("store the confirmation code: %w", err)
	}
	return c, nil
}

// Get returns the challenge of the quote.
func (s *ConsentChallengeStore) Get(ctx context.Context, quoteID string) (domain.ConsentChallenge, error) {
	c := domain.ConsentChallenge{QuoteID: quoteID}
	err := s.db.QueryRowContext(ctx, s.sql.get, quoteID).Scan(&c.AdvisorID, &c.CodeHash, &c.Sends, &c.Attempts, &c.CreatedAt, &c.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ConsentChallenge{}, ports.ErrConsentChallengeNotFound
	}
	if err != nil {
		return domain.ConsentChallenge{}, fmt.Errorf("read the confirmation code: %w", err)
	}
	c.CreatedAt, c.ExpiresAt = c.CreatedAt.UTC(), c.ExpiresAt.UTC()
	return c, nil
}

// RegisterAttempt counts one typed code.
func (s *ConsentChallengeStore) RegisterAttempt(ctx context.Context, quoteID string) (int, error) {
	var attempts int
	err := s.db.QueryRowContext(ctx, s.sql.attempt, quoteID).Scan(&attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ports.ErrConsentChallengeNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("count the confirmation code attempt: %w", err)
	}
	return attempts, nil
}
