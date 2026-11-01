package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/ports"
)

// Store implements ports.NotificationStore and ports.DeliveryStore over the
// notifications database (HU-M3.1-01).
type Store struct {
	db *sql.DB
}

// NewStore builds the store.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

var (
	_ ports.NotificationStore = (*Store)(nil)
	_ ports.DeliveryStore     = (*Store)(nil)
)

// payoutJSON is the payload of a parametric payout notification.
type payoutJSON struct {
	PayoutID       string      `json:"payout_id"`
	ClaimID        string      `json:"claim_id"`
	PolicyID       string      `json:"policy_id"`
	AmountMinor    int64       `json:"amount_minor"`
	Currency       string      `json:"currency"`
	Trigger        triggerJSON `json:"trigger"`
	Brand          string      `json:"payment_brand"`
	Last4          string      `json:"payment_last4"`
	IdempotencyKey string      `json:"idempotency_key"`
	PaidAt         time.Time   `json:"paid_at"`
}

type triggerJSON struct {
	Parameter      string    `json:"parameter"`
	EventReference string    `json:"event_reference"`
	Threshold      float64   `json:"threshold"`
	Observed       float64   `json:"observed"`
	Unit           string    `json:"unit"`
	ObservedAt     time.Time `json:"observed_at"`
}

func encodePayout(p *domain.ParametricPayout) ([]byte, error) {
	if p == nil {
		return []byte(`{}`), nil
	}
	return json.Marshal(payoutJSON{
		PayoutID: p.PayoutID, ClaimID: p.ClaimID, PolicyID: p.PolicyID, AmountMinor: p.AmountMinor, Currency: p.Currency,
		Trigger: triggerJSON{Parameter: p.Trigger.Parameter, EventReference: p.Trigger.EventReference,
			Threshold: p.Trigger.Threshold, Observed: p.Trigger.Observed, Unit: p.Trigger.Unit, ObservedAt: p.Trigger.ObservedAt},
		Brand: p.Method.Brand, Last4: p.Method.Last4, IdempotencyKey: p.IdempotencyKey, PaidAt: p.PaidAt,
	})
}

func decodePayout(kind, subject string, raw []byte) (*domain.ParametricPayout, error) {
	if kind != domain.KindParametricPayout {
		return nil, nil
	}
	var j payoutJSON
	if err := json.Unmarshal(raw, &j); err != nil {
		return nil, fmt.Errorf("decode the payout: %w", err)
	}
	t := j.Trigger
	return &domain.ParametricPayout{
		PayoutID: j.PayoutID, ClaimID: j.ClaimID, PolicyID: j.PolicyID, Subject: subject, AmountMinor: j.AmountMinor,
		Currency: j.Currency, Trigger: domain.Trigger{Parameter: t.Parameter, EventReference: t.EventReference,
			Threshold: t.Threshold, Observed: t.Observed, Unit: t.Unit, ObservedAt: t.ObservedAt.UTC()},
		Method: domain.PaymentMethod{Brand: j.Brand, Last4: j.Last4}, IdempotencyKey: j.IdempotencyKey, PaidAt: j.PaidAt.UTC(),
	}, nil
}

