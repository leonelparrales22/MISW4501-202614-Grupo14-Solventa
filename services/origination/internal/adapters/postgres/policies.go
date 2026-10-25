package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// PolicyStore is the ports.PolicyRepository over the policy table
// (HU-W1.5-02, migration 0012). A policy is written once, from the activation
// of its coverage, and never changes.
type PolicyStore struct {
	db *sql.DB
}

var _ ports.PolicyRepository = (*PolicyStore)(nil)

// NewPolicyStore builds a PolicyStore over db (already migrated).
func NewPolicyStore(db *sql.DB) *PolicyStore { return &PolicyStore{db: db} }

const policyColumns = `policy_id, acceptance_id, customer_id, holder_full_name, holder_document_id, holder_email,
	premium_minor, currency, insured_amount_minor, coverages, coverage_start, coverage_end, renewal_of`

func scanPolicy(s scanner) (domain.Policy, error) {
	var (
		p                          domain.Policy
		premiumMinor, insuredMinor int64
		coverages                  []byte
		renewalOf                  sql.NullString
	)
	if err := s.Scan(&p.ID, &p.AcceptanceID, &p.CustomerID, &p.Holder.FullName, &p.Holder.DocumentID, &p.Holder.Email,
		&premiumMinor, &p.Currency, &insuredMinor, &coverages, &p.CoverageStart, &p.CoverageEnd, &renewalOf); err != nil {
		return domain.Policy{}, err
	}
	var err error
	if p.Coverages, err = decodeCoverages(coverages); err != nil {
		return domain.Policy{}, fmt.Errorf("decode the coverages of policy %s: %w", p.ID, err)
	}
	p.Premium, p.InsuredAmount = domain.FromMinorUnits(premiumMinor), domain.FromMinorUnits(insuredMinor)
	p.RenewalOf = renewalOf.String
	p.CoverageStart, p.CoverageEnd = p.CoverageStart.UTC(), p.CoverageEnd.UTC()
	return p, nil
}

// Create stores the policy; a policy that already exists is left as it is.
func (s *PolicyStore) Create(ctx context.Context, p domain.Policy) error {
	coverages, err := encodeCoverages(p.Coverages)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO policy (`+policyColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (policy_id) DO NOTHING`,
		p.ID, p.AcceptanceID, p.CustomerID, p.Holder.FullName, p.Holder.DocumentID, p.Holder.Email,
		domain.ToMinorUnits(p.Premium), p.Currency, domain.ToMinorUnits(p.InsuredAmount), string(coverages),
		p.CoverageStart.UTC(), p.CoverageEnd.UTC(), nullString(p.RenewalOf))
	if err != nil {
		return fmt.Errorf("store policy %s: %w", p.ID, err)
	}
	return nil
}

// Get returns ports.ErrPolicyNotFound when the id is unknown.
func (s *PolicyStore) Get(ctx context.Context, policyID string) (domain.Policy, error) {
	p, err := scanPolicy(s.db.QueryRowContext(ctx, `SELECT `+policyColumns+` FROM policy WHERE policy_id = $1`, policyID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Policy{}, ports.ErrPolicyNotFound
	}
	if err != nil {
		return domain.Policy{}, fmt.Errorf("read policy %s: %w", policyID, err)
	}
	return p, nil
}

func (s *PolicyStore) list(ctx context.Context, query string, args ...any) ([]domain.Policy, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list policies: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []domain.Policy
	for rows.Next() {
		p, err := scanPolicy(rows)
		if err != nil {
			return nil, fmt.Errorf("read a policy: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ListByCustomer returns the policies of the insured, the ones ending last first.
func (s *PolicyStore) ListByCustomer(ctx context.Context, customerID string) ([]domain.Policy, error) {
	return s.list(ctx, `SELECT `+policyColumns+` FROM policy WHERE customer_id = $1 ORDER BY coverage_end DESC, policy_id`, customerID)
}

// ListLapsed returns the policies that ended with no renewal row for their period.
func (s *PolicyStore) ListLapsed(ctx context.Context, since, now time.Time, limit int) ([]domain.Policy, error) {
	return s.list(ctx, `SELECT `+prefixed("p", policyColumns)+` FROM policy p
		WHERE p.coverage_end BETWEEN $1 AND $2
		  AND NOT EXISTS (SELECT 1 FROM renewal r WHERE r.policy_id = p.policy_id AND r.period_end = p.coverage_end)
		ORDER BY p.coverage_end LIMIT $3`, since.UTC(), now.UTC(), limit)
}

// prefixed qualifies every column of a comma-separated list with a table alias.
func prefixed(alias, columns string) string {
	parts := strings.Split(columns, ",")
	for i, c := range parts {
		parts[i] = alias + "." + strings.TrimSpace(c)
	}
	return strings.Join(parts, ", ")
}
