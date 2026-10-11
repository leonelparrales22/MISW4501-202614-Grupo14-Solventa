package security

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countingHandler records how many times the protected handler ran and the
// claims it saw.
type countingHandler struct {
	calls  int
	claims Claims
}

func (h *countingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.calls++
	h.claims, _ = ClaimsFromContext(r.Context())
	w.WriteHeader(http.StatusOK)
}

func decodeError(t *testing.T, rec *httptest.ResponseRecorder) errorBody {
	t.Helper()
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	var body errorBody
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&body))
	return body
}

func TestAuthenticate_Returns401WithoutCallingNext(t *testing.T) {
	iss := newTestIssuer(t)
	v := iss.verifier(t)

	valid := iss.idClaims("advisor-1", "advisor")
	with := func(key string, value any) string {
		c := iss.idClaims("advisor-1", "advisor")
		c[key] = value
		return "Bearer " + iss.sign(t, "k1", c)
	}
	none := jwt.NewWithClaims(jwt.SigningMethodNone, valid)
	none.Header["kid"] = "k1"
	noneSigned, err := none.SignedString(jwt.UnsafeAllowNoneSignatureType)
	require.NoError(t, err)
	hmac := jwt.NewWithClaims(jwt.SigningMethodHS256, valid)
	hmac.Header["kid"] = "k1"
	hmacSigned, err := hmac.SignedString([]byte("guessed"))
	require.NoError(t, err)
	goodSigned := iss.sign(t, "k1", valid)

	cases := map[string]string{
		"no header":             "",
		"basic scheme":          "Basic dXNlcjpwYXNz",
		"scheme only":           "Bearer",
		"empty token":           "Bearer    ",
		"two words":             "Bearer abc def",
		"malformed":             "Bearer not.a.jwt",
		"tampered signature":    "Bearer " + goodSigned[:len(goodSigned)-4] + "AAAA",
		"signed by another key": "Bearer " + signWith(t, rsaTestKey(t, 2), "k1", valid),
		"alg none":              "Bearer " + noneSigned,
		"alg HS256":             "Bearer " + hmacSigned,
		"unknown kid":           "Bearer " + signWith(t, rsaTestKey(t, 2), "k-unknown", valid),
		"wrong issuer":          with("iss", "https://cognito-idp.us-east-2.amazonaws.com/us-east-2_Other"),
		"wrong audience":        with("aud", "another-client"),
		"wrong token_use":       with("token_use", "access"),
		"expired":               with("exp", testNow.Add(-time.Hour).Unix()),
		"not valid yet":         with("nbf", testNow.Add(time.Hour).Unix()),
	}

	for name, header := range cases {
		t.Run(name, func(t *testing.T) {
			next := &countingHandler{}
			req := httptest.NewRequest(http.MethodGet, "/v1/anything", nil)
			if header != "" {
				req.Header.Set("Authorization", header)
			}
			rec := httptest.NewRecorder()

			Authenticate(v)(next).ServeHTTP(rec, req)

			assert.Equal(t, http.StatusUnauthorized, rec.Code)
			assert.Equal(t, 0, next.calls, "the protected handler must not run")
			assert.True(t, strings.HasPrefix(rec.Header().Get("WWW-Authenticate"), "Bearer"))
			assert.Equal(t, ErrorCodeUnauthorized, decodeError(t, rec).Error)
		})
	}
}

func TestAuthenticate_InvalidTokenChallenge(t *testing.T) {
	iss := newTestIssuer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/anything", nil)
	req.Header.Set("Authorization", "Bearer not.a.jwt")

	Authenticate(iss.verifier(t))(&countingHandler{}).ServeHTTP(rec, req)

	assert.Equal(t, `Bearer error="invalid_token"`, rec.Header().Get("WWW-Authenticate"))
}

func TestAuthenticate_PassesClaimsToNext(t *testing.T) {
	iss := newTestIssuer(t)
	v := iss.verifier(t)

	for _, scheme := range []string{"Bearer", "bearer", "BEARER"} {
		next := &countingHandler{}
		req := httptest.NewRequest(http.MethodGet, "/v1/anything", nil)
		req.Header.Set("Authorization", scheme+" "+iss.sign(t, "k1", iss.idClaims("ops-1", "operations")))
		rec := httptest.NewRecorder()

		Authenticate(v)(next).ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code, scheme)
		assert.Equal(t, 1, next.calls)
		assert.Equal(t, "ops-1", next.claims.Subject)
		assert.Equal(t, []Role{RoleOperations}, next.claims.Roles)
	}
}

func TestRequireRole_Returns403WithoutCallingNext(t *testing.T) {
	next := &countingHandler{}
	req := httptest.NewRequest(http.MethodPost, "/v1/quotes", nil)
	req = req.WithContext(WithClaims(context.Background(), Claims{Subject: "insured-1", Roles: []Role{RoleInsured}}))
	rec := httptest.NewRecorder()

	RequireRole(RoleAdvisor, RoleOperations)(next).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, 0, next.calls, "the business operation must not run")
	body := decodeError(t, rec)
	assert.Equal(t, ErrorCodeForbidden, body.Error)
	assert.NotEmpty(t, body.Message)
}

func TestRequireRole_Returns401WithoutClaims(t *testing.T) {
	next := &countingHandler{}
	rec := httptest.NewRecorder()

	RequireRole(RoleAdvisor)(next).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/quotes", nil))

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, "Bearer", rec.Header().Get("WWW-Authenticate"))
	assert.Equal(t, 0, next.calls)
	assert.Equal(t, ErrorCodeUnauthorized, decodeError(t, rec).Error)
}

func TestRequireRole_AllowsAnyListedRole(t *testing.T) {
	next := &countingHandler{}
	req := httptest.NewRequest(http.MethodGet, "/v1/claims/queue", nil)
	req = req.WithContext(WithClaims(context.Background(), Claims{Subject: "ops-1", Roles: []Role{RoleOperations}}))
	rec := httptest.NewRecorder()

	RequireRole(RoleAdvisor, RoleOperations)(next).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 1, next.calls)
}

func TestBearerToken(t *testing.T) {
	cases := map[string]struct {
		header string
		token  string
		ok     bool
	}{
		"valid":            {"Bearer abc.def.ghi", "abc.def.ghi", true},
		"surrounding ws":   {"  Bearer   abc.def.ghi  ", "abc.def.ghi", true},
		"lowercase":        {"bearer abc", "abc", true},
		"empty":            {"", "", false},
		"other scheme":     {"Token abc", "", false},
		"no space":         {"Bearerabc", "", false},
		"tab inside token": {"Bearer abc\tdef", "", false},
	}
	for name, tc := range cases {
		token, ok := bearerToken(tc.header)
		assert.Equal(t, tc.ok, ok, name)
		assert.Equal(t, tc.token, token, name)
	}
}

type failingWriter struct{ *httptest.ResponseRecorder }

func (failingWriter) Write([]byte) (int, error) { return 0, assert.AnError }

func TestWriteError_SurvivesBrokenConnection(t *testing.T) {
	w := failingWriter{httptest.NewRecorder()}
	WriteForbidden(w)
	assert.Equal(t, http.StatusForbidden, w.Code)
}
