package security

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Token uses accepted by the verifier ("token_use" claim of Cognito).
const (
	TokenUseID     = "id"
	TokenUseAccess = "access"
)

// Environment variables read by ConfigFromEnv. None of them is a secret.
const (
	EnvIssuer        = "AUTH_ISSUER"
	EnvJWKSURL       = "AUTH_JWKS_URL"
	EnvClientIDs     = "AUTH_CLIENT_IDS"
	EnvTokenUse      = "AUTH_TOKEN_USE" //nolint:gosec // G101 false positive: an environment variable name, not a credential
	EnvLeewaySeconds = "AUTH_LEEWAY_SECONDS"
)

const (
	defaultLeeway       = 5 * time.Second
	defaultHTTPTimeout  = 5 * time.Second
	defaultMinRefresh   = time.Minute
	jwksWellKnownSuffix = "/.well-known/jwks.json"
)

// Config tells the verifier which tokens to accept.
type Config struct {
	// Issuer is the exact "iss" of the user pool:
	// https://cognito-idp.<region>.amazonaws.com/<userPoolId>.
	Issuer string
	// JWKSURL is where the public keys are published. Defaults to
	// Issuer + "/.well-known/jwks.json".
	JWKSURL string
	// ClientIDs are the app clients allowed to call the service (web and
	// mobile). An ID token must carry one of them in "aud"; an access token
	// in "client_id".
	ClientIDs []string
	// TokenUse is TokenUseID (default) or TokenUseAccess.
	TokenUse string
	// Leeway absorbs clock skew when checking "exp", "nbf" and "iat".
	Leeway time.Duration
	// HTTPTimeout bounds every JWKS download. Defaults to 5 s.
	HTTPTimeout time.Duration
	// MinRefreshInterval limits how often an unknown "kid" can trigger a JWKS
	// reload. Defaults to one minute.
	MinRefreshInterval time.Duration
}

// ConfigFromEnv reads the configuration from the environment through getenv
// (os.Getenv in production, a map lookup in tests).
func ConfigFromEnv(getenv func(string) string) (Config, error) {
	cfg := Config{
		Issuer:   strings.TrimSpace(getenv(EnvIssuer)),
		JWKSURL:  strings.TrimSpace(getenv(EnvJWKSURL)),
		TokenUse: strings.TrimSpace(getenv(EnvTokenUse)),
		Leeway:   defaultLeeway,
	}
	for _, id := range strings.Split(getenv(EnvClientIDs), ",") {
		if id = strings.TrimSpace(id); id != "" {
			cfg.ClientIDs = append(cfg.ClientIDs, id)
		}
	}
	if raw := strings.TrimSpace(getenv(EnvLeewaySeconds)); raw != "" {
		seconds, err := strconv.Atoi(raw)
		if err != nil || seconds < 0 {
			return Config{}, fmt.Errorf("security: %s must be a non-negative integer, got %q", EnvLeewaySeconds, raw)
		}
		cfg.Leeway = time.Duration(seconds) * time.Second
	}

	cfg = cfg.withDefaults()
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) withDefaults() Config {
	c.Issuer = strings.TrimRight(c.Issuer, "/")
	if c.JWKSURL == "" && c.Issuer != "" {
		c.JWKSURL = c.Issuer + jwksWellKnownSuffix
	}
	if c.TokenUse == "" {
		c.TokenUse = TokenUseID
	}
	if c.HTTPTimeout <= 0 {
		c.HTTPTimeout = defaultHTTPTimeout
	}
	if c.MinRefreshInterval <= 0 {
		c.MinRefreshInterval = defaultMinRefresh
	}
	return c
}

func (c Config) validate() error {
	var errs []error
	if c.Issuer == "" {
		errs = append(errs, fmt.Errorf("security: %s is required", EnvIssuer))
	}
	if c.JWKSURL != "" {
		if u, err := url.Parse(c.JWKSURL); err != nil || u.Scheme == "" || u.Host == "" {
			errs = append(errs, fmt.Errorf("security: %s must be an absolute URL, got %q", EnvJWKSURL, c.JWKSURL))
		}
	}
	if len(c.ClientIDs) == 0 {
		errs = append(errs, fmt.Errorf("security: %s needs at least one client id", EnvClientIDs))
	}
	if c.TokenUse != TokenUseID && c.TokenUse != TokenUseAccess {
		errs = append(errs, fmt.Errorf("security: %s must be %q or %q, got %q", EnvTokenUse, TokenUseID, TokenUseAccess, c.TokenUse))
	}
	if c.Leeway < 0 {
		errs = append(errs, errors.New("security: leeway must not be negative"))
	}
	return errors.Join(errs...)
}
