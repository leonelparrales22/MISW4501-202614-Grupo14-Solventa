package security

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVerify_AcceptsValidIDToken(t *testing.T) {
	iss := newTestIssuer(t)
	v := iss.verifier(t)

	claims := iss.idClaims("partner-user-1", "partner")
	claims["custom:partner_id"] = "partner-7"
	c, err := v.Verify(context.Background(), iss.sign(t, "k1", claims))

	require.NoError(t, err)
	assert.Equal(t, "partner-user-1", c.Subject)
	assert.Equal(t, "partner-user-1", c.Username)
	assert.Equal(t, "partner-user-1@solventa.test", c.Email)
	assert.Equal(t, "partner-7", c.PartnerID)
	assert.Equal(t, TokenUseID, c.TokenUse)
	assert.Equal(t, testWebClient, c.ClientID)
	assert.Equal(t, []Role{RolePartner}, c.Roles)
	assert.Equal(t, testNow.Add(4*time.Minute), c.ExpiresAt)
	assert.Equal(t, testNow.Add(-time.Minute), c.IssuedAt)
}

func TestVerify_AcceptsAnyConfiguredClientAndAudienceList(t *testing.T) {
	iss := newTestIssuer(t)
	v := iss.verifier(t)

	claims := iss.idClaims("insured-1", "insured")
	claims["aud"] = []string{"someone-else", testAppClient}
	c, err := v.Verify(context.Background(), iss.sign(t, "k1", claims))

	require.NoError(t, err)
	assert.Equal(t, testAppClient, c.ClientID)
}

func TestVerify_AcceptsAccessTokenWhenConfigured(t *testing.T) {
	iss := newTestIssuer(t)
	v := iss.verifier(t, func(c *Config) { c.TokenUse = TokenUseAccess })

	claims := iss.idClaims("advisor-1", "advisor")
	delete(claims, "aud")
	delete(claims, "cognito:username")
	claims["token_use"] = "access"
	claims["client_id"] = testWebClient
	claims["username"] = "advisor.one"

	c, err := v.Verify(context.Background(), iss.sign(t, "k1", claims))
	require.NoError(t, err)
	assert.Equal(t, "advisor.one", c.Username)
	assert.Equal(t, testWebClient, c.ClientID)
	assert.Equal(t, []Role{RoleAdvisor}, c.Roles)

	claims["client_id"] = "unknown-client"
	_, err = v.Verify(context.Background(), iss.sign(t, "k1", claims))
	assert.ErrorIs(t, err, ErrInvalidToken)
}

func TestVerify_RejectsExpired(t *testing.T) {
	iss := newTestIssuer(t)
	v := iss.verifier(t)
	signed := iss.sign(t, "k1", iss.idClaims("insured-1", "insured"))

	_, err := v.Verify(context.Background(), signed)
	require.NoError(t, err, "valid before expiry")

	iss.clock.Advance(4*time.Minute + 6*time.Second) // past exp plus the 5 s leeway
	_, err = v.Verify(context.Background(), signed)
	assert.ErrorIs(t, err, ErrInvalidToken)
	assert.ErrorIs(t, err, jwt.ErrTokenExpired)
}

func TestVerify_LeewayAbsorbsClockSkew(t *testing.T) {
	iss := newTestIssuer(t)
	v := iss.verifier(t)

	claims := iss.idClaims("insured-1", "insured")
	claims["exp"] = testNow.Add(-3 * time.Second).Unix()
	_, err := v.Verify(context.Background(), iss.sign(t, "k1", claims))
	assert.NoError(t, err)
}

