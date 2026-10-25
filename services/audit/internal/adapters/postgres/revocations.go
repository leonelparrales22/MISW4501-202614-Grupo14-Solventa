package postgres

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/ports"
)

// RevocationStore implements ports.RevocationStore over consent_revocation,
// consent_event and the notice tables of migration 0004 (HU-M1.3-02). Every
// statement is an INSERT or a SELECT: the tables are append-only.
type RevocationStore struct {
	db *sql.DB
}

// NewRevocationStore builds the store over an opened and migrated database.
func NewRevocationStore(db *sql.DB) *RevocationStore { return &RevocationStore{db: db} }

var _ ports.RevocationStore = (*RevocationStore)(nil)

const revocationColumns = `revocation_id, consent_id, receipt_number, holder_document_id, holder_full_name, scope, entities,
	requested_at, effective_at, notice_deadline_at, channel, device, confirmation_method, actor_type, actor_id,
	reason, reference, request_id`

// entityEntry is the JSON shape of one entity of a revocation.
type entityEntry struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// Record implements ports.RevocationStore.
func (s *RevocationStore) Record(ctx context.Context, rev domain.Revocation, notices []ports.NoticeRecord) (domain.Revocation, bool, error) {
	stored, created, err := s.record(ctx, rev, notices)
	if err != nil || created {
		return stored, created, err
	}
	// Another request got there first: tell the same request from another.
	return s.resolveConflict(ctx, rev)
}

