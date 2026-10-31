package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// SupportStore keeps the metadata of the supports of the web filing in the
// support_file table (HU-W2.1-01). The file itself lives in the object store.
type SupportStore struct {
	db *sql.DB
}

// NewSupportStore builds the store over an open, migrated database.
func NewSupportStore(db *sql.DB) *SupportStore { return &SupportStore{db: db} }

var _ ports.SupportStore = (*SupportStore)(nil)

const supportColumns = `support_id, owner_sub, file_name, content_type, byte_size, sha256, uploaded_at, confirmed_at`

// Save stores a declared support, not confirmed yet.
func (s *SupportStore) Save(ctx context.Context, f domain.SupportFile) error {
	if _, err := s.db.ExecContext(ctx, `INSERT INTO support_file (support_id, owner_sub, file_name, content_type,
		byte_size, sha256, uploaded_at, object_key) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		f.ID, f.OwnerSubject, f.FileName, f.ContentType(), f.ByteSize, f.SHA256, f.UploadedAt, f.ObjectKey()); err != nil {
		return fmt.Errorf("store the support: %w", err)
	}
	return nil
}

// Confirm marks the support as checked. A support that was already confirmed
// keeps its first confirmation.
func (s *SupportStore) Confirm(ctx context.Context, id string, at time.Time) (domain.SupportFile, error) {
	f, err := scanSupport(s.db.QueryRowContext(ctx, `UPDATE support_file SET confirmed_at = COALESCE(confirmed_at, $2)
		WHERE support_id = $1 RETURNING `+supportColumns, id, at))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.SupportFile{}, domain.ErrSupportNotFound
	}
	return f, err
}

// Find returns the supports with the ids that exist, whoever owns them.
func (s *SupportStore) Find(ctx context.Context, ids []string) (map[string]domain.SupportFile, error) {
	out := make(map[string]domain.SupportFile, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+supportColumns+` FROM support_file WHERE support_id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("read the supports: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		f, err := scanSupport(rows)
		if err != nil {
			return nil, err
		}
		out[f.ID] = f
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the supports: %w", err)
	}
	return out, nil
}

// CountPending counts the supports of the owner not attached to any claim.
func (s *SupportStore) CountPending(ctx context.Context, owner string) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM support_file f
		WHERE f.owner_sub = $1
		  AND NOT EXISTS (SELECT 1 FROM claim_support cs WHERE cs.support_id = f.support_id)`, owner).Scan(&n); err != nil {
		return 0, fmt.Errorf("count the pending supports: %w", err)
	}
	return n, nil
}

// scanner is what *sql.Row and *sql.Rows have in common.
type scanner interface {
	Scan(dest ...any) error
}

// scanSupport reads the columns of supportColumns, in that order, from the
// first ones of the row.
func scanSupport(row scanner, extra ...any) (domain.SupportFile, error) {
	var (
		f           domain.SupportFile
		contentType string
		confirmedAt sql.NullTime
	)
	dest := append([]any{&f.ID, &f.OwnerSubject, &f.FileName, &contentType, &f.ByteSize, &f.SHA256, &f.UploadedAt,
		&confirmedAt}, extra...)
	if err := row.Scan(dest...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.SupportFile{}, err
		}
		return domain.SupportFile{}, fmt.Errorf("read a support: %w", err)
	}
	if confirmedAt.Valid {
		f.ConfirmedAt = confirmedAt.Time.UTC()
	}
	format, ok := domain.FormatOf(contentType)
	if !ok {
		return domain.SupportFile{}, fmt.Errorf("read a support: unknown content type %q", contentType)
	}
	f.Format = format
	f.UploadedAt = f.UploadedAt.UTC()
	return f, nil
}