func TestVerify_Rejects(t *testing.T) {
	iss := newTestIssuer(t)
	v := iss.verifier(t)

	valid := func() jwt.MapClaims { return iss.idClaims("user-1", "advisor") }
	with := func(key string, value any) string {
		c := valid()
		c[key] = value
		return iss.sign(t, "k1", c)
	}
	without := func(key string) string {
		c := valid()
		delete(c, key)
		return iss.sign(t, "k1", c)
	}

	tamperedPayload := func() string {
		parts := strings.Split(iss.sign(t, "k1", valid()), ".")
		other := strings.Split(iss.sign(t, "k1", iss.idClaims("user-2", "operations")), ".")
		return parts[0] + "." + other[1] + "." + parts[2]
	}

	noneAlg := func() string {
		tok := jwt.NewWithClaims(jwt.SigningMethodNone, valid())
		tok.Header["kid"] = "k1"
		s, err := tok.SignedString(jwt.UnsafeAllowNoneSignatureType)
		require.NoError(t, err)
		return s
	}

	hmacAlg := func() string {
		tok := jwt.NewWithClaims(jwt.SigningMethodHS256, valid())
		tok.Header["kid"] = "k1"
		s, err := tok.SignedString([]byte("shared-key-guess"))
		require.NoError(t, err)
		return s
	}

	noKid := func() string {
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, valid())
		s, err := tok.SignedString(rsaTestKey(t, 0))
		require.NoError(t, err)
		return s
	}

	cases := map[string]string{
		"empty":                 "",
		"not a jwt":             "abc.def",
		"tampered payload":      tamperedPayload(),
		"signed by another key": signWith(t, rsaTestKey(t, 2), "k1", valid()),
		"alg none":              noneAlg(),
		"alg HS256":             hmacAlg(),
		"no kid":                noKid(),
		"unknown kid":           signWith(t, rsaTestKey(t, 2), "k-unknown", valid()),
		"wrong issuer":          with("iss", "https://cognito-idp.us-east-2.amazonaws.com/us-east-2_Other"),
		"wrong audience":        with("aud", "another-client"),
		"no audience":           without("aud"),
		"access token when id":  with("token_use", "access"),
		"missing token_use":     without("token_use"),
		"missing exp":           without("exp"),
		"expired":               with("exp", testNow.Add(-time.Minute).Unix()),
		"not valid yet (nbf)":   with("nbf", testNow.Add(time.Minute).Unix()),
		"issued in the future":  with("iat", testNow.Add(time.Minute).Unix()),
		"missing subject":       without("sub"),
		"exp is not a number":   with("exp", "tomorrow"),
	}

	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			c, err := v.Verify(context.Background(), raw)
			assert.ErrorIs(t, err, ErrInvalidToken)
			assert.Equal(t, Claims{}, c)
		})
	}
}

// TestVerify_NoNetworkPerRequest checks the transversal restriction: verifying
// tokens does not add network hops to the critical path. The key set is
// downloaded once at startup; an unknown key id triggers at most one reload
// per minute.
func TestVerify_NoNetworkPerRequest(t *testing.T) {
	iss := newTestIssuer(t)
	v := iss.verifier(t)
	require.EqualValues(t, 1, iss.downloads.Load(), "one download when the verifier is built")

	signed := iss.sign(t, "k1", iss.idClaims("advisor-1", "advisor"))
	for range 50 {
		_, err := v.Verify(context.Background(), signed)
		require.NoError(t, err)
	}
	assert.EqualValues(t, 1, iss.downloads.Load(), "no download per verification")

	forged := signWith(t, rsaTestKey(t, 2), "k-unknown", iss.idClaims("advisor-1", "advisor"))
	_, err := v.Verify(context.Background(), forged)
	assert.ErrorIs(t, err, ErrInvalidToken)
	_, err = v.Verify(context.Background(), forged)
	assert.ErrorIs(t, err, ErrInvalidToken)
	assert.EqualValues(t, 1, iss.downloads.Load(), "a reload within the minimum interval is skipped")

	iss.clock.Advance(61 * time.Second)
	for range 10 {
		_, err = v.Verify(context.Background(), forged)
		assert.ErrorIs(t, err, ErrInvalidToken)
	}
	assert.EqualValues(t, 2, iss.downloads.Load(), "one reload for unknown key ids after the interval")
}

func TestVerify_PicksUpRotatedKey(t *testing.T) {
	iss := newTestIssuer(t)
	v := iss.verifier(t)
	iss.clock.Advance(2 * time.Minute)

	iss.publish("k2", rsaTestKey(t, 1))
	claims := iss.idClaims("ops-1", "operations")
	claims["iat"] = iss.clock.Now().Unix()
	claims["exp"] = iss.clock.Now().Add(5 * time.Minute).Unix()

	c, err := v.Verify(context.Background(), iss.sign(t, "k2", claims))
	require.NoError(t, err)
	assert.Equal(t, []Role{RoleOperations}, c.Roles)
	assert.EqualValues(t, 2, iss.downloads.Load())
}

func TestNewJWKSVerifier_FailsOnInvalidConfig(t *testing.T) {
	_, err := NewJWKSVerifier(context.Background(), Config{})
	assert.Error(t, err)
}

func TestNewJWKSVerifier_FailsWhenJWKSUnavailable(t *testing.T) {
	iss := newTestIssuer(t)
	iss.failWith(http.StatusBadGateway)

	_, err := NewJWKSVerifier(context.Background(), iss.config(), WithClock(iss.clock.Now))
	assert.ErrorContains(t, err, "initial JWKS load")
}

func TestNewJWKSVerifier_UsesCustomHTTPClient(t *testing.T) {
	iss := newTestIssuer(t)
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return http.DefaultTransport.RoundTrip(r)
	})}

	_, err := NewJWKSVerifier(context.Background(), iss.config(), WithHTTPClient(client), WithClock(iss.clock.Now))
	require.NoError(t, err)
	assert.Equal(t, 1, calls)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
