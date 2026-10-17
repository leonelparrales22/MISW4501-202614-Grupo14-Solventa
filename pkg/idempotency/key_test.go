package idempotency_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
)

func TestKeyIsDeterministic(t *testing.T) {
	k1, err := idempotency.Key("cobro", "6f1c2a9e-1b2c-4d5e-8f90-123456789abc")
	require.NoError(t, err)
	k2, _ := idempotency.Key("cobro", "6f1c2a9e-1b2c-4d5e-8f90-123456789abc")

	assert.Equal(t, "cobro-6f1c2a9e-1b2c-4d5e-8f90-123456789abc", k1)
	assert.Equal(t, k1, k2, "the same operation always gets the same key")
}

func TestKeyRequiresBothParts(t *testing.T) {
	for _, parts := range [][2]string{{"", "acc-1"}, {"cobro", ""}, {"", ""}} {
		_, err := idempotency.Key(parts[0], parts[1])
		assert.ErrorIs(t, err, idempotency.ErrEmptyKeyPart)
	}
}
