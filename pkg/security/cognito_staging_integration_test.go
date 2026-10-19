package security_test

// Integration with the real Cognito user pool of the HA-S01 staging window
// (infra/terraform/environments/staging/ha-s01): the tokens are issued and
// signed by Cognito, and verified with the JWKS it publishes and the same
// configuration a service reads from the environment (AUTH_ISSUER,
// AUTH_JWKS_URL, AUTH_CLIENT_IDS). It only uses the public user pool API
// (InitiateAuth), with no AWS credentials.
//
// Skipped unless AUTH_STAGING_ISSUER is set, so `make test` and the CI never
// need AWS. During the window:
//
//	D=infra/terraform/environments/staging/ha-s01
//	export AUTH_STAGING_ISSUER="$(terraform -chdir=$D output -raw issuer)"
//	export AUTH_STAGING_CLIENT_IDS="$(terraform -chdir=$D output -raw web_client_id),$(terraform -chdir=$D output -raw mobile_client_id)"
//	export TF_VAR_seed_user_password=...   # the one used in the apply; never printed
//	go test -count=1 -run CognitoStaging -v ./pkg/security/...
//
// Optional: AUTH_STAGING_COGNITO_URL and AUTH_STAGING_JWKS_URL (another
// endpoint, e.g. the local Moto of tests/security/ha-s01/local) and
// AUTH_STAGING_USERS ("role=email,..."; defaults to the seed users of the
// composition). Tokens and the password are never logged.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
)

const (
	envStagingIssuer     = "AUTH_STAGING_ISSUER"
	envStagingClientIDs  = "AUTH_STAGING_CLIENT_IDS"
	envStagingCognitoURL = "AUTH_STAGING_COGNITO_URL"
	envStagingJWKSURL    = "AUTH_STAGING_JWKS_URL"
	envStagingUsers      = "AUTH_STAGING_USERS"
	envSeedPassword      = "SEED_USER_PASSWORD"        //nolint:gosec // G101 false positive: an environment variable name, not a credential
	envTFSeedPassword    = "TF_VAR_seed_user_password" //nolint:gosec // G101 false positive: an environment variable name, not a credential

	stagingPartnerID = "socio-a"
)

// Roles in a fixed order, so the subtests always run the same way.
var stagingRoles = []security.Role{
	security.RoleInsured, security.RoleAdvisor, security.RoleOperations, security.RolePartner,
}

// Seed users of the staging/ha-s01 composition (variable seed_users).
var defaultStagingUsers = map[security.Role]string{
	security.RoleInsured:    "asegurado.ha-s01@example.com",
	security.RoleAdvisor:    "asesor.ha-s01@example.com",
	security.RoleOperations: "operaciones.ha-s01@example.com",
	security.RolePartner:    "socio.ha-s01@example.com",
}

type cognitoStaging struct {
	issuer         string
	jwksURL        string
	cognitoURL     string
	webClientID    string
	mobileClientID string
	password       string
	users          map[security.Role]string
	http           *http.Client
}

