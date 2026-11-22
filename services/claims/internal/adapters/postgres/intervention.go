package postgres

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// InterventionStore keeps the steps of the intervention of the provider and
// the closing of the case (HU-W2.3-02) in tables that only grow.
type InterventionStore struct {
	db *sql.DB
}

// NewInterventionStore builds the store over an open, migrated database.
func NewInterventionStore(db *sql.DB) *InterventionStore { return &InterventionStore{db: db} }

// InterventionStoreOf builds the store over the database of the claims, so
// the composition root does not open a second pool.
func InterventionStoreOf(records *ClaimRecordStore) *InterventionStore {
	return &InterventionStore{db: records.db}
}

var _ ports.InterventionStore = (*InterventionStore)(nil)

const stepColumns = `claim_id, step, position, occurred_at, note, cause, damage, repair_cost_minor, repair_days,
	photos, recorded_by, recorder_name, recorded_at`

// Intervention returns the claim with its assignment, its steps and its
// closing, read in one snapshot.
func (s *InterventionStore) Intervention(ctx context.Context, claimID string) (ports.InterventionCase, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return ports.InterventionCase{}, fmt.Errorf("read the intervention of the claim %s: %w", claimID, err)
	}
	defer func() { _ = tx.Rollback() }()

	ids := []string{claimID}
	cases, err := claimsByID(ctx, tx, ids)
	if err != nil {
		return ports.InterventionCase{}, err
	}
	k, ok := cases[claimID]
	if !ok {
		return ports.InterventionCase{}, ports.ErrClaimNotFound
	}
	if err := assignmentsByClaim(ctx, tx, ids, cases); err != nil {
		return ports.InterventionCase{}, err
	}
	out := ports.InterventionCase{Claim: k.Claim, Assignment: k.Assignment, Steps: []domain.StepRecord{}}
	if out.Steps, err = stepsOf(ctx, tx, claimID); err != nil {
		return ports.InterventionCase{}, err
	}
	if out.Closure, err = closureOf(ctx, tx, claimID); err != nil {
		return ports.InterventionCase{}, err
	}
	if err := tx.Commit(); err != nil {
		return ports.InterventionCase{}, fmt.Errorf("read the intervention of the claim %s: %w", claimID, err)
	}
	return out, nil
}

