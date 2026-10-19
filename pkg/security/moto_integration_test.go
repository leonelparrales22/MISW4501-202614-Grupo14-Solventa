package security_test

// Integration with the local Cognito emulator (Moto) of
// tests/security/ha-s01/local: the tokens are issued and signed by the
// emulator, and verified with the same configuration a service reads from the
// environment (AUTH_ISSUER, AUTH_JWKS_URL, AUTH_CLIENT_IDS).
//
// Skipped unless AUTH_MOTO_URL is set, so `make test` and the CI never need
// Docker:
//
//	docker compose -f tests/security/ha-s01/local/docker-compose.yml up -d --wait
//	export MOTO_USER_PASSWORD=...   # the one used by seed.sh
//	bash tests/security/ha-s01/local/seed.sh
//	AUTH_MOTO_URL=http://localhost:5100 go test -count=1 -run Moto ./pkg/security/...

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
)

const (
	envMotoURL       = "AUTH_MOTO_URL"
	envMotoPassword  = "MOTO_USER_PASSWORD" //nolint:gosec // G101 false positive: an environment variable name, not a credential
	motoRegion       = "us-east-2"
	motoPoolName     = "solventa-local-ha-s01"
	motoPartnerID    = "socio-a"
	motoClientPrefix = motoPoolName + "-"

	// Moto takes the region of administrative calls from the credential scope
	// of the Authorization header; it does not check the signature.
	motoAuthorization = "AWS4-HMAC-SHA256 Credential=test/20261011/" + motoRegion +
		"/cognito-idp/aws4_request, SignedHeaders=host, Signature=local"
)

// Users created by tests/security/ha-s01/local/seed.sh, one per role.
var motoUsers = []struct {
	email string
	role  security.Role
}{
	{"asegurado.local@example.com", security.RoleInsured},
	{"asesor.local@example.com", security.RoleAdvisor},
	{"operaciones.local@example.com", security.RoleOperations},
	{"socio.local@example.com", security.RolePartner},
}

type motoCognito struct {
	url            string
	password       string
	poolID         string
	webClientID    string
	mobileClientID string
	http           *http.Client
}

func newMotoCognito(t *testing.T) *motoCognito {
	t.Helper()
	url := strings.TrimRight(os.Getenv(envMotoURL), "/")
	if url == "" {
		t.Skipf("set %s (and %s) to run against the local Cognito of tests/security/ha-s01/local", envMotoURL, envMotoPassword)
	}
	m := &motoCognito{
		url:      url,
		password: os.Getenv(envMotoPassword),
		http:     &http.Client{Timeout: 10 * time.Second},
	}
	require.NotEmpty(t, m.password, "%s is required together with %s", envMotoPassword, envMotoURL)

	var pools struct {
		UserPools []struct{ Id, Name string }
	}
	m.call(t, "ListUserPools", map[string]any{"MaxResults": 60}, true, http.StatusOK, &pools)
	for _, p := range pools.UserPools {
		if p.Name == motoPoolName {
			m.poolID = p.Id
		}
	}
	require.NotEmpty(t, m.poolID, "user pool %q not found: run tests/security/ha-s01/local/seed.sh", motoPoolName)

	var clients struct {
		UserPoolClients []struct{ ClientId, ClientName string }
	}
	m.call(t, "ListUserPoolClients", map[string]any{"UserPoolId": m.poolID, "MaxResults": 60}, true, http.StatusOK, &clients)
	for _, c := range clients.UserPoolClients {
		switch c.ClientName {
		case motoClientPrefix + "web":
			m.webClientID = c.ClientId
		case motoClientPrefix + "mobile":
			m.mobileClientID = c.ClientId
		}
	}
	require.NotEmpty(t, m.webClientID, "web client not found")
	require.NotEmpty(t, m.mobileClientID, "mobile client not found")
	return m
}

// call posts to the Cognito JSON API of the emulator and decodes the answer.
func (m *motoCognito) call(t *testing.T, target string, body any, admin bool, wantStatus int, out any) {
	t.Helper()
	payload, err := json.Marshal(body)
	require.NoError(t, err)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, m.url+"/", bytes.NewReader(payload))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "AWSCognitoIdentityProviderService."+target)
	if admin {
		req.Header.Set("Authorization", motoAuthorization)
	}
	resp, err := m.http.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, wantStatus, resp.StatusCode, "%s answered %d", target, resp.StatusCode)
	if out != nil {
		require.NoError(t, json.NewDecoder(resp.Body).Decode(out))
	}
}

type motoTokens struct {
	IDToken     string `json:"IdToken"`
	AccessToken string `json:"AccessToken"`
}

