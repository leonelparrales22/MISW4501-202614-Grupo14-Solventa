package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// AcceptanceStore is the ports.AcceptanceRepository over the acceptance table
// (HU-W1.3-01, migration 0006_acceptance.sql): the no-loss point of the
// journey survives a restart of the service (CA-5, fix C5). Each operation is
// a single statement. Update replaces the row without a condition, like the
// in-memory store: origination runs one task, so its writes are not concurrent
// (decision D-3 c of the fix C5).
type AcceptanceStore struct {
	db *sql.DB
}

var _ ports.AcceptanceRepository = (*AcceptanceStore)(nil)

// NewAcceptanceStore builds an AcceptanceStore over db (already migrated).
func NewAcceptanceStore(db *sql.DB) *AcceptanceStore { return &AcceptanceStore{db: db} }

const acceptanceColumns = `acceptance_id, offer_id, customer_id, channel, accepted_at, status,
	underwriting_outcome, underwriting_reason, underwriting_decided_at,
	review_outcome, review_decided_at, review_adjusted_offer,
	attempts, last_failure_at, next_attempt_at, updated_at`

// adjustedOfferJSON is how the adjusted offer of a decision is kept in a JSONB
// column (acceptance.review_adjusted_offer). Money is in minor units.
type adjustedOfferJSON struct {
	OfferID      string    `json:"offer_id"`
	PremiumMinor int64     `json:"premium_minor"`
	Currency     string    `json:"currency"`
	ExpiresAt    time.Time `json:"expires_at"`
}

func encodeAdjustedOffer(o *domain.AdjustedOffer) (sql.NullString, error) {
	if o == nil {
		return sql.NullString{}, nil
	}
	// Microseconds, like every TIMESTAMPTZ column, so this copy and the
	// decision show the same validity.
	raw, err := json.Marshal(adjustedOfferJSON{
		OfferID: o.ID, PremiumMinor: domain.ToMinorUnits(o.Premium), Currency: o.Currency,
		ExpiresAt: o.ExpiresAt.UTC().Truncate(time.Microsecond),
	})
	if err != nil {
		return sql.NullString{}, err
	}
	return sql.NullString{String: string(raw), Valid: true}, nil
}

func decodeAdjustedOffer(raw []byte) (*domain.AdjustedOffer, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var o adjustedOfferJSON
	if err := json.Unmarshal(raw, &o); err != nil {
		return nil, err
	}
	return &domain.AdjustedOffer{
		ID: o.OfferID, Premium: domain.FromMinorUnits(o.PremiumMinor), Currency: o.Currency, ExpiresAt: o.ExpiresAt.UTC(),
	}, nil
}

// acceptanceArgs are the values of every column but acceptance_id, in the
// order of acceptanceColumns.
func acceptanceArgs(a domain.Acceptance) ([]any, error) {
	var outcome, reason sql.NullString
	var decidedAt sql.NullTime
	if a.Decision != nil {
		outcome = sql.NullString{String: string(a.Decision.Outcome), Valid: true}
		reason = nullString(a.Decision.Reason)
		decidedAt = sql.NullTime{Time: a.Decision.DecidedAt.UTC(), Valid: true}
	}
	var reviewOutcome sql.NullString
	var reviewAt sql.NullTime
	var adjusted sql.NullString
	if a.Review != nil {
		reviewOutcome = sql.NullString{String: string(a.Review.Outcome), Valid: true}
		reviewAt = sql.NullTime{Time: a.Review.DecidedAt.UTC(), Valid: true}
		var err error
		if adjusted, err = encodeAdjustedOffer(a.Review.AdjustedOffer); err != nil {
			return nil, fmt.Errorf("encode the adjusted offer of the acceptance %s: %w", a.ID, err)
		}
	}
	return []any{
		a.OfferID, a.CustomerID, string(a.Channel), a.AcceptedAt.UTC(), string(a.Status),
		outcome, reason, decidedAt,
		reviewOutcome, reviewAt, adjusted,
		a.Attempts, nullTime(a.LastFailureAt), nullTime(a.NextAttemptAt), nullTime(a.UpdatedAt),
	}, nil
}

