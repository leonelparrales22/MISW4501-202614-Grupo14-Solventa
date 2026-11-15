package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// EndorsementStore is the ports.EndorsementStore and the
// ports.EndorsementNoticeStore over the tables of migration 0018 (HU-W1.4-02)
// and the three columns of change_request that can change. Every decision is
// one short transaction that first claims the request with a conditional
// UPDATE (EC-D02): no lock is held while the validations call KYC or claims,
// which happens before. Two endorsements of the same policy never start from
// the same terms: the unique (policy_id, version) refuses the second.
type EndorsementStore struct {
	db *sql.DB
}

var (
	_ ports.EndorsementStore       = (*EndorsementStore)(nil)
	_ ports.EndorsementNoticeStore = (*EndorsementStore)(nil)
)

// NewEndorsementStore builds an EndorsementStore over db (already migrated).
func NewEndorsementStore(db *sql.DB) *EndorsementStore { return &EndorsementStore{db: db} }

// endorsementVersionKey is the unique constraint of (policy_id, version).
const endorsementVersionKey = "endorsement_policy_id_version_key"

// maxRenewalChain bounds how many renewals back the terms of a policy are
// looked for.
const maxRenewalChain = 50

// endorsedTermsJSON is the part of the photo of an endorsement that has no
// column of its own.
type endorsedTermsJSON struct {
	Beneficiaries []beneficiaryJSON `json:"beneficiaries"`
	Onerous       *onerousJSON      `json:"onerous_beneficiary,omitempty"`
	PersonalData  contactJSON       `json:"personal_data"`
}

func encodeEndorsedTerms(t domain.EndorsedTerms) ([]byte, error) {
	out := endorsedTermsJSON{Beneficiaries: make([]beneficiaryJSON, 0, len(t.Beneficiaries)),
		PersonalData: contactJSON{Address: t.Contact.Address, Email: t.Contact.Email, Phone: t.Contact.Phone}}
	for _, b := range t.Beneficiaries {
		out.Beneficiaries = append(out.Beneficiaries, beneficiaryJSON{FullName: b.FullName, Relationship: string(b.Relationship),
			DocumentType: string(b.DocumentType), DocumentNumber: b.DocumentNumber, ShareBasisPoints: b.ShareBasisPoints})
	}
	if t.Onerous != nil {
		out.Onerous = &onerousJSON{PartnerID: t.Onerous.PartnerID, Name: t.Onerous.Name, Basis: t.Onerous.Basis}
	}
	return json.Marshal(out)
}

func decodeEndorsedTerms(raw []byte, t *domain.EndorsedTerms) error {
	var in endorsedTermsJSON
	if err := json.Unmarshal(raw, &in); err != nil {
		return err
	}
	t.Beneficiaries = make([]domain.Beneficiary, 0, len(in.Beneficiaries))
	for _, b := range in.Beneficiaries {
		t.Beneficiaries = append(t.Beneficiaries, domain.Beneficiary{FullName: b.FullName,
			Relationship: domain.Relationship(b.Relationship), DocumentType: domain.DocumentType(b.DocumentType),
			DocumentNumber: b.DocumentNumber, ShareBasisPoints: b.ShareBasisPoints})
	}
	if in.Onerous != nil {
		t.Onerous = &domain.OnerousBeneficiary{PartnerID: in.Onerous.PartnerID, Name: in.Onerous.Name, Basis: in.Onerous.Basis}
	}
	t.Contact = domain.ContactData{Address: in.PersonalData.Address, Email: in.PersonalData.Email, Phone: in.PersonalData.Phone}
	return nil
}

type validationJSON struct {
	Code    string            `json:"code"`
	Outcome string            `json:"outcome"`
	Guard   bool              `json:"guard,omitempty"`
	Params  map[string]string `json:"params,omitempty"`
}

func encodeValidations(v domain.Validations) ([]byte, error) {
	out := make([]validationJSON, 0, len(v))
	for _, r := range v {
		out = append(out, validationJSON{Code: string(r.Code), Outcome: string(r.Outcome), Guard: r.Guard, Params: r.Params})
	}
	return json.Marshal(out)
}

