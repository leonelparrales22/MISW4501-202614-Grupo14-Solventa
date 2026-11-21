package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// AssistanceInboxStore keeps the decisions of the inbox of operations on the
// requests for assistance (HU-W2.3-03) in tables that only grow (decision
// D7), and the notices of the dispatches in assistance_notice. The request
// only changes its status. Every write locks the request and checks that it
// has the decisions the analyst read, so two analysts never both decide.
type AssistanceInboxStore struct {
	db *sql.DB
}

// NewAssistanceInboxStore builds the store over an open, migrated database.
func NewAssistanceInboxStore(db *sql.DB) *AssistanceInboxStore { return &AssistanceInboxStore{db: db} }

// AssistanceInboxStoreOf builds the store over the database of the claims, so
// the composition root does not open a second pool.
func AssistanceInboxStoreOf(records *ClaimRecordStore) *AssistanceInboxStore {
	return &AssistanceInboxStore{db: records.db}
}

var _ ports.AssistanceInboxStore = (*AssistanceInboxStore)(nil)

const (
	actionColumns   = `request_id, seq, action, actor_email, actor_name, at, need, provider_id, reason, taken_from`
	dispatchColumns = `request_id, provider_id, provider_name, contact_email, contact_phone, need, specialty,
	distance_km, eta_minutes, location_source, latitude, longitude, accuracy_m, address, place,
	suggested_provider_id, candidates, attempts, assistance_policy_version, assignment_policy_version,
	dispatched_by, dispatcher_name, dispatched_at`
	assistanceNoticeColumns = `request_id, to_email, subject, body, status, attempts, next_attempt_at, sent_at,
	last_error`
)

// attemptJSON is one radius tried as assistance_dispatch.attempts keeps it.
type attemptJSON struct {
	RadiusKm      float64   `json:"radius_km"`
	EligibleCount int       `json:"eligible_count"`
	AttemptedAt   time.Time `json:"attempted_at"`
}

// readOnly runs read in one snapshot, so a request is never joined with the
// decisions of another moment.
func (s *AssistanceInboxStore) readOnly(ctx context.Context, read func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return fmt.Errorf("read the inbox of assistance: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := read(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// List returns the requests of the filter, the latest first.
func (s *AssistanceInboxStore) List(ctx context.Context, f ports.InboxFilter) ([]ports.AssistanceRecord, error) {
	statuses := make([]string, 0, len(f.Statuses))
	for _, st := range f.Statuses {
		statuses = append(statuses, string(st))
	}
	var out []ports.AssistanceRecord
	err := s.readOnly(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT `+assistanceColumns+` FROM assistance_request
			WHERE cardinality($1::text[]) = 0 OR status = ANY($1::text[])
			ORDER BY requested_at DESC, id DESC LIMIT $2`, statuses, f.Limit)
		if err != nil {
			return fmt.Errorf("list the inbox of assistance: %w", err)
		}
		requests, err := scanRequests(rows)
		if err != nil {
			return fmt.Errorf("list the inbox of assistance: %w", err)
		}
		out, err = withDecisions(ctx, tx, requests)
		return err
	})
	return out, err
}

// Case returns the request with its decisions.
func (s *AssistanceInboxStore) Case(ctx context.Context, id string) (ports.AssistanceRecord, error) {
	var out []ports.AssistanceRecord
	err := s.readOnly(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT `+assistanceColumns+` FROM assistance_request WHERE id = $1`, id)
		if err != nil {
			return fmt.Errorf("read the request for assistance: %w", err)
		}
		requests, err := scanRequests(rows)
		if err != nil {
			return fmt.Errorf("read the request for assistance: %w", err)
		}
		out, err = withDecisions(ctx, tx, requests)
		return err
	})
	if err != nil {
		return ports.AssistanceRecord{}, err
	}
	if len(out) == 0 {
		return ports.AssistanceRecord{}, ports.ErrAssistanceRequestNotFound
	}
	return out[0], nil
}

