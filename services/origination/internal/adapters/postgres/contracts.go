package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// contractLease is how long the worker that took a due issuance holds it. It
// is far longer than one attempt (10 s) so a batch never loses its claim
// halfway, and short enough that an issuance whose worker died is taken again
// soon.
const contractLease = 2 * time.Minute

// ContractStore is the ports.ContractRepository over the contract_issuance
// table (HU-W1.3-04; it lived in memory since HU-W1.3-03). The worker of each
// task polls the same table, so ListDue claims what it returns: a row with a
// live lease is never handed to another task, and Update releases it.
type ContractStore struct {
	db *sql.DB
}

var (
	_ ports.ContractRepository = (*ContractStore)(nil)
	_ ports.ContractRetrier    = (*ContractStore)(nil)
)

// NewContractStore builds the store over an open, migrated database.
func NewContractStore(db *sql.DB) *ContractStore { return &ContractStore{db: db} }

// eventJSON is how a CoverageActivated is kept in the event column.
type eventJSON struct {
	EventID       string         `json:"event_id"`
	PolicyID      string         `json:"policy_id"`
	AcceptanceID  string         `json:"acceptance_id"`
	CustomerID    string         `json:"customer_id"`
	FullName      string         `json:"full_name"`
	DocumentID    string         `json:"document_id"`
	Email         string         `json:"email"`
	PremiumMinor  int64          `json:"premium_minor"`
	Currency      string         `json:"currency"`
	InsuredMinor  int64          `json:"insured_amount_minor"`
	Coverages     []coverageJSON `json:"coverages"`
	ActivatedAt   time.Time      `json:"activated_at"`
	CoverageStart time.Time      `json:"coverage_start"`
	CoverageEnd   time.Time      `json:"coverage_end"`
}

// documentJSON is how a ContractDocument is kept in the document column.
type documentJSON struct {
	ID          string    `json:"id"`
	PolicyID    string    `json:"policy_id"`
	Version     int       `json:"version"`
	StorageKey  string    `json:"storage_key"`
	ContentType string    `json:"content_type"`
	SizeBytes   int       `json:"size_bytes"`
	Checksum    string    `json:"checksum"`
	GeneratedAt time.Time `json:"generated_at"`
}

func encodeEvent(e domain.CoverageActivated) ([]byte, error) {
	cs := make([]coverageJSON, 0, len(e.Coverages))
	for _, c := range e.Coverages {
		cs = append(cs, coverageJSON{Code: c.Code, InsuredAmountMinor: domain.ToMinorUnits(c.InsuredAmount)})
	}
	return json.Marshal(eventJSON{
		EventID: e.EventID, PolicyID: e.PolicyID, AcceptanceID: e.AcceptanceID, CustomerID: e.CustomerID,
		FullName: e.Holder.FullName, DocumentID: e.Holder.DocumentID, Email: e.Holder.Email,
		PremiumMinor: domain.ToMinorUnits(e.Premium), Currency: e.Currency, InsuredMinor: domain.ToMinorUnits(e.InsuredAmount),
		Coverages: cs, ActivatedAt: e.ActivatedAt.UTC(), CoverageStart: e.CoverageStart.UTC(), CoverageEnd: e.CoverageEnd.UTC(),
	})
}

func decodeEvent(raw []byte) (domain.CoverageActivated, error) {
	var in eventJSON
	if err := json.Unmarshal(raw, &in); err != nil {
		return domain.CoverageActivated{}, err
	}
	cs := make([]domain.Coverage, 0, len(in.Coverages))
	for _, c := range in.Coverages {
		cs = append(cs, domain.Coverage{Code: c.Code, InsuredAmount: domain.FromMinorUnits(c.InsuredAmountMinor)})
	}
	return domain.CoverageActivated{
		EventID: in.EventID, PolicyID: in.PolicyID, AcceptanceID: in.AcceptanceID, CustomerID: in.CustomerID,
		Holder:  domain.Applicant{FullName: in.FullName, DocumentID: in.DocumentID, Email: in.Email},
		Premium: domain.FromMinorUnits(in.PremiumMinor), Currency: in.Currency, InsuredAmount: domain.FromMinorUnits(in.InsuredMinor),
		Coverages: cs, ActivatedAt: in.ActivatedAt, CoverageStart: in.CoverageStart, CoverageEnd: in.CoverageEnd,
	}, nil
}

