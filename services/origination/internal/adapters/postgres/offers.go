package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// Constraint names the store reacts to (migration 0003_offer.sql).
const (
	uniqueViolation       = "23505"
	foreignKeyViolation   = "23503"
	supersedesUniqueIndex = "offer_supersedes_uidx"
	renewsUniqueIndex     = "offer_renews_uidx"
)

// OfferStore is the ports.PersonalizedOfferStore over the offer and
// offer_owner tables (HU-W1.2-03). The offer table is immutable (triggers
// reject UPDATE, DELETE and TRUNCATE), so the owner, the replacement and the
// renewal are separate rows that Get joins. Each operation is a single
// statement or a short transaction, so every task of the service converges:
// the primary key makes Create idempotent, ON CONFLICT DO NOTHING makes the
// first claim win, and the unique indexes on supersedes and renews give an
// offer a single replacement and a single renewal.
type OfferStore struct {
	db *sql.DB
}

var _ ports.PersonalizedOfferStore = (*OfferStore)(nil)

// NewOfferStore builds an OfferStore over db (already migrated).
func NewOfferStore(db *sql.DB) *OfferStore { return &OfferStore{db: db} }

const offerColumns = `o.offer_id, o.customer_id, o.channel, o.quote_id, o.consent_id, o.partner_id, o.application_id,
	o.applicant, o.birth_date, o.age_at_quote, o.terms, o.premium_minor, o.currency, o.conditions,
	o.mode, o.profile_source, o.provisional_reason, o.profile_date, o.unavailable_source, o.profile,
	o.rules, o.rater_version, o.created_at, o.expires_at, o.supersedes, o.renews,
	w.owner_id, sup.offer_id, sup.created_at, ren.offer_id`

// offerJoins adds the derived fields: the owner, the offer that replaced this
// one and the one that renewed it.
const offerJoins = `FROM offer o
	LEFT JOIN offer_owner w ON w.offer_id = o.offer_id
	LEFT JOIN offer sup ON sup.supersedes = o.offer_id
	LEFT JOIN offer ren ON ren.renews = o.offer_id`

// Stored shapes of the JSONB columns. Money is in minor units, like the charge.
type (
	applicantJSON struct {
		FullName   string `json:"full_name"`
		DocumentID string `json:"document_id"`
		Email      string `json:"email"`
	}
	termsJSON struct {
		InsuredAmountMinor int64          `json:"insured_amount_minor"`
		TermMonths         int            `json:"term_months"`
		Coverages          []coverageJSON `json:"coverages"`
	}
	sourceJSON struct {
		Source    string `json:"source"`
		Status    string `json:"status"`
		LatencyMs int64  `json:"latency_ms"`
	}
	profileJSON struct {
		Score        float64      `json:"score"`
		Category     string       `json:"category"`
		CalculatedAt *time.Time   `json:"calculated_at"`
		Partial      bool         `json:"partial"`
		Sources      []sourceJSON `json:"sources"`
	}
	ruleJSON struct {
		Code  string  `json:"code"`
		Value float64 `json:"value"`
	}
)

// Create stores the offer unless its id exists.
func (s *OfferStore) Create(ctx context.Context, o domain.PersonalizedOffer) (domain.PersonalizedOffer, bool, error) {
	created, err := insertOffer(ctx, s.db, o)
	if err != nil {
		return domain.PersonalizedOffer{}, false, err
	}
	stored, err := s.Get(ctx, o.ID)
	return stored, created, err
}

// Get returns the offer with its owner, replacement and renewal.
func (s *OfferStore) Get(ctx context.Context, offerID string) (domain.PersonalizedOffer, error) {
	if !validOfferID(offerID) {
		return domain.PersonalizedOffer{}, ports.ErrOfferNotFound
	}
	row := s.db.QueryRowContext(ctx, `SELECT `+offerColumns+` `+offerJoins+` WHERE o.offer_id = $1`, offerID)
	o, err := scanOffer(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.PersonalizedOffer{}, ports.ErrOfferNotFound
	}
	return o, err
}

