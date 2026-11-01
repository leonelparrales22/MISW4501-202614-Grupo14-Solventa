package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// ReviewStore is the ports.ReviewRepository over the review_request and
// human_decision tables (HU-W1.1-02, migration 0007_review_inbox.sql): the
// advisor inbox, the append-only record of the decisions (CA-4) and the
// customer notices survive a restart of the service (fix C5).
//
// RecordDecision is the atomic claim of EC-D02: a conditional UPDATE moves the
// case from pending to decided and, in the same transaction, the decision is
// inserted. Under READ COMMITTED a second transaction on the same case waits
// for the first and re-evaluates the condition, so there is a single winner
// even when the advisors are served by different tasks; the unique
// acceptance_id of human_decision is the second barrier. There is no
// SELECT ... FOR UPDATE.
type ReviewStore struct {
	db *sql.DB
}

var _ ports.ReviewRepository = (*ReviewStore)(nil)

// NewReviewStore builds a ReviewStore over db (already migrated).
func NewReviewStore(db *sql.DB) *ReviewStore { return &ReviewStore{db: db} }

const reviewRequestColumns = `r.acceptance_id, r.offer_id, r.customer_id, r.applicant, r.premium_minor, r.currency, r.reason,
	r.received_at, r.status, r.offer_mode, r.offer_profile_score, r.offer_expires_at, r.offer_terms,
	r.notice_channel, r.notice_status, r.notice_attempts, r.notice_next_attempt_at, r.notice_sent_at,
	r.notice_last_failure_at, r.notice_last_failure`

const humanDecisionColumns = `d.decision_id, d.acceptance_id, d.type, d.decided_by, d.decided_by_email, d.decided_at,
	d.observations, d.premium_adjustment_pct, d.adjusted_offer_id, d.adjusted_premium_minor, d.adjusted_currency,
	d.adjusted_expires_at`

// reviewCaseQuery reads a case with its decision, if any.
const reviewCaseQuery = `SELECT ` + reviewRequestColumns + `, ` + humanDecisionColumns + `
	FROM review_request r LEFT JOIN human_decision d ON d.acceptance_id = r.acceptance_id`

// querier is what reading a case needs from a database or a transaction.
type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// Enqueue stores the request once per acceptance, pending. The composite
// foreign key refuses a request without its acceptance or with another
// customer: the processor then retries the acceptance.
func (s *ReviewStore) Enqueue(ctx context.Context, req domain.ReviewRequest) error {
	applicant, err := encodeApplicant(req.Applicant)
	if err != nil {
		return err
	}
	terms, err := encodeTerms(req.Terms)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO review_request (acceptance_id, offer_id, customer_id, applicant,
			premium_minor, currency, reason, received_at, status, offer_mode, offer_profile_score, offer_expires_at, offer_terms)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'pending', $9, $10, $11, $12)
		ON CONFLICT (acceptance_id) DO NOTHING`,
		req.AcceptanceID, req.OfferID, req.CustomerID, applicant, domain.ToMinorUnits(req.Premium), req.Currency, req.Reason,
		req.ReceivedAt.UTC(), nullString(string(req.OfferMode)), req.ProfileScore, nullTime(req.OfferExpiresAt), terms)
	if err != nil {
		return fmt.Errorf("enqueue the review request %s: %w", req.AcceptanceID, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 1 {
		slog.InfoContext(ctx, "request routed to human decision", "acceptance_id", req.AcceptanceID, "offer_id", req.OfferID, "reason", req.Reason)
	}
	return nil
}

// List returns the cases that match the filter ordered by arrival (oldest
// first unless Descending, ties by acceptance id) and the counts per status
// for the filter reason. Both are read in one snapshot, so they agree.
func (s *ReviewStore) List(ctx context.Context, f domain.ReviewFilter) ([]domain.ReviewCase, domain.ReviewCounts, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, domain.ReviewCounts{}, fmt.Errorf("list the review requests: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	order := "ASC"
	if f.Descending {
		order = "DESC"
	}
	rows, err := tx.QueryContext(ctx, reviewCaseQuery+`
		WHERE ($1 = '' OR r.reason = $1) AND ($2 = '' OR r.status = $2)
		ORDER BY r.received_at `+order+`, r.acceptance_id ASC`, f.Reason, string(f.Status))
	if err != nil {
		return nil, domain.ReviewCounts{}, fmt.Errorf("list the review requests: %w", err)
	}
	out, err := scanReviewCases(rows)
	if err != nil {
		return nil, domain.ReviewCounts{}, err
	}

	var counts domain.ReviewCounts
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FILTER (WHERE status = 'pending'), count(*) FILTER (WHERE status = 'decided')
		FROM review_request WHERE ($1 = '' OR reason = $1)`, f.Reason).Scan(&counts.Pending, &counts.Decided); err != nil {
		return nil, domain.ReviewCounts{}, fmt.Errorf("count the review requests: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, domain.ReviewCounts{}, fmt.Errorf("list the review requests: %w", err)
	}
	return out, counts, nil
}