func encodeDocument(d *domain.ContractDocument) (any, error) {
	if d == nil {
		return nil, nil
	}
	return json.Marshal(documentJSON{
		ID: d.ID, PolicyID: d.PolicyID, Version: d.Version, StorageKey: d.StorageKey, ContentType: d.ContentType,
		SizeBytes: d.SizeBytes, Checksum: d.Checksum, GeneratedAt: d.GeneratedAt.UTC(),
	})
}

const contractColumns = `event, status, document, notification_status, notification_channel, notified_at, available_at,
	attempts, last_failure_at, last_failure, next_attempt_at, received_at, updated_at`

func scanContract(s scanner) (domain.ContractIssuance, error) {
	var (
		c                                 domain.ContractIssuance
		event, document                   []byte
		status, notification              string
		notifiedAt, availableAt, lastFail sql.NullTime
		next                              sql.NullTime
	)
	if err := s.Scan(&event, &status, &document, &notification, &c.NotificationChannel, &notifiedAt, &availableAt,
		&c.Attempts, &lastFail, &c.LastFailure, &next, &c.ReceivedAt, &c.UpdatedAt); err != nil {
		return domain.ContractIssuance{}, err
	}
	var err error
	if c.Event, err = decodeEvent(event); err != nil {
		return domain.ContractIssuance{}, fmt.Errorf("decode the event of a contract issuance: %w", err)
	}
	if document != nil {
		var d documentJSON
		if err := json.Unmarshal(document, &d); err != nil {
			return domain.ContractIssuance{}, fmt.Errorf("decode the document of %s: %w", c.Event.PolicyID, err)
		}
		c.Document = &domain.ContractDocument{
			ID: d.ID, PolicyID: d.PolicyID, Version: d.Version, StorageKey: d.StorageKey, ContentType: d.ContentType,
			SizeBytes: d.SizeBytes, Checksum: d.Checksum, GeneratedAt: d.GeneratedAt.UTC(),
		}
	}
	c.Status, c.NotificationStatus = domain.ContractStatus(status), domain.NotificationStatus(notification)
	c.NotifiedAt, c.AvailableAt, c.LastFailureAt, c.NextAttemptAt = utc(notifiedAt), utc(availableAt), utc(lastFail), utc(next)
	c.ReceivedAt, c.UpdatedAt = c.ReceivedAt.UTC(), c.UpdatedAt.UTC()
	return c, nil
}