func decodeValidations(raw []byte) (domain.Validations, error) {
	var in []validationJSON
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	out := make(domain.Validations, 0, len(in))
	for _, r := range in {
		params := r.Params
		if params == nil {
			params = map[string]string{}
		}
		out = append(out, domain.ValidationResult{Code: domain.ValidationCode(r.Code), Outcome: domain.ValidationOutcome(r.Outcome),
			Guard: r.Guard, Params: params})
	}
	return out, nil
}

const endorsementColumns = `endorsement_id, request_id, attempt_id, policy_id, version, change_type, insured_amount_minor,
	premium_minor, previous_premium_minor, currency, coverages, contact_email, terms, applied_by_sub, applied_by_name,
	applied_by_email, applied_at`

func scanEndorsement(s scanner) (domain.Endorsement, error) {
	var (
		e                                domain.Endorsement
		attemptID                        int64
		changeType                       string
		amount, premium, previousPremium int64
		coverages, terms                 []byte
		contactEmail                     sql.NullString
	)
	if err := s.Scan(&e.ID, &e.RequestID, &attemptID, &e.PolicyID, &e.Version, &changeType, &amount, &premium, &previousPremium,
		&e.Terms.Currency, &coverages, &contactEmail, &terms, &e.By.Subject, &e.By.Name, &e.By.Email, &e.At); err != nil {
		return domain.Endorsement{}, err
	}
	e.AttemptID, e.Type = strconv.FormatInt(attemptID, 10), domain.ChangeType(changeType)
	e.Terms.InsuredAmount, e.Terms.Premium = domain.FromMinorUnits(amount), domain.FromMinorUnits(premium)
	e.PreviousPremium, e.At = domain.FromMinorUnits(previousPremium), e.At.UTC()
	var err error
	if e.Terms.Coverages, err = decodeCoverages(coverages); err != nil {
		return domain.Endorsement{}, fmt.Errorf("decode the coverages of endorsement %s: %w", e.ID, err)
	}
	if err := decodeEndorsedTerms(terms, &e.Terms); err != nil {
		return domain.Endorsement{}, fmt.Errorf("decode the terms of endorsement %s: %w", e.ID, err)
	}
	return e, nil
}

const attemptColumns = `attempt_id, request_id, policy_id, outcome, validations, attempted_by_sub, attempted_by_name,
	attempted_by_email, attempted_at`

func scanAttempt(s scanner) (domain.EndorsementAttempt, error) {
	var (
		a           domain.EndorsementAttempt
		id          int64
		outcome     string
		validations []byte
	)
	if err := s.Scan(&id, &a.RequestID, &a.PolicyID, &outcome, &validations, &a.By.Subject, &a.By.Name, &a.By.Email, &a.At); err != nil {
		return domain.EndorsementAttempt{}, err
	}
	a.ID, a.Outcome, a.At = strconv.FormatInt(id, 10), domain.EndorsementOutcome(outcome), a.At.UTC()
	var err error
	if a.Validations, err = decodeValidations(validations); err != nil {
		return domain.EndorsementAttempt{}, fmt.Errorf("decode the validations of attempt %s: %w", a.ID, err)
	}
	return a, nil
}

// latestTermsQuery follows the policy $1 back through the policies it renews
// (up to $2 steps) and reads the last endorsement of the closest one that has
// any, with how many steps back it is.
var latestTermsQuery = `WITH RECURSIVE chain AS (
		SELECT policy_id, renewal_of, 0 AS depth FROM policy WHERE policy_id = $1
		UNION ALL
		SELECT p.policy_id, p.renewal_of, c.depth + 1 FROM policy p JOIN chain c ON p.policy_id = c.renewal_of
		WHERE c.depth < $2
	)
	SELECT c.depth, ` + prefixed("e", endorsementColumns) + `
	FROM chain c
	JOIN LATERAL (SELECT * FROM endorsement x WHERE x.policy_id = c.policy_id ORDER BY x.version DESC LIMIT 1) e ON true
	ORDER BY c.depth LIMIT 1`

