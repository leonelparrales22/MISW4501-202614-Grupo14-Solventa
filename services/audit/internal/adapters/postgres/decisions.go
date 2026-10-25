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

// DecisionStore implements ports.DecisionStore over the underwriting_decision
// table (HU-W4.1-01). The table is append-only: its triggers reject UPDATE,
// DELETE and TRUNCATE, and this adapter has no statement that issues them.
type DecisionStore struct {
	db *sql.DB
}

// NewDecisionStore builds the store over an opened and migrated database.
func NewDecisionStore(db *sql.DB) *DecisionStore { return &DecisionStore{db: db} }

var _ ports.DecisionStore = (*DecisionStore)(nil)

const decisionColumns = `offer_id, customer_id, partner_id, application_id, channel, requested_at, rater_version,
	sources, profile_score, profile_category, profile_partial, rules, premium, currency, decision,
	decision_reason, recorded_at`

// decisionSelect reads premium as a float, as the domain keeps it.
const decisionSelect = `offer_id, customer_id, partner_id, application_id, channel, requested_at, rater_version,
	sources, profile_score, profile_category, profile_partial, rules, premium::float8, currency, decision,
	decision_reason, recorded_at`

type sourceEntry struct {
	Source    string `json:"source"`
	Status    string `json:"status"`
	LatencyMS int64  `json:"latency_ms"`
}

type ruleEntry struct {
	Order int     `json:"order"`
	Code  string  `json:"code"`
	Value float64 `json:"value"`
}

// Create inserts the record, or returns the one already stored for the offer
// with created=false.
func (s *DecisionStore) Create(ctx context.Context, r domain.DecisionRecord) (domain.DecisionRecord, bool, error) {
	sources := make([]sourceEntry, len(r.Sources))
	for i, src := range r.Sources {
		sources[i] = sourceEntry{Source: src.Source, Status: src.Status, LatencyMS: src.LatencyMS}
	}
	rules := make([]ruleEntry, len(r.Rules))
	for i, rule := range r.Rules {
		rules[i] = ruleEntry{Order: rule.Order, Code: rule.Code, Value: rule.Value}
	}
	rawSources, err := json.Marshal(sources)
	if err != nil {
		return domain.DecisionRecord{}, false, fmt.Errorf("encode the sources: %w", err)
	}
	rawRules, err := json.Marshal(rules)
	if err != nil {
		return domain.DecisionRecord{}, false, fmt.Errorf("encode the rules: %w", err)
	}

	res, err := s.db.ExecContext(ctx, `INSERT INTO underwriting_decision (`+decisionColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
		ON CONFLICT (offer_id) DO NOTHING`,
		r.OfferID, r.CustomerID, r.PartnerID, r.ApplicationID, r.Channel, r.RequestedAt, r.RaterVersion,
		rawSources, r.ProfileScore, r.ProfileCategory, r.ProfilePartial, rawRules, r.Premium, r.Currency,
		string(r.Decision), r.DecisionReason, r.RecordedAt)
	if err != nil {
		return domain.DecisionRecord{}, false, fmt.Errorf("insert the decision: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 1 {
		return r, true, nil
	}
	existing, err := s.Get(ctx, r.OfferID)
	if err != nil {
		return domain.DecisionRecord{}, false, fmt.Errorf("read the existing decision: %w", err)
	}
	return existing, false, nil
}

// Get returns the record of the offer, or domain.ErrDecisionNotFound.
func (s *DecisionStore) Get(ctx context.Context, offerID string) (domain.DecisionRecord, error) {
	var (
		r                    domain.DecisionRecord
		rawSources, rawRules []byte
		decision             string
	)
	err := s.db.QueryRowContext(ctx, `SELECT `+decisionSelect+` FROM underwriting_decision WHERE offer_id = $1`, offerID).Scan(
		&r.OfferID, &r.CustomerID, &r.PartnerID, &r.ApplicationID, &r.Channel, &r.RequestedAt, &r.RaterVersion,
		&rawSources, &r.ProfileScore, &r.ProfileCategory, &r.ProfilePartial, &rawRules, &r.Premium, &r.Currency,
		&decision, &r.DecisionReason, &r.RecordedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.DecisionRecord{}, domain.ErrDecisionNotFound
	}
	if err != nil {
		return domain.DecisionRecord{}, err
	}
	var sources []sourceEntry
	if err := json.Unmarshal(rawSources, &sources); err != nil {
		return domain.DecisionRecord{}, fmt.Errorf("decode the sources: %w", err)
	}
	var rules []ruleEntry
	if err := json.Unmarshal(rawRules, &rules); err != nil {
		return domain.DecisionRecord{}, fmt.Errorf("decode the rules: %w", err)
	}
	r.Decision = domain.Decision(decision)
	r.RequestedAt, r.RecordedAt = r.RequestedAt.UTC(), r.RecordedAt.UTC()
	for _, s := range sources {
		r.Sources = append(r.Sources, domain.SourceConsulted{Source: s.Source, Status: s.Status, LatencyMS: s.LatencyMS})
	}
	for _, rule := range rules {
		r.Rules = append(r.Rules, domain.RuleApplied{Order: rule.Order, Code: rule.Code, Value: rule.Value})
	}
	return r, nil
}
