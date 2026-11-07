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

// ReviewStore is the ports.ReviewStore and the ports.StatusHistory over the
// claim_review and claim_status_change tables (HU-W2.1-03). The history is
// written by a trigger of the claim table, so this store only reads it.
type ReviewStore struct {
	db *sql.DB
}

var (
	_ ports.ReviewStore   = (*ReviewStore)(nil)
	_ ports.StatusHistory = (*ReviewStore)(nil)
)

// ReviewStoreOf builds the store over the database of the claims, so the
// composition root does not open a second pool.
func ReviewStoreOf(records *ClaimRecordStore) *ReviewStore { return &ReviewStore{db: records.db} }

// assessmentJSON is one verdict as claim_review.supports keeps it.
type assessmentJSON struct {
	RequirementCode string `json:"requirement_code"`
	Label           string `json:"label"`
	Verdict         string `json:"verdict"`
	Note            string `json:"note,omitempty"`
}

// Review records the review and moves the claim in the same statement,
// conditionally on the claim waiting for review: of two advisors reviewing at
// once, the second finds the claim reviewed. The primary key (claim_id, seq)
// stops two reviews of the same round as well.
func (s *ReviewStore) Review(ctx context.Context, r domain.Review) (domain.Review, error) {
	assessments := make([]assessmentJSON, 0, len(r.Supports))
	for _, a := range r.Supports {
		assessments = append(assessments, assessmentJSON{RequirementCode: a.RequirementCode, Label: a.Label,
			Verdict: string(a.Verdict), Note: a.Note})
	}
	supports, err := json.Marshal(assessments)
	if err != nil {
		return domain.Review{}, fmt.Errorf("encode the verdicts of the supports: %w", err)
	}
	facts, err := json.Marshal(nonNil(r.FactsToCorrect))
	if err != nil {
		return domain.Review{}, fmt.Errorf("encode the facts to correct: %w", err)
	}
	var seq int
	err = s.db.QueryRowContext(ctx, `WITH target AS (
			UPDATE claim SET status = $2 WHERE id = $1 AND status = $3 RETURNING id
		)
		INSERT INTO claim_review (claim_id, seq, outcome, reviewed_by, reviewer_name, reviewed_at, supports,
			reason_code, message, facts_to_correct)
		SELECT id, COALESCE((SELECT max(seq) FROM claim_review WHERE claim_id = $1), 0) + 1,
			$4, $5, $6, $7, $8, $9, $10, $11 FROM target
		RETURNING seq`,
		r.ClaimID, string(r.StatusOf()), string(domain.StatusActive), string(r.Outcome), r.ReviewedBy,
		r.ReviewerName, r.ReviewedAt.UTC(), supports, optional(string(r.ReasonCode)), optional(r.Message),
		facts).Scan(&seq)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if err := (&DecisionStore{db: s.db}).claimExists(ctx, r.ClaimID); err != nil {
			return domain.Review{}, err
		}
		return domain.Review{}, domain.ErrNotReviewable
	case isUniqueViolation(err):
		return domain.Review{}, domain.ErrNotReviewable
	case err != nil:
		return domain.Review{}, fmt.Errorf("record the review of the claim %s: %w", r.ClaimID, err)
	}
	r.Seq = seq
	return r, nil
}

// Reviews returns the reviews of a claim, oldest first.
func (s *ReviewStore) Reviews(ctx context.Context, claimID string) ([]domain.Review, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT r.claim_id, c.filing_number, r.seq, r.outcome, r.reviewed_by,
			r.reviewer_name, r.reviewed_at, r.supports, r.reason_code, r.message, r.facts_to_correct
		FROM claim_review r JOIN claim c ON c.id = r.claim_id
		WHERE r.claim_id = $1 ORDER BY r.seq`, claimID)
	if err != nil {
		return nil, fmt.Errorf("read the reviews of the claim %s: %w", claimID, err)
	}
	defer func() { _ = rows.Close() }()
	var out []domain.Review
	for rows.Next() {
		var (
			r               domain.Review
			outcome         string
			reason, message sql.NullString
			supports, facts []byte
			assessments     []assessmentJSON
		)
		if err := rows.Scan(&r.ClaimID, &r.FilingNumber, &r.Seq, &outcome, &r.ReviewedBy, &r.ReviewerName,
			&r.ReviewedAt, &supports, &reason, &message, &facts); err != nil {
			return nil, fmt.Errorf("read a review of the claim %s: %w", claimID, err)
		}
		r.Outcome, r.ReviewedAt = domain.ReviewOutcome(outcome), r.ReviewedAt.UTC()
		r.ReasonCode, r.Message = domain.ReasonCode(reason.String), message.String
		if err := json.Unmarshal(supports, &assessments); err != nil {
			return nil, fmt.Errorf("decode the verdicts of the claim %s: %w", claimID, err)
		}
		for _, a := range assessments {
			r.Supports = append(r.Supports, domain.SupportAssessment{RequirementCode: a.RequirementCode,
				Label: a.Label, Verdict: domain.Verdict(a.Verdict), Note: a.Note})
		}
		if err := json.Unmarshal(facts, &r.FactsToCorrect); err != nil {
			return nil, fmt.Errorf("decode the facts to correct of the claim %s: %w", claimID, err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// History returns the changes of status of a claim, oldest first.
func (s *ReviewStore) History(ctx context.Context, claimID string) ([]domain.StatusChange, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, claim_id, status, previous_status, changed_at
		FROM claim_status_change WHERE claim_id = $1 ORDER BY id`, claimID)
	if err != nil {
		return nil, fmt.Errorf("read the history of the claim %s: %w", claimID, err)
	}
	defer func() { _ = rows.Close() }()
	var out []domain.StatusChange
	for rows.Next() {
		var (
			c        domain.StatusChange
			status   string
			previous sql.NullString
		)
		if err := rows.Scan(&c.ID, &c.ClaimID, &status, &previous, &c.ChangedAt); err != nil {
			return nil, fmt.Errorf("read a change of the claim %s: %w", claimID, err)
		}
		c.Status, c.PreviousStatus, c.ChangedAt = domain.Status(status), domain.Status(previous.String), c.ChangedAt.UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}

// isUniqueViolation tells whether err is a duplicate key: another review of
// the same round won the race.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolation
}

// nonNil keeps an empty list as [] in the JSON, never null.
func nonNil[T any](list []T) []T {
	if list == nil {
		return []T{}
	}
	return list
}