// Create stores the issuance unless its policy already has one.
func (s *ContractStore) Create(ctx context.Context, c domain.ContractIssuance) (domain.ContractIssuance, bool, error) {
	event, err := encodeEvent(c.Event)
	if err != nil {
		return domain.ContractIssuance{}, false, fmt.Errorf("encode the event of %s: %w", c.Event.PolicyID, err)
	}
	document, err := encodeDocument(c.Document)
	if err != nil {
		return domain.ContractIssuance{}, false, fmt.Errorf("encode the document of %s: %w", c.Event.PolicyID, err)
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO contract_issuance
		(policy_id, acceptance_id, event_id, event, status, document, notification_status, notification_channel, notified_at,
		 available_at, attempts, last_failure_at, last_failure, next_attempt_at, received_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
		ON CONFLICT (policy_id) DO NOTHING`,
		c.Event.PolicyID, c.Event.AcceptanceID, c.Event.EventID, event, string(c.Status), document,
		string(c.NotificationStatus), c.NotificationChannel, nullTime(c.NotifiedAt), nullTime(c.AvailableAt), c.Attempts,
		nullTime(c.LastFailureAt), c.LastFailure, nullTime(c.NextAttemptAt), c.ReceivedAt.UTC(), c.UpdatedAt.UTC())
	if err != nil {
		return domain.ContractIssuance{}, false, fmt.Errorf("create the contract issuance of %s: %w", c.Event.PolicyID, err)
	}
	created, err := res.RowsAffected()
	if err != nil {
		return domain.ContractIssuance{}, false, err
	}
	stored, err := s.Get(ctx, c.Event.PolicyID)
	if err != nil {
		return domain.ContractIssuance{}, false, err
	}
	return stored, created == 1, nil
}

// Get returns the issuance of the policy.
func (s *ContractStore) Get(ctx context.Context, policyID string) (domain.ContractIssuance, error) {
	c, err := scanContract(s.db.QueryRowContext(ctx, `SELECT `+contractColumns+` FROM contract_issuance WHERE policy_id = $1`, policyID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ContractIssuance{}, ports.ErrContractNotFound
	}
	if err != nil {
		return domain.ContractIssuance{}, fmt.Errorf("get the contract issuance of %s: %w", policyID, err)
	}
	return c, nil
}

// Update replaces a stored issuance and releases the claim on it.
func (s *ContractStore) Update(ctx context.Context, c domain.ContractIssuance) error {
	document, err := encodeDocument(c.Document)
	if err != nil {
		return fmt.Errorf("encode the document of %s: %w", c.Event.PolicyID, err)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE contract_issuance
		SET status = $2, document = $3, notification_status = $4, notification_channel = $5, notified_at = $6,
		    available_at = $7, attempts = $8, last_failure_at = $9, last_failure = $10, next_attempt_at = $11,
		    updated_at = $12, claimed_until = NULL
		WHERE policy_id = $1`,
		c.Event.PolicyID, string(c.Status), document, string(c.NotificationStatus), c.NotificationChannel,
		nullTime(c.NotifiedAt), nullTime(c.AvailableAt), c.Attempts, nullTime(c.LastFailureAt), c.LastFailure,
		nullTime(c.NextAttemptAt), c.UpdatedAt.UTC())
	if err != nil {
		return fmt.Errorf("update the contract issuance of %s: %w", c.Event.PolicyID, err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ports.ErrContractNotFound
	}
	return nil
}

// ListDue claims and returns up to limit issuances in progress whose next
// attempt is due at now, oldest first. The claim lasts contractLease or until
// Update.
func (s *ContractStore) ListDue(ctx context.Context, now time.Time, limit int) ([]domain.ContractIssuance, error) {
	now = now.UTC()
	rows, err := s.db.QueryContext(ctx, `UPDATE contract_issuance
		SET claimed_until = $2
		WHERE policy_id IN (
			SELECT policy_id FROM contract_issuance
			WHERE status IN ('pending', 'document_ready') AND next_attempt_at <= $1
			  AND (claimed_until IS NULL OR claimed_until < $1)
			ORDER BY received_at, policy_id LIMIT $3
			FOR UPDATE SKIP LOCKED)
		RETURNING `+contractColumns, now, now.Add(contractLease), limit)
	if err != nil {
		return nil, fmt.Errorf("claim the due contract issuances: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []domain.ContractIssuance{}
	for rows.Next() {
		c, err := scanContract(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sortByReceived(out)
	return out, nil
}

func sortByReceived(cs []domain.ContractIssuance) {
	for i := 1; i < len(cs); i++ {
		for j := i; j > 0 && cs[j].ReceivedAt.Before(cs[j-1].ReceivedAt); j-- {
			cs[j], cs[j-1] = cs[j-1], cs[j]
		}
	}
}

// ReopenContract moves a failed contractual issuance back to pending, due now,
// and records the action in the same statement.
func (s *ContractStore) ReopenContract(ctx context.Context, acceptanceID string, r domain.IssuanceRetry) (domain.ContractIssuance, error) {
	at := r.At.UTC()
	c, err := scanContract(s.db.QueryRowContext(ctx, `WITH reopened AS (
			UPDATE contract_issuance
			SET status = CASE WHEN document IS NULL THEN 'pending' ELSE 'document_ready' END,
			    notification_status = CASE WHEN notification_status = 'failed' THEN 'pending' ELSE notification_status END,
			    attempts = 0, next_attempt_at = $2, claimed_until = NULL, updated_at = $2
			WHERE acceptance_id = $1 AND status = 'failed'
			RETURNING policy_id, acceptance_id, `+contractColumns+`
		), recorded AS (
			INSERT INTO issuance_action (acceptance_id, action, actor, note, created_at)
			SELECT acceptance_id, 'retry', $3::text, $4::text, $2::timestamptz FROM reopened
		)
		SELECT `+contractColumns+` FROM reopened`, acceptanceID, at, r.By, r.Note))
	if err == nil {
		return c, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.ContractIssuance{}, fmt.Errorf("retry the contract issuance of %s: %w", acceptanceID, err)
	}
	var exists bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM contract_issuance WHERE acceptance_id = $1)`,
		acceptanceID).Scan(&exists); err != nil {
		return domain.ContractIssuance{}, fmt.Errorf("look up the contract issuance of %s: %w", acceptanceID, err)
	}
	if !exists {
		return domain.ContractIssuance{}, ports.ErrContractNotFound
	}
	return domain.ContractIssuance{}, ports.ErrContractNotRetryable
}