// Claim sets the owner of the offer if it has none: the first writer wins.
func (s *OfferStore) Claim(ctx context.Context, offerID, ownerID string) (domain.PersonalizedOffer, error) {
	if !validOfferID(offerID) {
		return domain.PersonalizedOffer{}, ports.ErrOfferNotFound
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO offer_owner (offer_id, owner_id)
		SELECT offer_id, $2 FROM offer WHERE offer_id = $1
		ON CONFLICT (offer_id) DO NOTHING`, offerID, ownerID); err != nil {
		return domain.PersonalizedOffer{}, fmt.Errorf("claim the offer: %w", err)
	}
	return s.Get(ctx, offerID)
}

// Supersede stores newOffer as the replacement of oldID, with the owner of the
// old one, in one transaction. A second replacement violates the unique index
// on supersedes and returns created=false.
func (s *OfferStore) Supersede(ctx context.Context, oldID string, newOffer domain.PersonalizedOffer) (bool, error) {
	newOffer.Supersedes, newOffer.Renews = oldID, ""
	_, created, err := s.insertLinked(ctx, oldID, newOffer, supersedesUniqueIndex)
	return created, err
}

// Renew stores newOffer as the renewal of oldID, with the owner of the old one,
// in one transaction. A second renewal returns the existing one.
func (s *OfferStore) Renew(ctx context.Context, oldID string, newOffer domain.PersonalizedOffer) (domain.PersonalizedOffer, bool, error) {
	newOffer.Renews, newOffer.Supersedes = oldID, ""
	id, created, err := s.insertLinked(ctx, oldID, newOffer, renewsUniqueIndex)
	if err != nil {
		return domain.PersonalizedOffer{}, false, err
	}
	stored, err := s.Get(ctx, id)
	return stored, created, err
}

// insertLinked inserts the offer that points to oldID and copies the owner. When
// the unique index named by conflictIndex rejects it, the old offer already has
// that link: it returns the id of the offer that does, with created=false.
func (s *OfferStore) insertLinked(ctx context.Context, oldID string, o domain.PersonalizedOffer, conflictIndex string) (string, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", false, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	_, err = insertOffer(ctx, tx, o)
	var pgErr *pgconn.PgError
	switch {
	case errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && pgErr.ConstraintName == conflictIndex:
		_ = tx.Rollback()
		existing, qerr := s.linkedOffer(ctx, oldID, conflictIndex)
		return existing, false, qerr
	case errors.As(err, &pgErr) && pgErr.Code == foreignKeyViolation:
		return "", false, ports.ErrOfferNotFound
	case err != nil:
		return "", false, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO offer_owner (offer_id, owner_id)
		SELECT $2, owner_id FROM offer_owner WHERE offer_id = $1`, oldID, o.ID); err != nil {
		return "", false, fmt.Errorf("copy the owner: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", false, fmt.Errorf("commit: %w", err)
	}
	return o.ID, true, nil
}

// linkedOffer returns the id of the offer that already replaces or renews oldID.
func (s *OfferStore) linkedOffer(ctx context.Context, oldID, index string) (string, error) {
	column := "supersedes"
	if index == renewsUniqueIndex {
		column = "renews"
	}
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT offer_id FROM offer WHERE `+column+` = $1`, oldID).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("read the existing %s: %w", column, err)
	}
	return id, nil
}

// ListRequotable returns the provisional offers still valid at now that
// nobody replaced, oldest first.
func (s *OfferStore) ListRequotable(ctx context.Context, now time.Time, limit int) ([]domain.PersonalizedOffer, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+offerColumns+` `+offerJoins+`
		WHERE o.mode = 'provisional' AND o.expires_at > $1 AND sup.offer_id IS NULL
		ORDER BY o.created_at, o.offer_id LIMIT $2`, now, limit)
	if err != nil {
		return nil, fmt.Errorf("list the provisional offers: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []domain.PersonalizedOffer
	for rows.Next() {
		o, err := scanOffer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// execer is what insertOffer needs from a database or a transaction.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func insertOffer(ctx context.Context, db execer, o domain.PersonalizedOffer) (bool, error) {
	applicant, err := json.Marshal(applicantJSON{FullName: o.Applicant.FullName, DocumentID: o.Applicant.DocumentID, Email: o.Applicant.Email})
	if err != nil {
		return false, err
	}
	coverages := make([]coverageJSON, 0, len(o.Terms.Coverages))
	for _, c := range o.Terms.Coverages {
		coverages = append(coverages, coverageJSON{Code: c.Code, InsuredAmountMinor: domain.ToMinorUnits(c.InsuredAmount)})
	}
	terms, err := json.Marshal(termsJSON{
		InsuredAmountMinor: domain.ToMinorUnits(o.Terms.InsuredAmount), TermMonths: o.Terms.TermMonths, Coverages: coverages,
	})
	if err != nil {
		return false, err
	}
	conditions, err := json.Marshal(nonNil(o.Conditions))
	if err != nil {
		return false, err
	}
	snapshot := profileJSON{Score: o.Profile.Score, Category: o.Profile.Category, Partial: o.Profile.Partial, Sources: []sourceJSON{}}
	if !o.Profile.CalculatedAt.IsZero() {
		t := o.Profile.CalculatedAt.UTC()
		snapshot.CalculatedAt = &t
	}
	for _, src := range o.Profile.Sources {
		snapshot.Sources = append(snapshot.Sources, sourceJSON{Source: src.Source, Status: string(src.Status), LatencyMs: src.Latency.Milliseconds()})
	}
	profile, err := json.Marshal(snapshot)
	if err != nil {
		return false, err
	}
	rules := make([]ruleJSON, 0, len(o.Rules))
	for _, r := range o.Rules {
		rules = append(rules, ruleJSON{Code: r.Code, Value: r.Value})
	}
	rulesJSON, err := json.Marshal(rules)
	if err != nil {
		return false, err
	}

	res, err := db.ExecContext(ctx, `
		INSERT INTO offer (offer_id, customer_id, channel, quote_id, consent_id, partner_id, application_id,
			applicant, birth_date, age_at_quote, terms, premium_minor, currency, conditions,
			mode, profile_source, provisional_reason, profile_date, unavailable_source, profile,
			rules, rater_version, created_at, expires_at, supersedes, renews)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25, $26)
		ON CONFLICT (offer_id) DO NOTHING`,
		o.ID, o.CustomerID, string(o.Channel), nullString(o.QuoteID), o.ConsentID, o.PartnerID, o.ApplicationID,
		string(applicant), o.BirthDate.Format("2006-01-02"), o.AgeAtQuote, string(terms), domain.ToMinorUnits(o.Premium), o.Currency, string(conditions),
		string(o.Mode), string(o.ProfileSource), nullString(o.ProvisionalReason), nullTime(o.ProfileDate), nullString(o.UnavailableSource), string(profile),
		string(rulesJSON), o.RaterVersion, o.CreatedAt.UTC(), o.ExpiresAt.UTC(), nullString(o.Supersedes), nullString(o.Renews))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func scanOffer(row scanner) (domain.PersonalizedOffer, error) {
	var (
		o                                                        domain.PersonalizedOffer
		channel, mode, source                                    string
		quoteID, reason, unavailable, supersedes, renews         sql.NullString
		owner, supersededBy, renewedBy                           sql.NullString
		profileDate, supersededAt                                sql.NullTime
		birth                                                    time.Time
		applicantRaw, termsRaw, conditionsRaw, profileRaw, rules []byte
		premiumMinor                                             int64
	)
	err := row.Scan(&o.ID, &o.CustomerID, &channel, &quoteID, &o.ConsentID, &o.PartnerID, &o.ApplicationID,
		&applicantRaw, &birth, &o.AgeAtQuote, &termsRaw, &premiumMinor, &o.Currency, &conditionsRaw,
		&mode, &source, &reason, &profileDate, &unavailable, &profileRaw,
		&rules, &o.RaterVersion, &o.CreatedAt, &o.ExpiresAt, &supersedes, &renews,
		&owner, &supersededBy, &supersededAt, &renewedBy)
	if err != nil {
		return domain.PersonalizedOffer{}, err
	}

	var a applicantJSON
	var t termsJSON
	var p profileJSON
	var rs []ruleJSON
	for raw, into := range map[*[]byte]any{&applicantRaw: &a, &termsRaw: &t, &conditionsRaw: &o.Conditions, &profileRaw: &p, &rules: &rs} {
		if err := json.Unmarshal(*raw, into); err != nil {
			return domain.PersonalizedOffer{}, fmt.Errorf("decode the offer %s: %w", o.ID, err)
		}
	}

	o.Channel, o.Mode, o.ProfileSource = domain.OfferChannel(channel), domain.OfferMode(mode), domain.ProfileSource(source)
	o.QuoteID, o.ProvisionalReason, o.UnavailableSource = quoteID.String, reason.String, unavailable.String
	o.Supersedes, o.Renews = supersedes.String, renews.String
	o.OwnerID, o.SupersededBy, o.RenewedBy = owner.String, supersededBy.String, renewedBy.String
	o.BirthDate = birth.UTC()
	o.CreatedAt, o.ExpiresAt = o.CreatedAt.UTC(), o.ExpiresAt.UTC()
	if profileDate.Valid {
		o.ProfileDate = profileDate.Time.UTC()
	}
	if supersededAt.Valid {
		o.SupersededAt = supersededAt.Time.UTC()
	}
	o.Applicant = domain.Applicant{FullName: a.FullName, DocumentID: a.DocumentID, Email: a.Email}
	o.Premium = domain.FromMinorUnits(premiumMinor)
	o.Terms = domain.CoverageTerms{InsuredAmount: domain.FromMinorUnits(t.InsuredAmountMinor), TermMonths: t.TermMonths}
	for _, c := range t.Coverages {
		o.Terms.Coverages = append(o.Terms.Coverages, domain.Coverage{Code: c.Code, InsuredAmount: domain.FromMinorUnits(c.InsuredAmountMinor)})
	}
	o.Profile = domain.ProfileSnapshot{Score: p.Score, Category: p.Category, Partial: p.Partial}
	if p.CalculatedAt != nil {
		o.Profile.CalculatedAt = p.CalculatedAt.UTC()
	}
	for _, src := range p.Sources {
		o.Profile.Sources = append(o.Profile.Sources, domain.SourceOutcome{
			Source: src.Source, Status: domain.SourceStatus(src.Status), Latency: time.Duration(src.LatencyMs) * time.Millisecond,
		})
	}
	for _, r := range rs {
		o.Rules = append(o.Rules, domain.AppliedRule{Code: r.Code, Value: r.Value})
	}
	return o, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// validOfferID tells whether the id can name an offer. PostgreSQL rejects text
// with a NUL byte, which would otherwise surface as a server error for an id
// that simply does not exist.
func validOfferID(id string) bool { return id != "" && !strings.ContainsRune(id, 0) }
