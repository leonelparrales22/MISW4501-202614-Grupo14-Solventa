package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/partners/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/partners/internal/ports"
)

// uniqueViolation is the SQLSTATE of a duplicate key.
const uniqueViolation = "23505"

// execer is what both *sql.DB and *sql.Tx offer to run a statement.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// queryer is what both *sql.DB and *sql.Tx offer to read.
type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// PartnerStore is ports.PartnerStore over the partner, partner_credential,
// partner_history and outbox_event tables (HU-W3.1-01).
type PartnerStore struct {
	db *sql.DB
}

var _ ports.PartnerStore = (*PartnerStore)(nil)

// NewPartnerStore builds the store over an open, migrated database.
func NewPartnerStore(db *sql.DB) *PartnerStore { return &PartnerStore{db: db} }

const partnerColumns = `partner_id, code, name, type, products, technical_contact_email, status, daily_limit, rate_limit,
	burst_limit, COALESCE(usage_plan_id, ''), registered_at, registered_by_type, registered_by_id, registered_by_label, approved_at`

// issuedColumns lists the columns of a partner_credential row; no secret is stored.
const issuedColumns = `credential_id, partner_id, client_id, gateway_key_id, secret_hint, status, reason, issued_at,
	issued_by_type, issued_by_id, issued_by_label, expires_at, revoked_at`

// Create implements ports.PartnerStore.
func (s *PartnerStore) Create(ctx context.Context, p domain.Partner, entry domain.HistoryEntry) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin the registration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	products, err := json.Marshal(p.Products)
	if err != nil {
		return fmt.Errorf("encode the products: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO partner (partner_id, code, name, type, products, technical_contact_email,
		status, daily_limit, rate_limit, burst_limit, registered_at, registered_by_type, registered_by_id, registered_by_label)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
		p.ID, p.Code, p.Name, string(p.Type), products, p.TechnicalContactEmail, string(p.Status), p.Quota.DailyLimit,
		p.Quota.RateLimit, p.Quota.BurstLimit, p.RegisteredAt.UTC(), p.RegisteredBy.Type, p.RegisteredBy.ID,
		p.RegisteredBy.Label); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && pgErr.ConstraintName == "partner_code_key" {
			return domain.ErrCodeTaken
		}
		return fmt.Errorf("insert the partner: %w", err)
	}
	if err := insertHistory(ctx, tx, entry); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit the registration: %w", err)
	}
	return nil
}

// List implements ports.PartnerStore.
func (s *PartnerStore) List(ctx context.Context) ([]ports.PartnerRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+partnerColumns+` FROM partner ORDER BY name, partner_id`)
	if err != nil {
		return nil, fmt.Errorf("list the partners: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []ports.PartnerRecord
	index := map[string]int{}
	for rows.Next() {
		p, err := scanPartner(rows)
		if err != nil {
			return nil, err
		}
		index[p.ID] = len(out)
		out = append(out, ports.PartnerRecord{Partner: p})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the partners: %w", err)
	}
	creds, err := queryCredentials(ctx, s.db, `SELECT `+issuedColumns+` FROM partner_credential
		WHERE status <> 'revoked' ORDER BY issued_at DESC, credential_id`)
	if err != nil {
		return nil, err
	}
	for _, c := range creds {
		if i, ok := index[c.PartnerID]; ok {
			out[i].Credentials = append(out[i].Credentials, c)
		}
	}
	return out, nil
}

// Get implements ports.PartnerStore.
func (s *PartnerStore) Get(ctx context.Context, id string) (ports.PartnerRecord, error) {
	return readRecord(ctx, s.db, id, false)
}

// Mutate implements ports.PartnerStore.
func (s *PartnerStore) Mutate(ctx context.Context, id string, fn func(ports.PartnerRecord) (ports.Change, error)) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin the change: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	rec, err := readRecord(ctx, tx, id, true)
	if err != nil {
		return err
	}
	change, err := fn(rec)
	if err != nil {
		return err
	}
	if p := change.Partner; p != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE partner SET status = $2, usage_plan_id = NULLIF($3, ''), approved_at = $4
			WHERE partner_id = $1`, p.ID, string(p.Status), p.UsagePlanID, utcPtr(p.ApprovedAt)); err != nil {
			return fmt.Errorf("update the partner: %w", err)
		}
	}
	// Status changes first, so the one-active and one-retiring indexes hold
	// when the new credential is inserted.
	for _, c := range change.UpdatedCredentials {
		if _, err := tx.ExecContext(ctx, `UPDATE partner_credential SET status = $2, expires_at = $3, revoked_at = $4
			WHERE credential_id = $1`, c.ID, string(c.Status), utcPtr(c.ExpiresAt), utcPtr(c.RevokedAt)); err != nil {
			return fmt.Errorf("update credential %s: %w", c.ID, err)
		}
	}
	for _, c := range change.NewCredentials {
		if _, err := tx.ExecContext(ctx, `INSERT INTO partner_credential (credential_id, partner_id, client_id, gateway_key_id,
			secret_hint, status, reason, issued_at, issued_by_type, issued_by_id, issued_by_label, expires_at, revoked_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
			c.ID, c.PartnerID, c.ClientID, c.GatewayKeyID, c.SecretHint, string(c.Status), c.Reason, c.IssuedAt.UTC(),
			c.IssuedBy.Type, c.IssuedBy.ID, c.IssuedBy.Label, utcPtr(c.ExpiresAt), utcPtr(c.RevokedAt)); err != nil {
			return fmt.Errorf("insert credential %s: %w", c.ID, err)
		}
	}
	for _, h := range change.History {
		if err := insertHistory(ctx, tx, h); err != nil {
			return err
		}
	}
	for _, e := range change.Events {
		body, err := e.Encode()
		if err != nil {
			return err
		}
		if err := insertOutboxEvent(ctx, tx, e.EventID, e.EventType, body, e.OccurredAt); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit the change: %w", err)
	}
	return nil
}

