package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// ResubmissionStore keeps the resubmissions of returned filings
// (HU-W2.1-04) in the database of the claims.
type ResubmissionStore struct {
	db *sql.DB
}

var _ ports.ResubmissionStore = (*ResubmissionStore)(nil)

// ResubmissionStoreOf builds the store over the database of the claims.
func ResubmissionStoreOf(records *ClaimRecordStore) *ResubmissionStore {
	return &ResubmissionStore{db: records.db}
}

type factChangeJSON struct {
	Fact   string `json:"fact"`
	Before string `json:"before"`
	After  string `json:"after"`
}

type supportChangeJSON struct {
	RequirementCode string   `json:"requirement_code"`
	Label           string   `json:"label"`
	Added           []string `json:"added"`
	Withdrawn       []string `json:"withdrawn"`
}

// Resubmit records the resubmission in one transaction. The claim is
// corrected and made active only while it is returned by the review the
// resubmission answers, which also locks it: of two resubmissions at once, the
// second finds it active.
func (s *ResubmissionStore) Resubmit(ctx context.Context, r domain.Resubmission) (domain.Resubmission, error) {
	facts, supports, err := encodeChanges(r)
	if err != nil {
		return domain.Resubmission{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Resubmission{}, fmt.Errorf("begin the resubmission: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	c := r.Claim
	var id string
	err = tx.QueryRowContext(ctx, `UPDATE claim SET occurred_at = $2, place = $3, description = $4,
			estimated_amount_minor = $5, status = $6
		WHERE id = $1 AND status = $7
		  AND (SELECT max(seq) FROM claim_review WHERE claim_id = $1) = $8
		RETURNING id`,
		r.ClaimID, c.OccurredAt.UTC(), c.Place, c.Description, nullInt64(c.EstimatedAmountMinor),
		string(domain.StatusActive), string(domain.StatusReturned), r.ReviewSeq).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		_ = tx.Rollback()
		if err := (&DecisionStore{db: s.db}).claimExists(ctx, r.ClaimID); err != nil {
			return domain.Resubmission{}, err
		}
		return domain.Resubmission{}, domain.ErrNotReturned
	}
	if err != nil {
		return domain.Resubmission{}, fmt.Errorf("correct the claim %s: %w", r.ClaimID, err)
	}

	err = tx.QueryRowContext(ctx, `INSERT INTO claim_resubmission (claim_id, seq, review_seq, resubmitted_by,
			resubmitted_at, changed_facts, changed_supports)
		VALUES ($1, COALESCE((SELECT max(seq) FROM claim_resubmission WHERE claim_id = $1), 0) + 1, $2, $3, $4, $5, $6)
		RETURNING seq`,
		r.ClaimID, r.ReviewSeq, r.ResubmittedBy, r.ResubmittedAt.UTC(), facts, supports).Scan(&r.Seq)
	switch {
	case isUniqueViolation(err):
		return domain.Resubmission{}, domain.ErrNotReturned
	case err != nil:
		return domain.Resubmission{}, fmt.Errorf("record the resubmission of the claim %s: %w", r.ClaimID, err)
	}

	for _, a := range r.Added {
		if _, err := tx.ExecContext(ctx, `INSERT INTO claim_support (claim_id, position, requirement_code, support_id)
			VALUES ($1, $2, $3, $4)`, r.ClaimID, a.Position, a.RequirementCode, a.File.ID); err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && pgErr.ConstraintName == "claim_support_once" {
				return domain.Resubmission{}, ports.ErrSupportAlreadyAttached
			}
			return domain.Resubmission{}, fmt.Errorf("attach the support %s: %w", a.File.ID, err)
		}
	}
	for _, ch := range r.Supports {
		for _, id := range ch.Withdrawn {
			if _, err := tx.ExecContext(ctx, `INSERT INTO claim_support_withdrawal (support_id, claim_id, resubmission_seq)
				VALUES ($1, $2, $3)`, id, r.ClaimID, r.Seq); err != nil {
				return domain.Resubmission{}, fmt.Errorf("withdraw the support %s: %w", id, err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return domain.Resubmission{}, fmt.Errorf("commit the resubmission of the claim %s: %w", r.ClaimID, err)
	}
	return r, nil
}

// Resubmissions returns the resubmissions of a claim, oldest first.
func (s *ResubmissionStore) Resubmissions(ctx context.Context, claimID string) ([]domain.Resubmission, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT r.claim_id, c.filing_number, r.seq, r.review_seq, r.resubmitted_by,
			r.resubmitted_at, r.changed_facts, r.changed_supports
		FROM claim_resubmission r JOIN claim c ON c.id = r.claim_id
		WHERE r.claim_id = $1 ORDER BY r.seq`, claimID)
	if err != nil {
		return nil, fmt.Errorf("read the resubmissions of the claim %s: %w", claimID, err)
	}
	defer func() { _ = rows.Close() }()
	var out []domain.Resubmission
	for rows.Next() {
		var (
			r               domain.Resubmission
			facts, supports []byte
		)
		if err := rows.Scan(&r.ClaimID, &r.FilingNumber, &r.Seq, &r.ReviewSeq, &r.ResubmittedBy, &r.ResubmittedAt,
			&facts, &supports); err != nil {
			return nil, fmt.Errorf("read a resubmission of the claim %s: %w", claimID, err)
		}
		r.ResubmittedAt = r.ResubmittedAt.UTC()
		if err := decodeChanges(&r, facts, supports); err != nil {
			return nil, fmt.Errorf("decode a resubmission of the claim %s: %w", claimID, err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func encodeChanges(r domain.Resubmission) (facts, supports []byte, err error) {
	fs := make([]factChangeJSON, 0, len(r.Facts))
	for _, f := range r.Facts {
		fs = append(fs, factChangeJSON{Fact: string(f.Fact), Before: f.Before, After: f.After})
	}
	ss := make([]supportChangeJSON, 0, len(r.Supports))
	for _, c := range r.Supports {
		ss = append(ss, supportChangeJSON{RequirementCode: c.RequirementCode, Label: c.Label,
			Added: nonNil(c.Added), Withdrawn: nonNil(c.Withdrawn)})
	}
	if facts, err = json.Marshal(fs); err != nil {
		return nil, nil, fmt.Errorf("encode the facts changed: %w", err)
	}
	if supports, err = json.Marshal(ss); err != nil {
		return nil, nil, fmt.Errorf("encode the supports changed: %w", err)
	}
	return facts, supports, nil
}

func decodeChanges(r *domain.Resubmission, facts, supports []byte) error {
	var fs []factChangeJSON
	if err := json.Unmarshal(facts, &fs); err != nil {
		return err
	}
	for _, f := range fs {
		r.Facts = append(r.Facts, domain.FactChange{Fact: domain.Fact(f.Fact), Before: f.Before, After: f.After})
	}
	var ss []supportChangeJSON
	if err := json.Unmarshal(supports, &ss); err != nil {
		return err
	}
	for _, c := range ss {
		r.Supports = append(r.Supports, domain.SupportChange{RequirementCode: c.RequirementCode, Label: c.Label,
			Added: c.Added, Withdrawn: c.Withdrawn})
	}
	return nil
}
