package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

var _ ports.ParametricStore = (*ClaimRecordStore)(nil)

// Resolve files the claim of the parametric engine with the insert of every
// claim, and in the same transaction records its automatic resolution and its
// approval (HU-M2.4-01). The filing key of the observation finds a claim
// already resolved: then nothing is written and that claim is returned.
func (s *ClaimRecordStore) Resolve(ctx context.Context, c domain.ClaimRecord, r domain.ParametricResolution,
	d domain.Decision) (domain.ClaimRecord, bool, error) {
	c.Channel = domain.ChannelParametric
	stored, created, err := s.create(ctx, c, c.Fingerprint(), func(ctx context.Context, tx *sql.Tx, filed domain.ClaimRecord) error {
		t := r.Trigger
		if _, err := tx.ExecContext(ctx, `INSERT INTO claim_parametric_resolution (claim_id, observation_id,
				rule_version, parameter, event_reference, threshold, observed, unit, observed_at, resolved_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			filed.ID, r.ObservationID, r.RuleVersion, string(t.Parameter), t.EventReference, t.Threshold, t.Observed,
			t.Unit, t.ObservedAt.UTC(), r.ResolvedAt.UTC()); err != nil {
			return fmt.Errorf("record the automatic resolution of the claim %s: %w", filed.ID, err)
		}
		d.ClaimID, d.FilingNumber = filed.ID, filed.FilingNumber
		if _, err := decideIn(ctx, tx, d); err != nil {
			return fmt.Errorf("record the automatic approval of the claim %s: %w", filed.ID, err)
		}
		return nil
	})
	if err != nil {
		return domain.ClaimRecord{}, false, err
	}
	out, err := s.Get(ctx, stored.ID)
	if err != nil {
		return domain.ClaimRecord{}, false, err
	}
	return out, created, nil
}

// parametricOf reads the automatic resolution of a parametric claim; nil for
// the claims of the other channels.
func (s *ClaimRecordStore) parametricOf(ctx context.Context, c domain.ClaimRecord) (*domain.ParametricResolution, error) {
	if c.Channel != domain.ChannelParametric {
		return nil, nil
	}
	var (
		r         domain.ParametricResolution
		parameter string
	)
	err := s.db.QueryRowContext(ctx, `SELECT observation_id, rule_version, parameter, event_reference, threshold,
			observed, unit, observed_at, resolved_at
		FROM claim_parametric_resolution WHERE claim_id = $1`, c.ID).Scan(&r.ObservationID, &r.RuleVersion, &parameter,
		&r.Trigger.EventReference, &r.Trigger.Threshold, &r.Trigger.Observed, &r.Trigger.Unit, &r.Trigger.ObservedAt,
		&r.ResolvedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the automatic resolution of the claim %s: %w", c.ID, err)
	}
	r.Trigger.Parameter = domain.IncidentType(parameter)
	r.Trigger.ObservedAt, r.ResolvedAt = r.Trigger.ObservedAt.UTC(), r.ResolvedAt.UTC()
	return &r, nil
}
