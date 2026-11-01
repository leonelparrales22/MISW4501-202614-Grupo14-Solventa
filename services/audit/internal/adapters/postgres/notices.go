package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/ports"
)

// NoticeStore implements ports.NoticeStore over revocation_notice and
// revocation_notice_step (HU-M1.3-02, decision D-3 a) with inserts only: every
// change of state of a notice is a new step with the next seq, and the
// primary key (consent_id, entity_code, seq) lets one statement win when
// several workers race for the same step. pkg/idempotency asks that a claim
// be one atomic conditional statement (doc.go): here it is one INSERT ...
// SELECT over the last step with ON CONFLICT DO NOTHING.
type NoticeStore struct {
	db *sql.DB
}

// NewNoticeStore builds the store over an opened and migrated database.
func NewNoticeStore(db *sql.DB) *NoticeStore { return &NoticeStore{db: db} }

var _ ports.NoticeStore = (*NoticeStore)(nil)

// stepColumns are the columns of a step returned by the statements below,
// followed by the data of its notice and revocation.
const stepColumns = `s.consent_id, s.entity_code, s.seq, s.status, s.failed_attempts, s.window_ends_at, s.next_attempt_at,
	s.claimed_until, s.reference, s.cause`

// withNotice joins a set of steps "s" with the key of their notice, the
// effective time of the revocation and the customer of the consent.
const withNotice = ` JOIN revocation_notice n ON n.consent_id = s.consent_id AND n.entity_code = s.entity_code
	JOIN consent_revocation r ON r.consent_id = s.consent_id
	JOIN consent_record c ON c.consent_id = s.consent_id`

const noticeExtra = `, n.idempotency_key, r.effective_at, c.customer_id`

// Claim implements idempotency.Store: it inserts the in-progress step seq+1
// when the last step is pending and due, or in progress with an expired
// lease. A concurrent claim of the same step loses on the primary key and
// gets claimed=false.
func (s *NoticeStore) Claim(ctx context.Context, id string, now time.Time, lease time.Duration) (ports.NoticeRecord, bool, error) {
	consentID, entity, ok := ports.ParseNoticeID(id)
	if !ok {
		return ports.NoticeRecord{}, false, nil
	}
	now = now.UTC()
	rec, err := scanNotice(s.db.QueryRowContext(ctx, `WITH last AS (
			SELECT * FROM revocation_notice_step
			WHERE consent_id = $1 AND entity_code = $2
			ORDER BY seq DESC LIMIT 1
		), s AS (
			INSERT INTO revocation_notice_step (consent_id, entity_code, seq, status, failed_attempts, window_ends_at,
				next_attempt_at, claimed_until, cause)
			SELECT consent_id, entity_code, seq + 1, 'in_progress', failed_attempts, window_ends_at,
				next_attempt_at, $4, cause
			FROM last
			WHERE (status = 'pending' AND next_attempt_at <= $3) OR (status = 'in_progress' AND claimed_until < $3)
			ON CONFLICT DO NOTHING
			RETURNING *
		)
		SELECT `+stepColumns+noticeExtra+` FROM s`+withNotice,
		consentID, entity, now, now.Add(lease)))
	if errors.Is(err, sql.ErrNoRows) {
		return ports.NoticeRecord{}, false, nil
	}
	if err != nil {
		return ports.NoticeRecord{}, false, fmt.Errorf("claim the notice: %w", err)
	}
	return rec, true, nil
}

