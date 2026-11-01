package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// AdjustedOfferStore is the ports.AdjustedOfferStore over the adjusted_offer
// table (HU-W1.1-02, decision D-1 a of the fix C5, migration
// 0007_review_inbox.sql): the offer an advisor adjusts survives a restart, so
// the customer accepts it later through the same acceptance flow. The table is
// immutable (triggers reject UPDATE, DELETE and TRUNCATE) and its foreign keys
// keep the invariants of the inbox: an adjusted offer exists only for the
// decision that won the claim (B-1, decision #51) and belongs to the customer
// of its case, never to the advisor (decision #75).
type AdjustedOfferStore struct {
	db *sql.DB
}

var _ ports.AdjustedOfferStore = (*AdjustedOfferStore)(nil)

// NewAdjustedOfferStore builds an AdjustedOfferStore over db (already migrated).
func NewAdjustedOfferStore(db *sql.DB) *AdjustedOfferStore { return &AdjustedOfferStore{db: db} }

// acceptanceOfAdjusted returns the acceptance id an adjusted offer id names
// (adj-<acceptance id>, domain.AdjustedOfferID), or false for another id.
func acceptanceOfAdjusted(offerID string) (string, bool) {
	id, ok := strings.CutPrefix(offerID, domain.AdjustedOfferIDPrefix)
	return id, ok && validID(id)
}

// Create stores the adjusted offer unless its id exists: it is idempotent and
// never overwrites the stored offer. It refuses an id that is not adj-<id>.
func (s *AdjustedOfferStore) Create(ctx context.Context, o domain.Offer) error {
	acceptanceID, ok := acceptanceOfAdjusted(o.ID)
	if !ok {
		return fmt.Errorf("create the adjusted offer %q: the id must be %s<acceptance id>", o.ID, domain.AdjustedOfferIDPrefix)
	}
	applicant, err := encodeApplicant(o.Applicant)
	if err != nil {
		return err
	}
	terms, err := encodeTerms(o.Terms)
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO adjusted_offer (offer_id, acceptance_id, customer_id, applicant,
			premium_minor, currency, mode, profile_score, expires_at, terms)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (offer_id) DO NOTHING`,
		o.ID, acceptanceID, o.CustomerID, applicant, domain.ToMinorUnits(o.Premium), o.Currency, string(o.Mode),
		o.ProfileScore, o.ExpiresAt.UTC(), terms); err != nil {
		return fmt.Errorf("create the adjusted offer %s: %w", o.ID, err)
	}
	return nil
}

// Get returns ports.ErrOfferNotFound for an unknown id.
func (s *AdjustedOfferStore) Get(ctx context.Context, offerID string) (domain.Offer, error) {
	if _, ok := acceptanceOfAdjusted(offerID); !ok {
		return domain.Offer{}, ports.ErrOfferNotFound
	}
	var (
		o                domain.Offer
		applicant, terms []byte
		premium          int64
		mode             string
	)
	err := s.db.QueryRowContext(ctx, `SELECT offer_id, customer_id, applicant, premium_minor, currency, mode, profile_score,
			expires_at, terms
		FROM adjusted_offer WHERE offer_id = $1`, offerID).
		Scan(&o.ID, &o.CustomerID, &applicant, &premium, &o.Currency, &mode, &o.ProfileScore, &o.ExpiresAt, &terms)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Offer{}, ports.ErrOfferNotFound
	}
	if err != nil {
		return domain.Offer{}, fmt.Errorf("read the adjusted offer %s: %w", offerID, err)
	}
	if o.Applicant, err = decodeApplicant(applicant); err != nil {
		return domain.Offer{}, fmt.Errorf("decode the applicant of %s: %w", offerID, err)
	}
	if o.Terms, err = decodeTerms(terms); err != nil {
		return domain.Offer{}, fmt.Errorf("decode the terms of %s: %w", offerID, err)
	}
	o.Premium, o.Mode, o.ExpiresAt = domain.FromMinorUnits(premium), domain.OfferMode(mode), o.ExpiresAt.UTC()
	return o, nil
}
