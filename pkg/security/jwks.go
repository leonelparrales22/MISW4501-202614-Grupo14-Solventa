package security

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	// maxJWKSBytes bounds the JWKS document read from the network.
	maxJWKSBytes = 1 << 20
	// minRSAKeyBits rejects weak keys; Cognito signs with 2048-bit RSA keys.
	minRSAKeyBits = 2048
)

var errUnknownKey = errors.New("security: unknown signing key")

// jwk is one entry of a JSON Web Key Set (RFC 7517); only RSA fields are read.
type jwk struct {
	Kid string `json:"kid"`
	Kty string `json:"kty"`
	Alg string `json:"alg"`
	Use string `json:"use"`
	N   string `json:"n"`
	E   string `json:"e"`
}

// jwksCache keeps the public keys of the user pool in memory. It downloads the
// key set once when the verifier is built and again only when a token names
// an unknown key id, at most once per minRefresh. Verifying a token with a
// known key never touches the network.
type jwksCache struct {
	url        string
	client     *http.Client
	minRefresh time.Duration
	now        func() time.Time

	mu   sync.RWMutex
	keys map[string]*rsa.PublicKey

	// reloadMu serializes downloads; lastAttempt is guarded by it.
	reloadMu    sync.Mutex
	lastAttempt time.Time
}

func newJWKSCache(url string, client *http.Client, minRefresh time.Duration, now func() time.Time) *jwksCache {
	return &jwksCache{url: url, client: client, minRefresh: minRefresh, now: now}
}

// load downloads the key set and replaces the cached keys.
func (c *jwksCache) load(ctx context.Context) error {
	c.reloadMu.Lock()
	defer c.reloadMu.Unlock()
	return c.loadLocked(ctx)
}

func (c *jwksCache) loadLocked(ctx context.Context) error {
	c.lastAttempt = c.now()

	keys, err := c.fetch(ctx)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.keys = keys
	c.mu.Unlock()
	return nil
}

// key returns the public key named kid. An unknown kid triggers a reload only
// if the previous download attempt is older than minRefresh, so a flood of
// tokens with made-up key ids cannot turn into a flood of requests to the
// identity provider.
func (c *jwksCache) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	if k := c.lookup(kid); k != nil {
		return k, nil
	}

	c.reloadMu.Lock()
	defer c.reloadMu.Unlock()
	if k := c.lookup(kid); k != nil {
		return k, nil
	}
	if c.now().Sub(c.lastAttempt) < c.minRefresh {
		return nil, errUnknownKey
	}
	if err := c.loadLocked(ctx); err != nil {
		return nil, err
	}
	if k := c.lookup(kid); k != nil {
		return k, nil
	}
	return nil, errUnknownKey
}

func (c *jwksCache) lookup(kid string) *rsa.PublicKey {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.keys[kid]
}

func (c *jwksCache) fetch(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return nil, fmt.Errorf("security: building the JWKS request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("security: downloading the JWKS: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("security: downloading the JWKS: unexpected status %d", resp.StatusCode)
	}
	return parseJWKS(io.LimitReader(resp.Body, maxJWKSBytes))
}

// parseJWKS keeps the RSA signing keys of the set and skips any other entry.
func parseJWKS(r io.Reader) (map[string]*rsa.PublicKey, error) {
	var doc struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.NewDecoder(r).Decode(&doc); err != nil {
		return nil, fmt.Errorf("security: decoding the JWKS: %w", err)
	}

	keys := make(map[string]*rsa.PublicKey, len(doc.Keys))
	for _, k := range doc.Keys {
		pub, err := k.rsaPublicKey()
		if err != nil {
			continue
		}
		keys[k.Kid] = pub
	}
	if len(keys) == 0 {
		return nil, errors.New("security: the JWKS has no usable RSA signing key")
	}
	return keys, nil
}

func (k jwk) rsaPublicKey() (*rsa.PublicKey, error) {
	switch {
	case k.Kid == "":
		return nil, errors.New("missing kid")
	case k.Kty != "RSA":
		return nil, fmt.Errorf("unsupported key type %q", k.Kty)
	case k.Use != "" && k.Use != "sig":
		return nil, fmt.Errorf("unsupported key use %q", k.Use)
	case k.Alg != "" && k.Alg != "RS256":
		return nil, fmt.Errorf("unsupported key algorithm %q", k.Alg)
	}

	nBytes, err := decodeBase64URL(k.N)
	if err != nil {
		return nil, fmt.Errorf("invalid modulus: %w", err)
	}
	eBytes, err := decodeBase64URL(k.E)
	if err != nil {
		return nil, fmt.Errorf("invalid exponent: %w", err)
	}
	if len(eBytes) == 0 || len(eBytes) > 4 {
		return nil, errors.New("invalid exponent size")
	}

	e := 0
	for _, b := range eBytes {
		e = e<<8 | int(b)
	}
	n := new(big.Int).SetBytes(nBytes)
	if e < 3 || n.BitLen() < minRSAKeyBits {
		return nil, errors.New("weak RSA key")
	}
	return &rsa.PublicKey{N: n, E: e}, nil
}

func decodeBase64URL(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
}
