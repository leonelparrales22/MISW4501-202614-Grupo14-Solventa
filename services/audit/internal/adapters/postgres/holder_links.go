package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/ports"
)

// HolderLinkStore implements ports.HolderLinkStore over the append-only
// holder_link table (HU-M1.3-01). It only inserts and reads.
type HolderLinkStore struct {
	db *sql.DB
}

// NewHolderLinkStore builds the store over an opened and migrated database.
func NewHolderLinkStore(db *sql.DB) *HolderLinkStore { return &HolderLinkStore{db: db} }

var _ ports.HolderLinkStore = (*HolderLinkStore)(nil)

// Link inserts the link unless the subject already has one; then it returns
// the stored link with created=false.
func (s *HolderLinkStore) Link(ctx context.Context, l domain.HolderLink) (domain.HolderLink, bool, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO holder_link (subject, document_id, customer_id, linked_at, event_id)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT DO NOTHING`,
		l.Subject, l.DocumentID, l.CustomerID, l.LinkedAt.UTC(), l.EventID)
	if err != nil {
		return domain.HolderLink{}, false, fmt.Errorf("insert the holder link: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 1 {
		return l, true, nil
	}
	stored, err := s.get(ctx, l.Subject)
	if errors.Is(err, ports.ErrHolderNotLinked) {
		// The conflict was on the event id, linked to another subject.
		return domain.HolderLink{}, false, fmt.Errorf("event %s already linked another account", l.EventID)
	}
	if err != nil {
		return domain.HolderLink{}, false, err
	}
	return stored, false, nil
}

// DocumentOf returns the document linked to the subject, or
// ports.ErrHolderNotLinked.
func (s *HolderLinkStore) DocumentOf(ctx context.Context, subject string) (string, error) {
	l, err := s.get(ctx, subject)
	if err != nil {
		return "", err
	}
	return l.DocumentID, nil
}

func (s *HolderLinkStore) get(ctx context.Context, subject string) (domain.HolderLink, error) {
	var l domain.HolderLink
	err := s.db.QueryRowContext(ctx, `SELECT subject, document_id, customer_id, linked_at, event_id
		FROM holder_link WHERE subject = $1`, subject).Scan(&l.Subject, &l.DocumentID, &l.CustomerID, &l.LinkedAt, &l.EventID)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.HolderLink{}, ports.ErrHolderNotLinked
	}
	if err != nil {
		return domain.HolderLink{}, fmt.Errorf("read the holder link: %w", err)
	}
	l.LinkedAt = l.LinkedAt.UTC()
	return l, nil
}
