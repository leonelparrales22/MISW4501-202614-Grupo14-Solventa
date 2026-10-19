package security

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Test issuer: an in-process stand-in for the Cognito user pool. It signs
// tokens with RSA keys generated for the test run and serves their JWKS from
// an httptest server, counting every download.
// ---------------------------------------------------------------------------

const (
	testPoolPath  = "/us-east-2_TestPool"
	testWebClient = "web-client-id"
	testAppClient = "mobile-client-id"
)

var testNow = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

var (
	testKeysOnce sync.Once
	testKeys     [3]*rsa.PrivateKey
)

// rsaTestKey returns one of three 2048-bit keys generated once per test run.
func rsaTestKey(t testing.TB, i int) *rsa.PrivateKey {
	t.Helper()
	testKeysOnce.Do(func() {
		for n := range testKeys {
			k, err := rsa.GenerateKey(rand.Reader, 2048)
			if err != nil {
				panic(err)
			}
			testKeys[n] = k
		}
	})
	return testKeys[i]
}

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newTestClock() *testClock { return &testClock{now: testNow} }

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type testIssuer struct {
	server    *httptest.Server
	downloads atomic.Int32
	clock     *testClock

	mu        sync.Mutex
	published map[string]*rsa.PrivateKey
	status    int
}

func newTestIssuer(t *testing.T) *testIssuer {
	t.Helper()
	iss := &testIssuer{
		clock:     newTestClock(),
		published: map[string]*rsa.PrivateKey{"k1": rsaTestKey(t, 0)},
		status:    http.StatusOK,
	}
	iss.server = httptest.NewServer(http.HandlerFunc(iss.serveJWKS))
	t.Cleanup(iss.server.Close)
	return iss
}

func (i *testIssuer) serveJWKS(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != testPoolPath+jwksWellKnownSuffix {
		http.NotFound(w, r)
		return
	}
	i.downloads.Add(1)

	i.mu.Lock()
	defer i.mu.Unlock()
	if i.status != http.StatusOK {
		w.WriteHeader(i.status)
		return
	}
	set := struct {
		Keys []jwk `json:"keys"`
	}{}
	for kid, k := range i.published {
		set.Keys = append(set.Keys, publicJWK(kid, &k.PublicKey))
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(set)
}

func publicJWK(kid string, pub *rsa.PublicKey) jwk {
	return jwk{
		Kid: kid,
		Kty: "RSA",
		Alg: "RS256",
		Use: "sig",
		N:   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		E:   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
	}
}

func (i *testIssuer) publish(kid string, k *rsa.PrivateKey) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.published[kid] = k
}

func (i *testIssuer) failWith(status int) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.status = status
}

func (i *testIssuer) issuer() string { return i.server.URL + testPoolPath }

func (i *testIssuer) config() Config {
	return Config{Issuer: i.issuer(), ClientIDs: []string{testWebClient, testAppClient}, Leeway: 5 * time.Second}
}

func (i *testIssuer) verifier(t *testing.T, mutate ...func(*Config)) *JWKSVerifier {
	t.Helper()
	cfg := i.config()
	for _, m := range mutate {
		m(&cfg)
	}
	v, err := NewJWKSVerifier(context.Background(), cfg, WithClock(i.clock.Now))
	require.NoError(t, err)
	return v
}

// idClaims returns the payload of a valid Cognito ID token for the web client.
func (i *testIssuer) idClaims(sub string, groups ...string) jwt.MapClaims {
	return jwt.MapClaims{
		"sub":              sub,
		"cognito:username": sub,
		"email":            sub + "@solventa.test",
		"cognito:groups":   groups,
		"token_use":        "id",
		"aud":              testWebClient,
		"iss":              i.issuer(),
		"iat":              testNow.Add(-time.Minute).Unix(),
		"auth_time":        testNow.Add(-time.Minute).Unix(),
		"exp":              testNow.Add(4 * time.Minute).Unix(),
	}
}

// sign signs claims with RS256 and the given key id, using the published key
// with that id (or key 0 when the id is not published).
func (i *testIssuer) sign(t testing.TB, kid string, claims jwt.MapClaims) string {
	t.Helper()
	i.mu.Lock()
	k, ok := i.published[kid]
	i.mu.Unlock()
	if !ok {
		k = rsaTestKey(t, 0)
	}
	return signWith(t, k, kid, claims)
}

func signWith(t testing.TB, k *rsa.PrivateKey, kid string, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = kid
	s, err := tok.SignedString(k)
	require.NoError(t, err)
	return s
}

// ---------------------------------------------------------------------------
// JWKS parsing and caching
// ---------------------------------------------------------------------------