func stepsOf(ctx context.Context, tx *sql.Tx, claimID string) ([]domain.StepRecord, error) {
	rows, err := tx.QueryContext(ctx, `SELECT `+stepColumns+` FROM claim_intervention_step
		WHERE claim_id = $1 ORDER BY position`, claimID)
	if err != nil {
		return nil, fmt.Errorf("read the steps of the intervention: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []domain.StepRecord{}
	for rows.Next() {
		var (
			r             domain.StepRecord
			step          string
			position      int
			cause, damage sql.NullString
			cost          sql.NullInt64
			days, photos  sql.NullInt32
		)
		if err := rows.Scan(&r.ClaimID, &step, &position, &r.OccurredAt, &r.Note, &cause, &damage, &cost, &days,
			&photos, &r.RecordedBy.Email, &r.RecordedBy.Name, &r.RecordedAt); err != nil {
			return nil, fmt.Errorf("read the steps of the intervention: %w", err)
		}
		r.Step, r.OccurredAt, r.RecordedAt = domain.InterventionStep(step), r.OccurredAt.UTC(), r.RecordedAt.UTC()
		if cause.Valid {
			r.Report = &domain.ProviderReport{Cause: cause.String, Damage: damage.String, RepairCostMinor: cost.Int64,
				RepairDays: int(days.Int32), Photos: int(photos.Int32)}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func closureOf(ctx context.Context, tx *sql.Tx, claimID string) (*domain.Closure, error) {
	var (
		c      domain.Closure
		result string
	)
	err := tx.QueryRowContext(ctx, `SELECT claim_id, result, closed_at, notes, closed_by, closer_name, recorded_at
		FROM claim_closure WHERE claim_id = $1`, claimID).Scan(&c.ClaimID, &result, &c.ClosedAt, &c.Notes,
		&c.ClosedBy.Email, &c.ClosedBy.Name, &c.RecordedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the closing of the case: %w", err)
	}
	c.Result, c.ClosedAt, c.RecordedAt = domain.ClosingResult(result), c.ClosedAt.UTC(), c.RecordedAt.UTC()
	return &c, nil
}

// positionOf is the place of the step in the intervention, from 1.
func positionOf(step domain.InterventionStep) int {
	for i, s := range domain.InterventionSteps {
		if s == step {
			return i + 1
		}
	}
	return 0
}

// RecordStep adds the step after the one before it, unless the case was
// closed meanwhile. The primary key and the unique position settle two
// analysts recording the same step at once.
func (s *InterventionStore) RecordStep(ctx context.Context, r domain.StepRecord) error {
	var (
		cause, damage       sql.NullString
		cost                sql.NullInt64
		days, photos        sql.NullInt32
		position            = positionOf(r.Step)
		previous            string
		previousRequirement = position == 1
	)
	if position > 1 {
		previous = string(domain.InterventionSteps[position-2])
	}
	if p := r.Report; p != nil {
		cause, damage = sql.NullString{String: p.Cause, Valid: true}, sql.NullString{String: p.Damage, Valid: true}
		cost = sql.NullInt64{Int64: p.RepairCostMinor, Valid: true}
		days = sql.NullInt32{Int32: int32(p.RepairDays), Valid: true} //nolint:gosec // bounded by the domain (365)
		photos = sql.NullInt32{Int32: int32(p.Photos), Valid: true}   //nolint:gosec // bounded by the domain (50)
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO claim_intervention_step (`+stepColumns+`)
		SELECT $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13
		WHERE NOT EXISTS (SELECT 1 FROM claim_closure WHERE claim_id = $1)
		  AND ($14 OR EXISTS (SELECT 1 FROM claim_intervention_step WHERE claim_id = $1 AND step = $15))`,
		r.ClaimID, string(r.Step), position, r.OccurredAt.UTC(), r.Note, cause, damage, cost, days, photos,
		r.RecordedBy.Email, r.RecordedBy.Name, r.RecordedAt.UTC(), previousRequirement, previous)
	if isConstraint(err, "claim_intervention_step_pkey") ||
		isConstraint(err, "claim_intervention_step_claim_id_position_key") {
		return ports.ErrStepConflict
	}
	if err != nil {
		return fmt.Errorf("record the step %s of the claim %s: %w", r.Step, r.ClaimID, err)
	}
	if rows, err := res.RowsAffected(); err != nil || rows == 0 {
		if err != nil {
			return fmt.Errorf("record the step %s of the claim %s: %w", r.Step, r.ClaimID, err)
		}
		var closed bool
		if err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM claim_closure WHERE claim_id = $1)`,
			r.ClaimID).Scan(&closed); err != nil {
			return fmt.Errorf("record the step %s of the claim %s: %w", r.Step, r.ClaimID, err)
		}
		if closed {
			return domain.ErrCaseClosed
		}
		return ports.ErrStepConflict
	}
	return nil
}

// Close records the closing and releases the provider busy with the claim in
// one transaction. The primary key of claim_closure settles two analysts
// closing at once.
func (s *InterventionStore) Close(ctx context.Context, c domain.Closure) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("close the case %s: %w", c.ClaimID, err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `INSERT INTO claim_closure (claim_id, result, closed_at, notes, closed_by,
			closer_name, recorded_at)
		SELECT $1, $2, $3, $4, $5, $6, $7
		WHERE EXISTS (SELECT 1 FROM claim_intervention_step WHERE claim_id = $1 AND step = $8)`,
		c.ClaimID, string(c.Result), c.ClosedAt.UTC(), c.Notes, c.ClosedBy.Email, c.ClosedBy.Name,
		c.RecordedAt.UTC(), string(domain.StepRepairExecuted))
	if isConstraint(err, "claim_closure_pkey") {
		return domain.ErrCaseClosed
	}
	if err != nil {
		return fmt.Errorf("close the case %s: %w", c.ClaimID, err)
	}
	if rows, err := res.RowsAffected(); err != nil || rows == 0 {
		if err != nil {
			return fmt.Errorf("close the case %s: %w", c.ClaimID, err)
		}
		return domain.ErrInterventionNotFinished
	}
	// The provider is free for another claim once its intervention is closed.
	// Operations may have released it by hand already: nothing to do then.
	if _, err := tx.ExecContext(ctx, `UPDATE provider SET availability = 'available', busy_claim_id = NULL,
			unavailable_until = NULL, updated_by = $2, updated_at = $3
		WHERE busy_claim_id = $1`, c.ClaimID, c.ClosedBy.Email, c.RecordedAt.UTC()); err != nil {
		return fmt.Errorf("release the provider of the case %s: %w", c.ClaimID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("close the case %s: %w", c.ClaimID, err)
	}
	return nil
}

// InterventionStoreOver builds the store over the database of the
// assignments it continues.
func InterventionStoreOver(assignments *AssignmentStore) *InterventionStore {
	return &InterventionStore{db: assignments.db}
}