// Counts returns the new and in call requests and how long each dispatch
// made since dayStart took.
func (s *AssistanceInboxStore) Counts(ctx context.Context, dayStart time.Time) (int, int, []time.Duration, error) {
	var newCount, inCall int
	var took []time.Duration
	err := s.readOnly(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FILTER (WHERE status = 'new'),
				count(*) FILTER (WHERE status = 'in_call')
			FROM assistance_request WHERE status IN ('new', 'in_call')`).Scan(&newCount, &inCall); err != nil {
			return fmt.Errorf("count the open requests for assistance: %w", err)
		}
		rows, err := tx.QueryContext(ctx, `SELECT EXTRACT(EPOCH FROM d.dispatched_at - r.requested_at)::float8
			FROM assistance_dispatch d JOIN assistance_request r ON r.id = d.request_id
			WHERE d.dispatched_at >= $1`, dayStart.UTC())
		if err != nil {
			return fmt.Errorf("read the dispatches of today: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var seconds float64
			if err := rows.Scan(&seconds); err != nil {
				return fmt.Errorf("read the dispatches of today: %w", err)
			}
			took = append(took, time.Duration(seconds*float64(time.Second)))
		}
		return rows.Err()
	})
	return newCount, inCall, took, err
}

// DispatchesSince counts the dispatches of each provider since dayStart.
func (s *AssistanceInboxStore) DispatchesSince(ctx context.Context, providerIDs []string,
	dayStart time.Time) (map[string]int, error) {
	out := map[string]int{}
	if len(providerIDs) == 0 {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT provider_id, count(*) FROM assistance_dispatch
		WHERE provider_id = ANY($1::text[]) AND dispatched_at >= $2 GROUP BY provider_id`, providerIDs, dayStart.UTC())
	if err != nil {
		return nil, fmt.Errorf("count the dispatches of the providers: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, fmt.Errorf("count the dispatches of the providers: %w", err)
		}
		out[id] = n
	}
	return out, rows.Err()
}