// LatestTerms follows the policy back through the policies it renews and
// returns the last endorsement of the closest one that has any.
func (s *EndorsementStore) LatestTerms(ctx context.Context, policyID string) (domain.LatestTerms, error) {
	row := s.db.QueryRowContext(ctx, latestTermsQuery, policyID, maxRenewalChain)
	var depth int
	e, err := scanEndorsement(withPrefix{row: row, first: &depth})
	if errors.Is(err, sql.ErrNoRows) {
		return domain.LatestTerms{}, nil
	}
	if err != nil {
		return domain.LatestTerms{}, fmt.Errorf("read the latest terms of policy %s: %w", policyID, err)
	}
	out := domain.LatestTerms{Found: true, Own: depth == 0, Terms: e.Terms, EndorsementID: e.ID, Type: e.Type, At: e.At}
	if out.Own {
		out.Version = e.Version
	}
	return out, nil
}

// withPrefix scans a leading column before the ones of the wrapped scan.
type withPrefix struct {
	row   scanner
	first any
}

func (w withPrefix) Scan(dest ...any) error { return w.row.Scan(append([]any{w.first}, dest...)...) }

// Record returns the endorsement, the last attempt and the actions of a request.
func (s *EndorsementStore) Record(ctx context.Context, requestID string) (domain.RequestRecord, error) {
	var out domain.RequestRecord
	e, err := scanEndorsement(s.db.QueryRowContext(ctx, `SELECT `+endorsementColumns+` FROM endorsement WHERE request_id = $1`, requestID))
	switch {
	case err == nil:
		out.Endorsement = &e
	case !errors.Is(err, sql.ErrNoRows):
		return domain.RequestRecord{}, fmt.Errorf("read the endorsement of %s: %w", requestID, err)
	}
	a, err := scanAttempt(s.db.QueryRowContext(ctx, `SELECT `+attemptColumns+` FROM endorsement_attempt
		WHERE request_id = $1 ORDER BY attempt_id DESC LIMIT 1`, requestID))
	switch {
	case err == nil:
		out.LastAttempt = &a
	case !errors.Is(err, sql.ErrNoRows):
		return domain.RequestRecord{}, fmt.Errorf("read the last attempt of %s: %w", requestID, err)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT action, COALESCE(observations, ''), actor_sub, actor_name, actor_email, acted_at
		FROM change_request_action WHERE request_id = $1 ORDER BY action_id`, requestID)
	if err != nil {
		return domain.RequestRecord{}, fmt.Errorf("read the actions of %s: %w", requestID, err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		a := domain.RequestAction{RequestID: requestID}
		var action string
		if err := rows.Scan(&action, &a.Observations, &a.By.Subject, &a.By.Name, &a.By.Email, &a.At); err != nil {
			return domain.RequestRecord{}, fmt.Errorf("read an action of %s: %w", requestID, err)
		}
		a.Type, a.At = domain.RequestActionType(action), a.At.UTC()
		out.Actions = append(out.Actions, a)
	}
	return out, rows.Err()
}

// Endorsements counts the endorsements of a policy and reads the last.
func (s *EndorsementStore) Endorsements(ctx context.Context, policyID string) (ports.PolicyEndorsements, error) {
	var out ports.PolicyEndorsements
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM endorsement WHERE policy_id = $1`, policyID).Scan(&out.Count); err != nil {
		return ports.PolicyEndorsements{}, fmt.Errorf("count the endorsements of %s: %w", policyID, err)
	}
	if out.Count == 0 {
		return out, nil
	}
	e, err := scanEndorsement(s.db.QueryRowContext(ctx, `SELECT `+endorsementColumns+` FROM endorsement
		WHERE policy_id = $1 ORDER BY version DESC LIMIT 1`, policyID))
	if err != nil {
		return ports.PolicyEndorsements{}, fmt.Errorf("read the last endorsement of %s: %w", policyID, err)
	}
	out.Last = &e
	return out, nil
}

// statusOf reads the status of a request, to tell why a claim did not win.
func statusOf(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, requestID string) (domain.ChangeRequestStatus, error) {
	var st string
	err := q.QueryRowContext(ctx, `SELECT status FROM change_request WHERE request_id = $1`, requestID).Scan(&st)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ports.ErrChangeRequestNotFound
	}
	if err != nil {
		return "", fmt.Errorf("read the status of %s: %w", requestID, err)
	}
	return domain.ChangeRequestStatus(st), nil
}

