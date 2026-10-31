package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// uniqueViolation is the PostgreSQL error code of a broken unique constraint.
const uniqueViolation = "23505"

// ClaimRecordStore reads and writes the filed claims of both channels: the
// mobile filing with its evidence (HU-M2.2-01, HU-M2.1-01) and the web filing
// with its policy copy and supports (HU-W2.1-01). Both share the claim table,
// the filing numbers and the filing keys, and one insert.
type ClaimRecordStore struct {
	db *sql.DB
}

// NewClaimRecordStore builds the store over an open, migrated database.
func NewClaimRecordStore(db *sql.DB) *ClaimRecordStore { return &ClaimRecordStore{db: db} }

var _ ports.ClaimRecords = (*ClaimRecordStore)(nil)

const claimColumns = `id, filing_number, cognito_sub, filing_key, fingerprint, policy_id,
	incident_type, occurred_at, place, description, details, status, filed_at`

const recordColumns = claimColumns + `, channel, policy_snapshot, catalog_version, estimated_amount_minor`

// policySnapshot is the JSON shape of the copy of the policy.
type policySnapshot struct {
	PolicyID         string    `json:"policy_id"`
	Line             string    `json:"line"`
	Product          string    `json:"product"`
	InsuredObject    string    `json:"insured_object"`
	Status           string    `json:"status"`
	ValidFrom        time.Time `json:"valid_from"`
	ValidTo          time.Time `json:"valid_to"`
	CoveredIncidents []string  `json:"covered_incidents"`
}

// Create stores a claim of the mobile filing (HU-M2.2-01) with its evidence
// and assigns its filing number. When the subject already filed with the same
// key it returns that claim and created=false, or ErrFilingKeyReused.
func (s *ClaimRecordStore) Create(ctx context.Context, c domain.Claim) (domain.Claim, bool, error) {
	stored, created, err := s.create(ctx, domain.ClaimRecord{Claim: c, Channel: domain.ChannelMobile}, c.Fingerprint())
	if err != nil {
		return domain.Claim{}, false, err
	}
	return stored.Claim, created, nil
}

// CreateWeb stores the web claim and attaches its supports in one
// transaction.
func (s *ClaimRecordStore) CreateWeb(ctx context.Context, c domain.ClaimRecord) (domain.ClaimRecord, bool, error) {
	c.Channel = domain.ChannelWeb
	return s.create(ctx, c, c.WebFingerprint())
}