// signIn runs USER_PASSWORD_AUTH with the web client, as the portal does.
func (m *motoCognito) signIn(t *testing.T, email string) motoTokens {
	t.Helper()
	var out struct{ AuthenticationResult motoTokens }
	m.call(t, "InitiateAuth", map[string]any{
		"AuthFlow":       "USER_PASSWORD_AUTH",
		"ClientId":       m.webClientID,
		"AuthParameters": map[string]string{"USERNAME": email, "PASSWORD": m.password},
	}, false, http.StatusOK, &out)
	require.NotEmpty(t, out.AuthenticationResult.IDToken)
	require.NotEmpty(t, out.AuthenticationResult.AccessToken)
	return out.AuthenticationResult
}

// env is what a service would have in its environment for this pool.
func (m *motoCognito) env(tokenUse string) map[string]string {
	return map[string]string{
		security.EnvIssuer:    fmt.Sprintf("https://cognito-idp.%s.amazonaws.com/%s", motoRegion, m.poolID),
		security.EnvJWKSURL:   fmt.Sprintf("%s/%s/.well-known/jwks.json", m.url, m.poolID),
		security.EnvClientIDs: m.webClientID + "," + m.mobileClientID,
		security.EnvTokenUse:  tokenUse,
	}
}

func (m *motoCognito) verifier(t *testing.T, tokenUse string) *security.JWKSVerifier {
	t.Helper()
	env := m.env(tokenUse)
	cfg, err := security.ConfigFromEnv(func(k string) string { return env[k] })
	require.NoError(t, err)
	v, err := security.NewJWKSVerifier(context.Background(), cfg)
	require.NoError(t, err)
	return v
}

func TestMoto_VerifiesTheIDTokenOfEveryRole(t *testing.T) {
	m := newMotoCognito(t)
	v := m.verifier(t, security.TokenUseID)

	for _, u := range motoUsers {
		t.Run(string(u.role), func(t *testing.T) {
			c, err := v.Verify(context.Background(), m.signIn(t, u.email).IDToken)

			require.NoError(t, err)
			assert.NotEmpty(t, c.Subject)
			assert.Equal(t, u.email, c.Email)
			assert.Equal(t, []security.Role{u.role}, c.Roles)
			assert.Equal(t, security.TokenUseID, c.TokenUse)
			assert.Equal(t, m.webClientID, c.ClientID)
			if u.role == security.RolePartner {
				assert.Equal(t, motoPartnerID, c.PartnerID)
			} else {
				assert.Empty(t, c.PartnerID)
			}
		})
	}
}

func TestMoto_RejectsTamperedTokensAndTheWrongTokenUse(t *testing.T) {
	m := newMotoCognito(t)
	tokens := m.signIn(t, "asesor.local@example.com")

	idVerifier := m.verifier(t, security.TokenUseID)
	_, err := idVerifier.Verify(context.Background(), tamperSignature(tokens.IDToken))
	require.Error(t, err, "a token with another signature must be rejected")

	_, err = idVerifier.Verify(context.Background(), tokens.AccessToken)
	require.Error(t, err, "an access token must be rejected when the service expects ID tokens")

	c, err := m.verifier(t, security.TokenUseAccess).Verify(context.Background(), tokens.AccessToken)
	require.NoError(t, err)
	assert.Equal(t, []security.Role{security.RoleAdvisor}, c.Roles)
	assert.Equal(t, m.webClientID, c.ClientID)
}

// tamperSignature changes one character inside the signature (not the last
// ones, which may only carry base64 padding bits).
func tamperSignature(token string) string {
	b := []byte(token)
	i := len(b) - 10
	if b[i] == 'A' {
		b[i] = 'B'
	} else {
		b[i] = 'A'
	}
	return string(b)
}

func TestMoto_MiddlewareAuthorizesByRole(t *testing.T) {
	m := newMotoCognito(t)
	advisorOnly := security.Authenticate(m.verifier(t, security.TokenUseID))(
		security.RequireRole(security.RoleAdvisor)(http.HandlerFunc(
			func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })))

	cases := []struct {
		name   string
		header string
		want   int
	}{
		{"without token", "", http.StatusUnauthorized},
		{"advisor", "Bearer " + m.signIn(t, "asesor.local@example.com").IDToken, http.StatusOK},
		{"operations", "Bearer " + m.signIn(t, "operaciones.local@example.com").IDToken, http.StatusForbidden},
		{"partner", "Bearer " + m.signIn(t, "socio.local@example.com").IDToken, http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v1/quotes", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()
			advisorOnly.ServeHTTP(rec, req)
			assert.Equal(t, tc.want, rec.Code)
		})
	}
}
