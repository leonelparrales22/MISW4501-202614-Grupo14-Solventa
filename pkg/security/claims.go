package security

import (
	"encoding/json"
	"math"
	"time"
)

// Names of the Cognito claims read by this package.
const (
	claimSubject     = "sub"
	claimGroups      = "cognito:groups"
	claimIDUsername  = "cognito:username"
	claimAccUsername = "username"
	claimEmail       = "email"
	claimPartnerID   = "custom:partner_id"
	claimTokenUse    = "token_use"
	claimClientID    = "client_id"
	claimIssuedAt    = "iat"
	claimExpiresAt   = "exp"
)

// Claims is the verified identity of the caller of a human route.
type Claims struct {
	// Subject is the stable user identifier ("sub"). Use it to check
	// ownership of a resource.
	Subject string
	// Username is "cognito:username" (ID token) or "username" (access token).
	Username string
	Email    string
	// PartnerID is "custom:partner_id": the partner a partner-portal user
	// belongs to. It is written only when the user is provisioned.
	PartnerID string
	// TokenUse is "id" or "access".
	TokenUse string
	// ClientID is the app client that obtained the token ("aud" in ID tokens,
	// "client_id" in access tokens).
	ClientID  string
	Roles     []Role
	IssuedAt  time.Time
	ExpiresAt time.Time
}

// HasAnyRole reports whether the caller has at least one of the given roles.
func (c Claims) HasAnyRole(roles ...Role) bool {
	for _, have := range c.Roles {
		for _, want := range roles {
			if have == want {
				return true
			}
		}
	}
	return false
}

// claimsFromMap builds Claims from the decoded token payload. Unknown groups
// in "cognito:groups" are ignored, so a group that is not a human role never
// grants access.
func claimsFromMap(m map[string]any) Claims {
	c := Claims{
		Subject:   stringClaim(m, claimSubject),
		Username:  stringClaim(m, claimIDUsername),
		Email:     stringClaim(m, claimEmail),
		PartnerID: stringClaim(m, claimPartnerID),
		TokenUse:  stringClaim(m, claimTokenUse),
		ClientID:  stringClaim(m, claimClientID),
		Roles:     rolesFromGroups(m[claimGroups]),
		IssuedAt:  timeClaim(m, claimIssuedAt),
		ExpiresAt: timeClaim(m, claimExpiresAt),
	}
	if c.Username == "" {
		c.Username = stringClaim(m, claimAccUsername)
	}
	return c
}

func rolesFromGroups(v any) []Role {
	var groups []string
	switch g := v.(type) {
	case string:
		groups = []string{g}
	case []string:
		groups = g
	case []any:
		for _, item := range g {
			if s, ok := item.(string); ok {
				groups = append(groups, s)
			}
		}
	}

	roles := make([]Role, 0, len(groups))
	seen := make(map[Role]bool, len(groups))
	for _, group := range groups {
		r, ok := ParseRole(group)
		if !ok || seen[r] {
			continue
		}
		seen[r] = true
		roles = append(roles, r)
	}
	return roles
}

func stringClaim(m map[string]any, name string) string {
	s, _ := m[name].(string)
	return s
}

// timeClaim reads a NumericDate (seconds since the epoch). It returns the
// zero time when the claim is missing or is not a number.
func timeClaim(m map[string]any, name string) time.Time {
	var seconds float64
	switch v := m[name].(type) {
	case float64:
		seconds = v
	case int64:
		seconds = float64(v)
	case json.Number:
		f, err := v.Float64()
		if err != nil {
			return time.Time{}
		}
		seconds = f
	default:
		return time.Time{}
	}
	whole, frac := math.Modf(seconds)
	return time.Unix(int64(whole), int64(frac*1e9)).UTC()
}
