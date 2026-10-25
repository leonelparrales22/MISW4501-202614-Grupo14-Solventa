package postgres

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// SupportStore keeps the uploaded supports of the web filing in the
// support_file table (HU-W2.1-01).
//
// Provisional (HU-W2.1-01 §9): the content is a BYTEA column until the
// supports move to the evidence store of HU-M2.1-01.
type SupportStore struct {
	db *sql.DB
}

// NewSupportStore builds the store over an open, migrated database.
func NewSupportStore(db *sql.DB) *SupportStore { return &SupportStore{db: db} }

var _ ports.SupportStore = (*SupportStore)(nil)

// supportColumns are the columns of a support without its content, which is
// never read back in this story.
const supportColumns = `support_id, owner_sub, file_name, content_type, byte_size, sha256, uploaded_at`

// Save stores a new support with its content.
func (s *SupportStore) Save(ctx context.Context, f domain.SupportFile, content []byte) error {
	if _, err := s.db.ExecContext(ctx, `INSERT INTO support_file (`+supportColumns+`, content)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		f.ID, f.OwnerSubject, f.FileName, f.ContentType(), f.ByteSize, f.SHA256, f.UploadedAt, content); err != nil {
		return fmt.Errorf("store the support: %w", err)
	}
	return nil
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
	)
	dest := append([]any{&f.ID, &f.OwnerSubject, &f.FileName, &contentType, &f.ByteSize, &f.SHA256, &f.UploadedAt}, extra...)
	if err := row.Scan(dest...); err != nil {
		return domain.SupportFile{}, fmt.Errorf("read a support: %w", err)
	}
	format, ok := domain.FormatOf(contentType)
	if !ok {
		return domain.SupportFile{}, fmt.Errorf("read a support: unknown content type %q", contentType)
	}
	f.Format = format
	f.UploadedAt = f.UploadedAt.UTC()
	return f, nil
}