// Create stores the acceptance unless its offer already has one; then it
// returns the existing acceptance with created=false. Concurrent acceptances of
// the same offer converge on one row (the unique offer_id).
func (s *AcceptanceStore) Create(ctx context.Context, a domain.Acceptance) (domain.Acceptance, bool, error) {
	args, err := acceptanceArgs(a)
	if err != nil {
		return domain.Acceptance{}, false, err
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO acceptance (`+acceptanceColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
		ON CONFLICT DO NOTHING`, append([]any{a.ID}, args...)...)
	if err != nil {
		return domain.Acceptance{}, false, fmt.Errorf("create the acceptance %s: %w", a.ID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return domain.Acceptance{}, false, fmt.Errorf("create the acceptance %s: %w", a.ID, err)
	}
	if n == 1 {
		stored, err := s.Get(ctx, a.ID)
		return stored, true, err
	}
	existing, err := s.GetByOffer(ctx, a.OfferID)
	if errors.Is(err, ports.ErrAcceptanceNotFound) {
		return domain.Acceptance{}, false, fmt.Errorf("create the acceptance %s: the id is taken by another offer", a.ID)
	}
	return existing, false, err
}

// Get returns ports.ErrAcceptanceNotFound for an unknown id.
func (s *AcceptanceStore) Get(ctx context.Context, acceptanceID string) (domain.Acceptance, error) {
	if !validID(acceptanceID) {
		return domain.Acceptance{}, ports.ErrAcceptanceNotFound
	}
	return s.one(ctx, `SELECT `+acceptanceColumns+` FROM acceptance WHERE acceptance_id = $1`, acceptanceID)
}

// GetByOffer returns ports.ErrAcceptanceNotFound when the offer has no acceptance.
func (s *AcceptanceStore) GetByOffer(ctx context.Context, offerID string) (domain.Acceptance, error) {
	if !validID(offerID) {
		return domain.Acceptance{}, ports.ErrAcceptanceNotFound
	}
	return s.one(ctx, `SELECT `+acceptanceColumns+` FROM acceptance WHERE offer_id = $1`, offerID)
}

func (s *AcceptanceStore) one(ctx context.Context, query string, arg string) (domain.Acceptance, error) {
	a, err := scanAcceptance(s.db.QueryRowContext(ctx, query, arg))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Acceptance{}, ports.ErrAcceptanceNotFound
	}
	if err != nil {
		return domain.Acceptance{}, fmt.Errorf("read the acceptance: %w", err)
	}
	return a, nil
}

// Update replaces the stored acceptance. It returns ports.ErrAcceptanceNotFound
// when the id is unknown.
func (s *AcceptanceStore) Update(ctx context.Context, a domain.Acceptance) error {
	if !validID(a.ID) {
		return ports.ErrAcceptanceNotFound
	}
	args, err := acceptanceArgs(a)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE acceptance SET
		offer_id = $2, customer_id = $3, channel = $4, accepted_at = $5, status = $6,
		underwriting_outcome = $7, underwriting_reason = $8, underwriting_decided_at = $9,
		review_outcome = $10, review_decided_at = $11, review_adjusted_offer = $12,
		attempts = $13, last_failure_at = $14, next_attempt_at = $15, updated_at = $16
		WHERE acceptance_id = $1`, append([]any{a.ID}, args...)...)
	if err != nil {
		return fmt.Errorf("update the acceptance %s: %w", a.ID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update the acceptance %s: %w", a.ID, err)
	}
	if n == 0 {
		return ports.ErrAcceptanceNotFound
	}
	return nil
}

// ListDue returns up to limit registered acceptances whose next attempt is due
// at now (or was never set), oldest first; limit <= 0 means no limit.
func (s *AcceptanceStore) ListDue(ctx context.Context, now time.Time, limit int) ([]domain.Acceptance, error) {
	query := `SELECT ` + acceptanceColumns + ` FROM acceptance
		WHERE status = 'registered' AND (next_attempt_at IS NULL OR next_attempt_at <= $1)
		ORDER BY accepted_at, acceptance_id`
	args := []any{now.UTC()}
	if limit > 0 {
		query += ` LIMIT $2`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list the due acceptances: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []domain.Acceptance
	for rows.Next() {
		a, err := scanAcceptance(rows)
		if err != nil {
			return nil, fmt.Errorf("read a due acceptance: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func scanAcceptance(row scanner) (domain.Acceptance, error) {
	var (
		a                                   domain.Acceptance
		channel, status                     string
		outcome, reason, reviewOutcome      sql.NullString
		decidedAt, reviewAt                 sql.NullTime
		lastFailure, nextAttempt, updatedAt sql.NullTime
		adjusted                            []byte
	)
	if err := row.Scan(&a.ID, &a.OfferID, &a.CustomerID, &channel, &a.AcceptedAt, &status,
		&outcome, &reason, &decidedAt,
		&reviewOutcome, &reviewAt, &adjusted,
		&a.Attempts, &lastFailure, &nextAttempt, &updatedAt); err != nil {
		return domain.Acceptance{}, err
	}
	a.Channel, a.Status = domain.Channel(channel), domain.AcceptanceStatus(status)
	a.AcceptedAt = a.AcceptedAt.UTC()
	if outcome.Valid {
		a.Decision = &domain.UnderwritingDecision{
			Outcome: domain.UnderwritingOutcome(outcome.String), Reason: reason.String, DecidedAt: decidedAt.Time.UTC(),
		}
	}
	if reviewOutcome.Valid {
		offer, err := decodeAdjustedOffer(adjusted)
		if err != nil {
			return domain.Acceptance{}, fmt.Errorf("decode the adjusted offer of the acceptance %s: %w", a.ID, err)
		}
		a.Review = &domain.AcceptanceReview{
			Outcome: domain.DecisionType(reviewOutcome.String), DecidedAt: reviewAt.Time.UTC(), AdjustedOffer: offer,
		}
	}
	a.LastFailureAt, a.NextAttemptAt, a.UpdatedAt = timeOrZero(lastFailure), timeOrZero(nextAttempt), timeOrZero(updatedAt)
	return a, nil
}

// timeOrZero returns the time in UTC, or the zero time for NULL.
func timeOrZero(t sql.NullTime) time.Time {
	if !t.Valid {
		return time.Time{}
	}
	return t.Time.UTC()
}

// validID tells whether the id can name a row. PostgreSQL rejects text with a
// NUL byte, which would otherwise surface as a server error for an id that
// simply does not exist (the pattern of validOfferID).
func validID(id string) bool { return id != "" && !strings.ContainsRune(id, 0) }