func newCognitoStaging(t *testing.T) *cognitoStaging {
	t.Helper()
	issuer := strings.TrimRight(strings.TrimSpace(os.Getenv(envStagingIssuer)), "/")
	if issuer == "" {
		t.Skipf("set %s, %s and %s to run against the user pool of the HA-S01 staging window", envStagingIssuer, envStagingClientIDs, envTFSeedPassword)
	}
	s := &cognitoStaging{
		issuer:  issuer,
		jwksURL: strings.TrimSpace(os.Getenv(envStagingJWKSURL)),
		users:   defaultStagingUsers,
		http:    &http.Client{Timeout: 10 * time.Second},
	}

	var ids []string
	for _, id := range strings.Split(os.Getenv(envStagingClientIDs), ",") {
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
		}
	}
	require.Len(t, ids, 2, "%s must be <web client id>,<mobile client id>", envStagingClientIDs)
	s.webClientID, s.mobileClientID = ids[0], ids[1]

	s.password = os.Getenv(envSeedPassword)
	if s.password == "" {
		s.password = os.Getenv(envTFSeedPassword)
	}
	require.NotEmpty(t, s.password, "%s (or %s) is required together with %s", envTFSeedPassword, envSeedPassword, envStagingIssuer)

	s.cognitoURL = strings.TrimSpace(os.Getenv(envStagingCognitoURL))
	if s.cognitoURL == "" {
		u, err := url.Parse(issuer)
		require.NoError(t, err)
		require.NotEmpty(t, u.Host, "%s must be an absolute URL", envStagingIssuer)
		s.cognitoURL = u.Scheme + "://" + u.Host // public endpoint of the issuer's region
	}
	s.cognitoURL = strings.TrimRight(s.cognitoURL, "/") + "/"

	if raw := strings.TrimSpace(os.Getenv(envStagingUsers)); raw != "" {
		s.users = parseStagingUsers(t, raw)
	}
	return s
}

// parseStagingUsers reads "role=email,..." and requires one user per role.
func parseStagingUsers(t *testing.T, raw string) map[security.Role]string {
	t.Helper()
	users := map[security.Role]string{}
	for _, pair := range strings.Split(raw, ",") {
		name, email, ok := strings.Cut(strings.TrimSpace(pair), "=")
		require.True(t, ok, "%s entries must be role=email", envStagingUsers)
		role, known := security.ParseRole(strings.TrimSpace(name))
		require.True(t, known, "%s: unknown role %q", envStagingUsers, name)
		users[role] = strings.TrimSpace(email)
	}
	for _, role := range stagingRoles {
		require.NotEmpty(t, users[role], "%s needs a user for the role %s", envStagingUsers, role)
	}
	return users
}

type stagingTokens struct {
	IDToken     string `json:"IdToken"`
	AccessToken string `json:"AccessToken"`
}

// signIn runs USER_PASSWORD_AUTH with the given app client, as the portal and
// the mobile app do. Only the error type of a failed answer is reported.
func (s *cognitoStaging) signIn(t *testing.T, clientID, email string) stagingTokens {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"AuthFlow":       "USER_PASSWORD_AUTH",
		"ClientId":       clientID,
		"AuthParameters": map[string]string{"USERNAME": email, "PASSWORD": s.password},
	})
	require.NoError(t, err)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, s.cognitoURL, bytes.NewReader(payload))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "AWSCognitoIdentityProviderService.InitiateAuth")

	resp, err := s.http.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	var out struct {
		Type                 string `json:"__type"`
		AuthenticationResult stagingTokens
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	require.Equal(t, http.StatusOK, resp.StatusCode, "sign-in of %s answered %d %s", email, resp.StatusCode, out.Type)
	require.NotEmpty(t, out.AuthenticationResult.IDToken, "sign-in of %s returned no ID token", email)
	require.NotEmpty(t, out.AuthenticationResult.AccessToken, "sign-in of %s returned no access token", email)
	return out.AuthenticationResult
}

// verifier is what a service would build from its environment for this pool.
func (s *cognitoStaging) verifier(t *testing.T, tokenUse string) *security.JWKSVerifier {
	t.Helper()
	env := map[string]string{
		security.EnvIssuer:    s.issuer,
		security.EnvJWKSURL:   s.jwksURL, // empty: issuer + /.well-known/jwks.json
		security.EnvClientIDs: s.webClientID + "," + s.mobileClientID,
		security.EnvTokenUse:  tokenUse,
	}
	cfg, err := security.ConfigFromEnv(func(k string) string { return env[k] })
	require.NoError(t, err)
	v, err := security.NewJWKSVerifier(context.Background(), cfg)
	require.NoError(t, err)
	return v
}

