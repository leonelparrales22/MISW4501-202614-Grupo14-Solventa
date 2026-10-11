package security

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// ErrInvalidToken wraps every reason a token is rejected (malformed, bad
// signature, unknown key, wrong issuer, audience or token use, expired or not
// yet valid). The Authenticate middleware answers 401 for all of them.
var ErrInvalidToken = errors.New("security: invalid token")

// Verifier turns a raw bearer token into verified claims.
type Verifier interface {
	Verify(ctx context.Context, raw string) (Claims, error)
}

// Option customizes a JWKSVerifier.
type Option func(*verifierOptions)

type verifierOptions struct {
	now    func() time.Time
	client *http.Client
}

// WithClock replaces time.Now; tests use it to control expiry.
func WithClock(now func() time.Time) Option {
	return func(o *verifierOptions) { o.now = now }
}

// WithHTTPClient replaces the client used to download the JWKS. By default a
// client with Config.HTTPTimeout is used.
func WithHTTPClient(c *http.Client) Option {
	return func(o *verifierOptions) { o.client = c }
}

// JWKSVerifier verifies RS256 tokens issued by a Cognito user pool against the
// public keys it keeps in memory (no network call per request).
type JWKSVerifier struct {
	cfg    Config
	keys   *jwksCache
	parser *jwt.Parser
}

var _ Verifier = (*JWKSVerifier)(nil)

// NewJWKSVerifier validates cfg and downloads the key set once. It fails if the
// configuration is incomplete or the JWKS cannot be loaded, so the service
// does not start half-configured.
func NewJWKSVerifier(ctx context.Context, cfg Config, opts ...Option) (*JWKSVerifier, error) {
	cfg = cfg.withDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	o := verifierOptions{now: time.Now}
	for _, opt := range opts {
		opt(&o)
	}
	if o.client == nil {
		o.client = &http.Client{Timeout: cfg.HTTPTimeout}
	}

	keys := newJWKSCache(cfg.JWKSURL, o.client, cfg.MinRefreshInterval, o.now)
	if err := keys.load(ctx); err != nil {
		return nil, fmt.Errorf("security: initial JWKS load: %w", err)
	}

	parserOpts := []jwt.ParserOption{
		jwt.WithValidMethods([]string{jwt.SigningMethodRS256.Alg()}),
		jwt.WithIssuer(cfg.Issuer),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(cfg.Leeway),
		jwt.WithTimeFunc(o.now),
	}
	if cfg.TokenUse == TokenUseID {
		parserOpts = append(parserOpts, jwt.WithAudience(cfg.ClientIDs...))
	}

	return &JWKSVerifier{cfg: cfg, keys: keys, parser: jwt.NewParser(parserOpts...)}, nil
}

// Verify checks the signature (RS256 only, known key id), the issuer, the
// audience or client, the token use and the validity window, and returns the
// caller's claims.
func (v *JWKSVerifier) Verify(ctx context.Context, raw string) (Claims, error) {
	mc := jwt.MapClaims{}
	_, err := v.parser.ParseWithClaims(raw, mc, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		if kid == "" {
			return nil, errors.New("security: the token has no key id")
		}
		return v.keys.key(ctx, kid)
	})
	if err != nil {
		return Claims{}, fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}

	c := claimsFromMap(mc)
	if c.TokenUse != v.cfg.TokenUse {
		return Claims{}, fmt.Errorf("%w: token_use %q is not accepted", ErrInvalidToken, c.TokenUse)
	}
	if c.Subject == "" {
		return Claims{}, fmt.Errorf("%w: the token has no subject", ErrInvalidToken)
	}

	switch v.cfg.TokenUse {
	case TokenUseID:
		// The parser already required one of the client ids in "aud".
		aud, _ := mc.GetAudience()
		for _, a := range aud {
			if slices.Contains(v.cfg.ClientIDs, a) {
				c.ClientID = a
				break
			}
		}
	case TokenUseAccess:
		if !slices.Contains(v.cfg.ClientIDs, c.ClientID) {
			return Claims{}, fmt.Errorf("%w: client_id %q is not accepted", ErrInvalidToken, c.ClientID)
		}
	}
	return c, nil
}