func (s *RevocationStore) record(ctx context.Context, rev domain.Revocation, notices []ports.NoticeRecord) (domain.Revocation, bool, error) {
	scope, entities, err := encodeRevocation(rev)
	if err != nil {
		return domain.Revocation{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Revocation{}, false, fmt.Errorf("begin the revocation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var seq int64
	if err := tx.QueryRowContext(ctx, `SELECT nextval('revocation_receipt_seq')`).Scan(&seq); err != nil {
		return domain.Revocation{}, false, fmt.Errorf("take a receipt number: %w", err)
	}
	rev.ReceiptNumber = domain.FormatReceiptNumber(rev.EffectiveAt, seq)

	res, err := tx.ExecContext(ctx, `INSERT INTO consent_revocation (`+revocationColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)
		ON CONFLICT DO NOTHING`,
		rev.ID, rev.ConsentID, rev.ReceiptNumber, rev.Holder.DocumentID, rev.Holder.FullName, scope, entities,
		rev.RequestedAt, rev.EffectiveAt, rev.NoticeDeadlineAt, string(rev.Channel), nullString(rev.Device),
		nullString(string(rev.ConfirmationMethod)), string(rev.Actor.Type), rev.Actor.ID,
		nullString(rev.Reason), nullString(rev.Reference), rev.RequestID)
	if err != nil {
		return domain.Revocation{}, false, fmt.Errorf("insert the revocation: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return domain.Revocation{}, false, err
	}

	if err := insertRevocationEvents(ctx, tx, rev, notices); err != nil {
		return domain.Revocation{}, false, err
	}
	for _, n := range notices {
		if _, err := tx.ExecContext(ctx, `INSERT INTO revocation_notice (consent_id, entity_code, idempotency_key)
			VALUES ($1, $2, $3)`, rev.ConsentID, n.EntityCode, n.Key); err != nil {
			return domain.Revocation{}, false, fmt.Errorf("insert the notice to %s: %w", n.EntityCode, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO revocation_notice_step
			(consent_id, entity_code, seq, status, failed_attempts, window_ends_at, next_attempt_at)
			VALUES ($1, $2, 1, $3, 0, $4, $5)`,
			rev.ConsentID, n.EntityCode, string(idempotency.StatusPending), n.WindowEndsAt.UTC(), n.NextAttemptAt.UTC()); err != nil {
			return domain.Revocation{}, false, fmt.Errorf("insert the first step of the notice to %s: %w", n.EntityCode, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return domain.Revocation{}, false, fmt.Errorf("commit the revocation: %w", err)
	}
	return rev, true, nil
}

// insertRevocationEvents writes the revocation_requested event (at the time
// of the request) and the revoked event (at the effective time, with the
// receipt and, in its details, the entities and the end of their window).
func insertRevocationEvents(ctx context.Context, tx *sql.Tx, rev domain.Revocation, notices []ports.NoticeRecord) error {
	windows := make([]domain.EntityWindow, 0, len(notices))
	for _, n := range notices {
		windows = append(windows, domain.EntityWindow{Code: n.EntityCode, DeadlineAt: n.WindowEndsAt.UTC()})
	}
	details, err := json.Marshal(domain.RevokedDetails{RevocationID: rev.ID, Entities: windows})
	if err != nil {
		return fmt.Errorf("encode the details of the revocation: %w", err)
	}
	for _, e := range []struct {
		status  string
		at      any
		receipt any
		details any
	}{
		{status: domain.HistoryRevocationRequested, at: rev.RequestedAt},
		{status: domain.HistoryRevoked, at: rev.EffectiveAt, receipt: rev.ReceiptNumber, details: details},
	} {
		id, err := newEventID()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO consent_event (event_id, consent_id, status, occurred_at,
				actor_type, actor_id, channel, device, receipt_id, reason, reference, details)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
			id, rev.ConsentID, e.status, e.at, string(rev.Actor.Type), rev.Actor.ID, string(rev.Channel),
			nullString(rev.Device), e.receipt, nullString(rev.Reason), nullString(rev.Reference), e.details); err != nil {
			return fmt.Errorf("insert the %s event: %w", e.status, err)
		}
	}
	return nil
}

// resolveConflict is called when the revocation was not inserted because a
// unique key already existed: the same request (returned, created=false), a
// request id used for another consent, or another revocation of the consent.
func (s *RevocationStore) resolveConflict(ctx context.Context, rev domain.Revocation) (domain.Revocation, bool, error) {
	byRequest, err := scanRevocation(s.db.QueryRowContext(ctx, `SELECT `+revocationColumns+` FROM consent_revocation
		WHERE request_id = $1`, rev.RequestID))
	switch {
	case err == nil && byRequest.ConsentID == rev.ConsentID:
		return byRequest, false, nil
	case err == nil:
		return domain.Revocation{}, false, domain.ErrIdempotencyKeyReused
	case !errors.Is(err, sql.ErrNoRows):
		return domain.Revocation{}, false, fmt.Errorf("read the revocation of the request: %w", err)
	}
	if _, err := s.GetByConsent(ctx, rev.ConsentID); err == nil {
		return domain.Revocation{}, false, domain.ErrAlreadyRevoked
	} else if !errors.Is(err, ports.ErrRevocationNotFound) {
		return domain.Revocation{}, false, err
	}
	return domain.Revocation{}, false, fmt.Errorf("the revocation of %s was not recorded and no conflicting one exists", rev.ConsentID)
}

// GetByConsent implements ports.RevocationStore.
func (s *RevocationStore) GetByConsent(ctx context.Context, consentID string) (domain.Revocation, error) {
	rev, err := scanRevocation(s.db.QueryRowContext(ctx, `SELECT `+revocationColumns+` FROM consent_revocation
		WHERE consent_id = $1`, consentID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Revocation{}, ports.ErrRevocationNotFound
	}
	if err != nil {
		return domain.Revocation{}, fmt.Errorf("read the revocation: %w", err)
	}
	return rev, nil
}

func encodeRevocation(rev domain.Revocation) (scope, entities []byte, err error) {
	entries := make([]scopeEntry, len(rev.Scope))
	for i, src := range rev.Scope {
		entries[i] = scopeEntry{Code: src.Code, DataCategories: src.DataCategories, Validity: src.Validity}
	}
	if scope, err = json.Marshal(entries); err != nil {
		return nil, nil, fmt.Errorf("encode the revoked scope: %w", err)
	}
	ents := make([]entityEntry, len(rev.Entities))
	for i, e := range rev.Entities {
		ents[i] = entityEntry{Code: e.Code, Name: e.Name}
	}
	if entities, err = json.Marshal(ents); err != nil {
		return nil, nil, fmt.Errorf("encode the revoked entities: %w", err)
	}
	return scope, entities, nil
}

func scanRevocation(row scanner) (domain.Revocation, error) {
	var (
		rev                                  domain.Revocation
		rawScope, rawEntities                []byte
		channel, actorType                   string
		device, method, reason, reference    sql.NullString
		documentID, holderName, receiptValue string
	)
	if err := row.Scan(&rev.ID, &rev.ConsentID, &receiptValue, &documentID, &holderName, &rawScope, &rawEntities,
		&rev.RequestedAt, &rev.EffectiveAt, &rev.NoticeDeadlineAt, &channel, &device, &method, &actorType, &rev.Actor.ID,
		&reason, &reference, &rev.RequestID); err != nil {
		return domain.Revocation{}, err
	}
	var scope []scopeEntry
	if err := json.Unmarshal(rawScope, &scope); err != nil {
		return domain.Revocation{}, fmt.Errorf("decode the revoked scope: %w", err)
	}
	var entities []entityEntry
	if err := json.Unmarshal(rawEntities, &entities); err != nil {
		return domain.Revocation{}, fmt.Errorf("decode the revoked entities: %w", err)
	}
	rev.ReceiptNumber = receiptValue
	rev.Holder = domain.Holder{DocumentID: documentID, FullName: holderName}
	rev.Scope = make([]domain.SourceTerm, len(scope))
	for i, e := range scope {
		rev.Scope[i] = domain.SourceTerm{Code: e.Code, DataCategories: e.DataCategories, Validity: e.Validity}
	}
	rev.Entities = make([]domain.Entity, len(entities))
	for i, e := range entities {
		rev.Entities[i] = domain.Entity{Code: e.Code, Name: e.Name}
	}
	rev.RequestedAt, rev.EffectiveAt, rev.NoticeDeadlineAt = rev.RequestedAt.UTC(), rev.EffectiveAt.UTC(), rev.NoticeDeadlineAt.UTC()
	rev.Channel = domain.RevocationChannel(channel)
	rev.Device, rev.ConfirmationMethod = device.String, domain.ConfirmationMethod(method.String)
	rev.Actor.Type = domain.ActorType(actorType)
	rev.Reason, rev.Reference = reason.String, reference.String
	return rev, nil
}

func nullString(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

// newEventID returns a random consent_event id.
func newEventID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate an event id: %w", err)
	}
	return "evt-" + hex.EncodeToString(b[:]), nil
}