// DueRetirements implements ports.PartnerStore.
func (s *PartnerStore) DueRetirements(ctx context.Context, now time.Time, limit int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT partner_id FROM partner_credential
		WHERE status = 'retiring' AND expires_at <= $1 ORDER BY partner_id LIMIT $2`, now.UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("find the due credentials: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("read a due partner: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func readRecord(ctx context.Context, q queryer, id string, lock bool) (ports.PartnerRecord, error) {
	query := `SELECT ` + partnerColumns + ` FROM partner WHERE partner_id = $1`
	if lock {
		query += ` FOR UPDATE`
	}
	p, err := scanPartner(q.QueryRowContext(ctx, query, id))
	if errors.Is(err, sql.ErrNoRows) {
		return ports.PartnerRecord{}, domain.ErrPartnerNotFound
	}
	if err != nil {
		return ports.PartnerRecord{}, err
	}
	creds, err := queryCredentials(ctx, q, `SELECT `+issuedColumns+` FROM partner_credential
		WHERE partner_id = $1 ORDER BY issued_at DESC, credential_id`, id)
	if err != nil {
		return ports.PartnerRecord{}, err
	}
	history, err := queryHistory(ctx, q, id)
	if err != nil {
		return ports.PartnerRecord{}, err
	}
	return ports.PartnerRecord{Partner: p, Credentials: creds, History: history}, nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanPartner(row scanner) (domain.Partner, error) {
	var (
		p        domain.Partner
		typ      string
		status   string
		products []byte
		approved sql.NullTime
	)
	err := row.Scan(&p.ID, &p.Code, &p.Name, &typ, &products, &p.TechnicalContactEmail, &status, &p.Quota.DailyLimit,
		&p.Quota.RateLimit, &p.Quota.BurstLimit, &p.UsagePlanID, &p.RegisteredAt, &p.RegisteredBy.Type, &p.RegisteredBy.ID,
		&p.RegisteredBy.Label, &approved)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Partner{}, err
		}
		return domain.Partner{}, fmt.Errorf("read a partner: %w", err)
	}
	p.Type, p.Status = domain.Type(typ), domain.Status(status)
	p.RegisteredAt = p.RegisteredAt.UTC()
	if err := json.Unmarshal(products, &p.Products); err != nil {
		return domain.Partner{}, fmt.Errorf("decode the products of %s: %w", p.ID, err)
	}
	if approved.Valid {
		t := approved.Time.UTC()
		p.ApprovedAt = &t
	}
	return p, nil
}

func queryCredentials(ctx context.Context, q queryer, query string, args ...any) ([]domain.Credential, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("read the credentials: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []domain.Credential
	for rows.Next() {
		var (
			c                domain.Credential
			status           string
			expires, revoked sql.NullTime
		)
		if err := rows.Scan(&c.ID, &c.PartnerID, &c.ClientID, &c.GatewayKeyID, &c.SecretHint, &status, &c.Reason,
			&c.IssuedAt, &c.IssuedBy.Type, &c.IssuedBy.ID, &c.IssuedBy.Label, &expires, &revoked); err != nil {
			return nil, fmt.Errorf("read a credential: %w", err)
		}
		c.Status, c.IssuedAt = domain.CredentialStatus(status), c.IssuedAt.UTC()
		c.ExpiresAt, c.RevokedAt = timePtr(expires), timePtr(revoked)
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the credentials: %w", err)
	}
	return out, nil
}

func queryHistory(ctx context.Context, q queryer, partnerID string) ([]domain.HistoryEntry, error) {
	rows, err := q.QueryContext(ctx, `SELECT event_id, partner_id, type, occurred_at, actor_type, actor_id, actor_label, details
		FROM partner_history WHERE partner_id = $1 ORDER BY occurred_at DESC, recorded_at DESC, event_id`, partnerID)
	if err != nil {
		return nil, fmt.Errorf("read the history: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []domain.HistoryEntry
	for rows.Next() {
		var (
			h       domain.HistoryEntry
			typ     string
			details []byte
		)
		if err := rows.Scan(&h.ID, &h.PartnerID, &typ, &h.OccurredAt, &h.Actor.Type, &h.Actor.ID, &h.Actor.Label,
			&details); err != nil {
			return nil, fmt.Errorf("read a history entry: %w", err)
		}
		h.Type, h.OccurredAt = domain.HistoryType(typ), h.OccurredAt.UTC()
		if err := json.Unmarshal(details, &h.Details); err != nil {
			return nil, fmt.Errorf("decode the details of %s: %w", h.ID, err)
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the history: %w", err)
	}
	return out, nil
}

func insertHistory(ctx context.Context, q execer, h domain.HistoryEntry) error {
	details := h.Details
	if details == nil {
		details = map[string]any{}
	}
	raw, err := json.Marshal(details)
	if err != nil {
		return fmt.Errorf("encode the details of %s: %w", h.ID, err)
	}
	if _, err := q.ExecContext(ctx, `INSERT INTO partner_history (event_id, partner_id, type, occurred_at, actor_type,
		actor_id, actor_label, details) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		h.ID, h.PartnerID, string(h.Type), h.OccurredAt.UTC(), h.Actor.Type, h.Actor.ID, h.Actor.Label, raw); err != nil {
		return fmt.Errorf("insert history entry %s: %w", h.ID, err)
	}
	return nil
}

func timePtr(t sql.NullTime) *time.Time {
	if !t.Valid {
		return nil
	}
	u := t.Time.UTC()
	return &u
}

func utcPtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC()
}
