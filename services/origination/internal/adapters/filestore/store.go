// Package filestore is a ports.DocumentStore over a directory. It is the
// document repository until the S3 module of the infrastructure exists
// (T4); the port is the same, so only this adapter changes.
package filestore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// ErrInvalidKey is returned for a key that is empty, absolute or tries to
// leave the store directory.
var ErrInvalidKey = errors.New("invalid document key")

// ErrImmutable is returned when a key already holds different content:
// documents are never overwritten, a new version gets a new key.
var ErrImmutable = errors.New("a stored document cannot be overwritten with different content")

// Store keeps each document in a file under Dir. Files are written with
// owner-only permissions.
type Store struct {
	dir string
}

var _ ports.DocumentStore = (*Store)(nil)

// New opens (creating it if needed) the store directory.
func New(dir string) (*Store, error) {
	if dir == "" {
		return nil, errors.New("filestore: the directory is required")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o750); err != nil {
		return nil, fmt.Errorf("filestore: %w", err)
	}
	return &Store{dir: abs}, nil
}

// Dir returns the absolute path of the store directory.
func (s *Store) Dir() string { return s.dir }

// resolve maps a key to a path inside the store directory, refusing any key
// that would escape it.
func (s *Store) resolve(key string) (string, error) {
	if key == "" || strings.ContainsRune(key, 0) || strings.Contains(key, "\\") || path.IsAbs(key) {
		return "", ErrInvalidKey
	}
	cleaned := path.Clean(key)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", ErrInvalidKey
	}
	return filepath.Join(s.dir, filepath.FromSlash(cleaned)), nil
}

// Put stores the content under the key. Writing the same content again is a
// no-op; different content under an existing key is ErrImmutable.
func (s *Store) Put(ctx context.Context, key string, content []byte, _ string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	target, err := s.resolve(key)
	if err != nil {
		return err
	}
	if existing, err := os.ReadFile(target); err == nil { //nolint:gosec // target is confined to the store directory by resolve
		if bytes.Equal(existing, content) {
			return nil
		}
		return ErrImmutable
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("filestore: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		return fmt.Errorf("filestore: %w", err)
	}
	// Write to a temporary name and rename, so a reader never sees half a file.
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, content, 0o600); err != nil {
		return fmt.Errorf("filestore: %w", err)
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("filestore: %w", err)
	}
	return nil
}

// Get returns the content stored under the key.
func (s *Store) Get(ctx context.Context, key string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	target, err := s.resolve(key)
	if err != nil {
		return nil, err
	}
	content, err := os.ReadFile(target) //nolint:gosec // target is confined to the store directory by resolve
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ports.ErrDocumentNotFound
		}
		return nil, fmt.Errorf("filestore: %w", err)
	}
	return content, nil
}
