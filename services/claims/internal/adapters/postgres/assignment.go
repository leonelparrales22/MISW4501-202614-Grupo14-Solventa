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

// AssignmentStore keeps the locations, the searches and the assignments of
// the claims (HU-W2.3-01) in tables that only grow, and the notices to the
// providers in provider_notice.
type AssignmentStore struct {
	db *sql.DB
}

// NewAssignmentStore builds the store over an open, migrated database.
func NewAssignmentStore(db *sql.DB) *AssignmentStore { return &AssignmentStore{db: db} }

// AssignmentStoreOf builds the store over the database of the claims, so the
// composition root does not open a second pool.
func AssignmentStoreOf(records *ClaimRecordStore) *AssignmentStore {
	return &AssignmentStore{db: records.db}
}

var _ ports.AssignmentStore = (*AssignmentStore)(nil)

const (
	locationColumns = `claim_id, seq, address, latitude, longitude, recorded_by, recorder_name, recorded_at`
	searchColumns   = `claim_id, seq, trigger, searched_by, searcher_name, specialty, location_source, latitude,
	longitude, accuracy_m, address, location_seq, location_recorded_at, from_radius_km, radius_km, outcome, reason,
	candidates, policy_version, searched_at`
	assignmentColumns = `claim_id, provider_id, provider_name, contact_email, contact_phone, search_seq,
	distance_km, location_source, latitude, longitude, accuracy_m, address, location_seq, location_recorded_at,
	policy_version, assigned_by, assigner_name, assigned_at`
	noticeColumns = `claim_id, to_email, subject, body, status, attempts, next_attempt_at, sent_at, last_error`
)

// candidateJSON is one provider of a search as claim_provider_search.candidates
// keeps it.
type candidateJSON struct {
	ProviderID       string   `json:"provider_id"`
	Name             string   `json:"name"`
	Specialties      []string `json:"specialties"`
	DistanceKm       float64  `json:"distance_km"`
	Availability     string   `json:"availability"`
	UnavailableUntil string   `json:"unavailable_until,omitempty"`
	Eligibility      string   `json:"eligibility"`
	Suggested        bool     `json:"suggested"`
}

// Case returns the claim with its assignment.
func (s *AssignmentStore) Case(ctx context.Context, claimID string) (ports.AssignmentCase, error) {
	cases, err := s.Cases(ctx, []string{claimID})
	if err != nil {
		return ports.AssignmentCase{}, err
	}
	k, ok := cases[claimID]
	if !ok {
		return ports.AssignmentCase{}, ports.ErrClaimNotFound
	}
	return k, nil
}