// RecordPayout stores the notification and one delivery per reachable device
// of its subject in one transaction. A notification that already exists
// changes nothing.
func (s *Store) RecordPayout(ctx context.Context, n domain.Notification, now time.Time) (bool, []string, error) {
	payload, err := encodePayout(n.Payout)
	if err != nil {
		return false, nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, nil, fmt.Errorf("record the notification: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `INSERT INTO notification (id, subject, kind, occurred_at, created_at, payload)
		VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT (id) DO NOTHING`,
		n.ID, n.Subject, n.Kind, n.OccurredAt.UTC(), n.CreatedAt.UTC(), payload)
	if err != nil {
		return false, nil, fmt.Errorf("record the notification: %w", err)
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		ids, err := deliveryIDs(ctx, tx, n.ID)
		return false, ids, err
	}

	devices, err := reachableDevices(ctx, tx, n.Subject)
	if err != nil {
		return false, nil, err
	}
	ids := make([]string, 0, len(devices))
	for _, d := range devices {
		del := domain.NewDelivery(n, d, now)
		if _, err := tx.ExecContext(ctx, `INSERT INTO push_delivery (id, notification_id, installation_id, token, locale,
				idempotency_key, status, window_ends_at, next_attempt_at, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $10)`,
			del.ID, n.ID, d.InstallationID, d.Token, d.Locale, del.Key, string(del.Status), del.WindowEndsAt,
			del.NextAttemptAt, now.UTC()); err != nil {
			return false, nil, fmt.Errorf("record the delivery to %s: %w", d.InstallationID, err)
		}
		ids = append(ids, del.ID)
	}
	if err := tx.Commit(); err != nil {
		return false, nil, fmt.Errorf("record the notification: %w", err)
	}
	return true, ids, nil
}

func deliveryIDs(ctx context.Context, tx *sql.Tx, notificationID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM push_delivery WHERE notification_id = $1 ORDER BY id`, notificationID)
	if err != nil {
		return nil, fmt.Errorf("list the deliveries: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func reachableDevices(ctx context.Context, tx *sql.Tx, subject string) ([]domain.Device, error) {
	rows, err := tx.QueryContext(ctx, `SELECT installation_id, token, locale FROM device
		WHERE subject = $1 AND push_enabled AND token <> '' ORDER BY installation_id`, subject)
	if err != nil {
		return nil, fmt.Errorf("list the devices: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []domain.Device
	for rows.Next() {
		d := domain.Device{Subject: subject, PushEnabled: true}
		if err := rows.Scan(&d.InstallationID, &d.Token, &d.Locale); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

const notificationColumns = `id, subject, kind, occurred_at, created_at, read_at, payload`

type scanner interface{ Scan(dest ...any) error }

func scanNotification(r scanner) (domain.Notification, error) {
	var n domain.Notification
	var readAt sql.NullTime
	var payload []byte
	if err := r.Scan(&n.ID, &n.Subject, &n.Kind, &n.OccurredAt, &n.CreatedAt, &readAt, &payload); err != nil {
		return domain.Notification{}, err
	}
	n.OccurredAt, n.CreatedAt = n.OccurredAt.UTC(), n.CreatedAt.UTC()
	if readAt.Valid {
		t := readAt.Time.UTC()
		n.ReadAt = &t
	}
	p, err := decodePayout(n.Kind, n.Subject, payload)
	if err != nil {
		return domain.Notification{}, err
	}
	n.Payout = p
	return n, nil
}

// List returns the notifications of the subject, the newest first.
func (s *Store) List(ctx context.Context, subject string, limit int) ([]domain.Notification, int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+notificationColumns+` FROM notification
		WHERE subject = $1 ORDER BY occurred_at DESC, id DESC LIMIT $2`, subject, limit)
	if err != nil {
		return nil, 0, fmt.Errorf("list the notifications: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []domain.Notification{}
	for rows.Next() {
		n, err := scanNotification(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var unread int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM notification WHERE subject = $1 AND read_at IS NULL`,
		subject).Scan(&unread); err != nil {
		return nil, 0, fmt.Errorf("count the unread notifications: %w", err)
	}
	return out, unread, nil
}

// Get returns one notification of the subject.
func (s *Store) Get(ctx context.Context, subject, id string) (domain.Notification, error) {
	n, err := scanNotification(s.db.QueryRowContext(ctx, `SELECT `+notificationColumns+` FROM notification
		WHERE id = $1 AND subject = $2`, id, subject))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Notification{}, ports.ErrNotificationNotFound
	}
	return n, err
}

// MarkRead marks one notification of the subject read, keeping the first time.
func (s *Store) MarkRead(ctx context.Context, subject, id string, now time.Time) error {
	res, err := s.db.ExecContext(ctx, `UPDATE notification SET read_at = COALESCE(read_at, $3) WHERE id = $1 AND subject = $2`,
		id, subject, now.UTC())
	if err != nil {
		return fmt.Errorf("mark the notification read: %w", err)
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return ports.ErrNotificationNotFound
	}
	return nil
}

// MarkAllRead marks every unread notification of the subject read.
func (s *Store) MarkAllRead(ctx context.Context, subject string, now time.Time) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE notification SET read_at = $2 WHERE subject = $1 AND read_at IS NULL`,
		subject, now.UTC()); err != nil {
		return fmt.Errorf("mark the notifications read: %w", err)
	}
	return nil
}

// SaveDevice creates or replaces the device.
func (s *Store) SaveDevice(ctx context.Context, d domain.Device) error {
	if _, err := s.db.ExecContext(ctx, `INSERT INTO device (installation_id, subject, platform, token, push_enabled, locale, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (installation_id) DO UPDATE SET subject = EXCLUDED.subject, platform = EXCLUDED.platform,
			token = EXCLUDED.token, push_enabled = EXCLUDED.push_enabled, locale = EXCLUDED.locale,
			updated_at = EXCLUDED.updated_at`,
		d.InstallationID, d.Subject, d.Platform, d.Token, d.PushEnabled, d.Locale, d.UpdatedAt.UTC()); err != nil {
		return fmt.Errorf("save the device: %w", err)
	}
	return nil
}

// --- deliveries (pkg/idempotency) ---

const deliveryColumns = `d.id, d.notification_id, d.installation_id, d.token, d.locale, d.idempotency_key, d.status,
	d.failed_attempts, d.window_ends_at, d.next_attempt_at, d.claimed_until, d.reference, d.sent_at, d.last_error,
	n.id, n.subject, n.kind, n.occurred_at, n.created_at, n.read_at, n.payload`

func scanDelivery(r scanner) (domain.Delivery, error) {
	var d domain.Delivery
	var status string
	var claimed, sent, nRead sql.NullTime
	var reference, lastErr sql.NullString
	var payload []byte
	n := &d.Notification
	if err := r.Scan(&d.ID, &d.NotificationID, &d.InstallationID, &d.Token, &d.Locale, &d.Key, &status,
		&d.FailedAttempts, &d.WindowEndsAt, &d.NextAttemptAt, &claimed, &reference, &sent, &lastErr,
		&n.ID, &n.Subject, &n.Kind, &n.OccurredAt, &n.CreatedAt, &nRead, &payload); err != nil {
		return domain.Delivery{}, err
	}
	d.Status = idempotency.Status(status)
	d.WindowEndsAt, d.NextAttemptAt = d.WindowEndsAt.UTC(), d.NextAttemptAt.UTC()
	if claimed.Valid {
		d.ClaimedUntil = claimed.Time.UTC()
	}
	if sent.Valid {
		t := sent.Time.UTC()
		d.SentAt = &t
	}
	d.Reference, d.LastError = reference.String, lastErr.String
	p, err := decodePayout(n.Kind, n.Subject, payload)
	if err != nil {
		return domain.Delivery{}, err
	}
	n.Payout = p
	return d, nil
}

// returning re-reads the delivery after a change, with its notification.
func (s *Store) returning(ctx context.Context, q execer, id string) (domain.Delivery, error) {
	return scanDelivery(q.QueryRowContext(ctx, `SELECT `+deliveryColumns+` FROM push_delivery d
		JOIN notification n ON n.id = d.notification_id WHERE d.id = $1`, id))
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Claim takes a due delivery with a lease, in one conditional statement.
func (s *Store) Claim(ctx context.Context, id string, now time.Time, lease time.Duration) (domain.Delivery, bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE push_delivery SET status = 'in_progress', claimed_until = $3, updated_at = $2
		WHERE id = $1 AND ((status = 'pending' AND next_attempt_at <= $2) OR (status = 'in_progress' AND claimed_until < $2))`,
		id, now.UTC(), now.UTC().Add(lease))
	if err != nil {
		return domain.Delivery{}, false, fmt.Errorf("claim the delivery: %w", err)
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return domain.Delivery{}, false, nil
	}
	d, err := s.returning(ctx, s.db, id)
	if err != nil {
		return domain.Delivery{}, false, err
	}
	return d, true, nil
}

// Each result is recorded only while rec's claim holds: the same status and
// the same lease it was claimed with. Otherwise nothing changes and the
// caller gets idempotency.ErrClaimLost.
const (
	confirmDelivery = `UPDATE push_delivery SET status = 'confirmed', claimed_until = NULL, reference = $3, sent_at = $4,
		updated_at = $4 WHERE id = $1 AND status = 'in_progress' AND claimed_until = $2`
	rescheduleDelivery = `UPDATE push_delivery SET status = 'pending', claimed_until = NULL,
		failed_attempts = failed_attempts + 1, next_attempt_at = $3, last_error = $4, updated_at = $5
		WHERE id = $1 AND status = 'in_progress' AND claimed_until = $2`
	failDelivery = `UPDATE push_delivery SET status = 'failed', claimed_until = NULL, failed_attempts = failed_attempts + 1,
		failure_reason = $3, last_error = $4, updated_at = $5
		WHERE id = $1 AND status = 'in_progress' AND claimed_until = $2`
)

// finish runs one of the result statements above for rec.
func (s *Store) finish(ctx context.Context, rec domain.Delivery, statement string, args ...any) (domain.Delivery, error) {
	all := append([]any{rec.ID, rec.ClaimedUntil}, args...)
	res, err := s.db.ExecContext(ctx, statement, all...)
	if err != nil {
		return domain.Delivery{}, fmt.Errorf("record the delivery result: %w", err)
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return domain.Delivery{}, idempotency.ErrClaimLost
	}
	return s.returning(ctx, s.db, rec.ID)
}

// Confirm records that the provider accepted the push.
func (s *Store) Confirm(ctx context.Context, rec domain.Delivery, now time.Time, out idempotency.Outcome) (domain.Delivery, error) {
	return s.finish(ctx, rec, confirmDelivery, out.Reference, now.UTC())
}

// Reschedule counts a failed attempt and makes the delivery due again at next.
func (s *Store) Reschedule(ctx context.Context, rec domain.Delivery, now, next time.Time, cause string) (domain.Delivery, error) {
	return s.finish(ctx, rec, rescheduleDelivery, next.UTC(), cause, now.UTC())
}

// Fail counts a failed attempt and closes the delivery for the reason.
func (s *Store) Fail(ctx context.Context, rec domain.Delivery, now time.Time, reason idempotency.FailureReason,
	cause string) (domain.Delivery, error) {
	return s.finish(ctx, rec, failDelivery, string(reason), cause, now.UTC())
}

// Due returns the deliveries due at now, the oldest first.
func (s *Store) Due(ctx context.Context, now time.Time, limit int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM push_delivery
		WHERE (status = 'pending' AND next_attempt_at <= $1) OR (status = 'in_progress' AND claimed_until < $1)
		ORDER BY next_attempt_at, id LIMIT $2`, now.UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("list the due deliveries: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
