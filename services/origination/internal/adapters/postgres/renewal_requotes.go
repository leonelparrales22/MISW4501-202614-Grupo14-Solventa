package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// RenewalRequoteStore is the ports.RenewalRequoteRepository over the
// renewal_requote table (HU-W1.5-03, migration 0014). The table is immutable:
// triggers reject UPDATE, DELETE and TRUNCATE.
type RenewalRequoteStore struct {
	db *sql.DB
}

var _ ports.RenewalRequoteRepository = (*RenewalRequoteStore)(nil)

// NewRenewalRequoteStore builds a RenewalRequoteStore over db (already migrated).
func NewRenewalRequoteStore(db *sql.DB) *RenewalRequoteStore { return &RenewalRequoteStore{db: db} }

const renewalRequoteColumns = `requote_id, policy_id, period_end, advisor_id, premium_minor, currency,
	insured_amount_minor, term_months, coverages, discount_rate, requoted, rating_version, applied_rules,
	mode, profile_source, profile_notice, profile_score, profile_category, profile_date, unavailable_source, sources,
	profile_customer_id, origin_channel, consent_basis, consent_id, consent_affirmed_by, consent_affirmed_at,
	consent_affirmed_by_email, consent_affirmed_by_name, created_at, expires_at`

const insertRenewalRequote = `INSERT INTO renewal_requote (` + renewalRequoteColumns + `)
	VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21,
		$22, $23, $24, $25, $26, $27, $28, $29, $30, $31)`

const selectRenewalRequote = `SELECT ` + renewalRequoteColumns + ` FROM renewal_requote WHERE requote_id = $1`

const selectLatestRenewalRequote = `SELECT ` + renewalRequoteColumns + ` FROM renewal_requote
	WHERE policy_id = $1 AND period_end = $2 ORDER BY created_at DESC, requote_id DESC LIMIT 1`

// Create stores the requote.
func (s *RenewalRequoteStore) Create(ctx context.Context, r domain.RenewalRequote) error {
	coverages, err := encodeCoverages(r.Terms.Coverages)
	if err != nil {
		return err
	}
	rules := make([]ruleJSON, 0, len(r.AppliedRules))
	for _, rule := range r.AppliedRules {
		rules = append(rules, ruleJSON{Code: rule.Code, Value: rule.Value})
	}
	rulesRaw, err := json.Marshal(rules)
	if err != nil {
		return err
	}
	sources := make([]sourceJSON, 0, len(r.Sources))
	for _, src := range r.Sources {
		entry := sourceJSON{Source: src.Source, Status: string(src.Status), LatencyMs: src.Latency.Milliseconds()}
		if !src.ConsultedAt.IsZero() {
			at := src.ConsultedAt.UTC()
			entry.ConsultedAt = &at
		}
		if src.Value != nil {
			score := src.Value.Score
			entry.Score, entry.Category = &score, src.Value.Category
		}
		sources = append(sources, entry)
	}
	sourcesRaw, err := json.Marshal(sources)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, insertRenewalRequote,
		r.ID, r.PolicyID, r.PeriodEnd.UTC(), r.AdvisorID, domain.ToMinorUnits(r.Premium), r.Currency,
		domain.ToMinorUnits(r.Terms.InsuredAmount), r.Terms.TermMonths, string(coverages), r.DiscountRate, r.Requoted,
		r.RatingVersion, string(rulesRaw),
		string(r.Mode), string(r.ProfileSource), string(r.ProfileNotice), r.ProfileScore, r.ProfileCategory,
		nullTime(r.ProfileDate), r.UnavailableSource, string(sourcesRaw),
		r.ProfileCustomerID, string(r.OriginChannel), nullString(string(r.ConsentBasis)), r.ConsentID,
		nullString(r.ConsentAffirmedBy), nullTime(r.ConsentAffirmedAt), r.ConsentAffirmedByEmail, r.ConsentAffirmedByName,
		r.CreatedAt.UTC(), r.ExpiresAt.UTC())
	if err != nil {
		return fmt.Errorf("store the requote %s: %w", r.ID, err)
	}
	return nil
}

// Get returns the requote with the given id.
func (s *RenewalRequoteStore) Get(ctx context.Context, requoteID string) (domain.RenewalRequote, error) {
	return s.one(ctx, selectRenewalRequote, requoteID)
}

