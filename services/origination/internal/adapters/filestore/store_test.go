package filestore_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/filestore"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

func newStore(t *testing.T) *filestore.Store {
	t.Helper()
	s, err := filestore.New(filepath.Join(t.TempDir(), "documents"))
	require.NoError(t, err)
	return s
}

func TestPutThenGet(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	require.NoError(t, s.Put(ctx, "policies/pol-1/contract-v1.pdf", []byte("%PDF-1"), "application/pdf"))

	got, err := s.Get(ctx, "policies/pol-1/contract-v1.pdf")
	require.NoError(t, err)
	assert.Equal(t, []byte("%PDF-1"), got)

	info, err := os.Stat(filepath.Join(s.Dir(), "policies", "pol-1", "contract-v1.pdf"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "documents are readable by the service only")
}

func TestGetUnknownKey(t *testing.T) {
	_, err := newStore(t).Get(context.Background(), "policies/none/contract-v1.pdf")
	assert.ErrorIs(t, err, ports.ErrDocumentNotFound)
}

func TestPutTheSameContentAgainIsANoOp(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	require.NoError(t, s.Put(ctx, "k/a.pdf", []byte("one"), "application/pdf"))

	assert.NoError(t, s.Put(ctx, "k/a.pdf", []byte("one"), "application/pdf"))
}

func TestPutDifferentContentUnderAnExistingKeyIsRefused(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	require.NoError(t, s.Put(ctx, "k/a.pdf", []byte("one"), "application/pdf"))

	err := s.Put(ctx, "k/a.pdf", []byte("two"), "application/pdf")

	assert.ErrorIs(t, err, filestore.ErrImmutable)
	got, _ := s.Get(ctx, "k/a.pdf")
	assert.Equal(t, []byte("one"), got, "the stored document is untouched")
}

func TestKeysCannotEscapeTheDirectory(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	for _, key := range []string{"", "..", "../outside.pdf", "a/../../outside.pdf", "/etc/passwd", `a\b`, "a\x00b", "."} {
		assert.ErrorIs(t, s.Put(ctx, key, []byte("x"), ""), filestore.ErrInvalidKey, "put %q", key)
		_, err := s.Get(ctx, key)
		assert.ErrorIs(t, err, filestore.ErrInvalidKey, "get %q", key)
	}
	_, err := os.Stat(filepath.Join(filepath.Dir(s.Dir()), "outside.pdf"))
	assert.True(t, os.IsNotExist(err))
}

func TestCanceledContext(t *testing.T) {
	s := newStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.ErrorIs(t, s.Put(ctx, "k/a.pdf", []byte("x"), ""), context.Canceled)
	_, err := s.Get(ctx, "k/a.pdf")
	assert.ErrorIs(t, err, context.Canceled)
}

func TestNewRequiresADirectory(t *testing.T) {
	_, err := filestore.New("")
	assert.Error(t, err)

	file := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o600))
	_, err = filestore.New(filepath.Join(file, "sub"))
	assert.Error(t, err, "a path below a regular file cannot be a directory")
}

func TestPutFailsWhenTheDirectoryCannotBeCreated(t *testing.T) {
	s := newStore(t)
	require.NoError(t, os.WriteFile(filepath.Join(s.Dir(), "policies"), []byte("a file"), 0o600))

	assert.Error(t, s.Put(context.Background(), "policies/pol-1/contract-v1.pdf", []byte("x"), ""))
}