func insertAction(ctx context.Context, tx *sql.Tx, requestID string, t domain.RequestActionType, observations string,
	by domain.Actor, at time.Time) error {
	var obs sql.NullString
	if t != domain.ActionReview {
		obs = sql.NullString{String: observations, Valid: true}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO change_request_action (request_id, action, observations, actor_sub, actor_name,
		actor_email, acted_at) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		requestID, string(t), obs, by.Subject, by.Name, by.Email, at.UTC()); err != nil {
		return fmt.Errorf("record the %s of %s: %w", t, requestID, err)
	}
	return nil
}

func insertNotice(ctx context.Context, tx *sql.Tx, requestID string, outcome domain.NoticeOutcome, at time.Time) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO endorsement_notice (request_id, outcome, status, next_attempt_at, created_at)
		VALUES ($1, $2, 'pending', $3, $3)`, requestID, string(outcome), at.UTC()); err != nil {
		return fmt.Errorf("schedule the notice of %s: %w", requestID, err)
	}
	return nil
}

// claim moves a request from one of the statuses to another, setting the
// decision reason when reason is not empty, and reports whether it won.
func claim(ctx context.Context, tx *sql.Tx, requestID string, to domain.ChangeRequestStatus, reason string, at time.Time,
	from ...domain.ChangeRequestStatus) (bool, error) {
	statuses := make([]string, 0, len(from))
	for _, f := range from {
		statuses = append(statuses, string(f))
	}
	res, err := tx.ExecContext(ctx, `UPDATE change_request SET status = $2, status_changed_at = $3,
		decision_reason = COALESCE($4, decision_reason)
		WHERE request_id = $1 AND status = ANY($5) AND decision_reason IS NULL`,
		requestID, string(to), at.UTC(), nullString(reason), statuses)
	if err != nil {
		return false, fmt.Errorf("move %s to %s: %w", requestID, to, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("move %s to %s: %w", requestID, to, err)
	}
	return n == 1, nil
}

// StartReview moves a filed request to in review.
func (s *EndorsementStore) StartReview(ctx context.Context, requestID string, by domain.Actor, at time.Time) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("start the review of %s: %w", requestID, err)
	}
	defer func() { _ = tx.Rollback() }()
	won, err := claim(ctx, tx, requestID, domain.ChangeRequestInReview, "", at, domain.ChangeRequestFiled)
	if err != nil {
		return false, err
	}
	if !won {
		st, err := statusOf(ctx, tx, requestID)
		if err != nil {
			return false, err
		}
		if st == domain.ChangeRequestInReview {
			return false, nil
		}
		return false, ports.ErrRequestAlreadyDecided
	}
	if err := insertAction(ctx, tx, requestID, domain.ActionReview, "", by, at); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("start the review of %s: %w", requestID, err)
	}
	return true, nil
}

func insertAttempt(ctx context.Context, tx *sql.Tx, a domain.EndorsementAttempt) (int64, error) {
	validations, err := encodeValidations(a.Validations)
	if err != nil {
		return 0, err
	}
	var id int64
	if err := tx.QueryRowContext(ctx, `INSERT INTO endorsement_attempt (request_id, policy_id, outcome, failed_code, validations,
		attempted_by_sub, attempted_by_name, attempted_by_email, attempted_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING attempt_id`,
		a.RequestID, a.PolicyID, string(a.Outcome), nullString(string(a.Validations.FirstFailed())), string(validations),
		a.By.Subject, a.By.Name, a.By.Email, a.At.UTC()).Scan(&id); err != nil {
		return 0, fmt.Errorf("record the attempt of %s: %w", a.RequestID, err)
	}
	return id, nil
}

// Apply claims the request, numbers the endorsement and stores the attempt, the
// endorsement and its notice in one transaction.
func (s *EndorsementStore) Apply(ctx context.Context, a ports.AppliedEndorsement) (domain.Endorsement, error) {
	r := a.Request
	coverages, err := encodeCoverages(a.Terms.Coverages)
	if err != nil {
		return domain.Endorsement{}, err
	}
	terms, err := encodeEndorsedTerms(a.Terms)
	if err != nil {
		return domain.Endorsement{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Endorsement{}, fmt.Errorf("apply %s: %w", r.ID, err)
	}
	defer func() { _ = tx.Rollback() }()
	won, err := claim(ctx, tx, r.ID, domain.ChangeRequestApplied, "", a.At, domain.ChangeRequestFiled, domain.ChangeRequestInReview)
	if err != nil {
		return domain.Endorsement{}, err
	}
	if !won {
		return domain.Endorsement{}, ports.ErrRequestAlreadyDecided
	}
	year := domain.EndorsementYear(a.At)
	var n int
	if err := tx.QueryRowContext(ctx, `INSERT INTO endorsement_counter (year, last_number) VALUES ($1, 1)
		ON CONFLICT (year) DO UPDATE SET last_number = endorsement_counter.last_number + 1
		RETURNING last_number`, year).Scan(&n); err != nil {
		return domain.Endorsement{}, fmt.Errorf("number the endorsement of %s: %w", r.ID, err)
	}
	attempt := domain.EndorsementAttempt{RequestID: r.ID, PolicyID: r.PolicyID, Outcome: domain.OutcomeApplied,
		Validations: a.Validations, By: a.By, At: a.At}
	attemptID, err := insertAttempt(ctx, tx, attempt)
	if err != nil {
		return domain.Endorsement{}, err
	}
	e := domain.Endorsement{ID: domain.FormatEndorsementID(year, n), RequestID: r.ID, PolicyID: r.PolicyID, Type: r.Type,
		Version: a.BaseVersion + 1, Terms: a.Terms, PreviousPremium: a.PreviousPremium,
		AttemptID: strconv.FormatInt(attemptID, 10), By: a.By, At: a.At.UTC()}
	var email sql.NullString
	if a.Terms.Contact.Email != nil {
		email = nullString(*a.Terms.Contact.Email)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO endorsement (`+endorsementColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)`,
		e.ID, e.RequestID, attemptID, e.PolicyID, e.Version, string(e.Type), domain.ToMinorUnits(a.Terms.InsuredAmount),
		domain.ToMinorUnits(a.Terms.Premium), domain.ToMinorUnits(a.PreviousPremium), a.Terms.Currency, string(coverages), email,
		string(terms), a.By.Subject, a.By.Name, a.By.Email, e.At)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && pgErr.ConstraintName == endorsementVersionKey {
		return domain.Endorsement{}, ports.ErrPolicyChanged
	}
	if err != nil {
		return domain.Endorsement{}, fmt.Errorf("store the endorsement of %s: %w", r.ID, err)
	}
	if err := insertNotice(ctx, tx, r.ID, domain.NoticeApplied, a.At); err != nil {
		return domain.Endorsement{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Endorsement{}, fmt.Errorf("apply %s: %w", r.ID, err)
	}
	return e, nil
}

// RecordNotApplied claims the request as not applied and records the attempt.
func (s *EndorsementStore) RecordNotApplied(ctx context.Context, a domain.EndorsementAttempt) (domain.EndorsementAttempt, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.EndorsementAttempt{}, fmt.Errorf("record the attempt of %s: %w", a.RequestID, err)
	}
	defer func() { _ = tx.Rollback() }()
	won, err := claim(ctx, tx, a.RequestID, domain.ChangeRequestNotApplied, "", a.At, domain.ChangeRequestFiled,
		domain.ChangeRequestInReview)
	if err != nil {
		return domain.EndorsementAttempt{}, err
	}
	if !won {
		return domain.EndorsementAttempt{}, ports.ErrRequestAlreadyDecided
	}
	a.Outcome = domain.OutcomeNotApplied
	id, err := insertAttempt(ctx, tx, a)
	if err != nil {
		return domain.EndorsementAttempt{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.EndorsementAttempt{}, fmt.Errorf("record the attempt of %s: %w", a.RequestID, err)
	}
	a.ID, a.At = strconv.FormatInt(id, 10), a.At.UTC()
	return a, nil
}

// Reject claims a filed or in review request as rejected.
func (s *EndorsementStore) Reject(ctx context.Context, requestID, observations string, by domain.Actor, at time.Time) error {
	return s.decide(ctx, requestID, observations, by, at, domain.ActionRejection, ports.ErrRequestAlreadyDecided,
		domain.ChangeRequestRejected, domain.ChangeRequestFiled, domain.ChangeRequestInReview)
}

// Return writes the observations on a not applied request: the insured reads
// it as rejected with them.
func (s *EndorsementStore) Return(ctx context.Context, requestID, observations string, by domain.Actor, at time.Time) error {
	return s.decide(ctx, requestID, observations, by, at, domain.ActionReturn, ports.ErrNotReturnable,
		domain.ChangeRequestNotApplied, domain.ChangeRequestNotApplied)
}

func (s *EndorsementStore) decide(ctx context.Context, requestID, observations string, by domain.Actor, at time.Time,
	action domain.RequestActionType, lost error, to domain.ChangeRequestStatus, from ...domain.ChangeRequestStatus) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("record the %s of %s: %w", action, requestID, err)
	}
	defer func() { _ = tx.Rollback() }()
	won, err := claim(ctx, tx, requestID, to, observations, at, from...)
	if err != nil {
		return err
	}
	if !won {
		if _, err := statusOf(ctx, tx, requestID); err != nil {
			return err
		}
		return lost
	}
	if err := insertAction(ctx, tx, requestID, action, observations, by, at); err != nil {
		return err
	}
	outcome := domain.NoticeRejected
	if action == domain.ActionReturn {
		outcome = domain.NoticeReturned
	}
	if err := insertNotice(ctx, tx, requestID, outcome, at); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("record the %s of %s: %w", action, requestID, err)
	}
	return nil
}

// inboxPending is the condition of a pending request (the default of the inbox).
const inboxPending = `(cr.status IN ('filed', 'in_review') OR (cr.status = 'not_applied' AND cr.decision_reason IS NULL))`

// inboxFilter selects the requests of the type $1 (empty for all), the
// status filter $2 and the first instant of filing $3 (NULL for no bound).
const inboxFilter = ` FROM change_request cr JOIN policy p ON p.policy_id = cr.policy_id
	WHERE ($1::text = '' OR cr.change_type = $1)
	  AND ($2::text = 'all'
	       OR ($2::text = 'pending' AND ` + inboxPending + `)
	       OR cr.status = $2)
	  AND ($3::timestamptz IS NULL OR cr.filed_at >= $3)`

// inboxListQuery is the page of the inbox: the request columns that
// scanChangeRequest reads followed by the holder of the policy.
var inboxListQuery = `SELECT ` + prefixed("cr", changeRequestColumns) + `, p.holder_full_name, p.holder_document_id` +
	inboxFilter + ` ORDER BY cr.filed_at DESC, cr.request_id DESC LIMIT $4 OFFSET $5`

// Inbox reads the page, its total and the indicators in one snapshot.
func (s *EndorsementStore) Inbox(ctx context.Context, f domain.EndorsementInboxFilter) (domain.EndorsementInboxPage, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return domain.EndorsementInboxPage{}, fmt.Errorf("read the endorsement inbox: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var from sql.NullTime
	if !f.FiledFrom.IsZero() {
		from = nullTime(domain.CivilDayOf(f.FiledFrom))
	}
	args := []any{string(f.ChangeType), string(f.Status), from}
	out := domain.EndorsementInboxPage{Items: []domain.EndorsementInboxItem{}, Page: f.Page, PageSize: f.PageSize}
	if err := tx.QueryRowContext(ctx, `SELECT count(*)`+inboxFilter, args...).Scan(&out.Total); err != nil {
		return domain.EndorsementInboxPage{}, fmt.Errorf("count the endorsement inbox: %w", err)
	}
	rows, err := tx.QueryContext(ctx, inboxListQuery, append(args, f.PageSize, f.Offset())...)
	if err != nil {
		return domain.EndorsementInboxPage{}, fmt.Errorf("list the endorsement inbox: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var item domain.EndorsementInboxItem
		r, err := scanChangeRequest(withColumns{row: rows, extra: []any{&item.HolderName, &item.HolderDocument}})
		if err != nil {
			return domain.EndorsementInboxPage{}, fmt.Errorf("read a request of the inbox: %w", err)
		}
		item.Request = r
		out.Items = append(out.Items, item)
	}
	if err := rows.Err(); err != nil {
		return domain.EndorsementInboxPage{}, fmt.Errorf("list the endorsement inbox: %w", err)
	}
	if out.Indicators, err = inboxIndicators(ctx, tx, f.Now); err != nil {
		return domain.EndorsementInboxPage{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.EndorsementInboxPage{}, fmt.Errorf("read the endorsement inbox: %w", err)
	}
	return out, nil
}

func inboxIndicators(ctx context.Context, tx *sql.Tx, now time.Time) (domain.EndorsementIndicators, error) {
	var out domain.EndorsementIndicators
	now = now.UTC()
	if err := tx.QueryRowContext(ctx, `SELECT count(*), count(*) FILTER (WHERE cr.filed_at < $1)
		FROM change_request cr WHERE `+inboxPending, now.Add(-domain.PendingOverdue)).Scan(&out.Pending, &out.PendingOverdue); err != nil {
		return domain.EndorsementIndicators{}, fmt.Errorf("count the pending requests: %w", err)
	}
	dayStart := domain.CivilDayOf(now)
	var mean sql.NullFloat64
	if err := tx.QueryRowContext(ctx, `SELECT count(*), avg(extract(epoch FROM e.applied_at - cr.filed_at))
		FROM endorsement e JOIN change_request cr ON cr.request_id = e.request_id
		WHERE e.applied_at >= $1 AND e.applied_at < $2`, dayStart.UTC(), dayStart.AddDate(0, 0, 1).UTC()).
		Scan(&out.AppliedToday, &mean); err != nil {
		return domain.EndorsementIndicators{}, fmt.Errorf("count the endorsements of today: %w", err)
	}
	if mean.Valid {
		d := time.Duration(mean.Float64 * float64(time.Second)).Round(time.Second)
		out.MeanTimeToApply = &d
	}
	since := now.Add(-domain.RecentDecisionWindow)
	var rejected int
	if err := tx.QueryRowContext(ctx, `SELECT count(*), count(*) FILTER (WHERE status = 'rejected') FROM change_request
		WHERE status IN ('rejected', 'not_applied') AND status_changed_at >= $1`, since).Scan(&out.RejectedRecently, &rejected); err != nil {
		return domain.EndorsementIndicators{}, fmt.Errorf("count the rejected requests: %w", err)
	}
	var (
		code    sql.NullString
		failing int
	)
	err := tx.QueryRowContext(ctx, `SELECT failed_code, count(*) FROM endorsement_attempt
		WHERE outcome = 'not_applied' AND attempted_at >= $1
		GROUP BY failed_code ORDER BY count(*) DESC, failed_code LIMIT 1`, since).Scan(&code, &failing)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return domain.EndorsementIndicators{}, fmt.Errorf("read the most frequent reason: %w", err)
	}
	switch {
	case rejected > failing:
		out.TopReason = domain.TopReasonAnalystRejection
	case code.Valid:
		out.TopReason = code.String
	}
	return out, nil
}

// History reads the endorsements and the attempts not applied of the policy,
// each with its request, the newest first.
func (s *EndorsementStore) History(ctx context.Context, policyID string) ([]domain.EndorsementHistoryEntry, error) {
	requests, err := s.requestsOf(ctx, policyID)
	if err != nil {
		return nil, err
	}
	out := []domain.EndorsementHistoryEntry{}
	rows, err := s.db.QueryContext(ctx, `SELECT `+endorsementColumns+` FROM endorsement WHERE policy_id = $1`, policyID)
	if err != nil {
		return nil, fmt.Errorf("read the endorsements of %s: %w", policyID, err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		e, err := scanEndorsement(rows)
		if err != nil {
			return nil, fmt.Errorf("read an endorsement of %s: %w", policyID, err)
		}
		out = append(out, domain.EndorsementHistoryEntry{Outcome: domain.OutcomeApplied, At: e.At, EndorsementID: e.ID,
			Request: requests[e.RequestID], PreviousPremium: e.PreviousPremium, NewPremium: e.Terms.Premium,
			Currency: e.Terms.Currency, By: e.By})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the endorsements of %s: %w", policyID, err)
	}
	attempts, err := s.db.QueryContext(ctx, `SELECT `+attemptColumns+` FROM endorsement_attempt
		WHERE policy_id = $1 AND outcome = 'not_applied'`, policyID)
	if err != nil {
		return nil, fmt.Errorf("read the attempts of %s: %w", policyID, err)
	}
	defer func() { _ = attempts.Close() }()
	for attempts.Next() {
		a, err := scanAttempt(attempts)
		if err != nil {
			return nil, fmt.Errorf("read an attempt of %s: %w", policyID, err)
		}
		out = append(out, domain.EndorsementHistoryEntry{Outcome: domain.OutcomeNotApplied, At: a.At,
			Request: requests[a.RequestID], Failed: a.Validations.Of(domain.ValidationFailed), By: a.By})
	}
	if err := attempts.Err(); err != nil {
		return nil, fmt.Errorf("read the attempts of %s: %w", policyID, err)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out, nil
}

// requestsOf reads the requests of a policy, by id.
func (s *EndorsementStore) requestsOf(ctx context.Context, policyID string) (map[string]domain.ChangeRequest, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+changeRequestColumns+` FROM change_request WHERE policy_id = $1`, policyID)
	if err != nil {
		return nil, fmt.Errorf("read the requests of %s: %w", policyID, err)
	}
	list, err := scanChangeRequests(rows)
	if err != nil {
		return nil, fmt.Errorf("read the requests of %s: %w", policyID, err)
	}
	out := make(map[string]domain.ChangeRequest, len(list))
	for _, r := range list {
		out[r.ID] = r
	}
	return out, nil
}

// ListDueNotices returns the pending e-mails due at now, with who receives
// them and what they say.
func (s *EndorsementStore) ListDueNotices(ctx context.Context, now time.Time, limit int) ([]domain.EndorsementNotice, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT n.request_id, cr.policy_id, cr.change_type, n.outcome,
			COALESCE(NULLIF(cr.requested_by_email, ''), pc.holder_email), COALESCE(NULLIF(cr.requested_by_name, ''), pc.holder_full_name),
			cr.status_changed_at, COALESCE(cr.decision_reason, ''), COALESCE(e.endorsement_id, ''),
			n.status, n.attempts, n.next_attempt_at, n.sent_at, n.last_failure_at, COALESCE(n.last_failure, '')
		FROM endorsement_notice n
		JOIN change_request cr ON cr.request_id = n.request_id
		JOIN policy_current pc ON pc.policy_id = cr.policy_id
		LEFT JOIN endorsement e ON e.request_id = n.request_id
		WHERE n.status = 'pending' AND n.next_attempt_at <= $1
		ORDER BY n.next_attempt_at, n.request_id LIMIT $2`, now.UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("list the due endorsement notices: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []domain.EndorsementNotice{}
	for rows.Next() {
		var (
			n                           domain.EndorsementNotice
			changeType, outcome, status string
			next, sent, lastFailure     sql.NullTime
		)
		if err := rows.Scan(&n.RequestID, &n.PolicyID, &changeType, &outcome, &n.To, &n.Name, &n.DecidedAt, &n.Observations,
			&n.EndorsementID, &status, &n.Delivery.Attempts, &next, &sent, &lastFailure, &n.Delivery.LastFailure); err != nil {
			return nil, fmt.Errorf("read a due endorsement notice: %w", err)
		}
		n.ChangeType, n.Outcome, n.Delivery.Status = domain.ChangeType(changeType), domain.NoticeOutcome(outcome),
			domain.NotificationStatus(status)
		n.DecidedAt = n.DecidedAt.UTC()
		n.Delivery.NextAttemptAt, n.Delivery.SentAt, n.Delivery.LastFailureAt = next.Time.UTC(), sent.Time.UTC(), lastFailure.Time.UTC()
		out = append(out, n)
	}
	return out, rows.Err()
}

// UpdateNotice records where the e-mail of a request stands.
func (s *EndorsementStore) UpdateNotice(ctx context.Context, requestID string, d domain.NoticeDelivery) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE endorsement_notice SET status = $2, attempts = $3, next_attempt_at = $4,
		sent_at = $5, last_failure_at = $6, last_failure = $7 WHERE request_id = $1`,
		requestID, string(d.Status), d.Attempts, nullTime(d.NextAttemptAt), nullTime(d.SentAt), nullTime(d.LastFailureAt),
		nullString(d.LastFailure)); err != nil {
		return fmt.Errorf("record the notice of %s: %w", requestID, err)
	}
	return nil
}