// create is the only writer of the claim table. The filing key is read first:
// a retry after a lost response is the common case, and answering it must not
// burn a filing number. The unique index settles the race between two retries
// that arrive at the same time, and then the row that won is read back. The
// claim, its evidence and its supports are written together: a case whose
// evidence is missing would claim what it cannot show.
func (s *ClaimRecordStore) create(ctx context.Context, c domain.ClaimRecord, fingerprint string) (domain.ClaimRecord, bool, error) {
	switch stored, filed, err := s.byFilingKey(ctx, c.Subject, c.FilingKey); {
	case err == nil:
		return sameFiling(stored, filed, c.Channel, fingerprint)
	case !errors.Is(err, sql.ErrNoRows):
		return domain.ClaimRecord{}, false, fmt.Errorf("read the filing by its key: %w", err)
	}

	snapshot, err := encodePolicy(c.Policy)
	if err != nil {
		return domain.ClaimRecord{}, false, err
	}
	details, err := json.Marshal(c.Details)
	if err != nil {
		return domain.ClaimRecord{}, false, fmt.Errorf("encode the details of the filing: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ClaimRecord{}, false, fmt.Errorf("begin the filing: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var sequence int64
	if err := tx.QueryRowContext(ctx, `SELECT nextval('claim_filing_number_seq')`).Scan(&sequence); err != nil {
		return domain.ClaimRecord{}, false, fmt.Errorf("take the next filing number: %w", err)
	}
	c.FilingNumber = domain.FilingNumber(c.FiledAt, sequence)

	res, err := tx.ExecContext(ctx, `INSERT INTO claim (`+recordColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
		ON CONFLICT (cognito_sub, filing_key) DO NOTHING`,
		c.ID, c.FilingNumber, c.Subject, c.FilingKey, fingerprint, c.PolicyID,
		string(c.IncidentType), c.OccurredAt, c.Place, c.Description, details,
		string(c.Status), c.FiledAt, string(c.Channel), nullBytes(snapshot), nullString(c.CatalogVersion),
		nullInt64(c.EstimatedAmountMinor))
	if err != nil {
		return domain.ClaimRecord{}, false, fmt.Errorf("store the filing: %w", err)
	}
	if rows, err := res.RowsAffected(); err == nil && rows == 0 {
		// Another retry of the same filing won the race.
		_ = tx.Rollback()
		stored, filed, err := s.byFilingKey(ctx, c.Subject, c.FilingKey)
		if err != nil {
			return domain.ClaimRecord{}, false, fmt.Errorf("read the filing that won the race: %w", err)
		}
		return sameFiling(stored, filed, c.Channel, fingerprint)
	}

	if err := insertEvidence(ctx, tx, c.Claim); err != nil {
		return domain.ClaimRecord{}, false, err
	}
	for _, a := range c.Supports {
		if _, err := tx.ExecContext(ctx, `INSERT INTO claim_support (claim_id, position, requirement_code, support_id)
			VALUES ($1, $2, $3, $4)`, c.ID, a.Position, a.RequirementCode, a.File.ID); err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && pgErr.ConstraintName == "claim_support_once" {
				return domain.ClaimRecord{}, false, ports.ErrSupportAlreadyAttached
			}
			return domain.ClaimRecord{}, false, fmt.Errorf("attach the support %s: %w", a.File.ID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return domain.ClaimRecord{}, false, fmt.Errorf("commit the filing: %w", err)
	}
	return c, true, nil
}

// sameFiling answers a repeated filing key: the claim that already exists
// when it is the same filing of the same channel, ErrFilingKeyReused
// otherwise.
func sameFiling(stored domain.ClaimRecord, filed string, channel domain.Channel, incoming string) (domain.ClaimRecord, bool, error) {
	if stored.Channel != channel || filed != incoming {
		return domain.ClaimRecord{}, false, ports.ErrFilingKeyReused
	}
	return stored, false, nil
}

func nullString(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func nullBytes(v []byte) any {
	if len(v) == 0 {
		return nil
	}
	return v
}

func nullInt64(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

func nullFloat(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

// insertEvidence writes the captures of the claim in the order they arrived.
func insertEvidence(ctx context.Context, tx *sql.Tx, c domain.Claim) error {
	for i, e := range c.Evidence {
		if _, err := tx.ExecContext(ctx, `INSERT INTO claim_evidence (
			claim_id, position, object_key, kind, captured_at, latitude, longitude,
			altitude_m, accuracy_m, location_trusted, sha256, byte_size, duration_s)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
			c.ID, i, e.ObjectKey, string(e.Kind), e.CapturedAt, e.Latitude, e.Longitude,
			nullFloat(e.AltitudeM), e.AccuracyM, e.LocationTrusted, e.SHA256, e.ByteSize,
			e.DurationS); err != nil {
			return fmt.Errorf("store the evidence %s: %w", e.ObjectKey, err)
		}
	}
	return nil
}

// evidenceOf reads the captures of the claim, oldest first.
func (s *ClaimRecordStore) evidenceOf(ctx context.Context, claimID string) ([]domain.Evidence, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT object_key, kind, captured_at, latitude, longitude,
		altitude_m, accuracy_m, location_trusted, sha256, byte_size, duration_s
		FROM claim_evidence WHERE claim_id = $1 ORDER BY position`, claimID)
	if err != nil {
		return nil, fmt.Errorf("read the evidence of the case: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.Evidence
	for rows.Next() {
		var (
			e        domain.Evidence
			kind     string
			altitude sql.NullFloat64
		)
		if err := rows.Scan(&e.ObjectKey, &kind, &e.CapturedAt, &e.Latitude, &e.Longitude,
			&altitude, &e.AccuracyM, &e.LocationTrusted, &e.SHA256, &e.ByteSize,
			&e.DurationS); err != nil {
			return nil, fmt.Errorf("read the evidence of the case: %w", err)
		}
		e.Kind = domain.EvidenceKind(kind)
		e.CapturedAt = e.CapturedAt.UTC()
		if altitude.Valid {
			e.AltitudeM = &altitude.Float64
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the evidence of the case: %w", err)
	}
	return out, nil
}

// withAttachments loads what hangs from the claim: its supports and its
// evidence.
func (s *ClaimRecordStore) withAttachments(ctx context.Context, c domain.ClaimRecord) (domain.ClaimRecord, error) {
	var err error
	if c.Supports, err = s.supportsOf(ctx, c.ID); err != nil {
		return domain.ClaimRecord{}, err
	}
	if c.Evidence, err = s.evidenceOf(ctx, c.ID); err != nil {
		return domain.ClaimRecord{}, err
	}
	return c, nil
}

// FindByKey returns the claim the subject filed with the key, with its
// supports.
func (s *ClaimRecordStore) FindByKey(ctx context.Context, subject, key string) (domain.ClaimRecord, error) {
	c, _, err := s.byFilingKey(ctx, subject, key)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ClaimRecord{}, ports.ErrClaimNotFound
	}
	if err != nil {
		return domain.ClaimRecord{}, fmt.Errorf("read the filing by its key: %w", err)
	}
	return c, nil
}

// Get returns the claim with its supports and its evidence.
func (s *ClaimRecordStore) Get(ctx context.Context, id string) (domain.ClaimRecord, error) {
	c, _, err := scanRecord(s.db.QueryRowContext(ctx, `SELECT `+recordColumns+` FROM claim WHERE id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ClaimRecord{}, ports.ErrClaimNotFound
	}
	if err != nil {
		return domain.ClaimRecord{}, fmt.Errorf("read the claim: %w", err)
	}
	return s.withAttachments(ctx, c)
}