// next inserts the step after the claimed one, inside tx. It returns
// idempotency.ErrClaimLost when that step already exists: another worker
// claimed the notice again after the lease of rec expired.
func next(ctx context.Context, tx *sql.Tx, rec ports.NoticeRecord, status idempotency.Status, failedAttempts int,
	nextAttempt time.Time, reference, cause, reason string) (ports.NoticeRecord, error) {
	var nextAt sql.NullTime
	if !nextAttempt.IsZero() {
		nextAt = sql.NullTime{Time: nextAttempt.UTC(), Valid: true}
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO revocation_notice_step (consent_id, entity_code, seq, status, failed_attempts,
			window_ends_at, next_attempt_at, reference, cause, reason)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT DO NOTHING`,
		rec.ConsentID, rec.EntityCode, rec.Seq+1, string(status), failedAttempts, rec.WindowEndsAt.UTC(), nextAt,
		nullString(reference), nullString(cause), nullString(reason))
	if err != nil {
		return ports.NoticeRecord{}, fmt.Errorf("record the step of the notice: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return ports.NoticeRecord{}, fmt.Errorf("record the step of the notice: %w", err)
	} else if n != 1 {
		return ports.NoticeRecord{}, idempotency.ErrClaimLost
	}
	out := rec
	out.Seq, out.Status, out.FailedAttempts = rec.Seq+1, status, failedAttempts
	out.NextAttemptAt, out.ClaimedUntil, out.Reference, out.Cause = nextAt.Time, time.Time{}, reference, cause
	return out, nil
}

// inTx runs fn in a transaction and commits it when fn succeeds.
func (s *NoticeStore) inTx(ctx context.Context, fn func(*sql.Tx) (ports.NoticeRecord, error)) (ports.NoticeRecord, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ports.NoticeRecord{}, fmt.Errorf("begin the step of the notice: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	rec, err := fn(tx)
	if err != nil {
		return ports.NoticeRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return ports.NoticeRecord{}, fmt.Errorf("commit the step of the notice: %w", err)
	}
	return rec, nil
}

// Confirm implements idempotency.Store: the confirmed step and the
// revocation_confirmed event of the entity, in one transaction.
func (s *NoticeStore) Confirm(ctx context.Context, rec ports.NoticeRecord, now time.Time, out idempotency.Outcome) (ports.NoticeRecord, error) {
	return s.inTx(ctx, func(tx *sql.Tx) (ports.NoticeRecord, error) {
		stored, err := next(ctx, tx, rec, idempotency.StatusConfirmed, rec.FailedAttempts, time.Time{}, out.Reference, "", "")
		if err != nil {
			return ports.NoticeRecord{}, err
		}
		return stored, insertEntityEvent(ctx, tx, stored, domain.HistoryRevocationConfirmed, now, out.Reference, "", nil,
			domain.Actor{Type: domain.ActorSystem, ID: "notice-worker"})
	})
}

// Reschedule implements idempotency.Store: a pending step due at nextAt.
func (s *NoticeStore) Reschedule(ctx context.Context, rec ports.NoticeRecord, _, nextAt time.Time, cause string) (ports.NoticeRecord, error) {
	return s.inTx(ctx, func(tx *sql.Tx) (ports.NoticeRecord, error) {
		return next(ctx, tx, rec, idempotency.StatusPending, rec.FailedAttempts+1, nextAt, "", cause, "")
	})
}

// Fail implements idempotency.Store: the failed step and the
// revocation_not_confirmed event of the entity, in one transaction.
func (s *NoticeStore) Fail(ctx context.Context, rec ports.NoticeRecord, now time.Time, reason idempotency.FailureReason, cause string) (ports.NoticeRecord, error) {
	return s.inTx(ctx, func(tx *sql.Tx) (ports.NoticeRecord, error) {
		stored, err := next(ctx, tx, rec, idempotency.StatusFailed, rec.FailedAttempts+1, time.Time{}, "", cause, string(reason))
		if err != nil {
			return ports.NoticeRecord{}, err
		}
		return stored, insertEntityEvent(ctx, tx, stored, domain.HistoryRevocationNotConfirmed, now, "", string(reason), nil,
			domain.Actor{Type: domain.ActorSystem, ID: "notice-worker"})
	})
}

// insertEntityEvent writes an event of one entity of the consent. The id is
// derived from the step, so a step never has two events.
func insertEntityEvent(ctx context.Context, tx *sql.Tx, rec ports.NoticeRecord, status string, at time.Time,
	reference, reason string, details []byte, actor domain.Actor) error {
	id := fmt.Sprintf("rne-%s-%s-%d", rec.ConsentID, rec.EntityCode, rec.Seq)
	var raw any
	if details != nil {
		raw = details
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO consent_event (event_id, consent_id, entity_code, status, occurred_at,
			actor_type, actor_id, reason, reference, details)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		id, rec.ConsentID, rec.EntityCode, status, at.UTC(), string(actor.Type), actor.ID,
		nullString(reason), nullString(reference), raw); err != nil {
		return fmt.Errorf("insert the %s event of %s: %w", status, rec.EntityCode, err)
	}
	return nil
}

// Due implements ports.NoticeStore.
func (s *NoticeStore) Due(ctx context.Context, now time.Time, limit int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT consent_id, entity_code FROM (
			SELECT DISTINCT ON (consent_id, entity_code) consent_id, entity_code, status, next_attempt_at, claimed_until
			FROM revocation_notice_step
			ORDER BY consent_id, entity_code, seq DESC
		) last
		WHERE (status = 'pending' AND next_attempt_at <= $1) OR (status = 'in_progress' AND claimed_until < $1)
		ORDER BY next_attempt_at NULLS LAST, consent_id, entity_code
		LIMIT $2`, now.UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("list the due notices: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var consentID, entity string
		if err := rows.Scan(&consentID, &entity); err != nil {
			return nil, fmt.Errorf("read a due notice: %w", err)
		}
		ids = append(ids, ports.NoticeID(consentID, entity))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list the due notices: %w", err)
	}
	return ids, nil
}

// Resume implements ports.NoticeStore.
func (s *NoticeStore) Resume(ctx context.Context, consentID string, entities []string, now time.Time, window time.Duration,
	actor domain.Actor) ([]string, error) {
	now = now.UTC()
	deadline := now.Add(window)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin the retry: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT `+stepColumns+noticeExtra+` FROM (
			SELECT DISTINCT ON (consent_id, entity_code) *
			FROM revocation_notice_step
			WHERE consent_id = $1
			ORDER BY consent_id, entity_code, seq DESC
		) s`+withNotice+`
		WHERE (s.status = 'failed' OR (s.status = 'pending' AND s.window_ends_at <= $2))
		  AND (COALESCE(cardinality($3::text[]), 0) = 0 OR s.entity_code = ANY($3::text[]))
		ORDER BY s.entity_code`, consentID, now, entities)
	if err != nil {
		return nil, fmt.Errorf("find the notices to retry: %w", err)
	}
	var due []ports.NoticeRecord
	for rows.Next() {
		rec, err := scanNotice(rows)
		if err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("read a notice to retry: %w", err)
		}
		due = append(due, rec)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("find the notices to retry: %w", err)
	}
	details, err := json.Marshal(domain.RetryDetails{DeadlineAt: deadline})
	if err != nil {
		return nil, fmt.Errorf("encode the retry: %w", err)
	}
	resumed := make([]string, 0, len(due))
	for _, rec := range due {
		res, err := tx.ExecContext(ctx, `INSERT INTO revocation_notice_step (consent_id, entity_code, seq, status,
				failed_attempts, window_ends_at, next_attempt_at)
			VALUES ($1, $2, $3, 'pending', 0, $4, $5)
			ON CONFLICT DO NOTHING`, rec.ConsentID, rec.EntityCode, rec.Seq+1, deadline, now)
		if err != nil {
			return nil, fmt.Errorf("resume the notice to %s: %w", rec.EntityCode, err)
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			// A worker moved the notice meanwhile: it is no longer stuck.
			continue
		}
		step := rec
		step.Seq++
		if err := insertEntityEvent(ctx, tx, step, domain.HistoryRevocationRetryRequested, now, "", "", details, actor); err != nil {
			return nil, err
		}
		resumed = append(resumed, rec.EntityCode)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit the retry: %w", err)
	}
	return resumed, nil
}

func scanNotice(row scanner) (ports.NoticeRecord, error) {
	var (
		rec                     ports.NoticeRecord
		status                  string
		nextAt, claimed         sql.NullTime
		reference, cause        sql.NullString
		key, customer           string
		windowEnds, effectiveAt time.Time
	)
	if err := row.Scan(&rec.ConsentID, &rec.EntityCode, &rec.Seq, &status, &rec.FailedAttempts, &windowEnds, &nextAt,
		&claimed, &reference, &cause, &key, &effectiveAt, &customer); err != nil {
		return ports.NoticeRecord{}, err
	}
	rec.Status, rec.Key, rec.CustomerID = idempotency.Status(status), key, customer
	rec.WindowEndsAt, rec.EffectiveAt = windowEnds.UTC(), effectiveAt.UTC()
	rec.NextAttemptAt, rec.ClaimedUntil = nextAt.Time.UTC(), claimed.Time.UTC()
	if !nextAt.Valid {
		rec.NextAttemptAt = time.Time{}
	}
	if !claimed.Valid {
		rec.ClaimedUntil = time.Time{}
	}
	rec.Reference, rec.Cause = reference.String, cause.String
	return rec, nil
}