func jwksDoc(t *testing.T, keys ...jwk) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"keys": keys})
	require.NoError(t, err)
	return string(b)
}

func TestParseJWKS_KeepsOnlyRSASigningKeys(t *testing.T) {
	good := publicJWK("good", &rsaTestKey(t, 0).PublicKey)

	ec := good
	ec.Kid, ec.Kty = "ec", "EC"
	enc := good
	enc.Kid, enc.Use = "enc", "enc"
	hs := good
	hs.Kid, hs.Alg = "hs", "HS256"
	noKid := good
	noKid.Kid = ""
	badN := good
	badN.Kid, badN.N = "bad-n", "***"
	badE := good
	badE.Kid, badE.E = "bad-e", "***"
	bigE := good
	bigE.Kid, bigE.E = "big-e", base64.RawURLEncoding.EncodeToString([]byte{1, 0, 0, 0, 1})
	weakE := good
	weakE.Kid, weakE.E = "weak-e", base64.RawURLEncoding.EncodeToString([]byte{1})
	small := good
	small.Kid, small.N = "small", base64.RawURLEncoding.EncodeToString(big.NewInt(1<<62).Bytes())
	padded := good
	padded.Kid, padded.N = "padded", base64.URLEncoding.EncodeToString(rsaTestKey(t, 1).N.Bytes())

	keys, err := parseJWKS(strings.NewReader(jwksDoc(t, good, ec, enc, hs, noKid, badN, badE, bigE, weakE, small, padded)))
	require.NoError(t, err)

	assert.Len(t, keys, 2)
	assert.True(t, keys["good"].Equal(&rsaTestKey(t, 0).PublicKey))
	assert.True(t, keys["padded"].Equal(&rsaTestKey(t, 1).PublicKey))
}

func TestParseJWKS_Errors(t *testing.T) {
	_, err := parseJWKS(strings.NewReader("{not json"))
	assert.Error(t, err)

	_, err = parseJWKS(strings.NewReader(`{"keys":[]}`))
	assert.Error(t, err)

	ec := publicJWK("ec", &rsaTestKey(t, 0).PublicKey)
	ec.Kty = "EC"
	_, err = parseJWKS(strings.NewReader(jwksDoc(t, ec)))
	assert.Error(t, err)
}

func TestJWKSCache_FetchErrors(t *testing.T) {
	iss := newTestIssuer(t)
	client := &http.Client{Timeout: time.Second}

	iss.failWith(http.StatusInternalServerError)
	c := newJWKSCache(iss.issuer()+jwksWellKnownSuffix, client, time.Minute, iss.clock.Now)
	assert.ErrorContains(t, c.load(context.Background()), "unexpected status 500")

	c = newJWKSCache("http://127.0.0.1:0/unreachable", client, time.Minute, iss.clock.Now)
	assert.Error(t, c.load(context.Background()))

	c = newJWKSCache("://bad-url", client, time.Minute, iss.clock.Now)
	assert.Error(t, c.load(context.Background()))
}

func TestJWKSCache_FailedReloadIsRateLimited(t *testing.T) {
	iss := newTestIssuer(t)
	c := newJWKSCache(iss.issuer()+jwksWellKnownSuffix, &http.Client{Timeout: time.Second}, time.Minute, iss.clock.Now)
	require.NoError(t, c.load(context.Background()))
	require.EqualValues(t, 1, iss.downloads.Load())

	iss.failWith(http.StatusServiceUnavailable)
	iss.clock.Advance(2 * time.Minute)

	_, err := c.key(context.Background(), "unknown")
	assert.ErrorContains(t, err, "unexpected status 503")
	_, err = c.key(context.Background(), "unknown")
	assert.ErrorIs(t, err, errUnknownKey)
	assert.EqualValues(t, 2, iss.downloads.Load(), "the second miss must not download again")

	k, err := c.key(context.Background(), "k1")
	require.NoError(t, err, "keys already cached keep working while the JWKS is down")
	assert.NotNil(t, k)
}

func TestJWKSCache_UnknownKidAfterReload(t *testing.T) {
	iss := newTestIssuer(t)
	c := newJWKSCache(iss.issuer()+jwksWellKnownSuffix, &http.Client{Timeout: time.Second}, time.Minute, iss.clock.Now)
	require.NoError(t, c.load(context.Background()))

	iss.clock.Advance(2 * time.Minute)
	_, err := c.key(context.Background(), "still-unknown")
	assert.ErrorIs(t, err, errUnknownKey)
	assert.EqualValues(t, 2, iss.downloads.Load())
}