// Latest returns the last requote of the period of a policy.
func (s *RenewalRequoteStore) Latest(ctx context.Context, policyID string, periodEnd time.Time) (domain.RenewalRequote, error) {
	return s.one(ctx, selectLatestRenewalRequote, policyID, periodEnd.UTC())
}

func (s *RenewalRequoteStore) one(ctx context.Context, query string, args ...any) (domain.RenewalRequote, error) {
	r, err := scanRenewalRequote(s.db.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.RenewalRequote{}, ports.ErrRenewalRequoteNotFound
	}
	if err != nil {
		return domain.RenewalRequote{}, fmt.Errorf("read the requote: %w", err)
	}
	return r, nil
}

func scanRenewalRequote(row scanner) (domain.RenewalRequote, error) {
	var (
		r                               domain.RenewalRequote
		premiumMinor, insuredMinor      int64
		coverages, rulesRaw, sourcesRaw []byte
		mode, source, notice, origin    string
		basis, affirmedBy               sql.NullString
		profileDate, affirmedAt         sql.NullTime
	)
	err := row.Scan(&r.ID, &r.PolicyID, &r.PeriodEnd, &r.AdvisorID, &premiumMinor, &r.Currency,
		&insuredMinor, &r.Terms.TermMonths, &coverages, &r.DiscountRate, &r.Requoted, &r.RatingVersion, &rulesRaw,
		&mode, &source, &notice, &r.ProfileScore, &r.ProfileCategory, &profileDate, &r.UnavailableSource, &sourcesRaw,
		&r.ProfileCustomerID, &origin, &basis, &r.ConsentID, &affirmedBy, &affirmedAt,
		&r.ConsentAffirmedByEmail, &r.ConsentAffirmedByName, &r.CreatedAt, &r.ExpiresAt)
	if err != nil {
		return domain.RenewalRequote{}, err
	}
	if r.Terms.Coverages, err = decodeCoverages(coverages); err != nil {
		return domain.RenewalRequote{}, fmt.Errorf("decode the coverages of requote %s: %w", r.ID, err)
	}
	var rules []ruleJSON
	var sources []sourceJSON
	if err := json.Unmarshal(rulesRaw, &rules); err != nil {
		return domain.RenewalRequote{}, fmt.Errorf("decode the rules of requote %s: %w", r.ID, err)
	}
	if err := json.Unmarshal(sourcesRaw, &sources); err != nil {
		return domain.RenewalRequote{}, fmt.Errorf("decode the sources of requote %s: %w", r.ID, err)
	}
	for _, rule := range rules {
		r.AppliedRules = append(r.AppliedRules, domain.AppliedRule{Code: rule.Code, Value: rule.Value})
	}
	for _, src := range sources {
		outcome := domain.SourceOutcome{
			Source: src.Source, Status: domain.SourceStatus(src.Status), Latency: time.Duration(src.LatencyMs) * time.Millisecond,
		}
		if src.ConsultedAt != nil {
			outcome.ConsultedAt = src.ConsultedAt.UTC()
		}
		if src.Score != nil {
			outcome.Value = &domain.SourceValue{Score: *src.Score, Category: src.Category}
		}
		r.Sources = append(r.Sources, outcome)
	}
	r.Premium, r.Terms.InsuredAmount = domain.FromMinorUnits(premiumMinor), domain.FromMinorUnits(insuredMinor)
	r.Mode, r.ProfileSource, r.ProfileNotice = domain.OfferMode(mode), domain.ProfileSource(source), domain.ProfileNotice(notice)
	r.OriginChannel, r.ConsentBasis, r.ConsentAffirmedBy = domain.OfferChannel(origin), domain.ConsentBasis(basis.String), affirmedBy.String
	if profileDate.Valid {
		r.ProfileDate = profileDate.Time.UTC()
	}
	if affirmedAt.Valid {
		r.ConsentAffirmedAt = affirmedAt.Time.UTC()
	}
	r.PeriodEnd, r.CreatedAt, r.ExpiresAt = r.PeriodEnd.UTC(), r.CreatedAt.UTC(), r.ExpiresAt.UTC()
	return r, nil
}
