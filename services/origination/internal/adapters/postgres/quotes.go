package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// QuoteStore is the ports.QuoteRepository over the assisted_quote table
// (HU-W1.1-03, migration 0011): the quotes of the advisors survive a restart
// until they are formalized. A quote is immutable.
type QuoteStore struct {
	db *sql.DB
}

var _ ports.QuoteRepository = (*QuoteStore)(nil)

// NewQuoteStore builds a QuoteStore over db (already migrated).
func NewQuoteStore(db *sql.DB) *QuoteStore { return &QuoteStore{db: db} }

const quoteColumns = `quote_id, customer_id, advisor_id, applicant, birth_date, insured_amount_minor, term_months,
	premium_minor, currency, coverages, rating_version, mode, profile_source, profile_score, profile_category,
	profile_date, unavailable_source, created_at, expires_at`

// Create stores the quote.
func (s *QuoteStore) Create(ctx context.Context, q domain.Quote) error {
	applicant, err := json.Marshal(applicantJSON{FullName: q.Applicant.FullName, DocumentID: q.Applicant.DocumentID, Email: q.Applicant.Email})
	if err != nil {
		return err
	}
	coverages, err := encodeCoverages(q.Coverages)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO assisted_quote (`+quoteColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19)`,
		q.ID, q.CustomerID, q.AdvisorID, string(applicant), q.BirthDate.Format("2006-01-02"),
		domain.ToMinorUnits(q.InsuredAmount), q.TermMonths, domain.ToMinorUnits(q.Premium), q.Currency, string(coverages),
		q.RatingVersion, string(q.Mode), string(q.ProfileSource), q.ProfileScore, q.ProfileCategory,
		nullTime(q.ProfileDate), nullString(q.UnavailableSource), q.CreatedAt.UTC(), q.ExpiresAt.UTC())
	if err != nil {
		return fmt.Errorf("store the quote: %w", err)
	}
	return nil
}

// Get returns the quote with the given id, or ports.ErrQuoteNotFound.
func (s *QuoteStore) Get(ctx context.Context, quoteID string) (domain.Quote, error) {
	if quoteID == "" || strings.ContainsRune(quoteID, 0) {
		return domain.Quote{}, ports.ErrQuoteNotFound
	}
	var (
		q                          domain.Quote
		applicantRaw, coveragesRaw []byte
		insuredMinor, premiumMinor int64
		mode, source               string
		profileDate                sql.NullTime
		unavailable                sql.NullString
	)
	err := s.db.QueryRowContext(ctx, `SELECT `+quoteColumns+` FROM assisted_quote WHERE quote_id = $1`, quoteID).Scan(
		&q.ID, &q.CustomerID, &q.AdvisorID, &applicantRaw, &q.BirthDate, &insuredMinor, &q.TermMonths,
		&premiumMinor, &q.Currency, &coveragesRaw, &q.RatingVersion, &mode, &source, &q.ProfileScore, &q.ProfileCategory,
		&profileDate, &unavailable, &q.CreatedAt, &q.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Quote{}, ports.ErrQuoteNotFound
	}
	if err != nil {
		return domain.Quote{}, fmt.Errorf("read the quote: %w", err)
	}
	var a applicantJSON
	if err := json.Unmarshal(applicantRaw, &a); err != nil {
		return domain.Quote{}, fmt.Errorf("decode the quote %s: %w", quoteID, err)
	}
	if q.Coverages, err = decodeCoverages(coveragesRaw); err != nil {
		return domain.Quote{}, fmt.Errorf("decode the quote %s: %w", quoteID, err)
	}
	q.Applicant = domain.Applicant{FullName: a.FullName, DocumentID: a.DocumentID, Email: a.Email}
	q.InsuredAmount, q.Premium = domain.FromMinorUnits(insuredMinor), domain.FromMinorUnits(premiumMinor)
	q.Mode, q.ProfileSource = domain.OfferMode(mode), domain.ProfileSource(source)
	q.BirthDate, q.CreatedAt, q.ExpiresAt = q.BirthDate.UTC(), q.CreatedAt.UTC(), q.ExpiresAt.UTC()
	if profileDate.Valid {
		q.ProfileDate = profileDate.Time.UTC()
	}
	q.UnavailableSource = unavailable.String
	return q, nil
}
