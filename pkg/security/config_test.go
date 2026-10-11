package security

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func env(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestConfigFromEnv_Defaults(t *testing.T) {
	cfg, err := ConfigFromEnv(env(map[string]string{
		EnvIssuer:    "https://cognito-idp.us-east-2.amazonaws.com/us-east-2_Abc123/",
		EnvClientIDs: " web-client , mobile-client ,,",
	}))
	require.NoError(t, err)

	assert.Equal(t, "https://cognito-idp.us-east-2.amazonaws.com/us-east-2_Abc123", cfg.Issuer)
	assert.Equal(t, "https://cognito-idp.us-east-2.amazonaws.com/us-east-2_Abc123/.well-known/jwks.json", cfg.JWKSURL)
	assert.Equal(t, []string{"web-client", "mobile-client"}, cfg.ClientIDs)
	assert.Equal(t, TokenUseID, cfg.TokenUse)
	assert.Equal(t, 5*time.Second, cfg.Leeway)
	assert.Equal(t, 5*time.Second, cfg.HTTPTimeout)
	assert.Equal(t, time.Minute, cfg.MinRefreshInterval)
}

func TestConfigFromEnv_ExplicitValues(t *testing.T) {
	cfg, err := ConfigFromEnv(env(map[string]string{
		EnvIssuer:        "http://localhost:5000/us-east-2_Local",
		EnvJWKSURL:       "http://localhost:5000/us-east-2_Local/jwks",
		EnvClientIDs:     "web-client",
		EnvTokenUse:      "access",
		EnvLeewaySeconds: "0",
	}))
	require.NoError(t, err)

	assert.Equal(t, "http://localhost:5000/us-east-2_Local/jwks", cfg.JWKSURL)
	assert.Equal(t, TokenUseAccess, cfg.TokenUse)
	assert.Equal(t, time.Duration(0), cfg.Leeway)
}

func TestConfigFromEnv_Errors(t *testing.T) {
	base := map[string]string{
		EnvIssuer:    "https://cognito-idp.us-east-2.amazonaws.com/us-east-2_Abc123",
		EnvClientIDs: "web-client",
	}
	cases := map[string]struct {
		key, value string
		want       string
	}{
		"missing issuer":     {EnvIssuer, "", EnvIssuer},
		"missing client ids": {EnvClientIDs, " , ", EnvClientIDs},
		"bad token use":      {EnvTokenUse, "refresh", EnvTokenUse},
		"bad leeway":         {EnvLeewaySeconds, "five", EnvLeewaySeconds},
		"negative leeway":    {EnvLeewaySeconds, "-1", EnvLeewaySeconds},
		"relative jwks url":  {EnvJWKSURL, "/jwks.json", EnvJWKSURL},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			values := map[string]string{}
			for k, v := range base {
				values[k] = v
			}
			values[tc.key] = tc.value

			_, err := ConfigFromEnv(env(values))
			assert.ErrorContains(t, err, tc.want)
		})
	}
}

func TestConfig_ValidateRejectsNegativeLeeway(t *testing.T) {
	cfg := Config{Issuer: "https://issuer", ClientIDs: []string{"c"}, Leeway: -time.Second}.withDefaults()
	assert.ErrorContains(t, cfg.validate(), "leeway")
}