// Get returns the case of the acceptance, with its decision and notice.
func (s *ReviewStore) Get(ctx context.Context, acceptanceID string) (domain.ReviewCase, error) {
	return getReviewCase(ctx, s.db, acceptanceID)
}

func getReviewCase(ctx context.Context, q querier, acceptanceID string) (domain.ReviewCase, error) {
	if !validID(acceptanceID) {
		return domain.ReviewCase{}, ports.ErrReviewRequestNotFound
	}
	c, err := scanReviewCase(q.QueryRowContext(ctx, reviewCaseQuery+` WHERE r.acceptance_id = $1`, acceptanceID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ReviewCase{}, ports.ErrReviewRequestNotFound
	}
	if err != nil {
		return domain.ReviewCase{}, fmt.Errorf("read the review request %s: %w", acceptanceID, err)
	}
	return c, nil
}

// RecordDecision is the atomic claim (EC-D02): in one transaction it moves the
// case from pending to decided and appends the decision. When the case is not
// pending (another advisor, maybe in another task, won) nothing is written and
// ports.ErrReviewAlreadyDecided is returned; an unknown case returns
// ports.ErrReviewRequestNotFound.
func (s *ReviewStore) RecordDecision(ctx context.Context, d domain.HumanDecision) (domain.ReviewCase, error) {
	if !validID(d.AcceptanceID) {
		return domain.ReviewCase{}, ports.ErrReviewRequestNotFound
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ReviewCase{}, fmt.Errorf("record the decision: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `UPDATE review_request SET status = 'decided'
		WHERE acceptance_id = $1 AND status = 'pending'`, d.AcceptanceID)
	if err != nil {
		return domain.ReviewCase{}, fmt.Errorf("claim the review request %s: %w", d.AcceptanceID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return domain.ReviewCase{}, fmt.Errorf("claim the review request %s: %w", d.AcceptanceID, err)
	}
	if n == 0 {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM review_request WHERE acceptance_id = $1)`,
			d.AcceptanceID).Scan(&exists); err != nil {
			return domain.ReviewCase{}, fmt.Errorf("read the review request %s: %w", d.AcceptanceID, err)
		}
		if !exists {
			return domain.ReviewCase{}, ports.ErrReviewRequestNotFound
		}
		return domain.ReviewCase{}, ports.ErrReviewAlreadyDecided
	}

	if err := insertDecision(ctx, tx, d); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return domain.ReviewCase{}, ports.ErrReviewAlreadyDecided
		}
		return domain.ReviewCase{}, fmt.Errorf("record the decision of %s: %w", d.AcceptanceID, err)
	}
	c, err := getReviewCase(ctx, tx, d.AcceptanceID)
	if err != nil {
		return domain.ReviewCase{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.ReviewCase{}, fmt.Errorf("record the decision of %s: %w", d.AcceptanceID, err)
	}
	return c, nil
}

func insertDecision(ctx context.Context, tx *sql.Tx, d domain.HumanDecision) error {
	var pct sql.NullFloat64
	if d.PremiumAdjustmentPct != nil {
		pct = sql.NullFloat64{Float64: *d.PremiumAdjustmentPct, Valid: true}
	}
	var offerID, currency sql.NullString
	var premium sql.NullInt64
	var expires sql.NullTime
	if o := d.AdjustedOffer; o != nil {
		offerID, currency = sql.NullString{String: o.ID, Valid: true}, sql.NullString{String: o.Currency, Valid: true}
		premium = sql.NullInt64{Int64: domain.ToMinorUnits(o.Premium), Valid: true}
		expires = sql.NullTime{Time: o.ExpiresAt.UTC(), Valid: true}
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO human_decision (decision_id, acceptance_id, type, decided_by, decided_by_email,
			decided_at, observations, premium_adjustment_pct, adjusted_offer_id, adjusted_premium_minor, adjusted_currency,
			adjusted_expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		d.ID, d.AcceptanceID, string(d.Type), d.Advisor.ID, nullString(d.Advisor.Email), d.DecidedAt.UTC(), d.Observations,
		pct, offerID, premium, currency, expires)
	return err
}

// DecisionForAdjustedOffer returns the decision that created the adjusted offer.
func (s *ReviewStore) DecisionForAdjustedOffer(ctx context.Context, offerID string) (domain.HumanDecision, bool, error) {
	if !validID(offerID) {
		return domain.HumanDecision{}, false, nil
	}
	var r decisionRow
	err := s.db.QueryRowContext(ctx, `SELECT `+humanDecisionColumns+` FROM human_decision d WHERE d.adjusted_offer_id = $1`,
		offerID).Scan(r.dest()...)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.HumanDecision{}, false, nil
	}
	if err != nil {
		return domain.HumanDecision{}, false, fmt.Errorf("read the decision of the adjusted offer %s: %w", offerID, err)
	}
	return *r.decision(), true, nil
}