// scanRequests reads the rows of assistanceColumns and closes them.
func scanRequests(rows *sql.Rows) ([]domain.AssistanceRequest, error) {
	defer func() { _ = rows.Close() }()
	var out []domain.AssistanceRequest
	for rows.Next() {
		r, err := scanRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// scanRequest reads a request of the rows of a list. It decodes the columns
// as scanAssistance does for one row (HU-M2.3-01).
func scanRequest(row scanner) (domain.AssistanceRequest, error) {
	var (
		r                        domain.AssistanceRequest
		snapshot                 []byte
		status, locationStatus   string
		latitude, longitude      sql.NullFloat64
		accuracy                 sql.NullFloat64
		capturedAt               sql.NullTime
		unavailableReason, place sql.NullString
		requestedAt              time.Time
	)
	if err := row.Scan(&r.ID, &r.Number, &r.Subject, &r.Key, &r.Fingerprint, &r.RequesterName, &r.Policy.ID,
		&snapshot, &locationStatus, &latitude, &longitude, &accuracy, &capturedAt, &unavailableReason, &place,
		&status, &requestedAt); err != nil {
		return domain.AssistanceRequest{}, err
	}
	var p assistancePolicyJSON
	if err := json.Unmarshal(snapshot, &p); err != nil {
		return domain.AssistanceRequest{}, fmt.Errorf("decode the policy of the request %s: %w", r.ID, err)
	}
	r.Policy = domain.AssistancePolicy{ID: p.PolicyID, Line: domain.PolicyLine(p.Line), Product: p.Product,
		InsuredObject: p.InsuredObject}
	r.Status, r.RequestedAt = domain.AssistanceStatus(status), requestedAt.UTC()
	r.Location = domain.AssistanceLocation{Status: domain.LocationStatus(locationStatus),
		UnavailableReason: domain.LocationUnavailableReason(unavailableReason.String), AccuracyM: floatOf(accuracy)}
	if latitude.Valid && longitude.Valid {
		r.Location.Coordinates = &domain.Coordinates{Latitude: latitude.Float64, Longitude: longitude.Float64}
	}
	if capturedAt.Valid {
		at := capturedAt.Time.UTC()
		r.Location.CapturedAt = &at
	}
	r.Place = stringOf(place)
	return r, nil
}

// withDecisions reads the decisions of the requests, in their order.
func withDecisions(ctx context.Context, tx *sql.Tx, requests []domain.AssistanceRequest) ([]ports.AssistanceRecord,
	error) {
	if len(requests) == 0 {
		return nil, nil
	}
	ids := make([]string, len(requests))
	byID := make(map[string]*ports.AssistanceRecord, len(requests))
	out := make([]ports.AssistanceRecord, len(requests))
	for i, r := range requests {
		ids[i] = r.ID
		out[i] = ports.AssistanceRecord{Case: domain.AssistanceCase{Request: r}}
		byID[r.ID] = &out[i]
	}
	for _, read := range []func(context.Context, *sql.Tx, []string, map[string]*ports.AssistanceRecord) error{
		actionsOf, needsOf, voicesOf, dispatchesOf, closuresOf, assistanceNoticesOf,
	} {
		if err := read(ctx, tx, ids, byID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// each runs scan on every row of the query and closes them.
func each(ctx context.Context, tx *sql.Tx, what, query string, ids []string, scan func(scanner) error) error {
	rows, err := tx.QueryContext(ctx, query, ids)
	if err != nil {
		return fmt.Errorf("read the %s of the requests for assistance: %w", what, err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		if err := scan(rows); err != nil {
			return fmt.Errorf("read the %s of the requests for assistance: %w", what, err)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read the %s of the requests for assistance: %w", what, err)
	}
	return nil
}

func actionsOf(ctx context.Context, tx *sql.Tx, ids []string, byID map[string]*ports.AssistanceRecord) error {
	return each(ctx, tx, "decisions", `SELECT `+actionColumns+` FROM assistance_action
		WHERE request_id = ANY($1::text[]) ORDER BY request_id, seq`, ids, func(row scanner) error {
		var (
			id, code                     string
			a                            domain.AssistanceAction
			need, provider, reason, from sql.NullString
		)
		if err := row.Scan(&id, &a.Seq, &code, &a.By.Email, &a.By.Name, &a.At, &need, &provider, &reason,
			&from); err != nil {
			return err
		}
		a.Code, a.At = domain.AssistanceActionCode(code), a.At.UTC()
		a.Need, a.ProviderID = domain.AssistanceNeed(need.String), provider.String
		a.Reason, a.From = domain.CloseReason(reason.String), from.String
		r := byID[id]
		r.Case.History = append(r.Case.History, a)
		return nil
	})
}

func needsOf(ctx context.Context, tx *sql.Tx, ids []string, byID map[string]*ports.AssistanceRecord) error {
	return each(ctx, tx, "needs", `SELECT DISTINCT ON (request_id) request_id, seq, need, specialty, policy_version,
			recorded_by, recorder_name, recorded_at
		FROM assistance_need WHERE request_id = ANY($1::text[]) ORDER BY request_id, seq DESC`, ids,
		func(row scanner) error {
			var (
				id, need, specialty string
				n                   domain.RecordedNeed
			)
			if err := row.Scan(&id, &n.Seq, &need, &specialty, &n.PolicyVersion, &n.By.Email, &n.By.Name,
				&n.At); err != nil {
				return err
			}
			n.Need, n.Specialty, n.At = domain.AssistanceNeed(need), domain.Specialty(specialty), n.At.UTC()
			byID[id].Case.Need = &n
			return nil
		})
}

func voicesOf(ctx context.Context, tx *sql.Tx, ids []string, byID map[string]*ports.AssistanceRecord) error {
	return each(ctx, tx, "locations by voice", `SELECT DISTINCT ON (request_id) request_id, seq, address, latitude,
			longitude, recorded_by, recorder_name, recorded_at
		FROM assistance_voice_location WHERE request_id = ANY($1::text[]) ORDER BY request_id, seq DESC`, ids,
		func(row scanner) error {
			var (
				id string
				v  domain.VoiceLocation
			)
			if err := row.Scan(&id, &v.Seq, &v.Address, &v.Point.Latitude, &v.Point.Longitude, &v.By.Email,
				&v.By.Name, &v.At); err != nil {
				return err
			}
			v.At = v.At.UTC()
			byID[id].Case.Voice = &v
			return nil
		})
}

func dispatchesOf(ctx context.Context, tx *sql.Tx, ids []string, byID map[string]*ports.AssistanceRecord) error {
	return each(ctx, tx, "dispatches", `SELECT `+dispatchColumns+` FROM assistance_dispatch
		WHERE request_id = ANY($1::text[])`, ids, func(row scanner) error {
		var (
			id, need, specialty, source string
			d                           domain.AssistanceDispatch
			phone, address, place       sql.NullString
			accuracy                    sql.NullFloat64
			candidates, attempts        []byte
		)
		if err := row.Scan(&id, &d.Provider.ID, &d.Provider.Name, &d.Provider.ContactEmail, &phone, &need, &specialty,
			&d.DistanceKm, &d.EtaMinutes, &source, &d.Location.Point.Latitude, &d.Location.Point.Longitude, &accuracy,
			&address, &place, &d.SuggestedProviderID, &candidates, &attempts, &d.AssistancePolicyVersion,
			&d.AssignmentPolicyVersion, &d.By.Email, &d.By.Name, &d.At); err != nil {
			return err
		}
		d.Provider.ContactPhone, d.Need, d.Specialty = stringOf(phone), domain.AssistanceNeed(need),
			domain.Specialty(specialty)
		d.Location.Source, d.Location.AccuracyM = domain.SharedSource(source), floatOf(accuracy)
		d.Location.Address, d.Location.Place, d.At = address.String, stringOf(place), d.At.UTC()
		var err error
		if d.Candidates, err = decodeCandidates(candidates); err != nil {
			return err
		}
		if d.Attempts, err = decodeAttempts(attempts); err != nil {
			return err
		}
		byID[id].Case.Dispatch = &d
		return nil
	})
}

func closuresOf(ctx context.Context, tx *sql.Tx, ids []string, byID map[string]*ports.AssistanceRecord) error {
	return each(ctx, tx, "closings", `SELECT request_id, reason, note, closed_by, closer_name, closed_at
		FROM assistance_closure WHERE request_id = ANY($1::text[])`, ids, func(row scanner) error {
		var (
			id, reason string
			note       sql.NullString
			c          domain.AssistanceClosure
		)
		if err := row.Scan(&id, &reason, &note, &c.By.Email, &c.By.Name, &c.At); err != nil {
			return err
		}
		c.Reason, c.Note, c.At = domain.CloseReason(reason), stringOf(note), c.At.UTC()
		byID[id].Case.Closure = &c
		return nil
	})
}

func assistanceNoticesOf(ctx context.Context, tx *sql.Tx, ids []string, byID map[string]*ports.AssistanceRecord) error {
	return each(ctx, tx, "notices", `SELECT `+assistanceNoticeColumns+` FROM assistance_notice
		WHERE request_id = ANY($1::text[])`, ids, func(row scanner) error {
		n, err := scanAssistanceNotice(row)
		if err != nil {
			return err
		}
		byID[n.RequestID].Notice = &n
		return nil
	})
}

func scanAssistanceNotice(row scanner) (ports.AssistanceNotice, error) {
	var (
		n          ports.AssistanceNotice
		status     string
		next, sent sql.NullTime
		cause      sql.NullString
	)
	if err := row.Scan(&n.RequestID, &n.To, &n.Subject, &n.Body, &status, &n.Attempts, &next, &sent,
		&cause); err != nil {
		return ports.AssistanceNotice{}, err
	}
	n.Status, n.NextAttemptAt, n.SentAt, n.LastError = ports.NoticeStatus(status), utc(next), utc(sent), cause.String
	return n, nil
}

// decide appends the action numbered version+1, moves the status (when one
// is given) and writes the rest of the decision, in one transaction. The
// request is locked first, so the count of its decisions cannot change
// before the commit.
func (s *AssistanceInboxStore) decide(ctx context.Context, id string, version int, a domain.AssistanceAction,
	status domain.AssistanceStatus, rest func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("decide on the request for assistance: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var current string
	err = tx.QueryRowContext(ctx, `SELECT status FROM assistance_request WHERE id = $1 FOR UPDATE`, id).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return ports.ErrAssistanceRequestNotFound
	}
	if err != nil {
		return fmt.Errorf("lock the request for assistance: %w", err)
	}
	var decided int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM assistance_action WHERE request_id = $1`,
		id).Scan(&decided); err != nil {
		return fmt.Errorf("count the decisions on the request for assistance: %w", err)
	}
	if decided != version {
		return ports.ErrAssistanceChanged
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO assistance_action (`+actionColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		id, version+1, string(a.Code), a.By.Email, a.By.Name, a.At.UTC(), optional(string(a.Need)),
		optional(a.ProviderID), optional(string(a.Reason)), optional(a.From)); err != nil {
		return fmt.Errorf("record the decision on the request for assistance: %w", err)
	}
	if status != "" && string(status) != current {
		if _, err := tx.ExecContext(ctx, `UPDATE assistance_request SET status = $2 WHERE id = $1`, id,
			string(status)); err != nil {
			return fmt.Errorf("move the status of the request for assistance: %w", err)
		}
	}
	if rest != nil {
		if err := rest(tx); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("decide on the request for assistance: %w", err)
	}
	return nil
}

// Attend makes the request in_call.
func (s *AssistanceInboxStore) Attend(ctx context.Context, id string, version int, a domain.AssistanceAction) error {
	return s.decide(ctx, id, version, a, domain.AssistanceInCall, nil)
}

// RecordNeed records the need in force.
func (s *AssistanceInboxStore) RecordNeed(ctx context.Context, id string, version int, n domain.RecordedNeed,
	a domain.AssistanceAction) error {
	return s.decide(ctx, id, version, a, "", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO assistance_need (request_id, seq, need, specialty,
				policy_version, recorded_by, recorder_name, recorded_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			id, n.Seq, string(n.Need), string(n.Specialty), n.PolicyVersion, n.By.Email, n.By.Name,
			n.At.UTC()); err != nil {
			return fmt.Errorf("record the need of the request for assistance: %w", err)
		}
		return nil
	})
}

// RecordVoiceLocation records the location given by voice in force.
func (s *AssistanceInboxStore) RecordVoiceLocation(ctx context.Context, id string, version int, v domain.VoiceLocation,
	a domain.AssistanceAction) error {
	return s.decide(ctx, id, version, a, "", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO assistance_voice_location (request_id, seq, address, latitude,
				longitude, recorded_by, recorder_name, recorded_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			id, v.Seq, v.Address, v.Point.Latitude, v.Point.Longitude, v.By.Email, v.By.Name, v.At.UTC()); err != nil {
			return fmt.Errorf("record the location by voice of the request for assistance: %w", err)
		}
		return nil
	})
}

// Dispatch makes the request dispatched, records the dispatch and leaves its
// notice due.
func (s *AssistanceInboxStore) Dispatch(ctx context.Context, id string, version int, d domain.AssistanceDispatch,
	a domain.AssistanceAction, n ports.AssistanceNotice) error {
	candidates, err := encodeCandidates(d.Candidates)
	if err != nil {
		return err
	}
	attempts := make([]attemptJSON, 0, len(d.Attempts))
	for _, at := range d.Attempts {
		attempts = append(attempts, attemptJSON{RadiusKm: at.RadiusKm, EligibleCount: at.EligibleCount,
			AttemptedAt: at.AttemptedAt.UTC()})
	}
	attemptsJSON, err := json.Marshal(attempts)
	if err != nil {
		return fmt.Errorf("encode the attempts of the dispatch: %w", err)
	}
	return s.decide(ctx, id, version, a, domain.AssistanceDispatched, func(tx *sql.Tx) error {
		l := d.Location
		if _, err := tx.ExecContext(ctx, `INSERT INTO assistance_dispatch (`+dispatchColumns+`)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21,
				$22, $23)`,
			id, d.Provider.ID, d.Provider.Name, d.Provider.ContactEmail, nullText(d.Provider.ContactPhone),
			string(d.Need), string(d.Specialty), d.DistanceKm, d.EtaMinutes, string(l.Source), l.Point.Latitude,
			l.Point.Longitude, nullFloat(l.AccuracyM), optional(l.Address), nullText(l.Place),
			d.SuggestedProviderID, candidates, attemptsJSON, d.AssistancePolicyVersion, d.AssignmentPolicyVersion,
			d.By.Email, d.By.Name, d.At.UTC()); err != nil {
			return fmt.Errorf("record the dispatch of the request for assistance: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO assistance_notice (request_id, to_email, subject, body, status,
				attempts, next_attempt_at) VALUES ($1, $2, $3, $4, 'pending', 0, $5)`,
			id, n.To, n.Subject, n.Body, n.NextAttemptAt.UTC()); err != nil {
			return fmt.Errorf("leave the notice of the dispatch due: %w", err)
		}
		return nil
	})
}

// Close makes the request closed and records why.
func (s *AssistanceInboxStore) Close(ctx context.Context, id string, version int, c domain.AssistanceClosure,
	a domain.AssistanceAction) error {
	return s.decide(ctx, id, version, a, domain.AssistanceClosed, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO assistance_closure (request_id, reason, note, closed_by,
				closer_name, closed_at) VALUES ($1, $2, $3, $4, $5, $6)`,
			id, string(c.Reason), nullText(c.Note), c.By.Email, c.By.Name, c.At.UTC()); err != nil {
			return fmt.Errorf("record the closing of the request for assistance: %w", err)
		}
		return nil
	})
}

// ClaimNoticesDue holds the due notices for lease by moving their next
// attempt; SKIP LOCKED lets two tasks take different ones.
func (s *AssistanceInboxStore) ClaimNoticesDue(ctx context.Context, now time.Time, lease time.Duration,
	limit int) ([]ports.AssistanceNotice, error) {
	rows, err := s.db.QueryContext(ctx, `UPDATE assistance_notice SET next_attempt_at = $2
		WHERE request_id IN (SELECT request_id FROM assistance_notice
		                     WHERE status = 'pending' AND next_attempt_at <= $1
		                     ORDER BY next_attempt_at, request_id LIMIT $3 FOR UPDATE SKIP LOCKED)
		RETURNING `+assistanceNoticeColumns, now.UTC(), now.Add(lease).UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("hold the due notices of the dispatches: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []ports.AssistanceNotice
	for rows.Next() {
		n, err := scanAssistanceNotice(rows)
		if err != nil {
			return nil, fmt.Errorf("hold the due notices of the dispatches: %w", err)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// NoticeSent records the delivery of the notice.
func (s *AssistanceInboxStore) NoticeSent(ctx context.Context, requestID string, at time.Time) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE assistance_notice
		SET status = 'sent', sent_at = $2, attempts = attempts + 1, next_attempt_at = NULL, last_error = NULL
		WHERE request_id = $1 AND status = 'pending'`, requestID, at.UTC()); err != nil {
		return fmt.Errorf("record the notice of the dispatch %s as sent: %w", requestID, err)
	}
	return nil
}

// NoticeFailed counts a failed delivery and makes the notice due at next.
func (s *AssistanceInboxStore) NoticeFailed(ctx context.Context, requestID string, next time.Time, cause string) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE assistance_notice
		SET attempts = attempts + 1, next_attempt_at = $2, last_error = $3
		WHERE request_id = $1 AND status = 'pending'`, requestID, next.UTC(), cause); err != nil {
		return fmt.Errorf("record the failed notice of the dispatch %s: %w", requestID, err)
	}
	return nil
}

// encodeCandidates keeps the candidates as claim_provider_search does
// (HU-W2.3-01).
func encodeCandidates(list []domain.Candidate) ([]byte, error) {
	out := make([]candidateJSON, 0, len(list))
	for _, c := range list {
		candidate := candidateJSON{ProviderID: c.ProviderID, Name: c.Name, Specialties: specialtyNames(c.Specialties),
			DistanceKm: c.DistanceKm, Availability: string(c.Availability), Eligibility: string(c.Eligibility),
			Suggested: c.Suggested}
		if until := day(c.UnavailableUntil); until.Valid {
			candidate.UnavailableUntil = until.String
		}
		out = append(out, candidate)
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("encode the candidates of the dispatch: %w", err)
	}
	return b, nil
}

func decodeCandidates(b []byte) ([]domain.Candidate, error) {
	var list []candidateJSON
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("decode the candidates of the dispatch: %w", err)
	}
	out := make([]domain.Candidate, 0, len(list))
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
				return nil, fmt.Errorf("decode the candidates of the dispatch: %w", err)
			}
			candidate.UnavailableUntil = &d
		}
		out = append(out, candidate)
	}
	return out, nil
}

func decodeAttempts(b []byte) ([]domain.SearchAttempt, error) {
	var list []attemptJSON
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("decode the attempts of the dispatch: %w", err)
	}
	out := make([]domain.SearchAttempt, 0, len(list))
	for _, a := range list {
		out = append(out, domain.SearchAttempt{RadiusKm: a.RadiusKm, EligibleCount: a.EligibleCount,
			AttemptedAt: a.AttemptedAt.UTC()})
	}
	return out, nil
}