// tamperGroup rewrites the cognito:groups claim of the payload to operations
// and keeps the original signature (a privilege escalation attempt).
func tamperGroup(t *testing.T, token string) string {
	t.Helper()
	parts := strings.Split(token, ".")
	require.Len(t, parts, 3, "a JWT has three parts")
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	var claims map[string]any
	require.NoError(t, json.Unmarshal(raw, &claims))
	require.Contains(t, claims, "cognito:groups", "the token must carry cognito:groups")
	claims["cognito:groups"] = []string{string(security.RoleOperations)}
	changed, err := json.Marshal(claims)
	require.NoError(t, err)
	parts[1] = base64.RawURLEncoding.EncodeToString(changed)
	return strings.Join(parts, ".")
}

func TestCognitoStaging_VerifiesTheIDTokenOfEveryRole(t *testing.T) {
	s := newCognitoStaging(t)
	v := s.verifier(t, security.TokenUseID)

	for _, role := range stagingRoles {
		t.Run(string(role), func(t *testing.T) {
			email := s.users[role]
			c, err := v.Verify(context.Background(), s.signIn(t, s.webClientID, email).IDToken)

			require.NoError(t, err)
			assert.NotEmpty(t, c.Subject)
			assert.Equal(t, email, c.Email)
			assert.Equal(t, []security.Role{role}, c.Roles)
			assert.Equal(t, security.TokenUseID, c.TokenUse)
			assert.Equal(t, s.webClientID, c.ClientID)
			if role == security.RolePartner {
				assert.Equal(t, stagingPartnerID, c.PartnerID)
			} else {
				assert.Empty(t, c.PartnerID)
			}
		})
	}
}

func TestCognitoStaging_AcceptsTheMobileClient(t *testing.T) {
	s := newCognitoStaging(t)
	email := s.users[security.RoleInsured]

	c, err := s.verifier(t, security.TokenUseID).Verify(context.Background(), s.signIn(t, s.mobileClientID, email).IDToken)

	require.NoError(t, err)
	assert.Equal(t, s.mobileClientID, c.ClientID)
	assert.Equal(t, []security.Role{security.RoleInsured}, c.Roles)
}

func TestCognitoStaging_RejectsTamperedTokensAndTheWrongTokenUse(t *testing.T) {
	s := newCognitoStaging(t)
	tokens := s.signIn(t, s.webClientID, s.users[security.RoleAdvisor])
	idVerifier := s.verifier(t, security.TokenUseID)

	_, err := idVerifier.Verify(context.Background(), tamperSignature(tokens.IDToken))
	require.ErrorIs(t, err, security.ErrInvalidToken, "a token with another signature must be rejected")

	_, err = idVerifier.Verify(context.Background(), tamperGroup(t, tokens.IDToken))
	require.ErrorIs(t, err, security.ErrInvalidToken, "a token whose group was changed must be rejected")

	_, err = idVerifier.Verify(context.Background(), tokens.AccessToken)
	require.ErrorIs(t, err, security.ErrInvalidToken, "an access token must be rejected when the service expects ID tokens")

	c, err := s.verifier(t, security.TokenUseAccess).Verify(context.Background(), tokens.AccessToken)
	require.NoError(t, err)
	assert.Equal(t, []security.Role{security.RoleAdvisor}, c.Roles)
	assert.Equal(t, s.webClientID, c.ClientID)
	assert.Equal(t, security.TokenUseAccess, c.TokenUse)
}

// bearer signs in the user of the role with the web client and returns the
// Authorization header value.
func (s *cognitoStaging) bearer(t *testing.T, role security.Role) string {
	t.Helper()
	return "Bearer " + s.signIn(t, s.webClientID, s.users[role]).IDToken
}

