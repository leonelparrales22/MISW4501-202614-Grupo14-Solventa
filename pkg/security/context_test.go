package security

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClaimsFromContext_Missing(t *testing.T) {
	c, ok := ClaimsFromContext(context.Background())
	assert.False(t, ok)
	assert.Equal(t, Claims{}, c)
}

func TestWithClaims_RoundTrip(t *testing.T) {
	want := Claims{Subject: "a1b2c3", Roles: []Role{RoleInsured}}
	ctx := WithClaims(context.Background(), want)

	got, ok := ClaimsFromContext(ctx)
	assert.True(t, ok)
	assert.Equal(t, want, got)
}

func TestClaimsFromContext_IgnoresForeignKeys(t *testing.T) {
	type otherKey struct{}
	ctx := context.WithValue(context.Background(), otherKey{}, Claims{Subject: "x"})

	_, ok := ClaimsFromContext(ctx)
	assert.False(t, ok)
}