// ListByOwner returns the subject's claims, newest first, without supports.
func (s *ClaimRecordStore) ListByOwner(ctx context.Context, subject string, f ports.ClaimFilter,
	after *ports.ClaimCursor, limit int) ([]domain.ClaimRecord, error) {
	var from, afterAt sql.NullTime
	var afterID string
	if !f.FiledFrom.IsZero() {
		from = sql.NullTime{Time: f.FiledFrom, Valid: true}
	}
	if after != nil {
		afterAt, afterID = sql.NullTime{Time: after.FiledAt, Valid: true}, after.ID
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+recordColumns+` FROM claim
		WHERE cognito_sub = $1
		  AND ($2 = '' OR status = $2)
		  AND ($3 = '' OR policy_id = $3)
		  AND ($4::timestamptz IS NULL OR filed_at >= $4)
		  AND ($5::timestamptz IS NULL OR (filed_at, id) < ($5, $6))
		ORDER BY filed_at DESC, id DESC
		LIMIT $7`,
		subject, string(f.Status), f.PolicyID, from, afterAt, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("list the claims: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.ClaimRecord
	for rows.Next() {
		c, _, err := scanRecord(rows)
		if err != nil {
			return nil, fmt.Errorf("list the claims: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list the claims: %w", err)
	}
	return out, nil
}

var _ ports.ClaimQueue = (*ClaimRecordStore)(nil)

// ListByStatus returns the claims of every insured in one of the statuses,
// oldest filed first, without supports or evidence (HU-W2.2-03). The index
// claim_by_status (status, filed_at, id) serves it.
func (s *ClaimRecordStore) ListByStatus(ctx context.Context, statuses []domain.Status, after *ports.ClaimCursor,
	limit int) ([]domain.ClaimRecord, error) {
	if len(statuses) == 0 || limit <= 0 {
		return nil, nil
	}
	names := make([]string, 0, len(statuses))
	for _, st := range statuses {
		names = append(names, string(st))
	}
	var afterAt sql.NullTime
	var afterID string
	if after != nil {
		afterAt, afterID = sql.NullTime{Time: after.FiledAt, Valid: true}, after.ID
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+recordColumns+` FROM claim
		WHERE status = ANY($1)
		  AND ($2::timestamptz IS NULL OR (filed_at, id) > ($2, $3))
		ORDER BY filed_at, id
		LIMIT $4`,
		names, afterAt, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("list the claims by status: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.ClaimRecord
	for rows.Next() {
		c, _, err := scanRecord(rows)
		if err != nil {
			return nil, fmt.Errorf("list the claims by status: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list the claims by status: %w", err)
	}
	return out, nil
}

// byFilingKey returns the claim with its supports and the fingerprint of what
// was filed with the key.
func (s *ClaimRecordStore) byFilingKey(ctx context.Context, subject, key string) (domain.ClaimRecord, string, error) {
	c, fingerprint, err := scanRecord(s.db.QueryRowContext(ctx, `SELECT `+recordColumns+`
		FROM claim WHERE cognito_sub = $1 AND filing_key = $2`, subject, key))
	if err != nil {
		return domain.ClaimRecord{}, "", err
	}
	if c, err = s.withAttachments(ctx, c); err != nil {
		return domain.ClaimRecord{}, "", err
	}
	return c, fingerprint, nil
}

func (s *ClaimRecordStore) supportsOf(ctx context.Context, claimID string) ([]domain.AttachedSupport, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT f.support_id, f.owner_sub, f.file_name, f.content_type, f.byte_size,
			f.sha256, f.uploaded_at, f.confirmed_at, cs.requirement_code, cs.position
		FROM claim_support cs JOIN support_file f ON f.support_id = cs.support_id
		WHERE cs.claim_id = $1 ORDER BY cs.position`, claimID)
	if err != nil {
		return nil, fmt.Errorf("read the supports of the claim: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.AttachedSupport
	for rows.Next() {
		var a domain.AttachedSupport
		f, err := scanSupport(rows, &a.RequirementCode, &a.Position)
		if err != nil {
			return nil, err
		}
		a.File = f
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the supports of the claim: %w", err)
	}
	return out, nil
}

// scanRecord reads the columns of recordColumns, in that order.
func scanRecord(row scanner) (domain.ClaimRecord, string, error) {
	var (
		c                         domain.ClaimRecord
		incident, status, channel string
		details, snapshot         []byte
		fingerprint               string
		catalog                   sql.NullString
		estimatedAmount           sql.NullInt64
	)
	if err := row.Scan(&c.ID, &c.FilingNumber, &c.Subject, &c.FilingKey, &fingerprint, &c.PolicyID,
		&incident, &c.OccurredAt, &c.Place, &c.Description, &details, &status, &c.FiledAt,
		&channel, &snapshot, &catalog, &estimatedAmount); err != nil {
		return domain.ClaimRecord{}, "", err
	}
	c.IncidentType = domain.IncidentType(incident)
	c.Status = domain.Status(status)
	c.Channel = domain.Channel(channel)
	c.CatalogVersion = catalog.String
	if estimatedAmount.Valid {
		c.EstimatedAmountMinor = &estimatedAmount.Int64
	}
	c.OccurredAt = c.OccurredAt.UTC()
	c.FiledAt = c.FiledAt.UTC()
	c.Details = map[string]string{}
	if len(details) > 0 {
		if err := json.Unmarshal(details, &c.Details); err != nil {
			return domain.ClaimRecord{}, "", fmt.Errorf("decode the details of the filing: %w", err)
		}
	}
	policy, err := decodePolicy(snapshot)
	if err != nil {
		return domain.ClaimRecord{}, "", err
	}
	c.Policy = policy
	return c, fingerprint, nil
}

func encodePolicy(p *domain.Policy) ([]byte, error) {
	if p == nil {
		return nil, nil
	}
	covered := make([]string, len(p.CoveredIncidents))
	for i, t := range p.CoveredIncidents {
		covered[i] = string(t)
	}
	raw, err := json.Marshal(policySnapshot{
		PolicyID: p.ID, Line: string(p.Line), Product: p.Product, InsuredObject: p.InsuredObject,
		Status: string(p.Status), ValidFrom: p.ValidFrom.UTC(), ValidTo: p.ValidTo.UTC(), CoveredIncidents: covered,
	})
	if err != nil {
		return nil, fmt.Errorf("encode the copy of the policy: %w", err)
	}
	return raw, nil
}

func decodePolicy(raw []byte) (*domain.Policy, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var s policySnapshot
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("decode the copy of the policy: %w", err)
	}
	p := domain.Policy{
		ID: s.PolicyID, Line: domain.PolicyLine(s.Line), Product: s.Product, InsuredObject: s.InsuredObject,
		Status: domain.PolicyStatus(s.Status), ValidFrom: s.ValidFrom.UTC(), ValidTo: s.ValidTo.UTC(),
	}
	for _, t := range s.CoveredIncidents {
		p.CoveredIncidents = append(p.CoveredIncidents, domain.IncidentType(t))
	}
	return &p, nil
}