// ListDueNotifications returns up to limit cases whose notice is pending and
// due at now (or with no due date), the earliest due first; limit <= 0 means
// no limit.
func (s *ReviewStore) ListDueNotifications(ctx context.Context, now time.Time, limit int) ([]domain.ReviewCase, error) {
	query := reviewCaseQuery + `
		WHERE r.notice_status = 'pending' AND (r.notice_next_attempt_at IS NULL OR r.notice_next_attempt_at <= $1)
		ORDER BY r.notice_next_attempt_at ASC NULLS FIRST, r.acceptance_id`
	args := []any{now.UTC()}
	if limit > 0 {
		query += ` LIMIT $2`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list the due review notices: %w", err)
	}
	return scanReviewCases(rows)
}

// UpdateNotification replaces the customer notice of the case. It returns
// ports.ErrReviewRequestNotFound when the acceptance has no request.
func (s *ReviewStore) UpdateNotification(ctx context.Context, acceptanceID string, n domain.ReviewNotification) error {
	if !validID(acceptanceID) {
		return ports.ErrReviewRequestNotFound
	}
	res, err := s.db.ExecContext(ctx, `UPDATE review_request SET notice_channel = $2, notice_status = $3, notice_attempts = $4,
			notice_next_attempt_at = $5, notice_sent_at = $6, notice_last_failure_at = $7, notice_last_failure = $8
		WHERE acceptance_id = $1`,
		acceptanceID, nullString(n.Channel), nullString(string(n.Status)), n.Attempts, nullTime(n.NextAttemptAt),
		nullTime(n.SentAt), nullTime(n.LastFailureAt), nullString(n.LastFailure))
	if err != nil {
		return fmt.Errorf("update the notice of %s: %w", acceptanceID, err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update the notice of %s: %w", acceptanceID, err)
	}
	if rows == 0 {
		return ports.ErrReviewRequestNotFound
	}
	return nil
}

func encodeApplicant(a domain.Applicant) (string, error) {
	raw, err := json.Marshal(applicantJSON{FullName: a.FullName, DocumentID: a.DocumentID, Email: a.Email})
	if err != nil {
		return "", fmt.Errorf("encode the applicant: %w", err)
	}
	return string(raw), nil
}

func decodeApplicant(raw []byte) (domain.Applicant, error) {
	var a applicantJSON
	if err := json.Unmarshal(raw, &a); err != nil {
		return domain.Applicant{}, err
	}
	return domain.Applicant{FullName: a.FullName, DocumentID: a.DocumentID, Email: a.Email}, nil
}

// encodeTerms keeps the coverage terms in the shape of offer.terms, or NULL
// when there are none (a demo offer).
func encodeTerms(t domain.CoverageTerms) (sql.NullString, error) {
	if t.InsuredAmount == 0 && t.TermMonths == 0 && len(t.Coverages) == 0 {
		return sql.NullString{}, nil
	}
	coverages := make([]coverageJSON, 0, len(t.Coverages))
	for _, c := range t.Coverages {
		coverages = append(coverages, coverageJSON{Code: c.Code, InsuredAmountMinor: domain.ToMinorUnits(c.InsuredAmount)})
	}
	raw, err := json.Marshal(termsJSON{InsuredAmountMinor: domain.ToMinorUnits(t.InsuredAmount), TermMonths: t.TermMonths, Coverages: coverages})
	if err != nil {
		return sql.NullString{}, fmt.Errorf("encode the coverage terms: %w", err)
	}
	return sql.NullString{String: string(raw), Valid: true}, nil
}

func decodeTerms(raw []byte) (domain.CoverageTerms, error) {
	if len(raw) == 0 {
		return domain.CoverageTerms{}, nil
	}
	var t termsJSON
	if err := json.Unmarshal(raw, &t); err != nil {
		return domain.CoverageTerms{}, err
	}
	out := domain.CoverageTerms{InsuredAmount: domain.FromMinorUnits(t.InsuredAmountMinor), TermMonths: t.TermMonths}
	for _, c := range t.Coverages {
		out.Coverages = append(out.Coverages, domain.Coverage{Code: c.Code, InsuredAmount: domain.FromMinorUnits(c.InsuredAmountMinor)})
	}
	return out, nil
}

// decisionRow holds the nullable columns of human_decision (also when they
// come from the LEFT JOIN of a pending case).
type decisionRow struct {
	id, acceptanceID, typ, decidedBy, email, observations, offerID, currency sql.NullString
	decidedAt, expires                                                       sql.NullTime
	pct                                                                      sql.NullFloat64
	premium                                                                  sql.NullInt64
}

func (r *decisionRow) dest() []any {
	return []any{&r.id, &r.acceptanceID, &r.typ, &r.decidedBy, &r.email, &r.decidedAt,
		&r.observations, &r.pct, &r.offerID, &r.premium, &r.currency, &r.expires}
}

// decision returns the decision of the row, or nil when there is none.
func (r *decisionRow) decision() *domain.HumanDecision {
	if !r.id.Valid {
		return nil
	}
	d := &domain.HumanDecision{
		ID: r.id.String, AcceptanceID: r.acceptanceID.String, Type: domain.DecisionType(r.typ.String),
		Advisor:   domain.Advisor{ID: r.decidedBy.String, Email: r.email.String},
		DecidedAt: r.decidedAt.Time.UTC(), Observations: r.observations.String,
	}
	if r.pct.Valid {
		p := r.pct.Float64
		d.PremiumAdjustmentPct = &p
	}
	if r.offerID.Valid {
		d.AdjustedOffer = &domain.AdjustedOffer{
			ID: r.offerID.String, Premium: domain.FromMinorUnits(r.premium.Int64), Currency: r.currency.String,
			ExpiresAt: r.expires.Time.UTC(),
		}
	}
	return d
}

func scanReviewCase(row scanner) (domain.ReviewCase, error) {
	var (
		c                                            domain.ReviewCase
		applicant, terms                             []byte
		premium                                      int64
		status                                       string
		mode, noticeChannel, noticeStatus, noticeErr sql.NullString
		expires, noticeNext, noticeSent, noticeFail  sql.NullTime
		noticeAttempts                               int
		d                                            decisionRow
	)
	req := &c.Request
	dest := []any{&req.AcceptanceID, &req.OfferID, &req.CustomerID, &applicant, &premium, &req.Currency, &req.Reason,
		&req.ReceivedAt, &status, &mode, &req.ProfileScore, &expires, &terms,
		&noticeChannel, &noticeStatus, &noticeAttempts, &noticeNext, &noticeSent, &noticeFail, &noticeErr}
	if err := row.Scan(append(dest, d.dest()...)...); err != nil {
		return domain.ReviewCase{}, err
	}
	var err error
	if req.Applicant, err = decodeApplicant(applicant); err != nil {
		return domain.ReviewCase{}, fmt.Errorf("decode the applicant of %s: %w", req.AcceptanceID, err)
	}
	if req.Terms, err = decodeTerms(terms); err != nil {
		return domain.ReviewCase{}, fmt.Errorf("decode the terms of %s: %w", req.AcceptanceID, err)
	}
	req.Premium = domain.FromMinorUnits(premium)
	req.ReceivedAt = req.ReceivedAt.UTC()
	req.OfferMode = domain.OfferMode(mode.String)
	req.OfferExpiresAt = timeOrZero(expires)
	c.Status = domain.ReviewStatus(status)
	c.Decision = d.decision()
	if noticeStatus.Valid {
		c.Notification = &domain.ReviewNotification{
			Channel: noticeChannel.String, Status: domain.NotificationStatus(noticeStatus.String), Attempts: noticeAttempts,
			NextAttemptAt: timeOrZero(noticeNext), SentAt: timeOrZero(noticeSent), LastFailureAt: timeOrZero(noticeFail),
			LastFailure: noticeErr.String,
		}
	}
	return c, nil
}

func scanReviewCases(rows *sql.Rows) ([]domain.ReviewCase, error) {
	defer func() { _ = rows.Close() }()
	out := []domain.ReviewCase{}
	for rows.Next() {
		c, err := scanReviewCase(rows)
		if err != nil {
			return nil, fmt.Errorf("read a review request: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