// Cases returns the claims of the ids that exist with their assignment. It
// reads them in one snapshot, so a search is never joined with the attempts
// of another.
func (s *AssignmentStore) Cases(ctx context.Context, ids []string) (map[string]ports.AssignmentCase, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("read the assignment of the claims: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	cases, err := claimsByID(ctx, tx, ids)
	if err != nil || len(cases) == 0 {
		return map[string]ports.AssignmentCase{}, err
	}
	for _, read := range []func(context.Context, *sql.Tx, []string, map[string]*ports.AssignmentCase) error{
		evidenceByClaim, lastLocations, lastSearches, assignmentsByClaim,
	} {
		if err := read(ctx, tx, ids, cases); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("read the assignment of the claims: %w", err)
	}
	out := make(map[string]ports.AssignmentCase, len(cases))
	for id, k := range cases {
		out[id] = *k
	}
	return out, nil
}

func claimsByID(ctx context.Context, tx *sql.Tx, ids []string) (map[string]*ports.AssignmentCase, error) {
	rows, err := tx.QueryContext(ctx, `SELECT `+recordColumns+` FROM claim WHERE id = ANY($1::text[])`, ids)
	if err != nil {
		return nil, fmt.Errorf("read the claims to assign: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]*ports.AssignmentCase{}
	for rows.Next() {
		c, _, err := scanRecord(rows)
		if err != nil {
			return nil, fmt.Errorf("read the claims to assign: %w", err)
		}
		out[c.ID] = &ports.AssignmentCase{Claim: c}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the claims to assign: %w", err)
	}
	return out, nil
}

// evidenceByClaim reads what the location of the app is chosen from: where,
// when and how precisely each capture was taken.
func evidenceByClaim(ctx context.Context, tx *sql.Tx, ids []string, cases map[string]*ports.AssignmentCase) error {
	rows, err := tx.QueryContext(ctx, `SELECT claim_id, kind, captured_at, latitude, longitude, accuracy_m,
			location_trusted
		FROM claim_evidence WHERE claim_id = ANY($1::text[]) ORDER BY claim_id, position`, ids)
	if err != nil {
		return fmt.Errorf("read the evidence of the claims to assign: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			id, kind string
			e        domain.Evidence
		)
		if err := rows.Scan(&id, &kind, &e.CapturedAt, &e.Latitude, &e.Longitude, &e.AccuracyM,
			&e.LocationTrusted); err != nil {
			return fmt.Errorf("read the evidence of the claims to assign: %w", err)
		}
		e.Kind, e.CapturedAt = domain.EvidenceKind(kind), e.CapturedAt.UTC()
		if k := cases[id]; k != nil {
			k.Claim.Evidence = append(k.Claim.Evidence, e)
		}
	}
	return rows.Err()
}

func lastLocations(ctx context.Context, tx *sql.Tx, ids []string, cases map[string]*ports.AssignmentCase) error {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT ON (claim_id) `+locationColumns+`
		FROM claim_incident_location WHERE claim_id = ANY($1::text[]) ORDER BY claim_id, seq DESC`, ids)
	if err != nil {
		return fmt.Errorf("read the locations of the claims: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			id string
			l  = domain.IncidentLocation{Source: domain.LocationFromAnalyst, RecordedBy: &domain.Analyst{}}
		)
		if err := rows.Scan(&id, &l.Seq, &l.Address, &l.Point.Latitude, &l.Point.Longitude, &l.RecordedBy.Email,
			&l.RecordedBy.Name, &l.RecordedAt); err != nil {
			return fmt.Errorf("read the locations of the claims: %w", err)
		}
		l.RecordedAt = l.RecordedAt.UTC()
		if k := cases[id]; k != nil {
			k.Location = &l
		}
	}
	return rows.Err()
}

func lastSearches(ctx context.Context, tx *sql.Tx, ids []string, cases map[string]*ports.AssignmentCase) error {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT ON (claim_id) `+searchColumns+`
		FROM claim_provider_search WHERE claim_id = ANY($1::text[]) ORDER BY claim_id, seq DESC`, ids)
	if err != nil {
		return fmt.Errorf("read the searches of the claims: %w", err)
	}
	err = func() error {
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			r, err := scanSearch(rows)
			if err != nil {
				return err
			}
			if k := cases[r.ClaimID]; k != nil {
				k.LastSearch = &r
			}
		}
		return rows.Err()
	}()
	if err != nil {
		return fmt.Errorf("read the searches of the claims: %w", err)
	}

	attempts, err := tx.QueryContext(ctx, `SELECT a.claim_id, a.radius_km, a.eligible_count, a.attempted_at
		FROM claim_provider_search_attempt a
		JOIN (SELECT claim_id, max(seq) AS seq FROM claim_provider_search
		      WHERE claim_id = ANY($1::text[]) GROUP BY claim_id) l ON l.claim_id = a.claim_id AND l.seq = a.seq
		ORDER BY a.claim_id, a.position`, ids)
	if err != nil {
		return fmt.Errorf("read the attempts of the searches: %w", err)
	}
	defer func() { _ = attempts.Close() }()
	for attempts.Next() {
		var (
			id string
			a  domain.SearchAttempt
		)
		if err := attempts.Scan(&id, &a.RadiusKm, &a.EligibleCount, &a.AttemptedAt); err != nil {
			return fmt.Errorf("read the attempts of the searches: %w", err)
		}
		a.AttemptedAt = a.AttemptedAt.UTC()
		if k := cases[id]; k != nil && k.LastSearch != nil {
			k.LastSearch.Result.Attempts = append(k.LastSearch.Result.Attempts, a)
		}
	}
	return attempts.Err()
}

// scanSearch reads the columns of searchColumns, in that order.
func scanSearch(row scanner) (domain.ProviderSearch, error) {
	var (
		r                                  domain.ProviderSearch
		trigger, specialty, source, outcom string
		by, byName, address, reason        sql.NullString
		accuracy                           sql.NullFloat64
		candidates                         []byte
		list                               []candidateJSON
	)
	if err := row.Scan(&r.ClaimID, &r.Seq, &trigger, &by, &byName, &specialty, &source, &r.Location.Point.Latitude,
		&r.Location.Point.Longitude, &accuracy, &address, &r.Location.Seq, &r.Location.RecordedAt,
		&r.Result.FromRadiusKm, &r.Result.RadiusKm, &outcom, &reason, &candidates, &r.PolicyVersion,
		&r.SearchedAt); err != nil {
		return domain.ProviderSearch{}, err
	}
	r.Trigger, r.Specialty, r.Result.Outcome = domain.SearchTrigger(trigger), domain.Specialty(specialty),
		domain.SearchOutcome(outcom)
	r.Location.Source, r.Location.Address, r.Result.Reason = domain.LocationSource(source), address.String, reason.String
	r.Location.AccuracyM = floatOf(accuracy)
	r.Location.RecordedAt, r.SearchedAt = r.Location.RecordedAt.UTC(), r.SearchedAt.UTC()
	if by.Valid {
		r.SearchedBy = &domain.Analyst{Email: by.String, Name: byName.String}
	}
	if err := json.Unmarshal(candidates, &list); err != nil {
		return domain.ProviderSearch{}, fmt.Errorf("decode the candidates of the search: %w", err)
	}
	for _, c := range list {
		candidate := domain.Candidate{ProviderID: c.ProviderID, Name: c.Name, DistanceKm: c.DistanceKm,
			Availability: domain.Availability(c.Availability), Eligibility: domain.Eligibility(c.Eligibility),
			Suggested: c.Suggested}
		for _, s := range c.Specialties {
			candidate.Specialties = append(candidate.Specialties, domain.Specialty(s))
		}
		if c.UnavailableUntil != "" {
			d, err := time.ParseInLocation(time.DateOnly, c.UnavailableUntil, domain.ColombiaTime)
			if err != nil {
				return domain.ProviderSearch{}, fmt.Errorf("decode the candidates of the search: %w", err)
			}
			candidate.UnavailableUntil = &d
		}
		r.Result.Candidates = append(r.Result.Candidates, candidate)
	}
	return r, nil
}

func assignmentsByClaim(ctx context.Context, tx *sql.Tx, ids []string, cases map[string]*ports.AssignmentCase) error {
	rows, err := tx.QueryContext(ctx, `SELECT a.`+assignmentColumns+`, n.to_email, n.subject, n.body, n.status,
			n.attempts, n.next_attempt_at, n.sent_at, n.last_error
		FROM claim_assignment a LEFT JOIN provider_notice n ON n.claim_id = a.claim_id
		WHERE a.claim_id = ANY($1::text[])`, ids)
	if err != nil {
		return fmt.Errorf("read the assignments of the claims: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			a                                domain.Assignment
			phone, address                   sql.NullString
			accuracy                         sql.NullFloat64
			source                           string
			to, subject, body, status, cause sql.NullString
			attempts                         sql.NullInt64
			next, sent                       sql.NullTime
		)
		if err := rows.Scan(&a.ClaimID, &a.ProviderID, &a.ProviderName, &a.ContactEmail, &phone, &a.SearchSeq,
			&a.DistanceKm, &source, &a.Location.Point.Latitude, &a.Location.Point.Longitude, &accuracy, &address,
			&a.Location.Seq, &a.Location.RecordedAt, &a.PolicyVersion, &a.AssignedBy.Email, &a.AssignedBy.Name,
			&a.AssignedAt, &to, &subject, &body, &status, &attempts, &next, &sent, &cause); err != nil {
			return fmt.Errorf("read the assignments of the claims: %w", err)
		}
		a.ContactPhone, a.Location.Source, a.Location.Address = stringOf(phone), domain.LocationSource(source), address.String
		a.Location.AccuracyM = floatOf(accuracy)
		a.Location.RecordedAt, a.AssignedAt = a.Location.RecordedAt.UTC(), a.AssignedAt.UTC()
		k := cases[a.ClaimID]
		if k == nil {
			continue
		}
		k.Assignment = &a
		if status.Valid {
			k.Notice = &ports.ProviderNotice{ClaimID: a.ClaimID, To: to.String, Subject: subject.String,
				Body: body.String, Status: ports.NoticeStatus(status.String), Attempts: int(attempts.Int64),
				NextAttemptAt: utc(next), SentAt: utc(sent), LastError: cause.String}
		}
	}
	return rows.Err()
}

// RecordLocation adds the location of the analyst, unless the claim was
// assigned meanwhile. The primary key (claim_id, seq) settles two analysts
// recording at once.
func (s *AssignmentStore) RecordLocation(ctx context.Context, claimID string, l domain.IncidentLocation) error {
	var by domain.Analyst
	if l.RecordedBy != nil {
		by = *l.RecordedBy
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO claim_incident_location (`+locationColumns+`)
		SELECT $1, $2, $3, $4, $5, $6, $7, $8
		WHERE NOT EXISTS (SELECT 1 FROM claim_assignment WHERE claim_id = $1)`,
		claimID, l.Seq, l.Address, l.Point.Latitude, l.Point.Longitude, by.Email, by.Name, l.RecordedAt.UTC())
	if isConstraint(err, "claim_incident_location_pkey") {
		return ports.ErrLocationConflict
	}
	if err != nil {
		return fmt.Errorf("record the location of the claim %s: %w", claimID, err)
	}
	return assignedUnlessInserted(res)
}

// RecordSearch adds the search and its attempts, unless the claim was
// assigned meanwhile. The primary key (claim_id, seq) settles two searches
// at once.
func (s *AssignmentStore) RecordSearch(ctx context.Context, r domain.ProviderSearch) error {
	list := make([]candidateJSON, 0, len(r.Result.Candidates))
	for _, c := range r.Result.Candidates {
		candidate := candidateJSON{ProviderID: c.ProviderID, Name: c.Name, Specialties: specialtyNames(c.Specialties),
			DistanceKm: c.DistanceKm, Availability: string(c.Availability), Eligibility: string(c.Eligibility),
			Suggested: c.Suggested}
		if until := day(c.UnavailableUntil); until.Valid {
			candidate.UnavailableUntil = until.String
		}
		list = append(list, candidate)
	}
	candidates, err := json.Marshal(list)
	if err != nil {
		return fmt.Errorf("encode the candidates of the search: %w", err)
	}
	var by, byName sql.NullString
	if r.SearchedBy != nil {
		by, byName = sql.NullString{String: r.SearchedBy.Email, Valid: true}, sql.NullString{String: r.SearchedBy.Name, Valid: true}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("record the search of the claim %s: %w", r.ClaimID, err)
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `INSERT INTO claim_provider_search (`+searchColumns+`)
		SELECT $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20
		WHERE NOT EXISTS (SELECT 1 FROM claim_assignment WHERE claim_id = $1)`,
		r.ClaimID, r.Seq, string(r.Trigger), by, byName, string(r.Specialty), string(r.Location.Source),
		r.Location.Point.Latitude, r.Location.Point.Longitude, nullFloat(r.Location.AccuracyM),
		optional(r.Location.Address), r.Location.Seq, r.Location.RecordedAt.UTC(), r.Result.FromRadiusKm,
		r.Result.RadiusKm, string(r.Result.Outcome), optional(r.Result.Reason), candidates, r.PolicyVersion,
		r.SearchedAt.UTC())
	if isConstraint(err, "claim_provider_search_pkey") {
		return ports.ErrSearchConflict
	}
	if err != nil {
		return fmt.Errorf("record the search of the claim %s: %w", r.ClaimID, err)
	}
	if err := assignedUnlessInserted(res); err != nil {
		return err
	}
	for i, a := range r.Result.Attempts {
		if _, err := tx.ExecContext(ctx, `INSERT INTO claim_provider_search_attempt (claim_id, seq, position,
				radius_km, eligible_count, attempted_at) VALUES ($1, $2, $3, $4, $5, $6)`,
			r.ClaimID, r.Seq, i, a.RadiusKm, a.EligibleCount, a.AttemptedAt.UTC()); err != nil {
			return fmt.Errorf("record the attempts of the search of the claim %s: %w", r.ClaimID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("record the search of the claim %s: %w", r.ClaimID, err)
	}
	return nil
}

// Assign records the assignment, makes the provider busy and leaves the
// notice due in one transaction. The primary key of claim_assignment settles
// two analysts assigning the same claim; the condition on the availability
// of the provider, which waits for the lock of its row, settles two claims
// taking the same provider: the second finds it busy and nothing is written.
func (s *AssignmentStore) Assign(ctx context.Context, a domain.Assignment, n ports.ProviderNotice) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("assign the claim %s: %w", a.ClaimID, err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `INSERT INTO claim_assignment (`+assignmentColumns+`)
		SELECT $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18
		WHERE EXISTS (SELECT 1 FROM claim WHERE id = $1 AND status = $19)
		  AND (SELECT max(seq) FROM claim_provider_search WHERE claim_id = $1) = $6`,
		a.ClaimID, a.ProviderID, a.ProviderName, a.ContactEmail, nullText(a.ContactPhone), a.SearchSeq,
		a.DistanceKm, string(a.Location.Source), a.Location.Point.Latitude, a.Location.Point.Longitude,
		nullFloat(a.Location.AccuracyM), optional(a.Location.Address), a.Location.Seq, a.Location.RecordedAt.UTC(),
		a.PolicyVersion, a.AssignedBy.Email, a.AssignedBy.Name, a.AssignedAt.UTC(), string(domain.StatusInEvaluation))
	if isConstraint(err, "claim_assignment_pkey") {
		return domain.ErrAssignmentExists
	}
	if err != nil {
		return fmt.Errorf("assign the claim %s: %w", a.ClaimID, err)
	}
	if rows, err := res.RowsAffected(); err != nil || rows == 0 {
		return whyNotAssigned(ctx, tx, a.ClaimID, err)
	}

	res, err = tx.ExecContext(ctx, `UPDATE provider SET availability = 'busy', busy_claim_id = $2,
			unavailable_until = NULL, updated_by = $3, updated_at = $4
		WHERE id = $1 AND availability = 'available'`, a.ProviderID, a.ClaimID, a.AssignedBy.Email, a.AssignedAt.UTC())
	if err != nil {
		return fmt.Errorf("make the provider %s busy: %w", a.ProviderID, err)
	}
	if rows, err := res.RowsAffected(); err != nil || rows == 0 {
		if err != nil {
			return fmt.Errorf("make the provider %s busy: %w", a.ProviderID, err)
		}
		return domain.ErrProviderUnavailable
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO provider_notice (claim_id, to_email, subject, body, status,
			attempts, next_attempt_at) VALUES ($1, $2, $3, $4, 'pending', 0, $5)`,
		a.ClaimID, n.To, n.Subject, n.Body, n.NextAttemptAt.UTC()); err != nil {
		return fmt.Errorf("leave the notice of the claim %s due: %w", a.ClaimID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("assign the claim %s: %w", a.ClaimID, err)
	}
	return nil
}

// whyNotAssigned tells why the assignment of a claim was not inserted: the
// claim does not exist, left evaluation, or has a later search.
func whyNotAssigned(ctx context.Context, tx *sql.Tx, claimID string, cause error) error {
	if cause != nil {
		return fmt.Errorf("assign the claim %s: %w", claimID, cause)
	}
	var status string
	err := tx.QueryRowContext(ctx, `SELECT status FROM claim WHERE id = $1`, claimID).Scan(&status)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return ports.ErrClaimNotFound
	case err != nil:
		return fmt.Errorf("read the claim %s: %w", claimID, err)
	case !domain.Assignable(domain.Status(status)):
		return domain.ErrClaimNotAssignable
	}
	return domain.ErrSearchOutdated
}

// PendingAssignment returns the claims in evaluation without a provider
// whose last search, made from the location in force, found none.
func (s *AssignmentStore) PendingAssignment(ctx context.Context, limit int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT r.claim_id
		FROM (SELECT DISTINCT ON (claim_id) claim_id, outcome, location_source, location_seq, searched_at
		      FROM claim_provider_search ORDER BY claim_id, seq DESC) r
		JOIN claim c ON c.id = r.claim_id
		WHERE r.outcome = 'exhausted' AND c.status = $1
		  AND NOT EXISTS (SELECT 1 FROM claim_assignment a WHERE a.claim_id = r.claim_id)
		  AND (r.location_source = 'app'
		       OR r.location_seq = (SELECT max(seq) FROM claim_incident_location l WHERE l.claim_id = r.claim_id))
		ORDER BY r.searched_at, r.claim_id LIMIT $2`, string(domain.StatusInEvaluation), limit)
	if err != nil {
		return nil, fmt.Errorf("list the claims pending assignment: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("list the claims pending assignment: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ClaimNoticesDue holds the due notices for lease by moving their next
// attempt; SKIP LOCKED lets two tasks take different ones.
func (s *AssignmentStore) ClaimNoticesDue(ctx context.Context, now time.Time, lease time.Duration,
	limit int) ([]ports.ProviderNotice, error) {
	rows, err := s.db.QueryContext(ctx, `UPDATE provider_notice SET next_attempt_at = $2
		WHERE claim_id IN (SELECT claim_id FROM provider_notice
		                   WHERE status = 'pending' AND next_attempt_at <= $1
		                   ORDER BY next_attempt_at, claim_id LIMIT $3 FOR UPDATE SKIP LOCKED)
		RETURNING `+noticeColumns, now.UTC(), now.Add(lease).UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("hold the due notices to the providers: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []ports.ProviderNotice
	for rows.Next() {
		var (
			n      ports.ProviderNotice
			status string
			next   sql.NullTime
			sent   sql.NullTime
			cause  sql.NullString
		)
		if err := rows.Scan(&n.ClaimID, &n.To, &n.Subject, &n.Body, &status, &n.Attempts, &next, &sent,
			&cause); err != nil {
			return nil, fmt.Errorf("hold the due notices to the providers: %w", err)
		}
		n.Status, n.NextAttemptAt, n.SentAt, n.LastError = ports.NoticeStatus(status), utc(next), utc(sent), cause.String
		out = append(out, n)
	}
	return out, rows.Err()
}

// NoticeSent records the delivery of the notice.
func (s *AssignmentStore) NoticeSent(ctx context.Context, claimID string, at time.Time) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE provider_notice
		SET status = 'sent', sent_at = $2, attempts = attempts + 1, next_attempt_at = NULL, last_error = NULL
		WHERE claim_id = $1 AND status = 'pending'`, claimID, at.UTC()); err != nil {
		return fmt.Errorf("record the notice of the claim %s as sent: %w", claimID, err)
	}
	return nil
}

// NoticeFailed counts a failed delivery and makes the notice due at next.
func (s *AssignmentStore) NoticeFailed(ctx context.Context, claimID string, next time.Time, cause string) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE provider_notice
		SET attempts = attempts + 1, next_attempt_at = $2, last_error = $3
		WHERE claim_id = $1 AND status = 'pending'`, claimID, next.UTC(), cause); err != nil {
		return fmt.Errorf("record the failed notice of the claim %s: %w", claimID, err)
	}
	return nil
}

// assignedUnlessInserted maps a conditional insert that wrote nothing to
// domain.ErrAssignmentExists: the claim was assigned meanwhile.
func assignedUnlessInserted(res sql.Result) error {
	rows, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("count the inserted rows: %w", err)
	}
	if rows == 0 {
		return domain.ErrAssignmentExists
	}
	return nil
}

// isConstraint tells whether err broke the unique constraint.
func isConstraint(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && pgErr.ConstraintName == constraint
}

func floatOf(v sql.NullFloat64) *float64 {
	if !v.Valid {
		return nil
	}
	f := v.Float64
	return &f
}
