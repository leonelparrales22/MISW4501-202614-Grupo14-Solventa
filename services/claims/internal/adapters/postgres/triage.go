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

// TriageStore keeps the calculations of the level of the claims in
// claim_triage (HU-W2.2-01), a table that only grows.
type TriageStore struct {
	db *sql.DB
}

// NewTriageStore builds the store over an open, migrated database.
func NewTriageStore(db *sql.DB) *TriageStore { return &TriageStore{db: db} }

var _ ports.TriageStore = (*TriageStore)(nil)

const triageColumns = `claim_id, seq, level, severity, amount_band, estimated_amount_minor, incident_type,
	policy_line, coverage, policy_version, input_fingerprint, target_kind, target_business_days,
	resolution_business_days, causes, previous_level, actor, assessed_at`

// Pending returns the claims to assess: in one of the statuses, without a
// calculation or with another amount, claim type or policy version than their
// last one. The coverage and the line follow the claim type (assumption S4),
// so comparing the type covers them.
func (s *TriageStore) Pending(ctx context.Context, statuses []domain.Status, policyVersion string,
	limit int) ([]ports.TriageCandidate, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+recordColumns+` FROM claim WHERE id IN (
			SELECT c.id FROM claim c LEFT JOIN claim_current_triage t ON t.claim_id = c.id
			WHERE c.status = ANY($1::text[])
			  AND (t.claim_id IS NULL
			       OR t.policy_version <> $2
			       OR t.incident_type <> c.incident_type
			       OR t.estimated_amount_minor IS DISTINCT FROM c.estimated_amount_minor)
			ORDER BY c.filed_at, c.id
			LIMIT $3)
		ORDER BY filed_at, id`, statusNames(statuses), policyVersion, limit)
	if err != nil {
		return nil, fmt.Errorf("read the claims to assess: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var claims []domain.ClaimRecord
	for rows.Next() {
		c, _, err := scanRecord(rows)
		if err != nil {
			return nil, fmt.Errorf("read the claims to assess: %w", err)
		}
		claims = append(claims, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the claims to assess: %w", err)
	}
	if len(claims) == 0 {
		return nil, nil
	}

	ids := make([]string, len(claims))
	for i, c := range claims {
		ids[i] = c.ID
	}
	last, err := s.lastOf(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]ports.TriageCandidate, len(claims))
	for i, c := range claims {
		out[i] = ports.TriageCandidate{Claim: c, Last: last[c.ID]}
	}
	return out, nil
}

// Candidate returns the claim and its last calculation.
func (s *TriageStore) Candidate(ctx context.Context, claimID string) (ports.TriageCandidate, error) {
	c, _, err := scanRecord(s.db.QueryRowContext(ctx, `SELECT `+recordColumns+` FROM claim WHERE id = $1`, claimID))
	if errors.Is(err, sql.ErrNoRows) {
		return ports.TriageCandidate{}, ports.ErrClaimNotFound
	}
	if err != nil {
		return ports.TriageCandidate{}, fmt.Errorf("read the claim to assess: %w", err)
	}
	last, err := s.lastOf(ctx, []string{claimID})
	if err != nil {
		return ports.TriageCandidate{}, err
	}
	return ports.TriageCandidate{Claim: c, Last: last[claimID]}, nil
}

// Record adds the calculation. The primary key (claim_id, seq) settles two
// tasks that assess the same claim at the same time: the second one gets
// ErrTriageConflict.
func (s *TriageStore) Record(ctx context.Context, r domain.TriageRecord) error {
	causes, err := json.Marshal(r.Causes)
	if err != nil {
		return fmt.Errorf("encode the causes of the triage: %w", err)
	}
	a, in := r.Assessment, r.Assessment.Input
	var previous any
	if r.PreviousLevel != nil {
		previous = int(*r.PreviousLevel)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO claim_triage (`+triageColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)`,
		r.ClaimID, r.Seq, int(a.Level), string(a.Severity), string(a.AmountBand), nullInt64(in.EstimatedAmountMinor),
		string(in.IncidentType), string(in.Line), in.Coverage, in.PolicyVersion, in.Fingerprint(), string(a.Target.Kind),
		a.Target.BusinessDays, a.Target.ResolutionBusinessDays, causes, previous, r.Actor, r.AssessedAt)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && pgErr.ConstraintName == "claim_triage_pkey" {
		return ports.ErrTriageConflict
	}
	if err != nil {
		return fmt.Errorf("record the triage of the claim: %w", err)
	}
	return nil
}

// History returns every calculation of the claim, newest first.
func (s *TriageStore) History(ctx context.Context, claimID string) ([]domain.TriageRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+triageColumns+` FROM claim_triage
		WHERE claim_id = $1 ORDER BY seq DESC`, claimID)
	if err != nil {
		return nil, fmt.Errorf("read the triage of the claim: %w", err)
	}
	return scanTriages(rows)
}

// Current returns the status and the last calculation of the claims that
// exist.
func (s *TriageStore) Current(ctx context.Context, ids []string) (map[string]ports.TriageCandidate, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, status FROM claim WHERE id = ANY($1::text[])`, ids)
	if err != nil {
		return nil, fmt.Errorf("read the claims of the triage: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := map[string]ports.TriageCandidate{}
	for rows.Next() {
		var c domain.ClaimRecord
		var status string
		if err := rows.Scan(&c.ID, &status); err != nil {
			return nil, fmt.Errorf("read the claims of the triage: %w", err)
		}
		c.Status = domain.Status(status)
		out[c.ID] = ports.TriageCandidate{Claim: c}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the claims of the triage: %w", err)
	}
	if len(out) == 0 {
		return out, nil
	}

	found := make([]string, 0, len(out))
	for id := range out {
		found = append(found, id)
	}
	last, err := s.lastOf(ctx, found)
	if err != nil {
		return nil, err
	}
	for id, candidate := range out {
		candidate.Last = last[id]
		out[id] = candidate
	}
	return out, nil
}

// lastOf returns the last calculation of each claim that has one.
func (s *TriageStore) lastOf(ctx context.Context, ids []string) (map[string]*domain.TriageRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+triageColumns+` FROM claim_current_triage
		WHERE claim_id = ANY($1::text[])`, ids)
	if err != nil {
		return nil, fmt.Errorf("read the last triage of the claims: %w", err)
	}
	records, err := scanTriages(rows)
	if err != nil {
		return nil, err
	}
	out := make(map[string]*domain.TriageRecord, len(records))
	for i := range records {
		out[records[i].ClaimID] = &records[i]
	}
	return out, nil
}

func scanTriages(rows *sql.Rows) ([]domain.TriageRecord, error) {
	defer func() { _ = rows.Close() }()
	var out []domain.TriageRecord
	for rows.Next() {
		r, err := scanTriage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the triage of the claims: %w", err)
	}
	return out, nil
}

// scanTriage reads the columns of triageColumns, in that order.
func scanTriage(row scanner) (domain.TriageRecord, error) {
	var (
		r                                    domain.TriageRecord
		level                                int
		severity, band, incident, line, kind string
		amount                               sql.NullInt64
		previous                             sql.NullInt32
		causes                               []byte
		coverage, version, fingerprint       string
		targetDays, resolutionDays           int
	)
	if err := row.Scan(&r.ClaimID, &r.Seq, &level, &severity, &band, &amount, &incident, &line, &coverage,
		&version, &fingerprint, &kind, &targetDays, &resolutionDays, &causes, &previous, &r.Actor, &r.AssessedAt); err != nil {
		return domain.TriageRecord{}, fmt.Errorf("read the triage of the claim: %w", err)
	}
	if err := json.Unmarshal(causes, &r.Causes); err != nil {
		return domain.TriageRecord{}, fmt.Errorf("decode the causes of the triage: %w", err)
	}
	in := domain.TriageInput{
		IncidentType: domain.IncidentType(incident), Line: domain.PolicyLine(line), Coverage: coverage,
		PolicyVersion: version,
	}
	if amount.Valid {
		in.EstimatedAmountMinor = &amount.Int64
	}
	r.Assessment = domain.TriageAssessment{
		Level: domain.PriorityLevel(level), Severity: domain.Severity(severity), AmountBand: domain.AmountBand(band),
		Input: in,
		Target: domain.TriageTarget{
			Kind: domain.TargetKind(kind), BusinessDays: targetDays, ResolutionBusinessDays: resolutionDays,
		},
	}
	if previous.Valid {
		p := domain.PriorityLevel(previous.Int32)
		r.PreviousLevel = &p
	}
	r.AssessedAt = r.AssessedAt.UTC()
	return r, nil
}

func statusNames(statuses []domain.Status) []string {
	out := make([]string, len(statuses))
	for i, s := range statuses {
		out[i] = string(s)
	}
	return out
}

// TriageStoreOf builds the triage store over the database of the claims, so
// the composition root does not open a second pool.
func TriageStoreOf(records *ClaimRecordStore) *TriageStore { return &TriageStore{db: records.db} }
