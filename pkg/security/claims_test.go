package security

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestParseRole(t *testing.T) {
	for _, group := range []string{"insured", "advisor", "operations", "partner"} {
		r, ok := ParseRole(group)
		assert.True(t, ok, group)
		assert.Equal(t, Role(group), r)
	}

	for _, group := range []string{"", "admin", "Advisor", "compliance", "us-east-2_pool_Google"} {
		r, ok := ParseRole(group)
		assert.False(t, ok, group)
		assert.Empty(t, r)
	}
}

func TestClaimsFromMap_IDToken(t *testing.T) {
	c := claimsFromMap(map[string]any{
		"sub":               "a1b2c3",
		"cognito:username":  "a1b2c3",
		"email":             "ana@solventa.test",
		"custom:partner_id": "partner-7",
		"token_use":         "id",
		"cognito:groups":    []any{"partner"},
		"iat":               float64(1_700_000_000),
		"exp":               json.Number("1700000300"),
	})

	assert.Equal(t, "a1b2c3", c.Subject)
	assert.Equal(t, "a1b2c3", c.Username)
	assert.Equal(t, "ana@solventa.test", c.Email)
	assert.Equal(t, "partner-7", c.PartnerID)
	assert.Equal(t, "id", c.TokenUse)
	assert.Equal(t, []Role{RolePartner}, c.Roles)
	assert.Equal(t, time.Unix(1_700_000_000, 0).UTC(), c.IssuedAt)
	assert.Equal(t, time.Unix(1_700_000_300, 0).UTC(), c.ExpiresAt)
}

func TestClaimsFromMap_AccessTokenUsesUsernameAndClientID(t *testing.T) {
	c := claimsFromMap(map[string]any{
		"sub":       "a1b2c3",
		"username":  "ana",
		"client_id": "web-client",
		"token_use": "access",
		"exp":       int64(1_700_000_300),
	})

	assert.Equal(t, "ana", c.Username)
	assert.Equal(t, "web-client", c.ClientID)
	assert.Equal(t, time.Unix(1_700_000_300, 0).UTC(), c.ExpiresAt)
	assert.True(t, c.IssuedAt.IsZero())
	assert.Empty(t, c.Roles)
}

func TestClaims_IgnoresUnknownGroups(t *testing.T) {
	c := claimsFromMap(map[string]any{
		"cognito:groups": []any{"admin", "advisor", 42, "advisor", "compliance", "operations"},
	})
	assert.Equal(t, []Role{RoleAdvisor, RoleOperations}, c.Roles)

	assert.Equal(t, []Role{RoleInsured}, claimsFromMap(map[string]any{"cognito:groups": "insured"}).Roles)
	assert.Equal(t, []Role{RolePartner}, claimsFromMap(map[string]any{"cognito:groups": []string{"partner", "root"}}).Roles)
	assert.Empty(t, claimsFromMap(map[string]any{"cognito:groups": map[string]any{"advisor": true}}).Roles)
	assert.Empty(t, claimsFromMap(map[string]any{"cognito:groups": []any{"root"}}).Roles)
}

func TestClaimsFromMap_IgnoresMalformedValues(t *testing.T) {
	c := claimsFromMap(map[string]any{
		"sub": 123,
		"exp": "tomorrow",
		"iat": json.Number("not-a-number"),
	})
	assert.Empty(t, c.Subject)
	assert.True(t, c.ExpiresAt.IsZero())
	assert.True(t, c.IssuedAt.IsZero())
}

func TestClaims_HasAnyRole(t *testing.T) {
	c := Claims{Roles: []Role{RoleAdvisor}}
	assert.True(t, c.HasAnyRole(RoleOperations, RoleAdvisor))
	assert.False(t, c.HasAnyRole(RoleOperations))
	assert.False(t, c.HasAnyRole())
	assert.False(t, Claims{}.HasAnyRole(RoleAdvisor))
}