func serve(h http.Handler, method, target, authorization string) int {
	req := httptest.NewRequest(method, target, nil)
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func TestCognitoStaging_MiddlewareAuthorizesByRole(t *testing.T) {
	s := newCognitoStaging(t)
	executed := 0
	advisorOnly := security.Authenticate(s.verifier(t, security.TokenUseID))(
		security.RequireRole(security.RoleAdvisor)(http.HandlerFunc(
			func(w http.ResponseWriter, _ *http.Request) {
				executed++
				w.WriteHeader(http.StatusOK)
			})))

	cases := []struct {
		name string
		role security.Role
		want int
	}{
		{"without token", "", http.StatusUnauthorized},
		{"advisor", security.RoleAdvisor, http.StatusOK},
		{"operations", security.RoleOperations, http.StatusForbidden},
		{"partner", security.RolePartner, http.StatusForbidden},
		{"insured", security.RoleInsured, http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			authorization := ""
			if tc.role != "" {
				authorization = s.bearer(t, tc.role)
			}
			assert.Equal(t, tc.want, serve(advisorOnly, http.MethodGet, "/v1/quotes", authorization))
		})
	}
	assert.Equal(t, 1, executed, "the business operation must run only for the advisor")
}

// writeAuthzError translates the authorization errors as an HTTP adapter does.
func writeAuthzError(w http.ResponseWriter, err error) {
	if errors.Is(err, security.ErrUnauthenticated) {
		security.WriteUnauthorized(w)
		return
	}
	security.WriteForbidden(w)
}

func TestCognitoStaging_AuthorizesByOwnership(t *testing.T) {
	s := newCognitoStaging(t)
	verifier := s.verifier(t, security.TokenUseID)
	executed := map[string]int{}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/partners/{partnerID}/policies", func(w http.ResponseWriter, r *http.Request) {
		if err := security.RequireSamePartner(r.Context(), r.PathValue("partnerID")); err != nil {
			writeAuthzError(w, err)
			return
		}
		executed["partner "+r.PathValue("partnerID")]++
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /v1/customers/{subject}/policies", func(w http.ResponseWriter, r *http.Request) {
		if err := security.RequireOwnerOrRole(r.Context(), r.PathValue("subject")); err != nil {
			writeAuthzError(w, err)
			return
		}
		executed["owner"]++
		w.WriteHeader(http.StatusOK)
	})
	h := security.Authenticate(verifier)(mux)

	partner := s.bearer(t, security.RolePartner)
	advisor := s.signIn(t, s.webClientID, s.users[security.RoleAdvisor]).IDToken
	insured := s.signIn(t, s.webClientID, s.users[security.RoleInsured]).IDToken
	advisorClaims, err := verifier.Verify(context.Background(), advisor)
	require.NoError(t, err)
	insuredClaims, err := verifier.Verify(context.Background(), insured)
	require.NoError(t, err)

	t.Run("partner on its own partner", func(t *testing.T) {
		assert.Equal(t, http.StatusOK, serve(h, http.MethodGet, "/v1/partners/"+stagingPartnerID+"/policies", partner))
	})
	t.Run("partner on another partner", func(t *testing.T) {
		assert.Equal(t, http.StatusForbidden, serve(h, http.MethodGet, "/v1/partners/socio-b/policies", partner))
	})
	t.Run("advisor on a partner", func(t *testing.T) {
		assert.Equal(t, http.StatusForbidden, serve(h, http.MethodGet, "/v1/partners/"+stagingPartnerID+"/policies", "Bearer "+advisor))
	})
	t.Run("insured on its own resource", func(t *testing.T) {
		target := fmt.Sprintf("/v1/customers/%s/policies", insuredClaims.Subject)
		assert.Equal(t, http.StatusOK, serve(h, http.MethodGet, target, "Bearer "+insured))
	})
	t.Run("insured on someone else's resource", func(t *testing.T) {
		target := fmt.Sprintf("/v1/customers/%s/policies", advisorClaims.Subject)
		assert.Equal(t, http.StatusForbidden, serve(h, http.MethodGet, target, "Bearer "+insured))
	})

	assert.Equal(t, map[string]int{"partner " + stagingPartnerID: 1, "owner": 1}, executed,
		"the business operation must run only for the owner")
}
