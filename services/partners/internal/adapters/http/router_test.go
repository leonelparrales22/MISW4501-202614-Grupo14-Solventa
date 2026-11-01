package httpadapter_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/partners/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/partners/internal/credentials"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/partners/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/partners/internal/onboarding"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/partners/internal/ports"
)

type tokenVerifier map[string]security.Claims

func (v tokenVerifier) Verify(_ context.Context, raw string) (security.Claims, error) {
	if c, ok := v[raw]; ok {
		return c, nil
	}
	return security.Claims{}, security.ErrInvalidToken
}

var (
	t0       = time.Date(2026, 11, 1, 15, 0, 0, 0, time.UTC)
	verifier = tokenVerifier{
		"ops":     {Subject: "sub-ops", Email: "andres@solventa.co", Roles: []security.Role{security.RoleOperations}},
		"ops-nom": {Subject: "sub-2", Roles: []security.Role{security.RoleOperations}},
		"advisor": {Subject: "sub-adv", Roles: []security.Role{security.RoleAdvisor}},
		"partner": {Subject: "sub-p", Roles: []security.Role{security.RolePartner}},
		"insured": {Subject: "sub-i", Roles: []security.Role{security.RoleInsured}},
	}
)

// fakeOnboarding records the calls and answers what the test sets.
type fakeOnboarding struct {
	view     onboarding.View
	views    []onboarding.View
	ind      onboarding.Indicators
	err      error
	actor    domain.Actor
	register domain.RegisterRequest
	calls    int
}

func (f *fakeOnboarding) Register(_ context.Context, r domain.RegisterRequest, a domain.Actor) (onboarding.View, error) {
	f.calls++
	f.register, f.actor = r, a
	return f.view, f.err
}

func (f *fakeOnboarding) Approve(_ context.Context, _ string, a domain.Actor) (onboarding.Approval, error) {
	f.calls++
	f.actor = a
	return onboarding.Approval{View: f.view, Secret: credentials.IssuedSecret{ClientID: "sol_new", ClientSecret: "SECRET-VALUE-0001"}}, f.err
}

func (f *fakeOnboarding) Get(_ context.Context, _ string) (onboarding.View, error) {
	return f.view, f.err
}

func (f *fakeOnboarding) List(_ context.Context) ([]onboarding.View, onboarding.Indicators, error) {
	f.calls++
	return f.views, f.ind, f.err
}

type fakeRotations struct {
	grace int
	err   error
	calls int
}

func (f *fakeRotations) Rotate(_ context.Context, _ string, g int, _ domain.Actor) (credentials.IssuedSecret, error) {
	f.calls++
	f.grace = g
	return credentials.IssuedSecret{ClientID: "sol_rot", ClientSecret: "ROTATED-SECRET-01"}, f.err
}

func (f *fakeRotations) DefaultGracePeriodHours() int { return 24 }

func activeView() onboarding.View {
	approved := t0.Add(-48 * time.Hour)
	exp := t0.Add(2 * time.Hour)
	ops := domain.Actor{Type: "operations", ID: "sub-ops", Label: "andres@solventa.co"}
	u := domain.NewUsage("2026-11-01", 3120, 5000)
	return onboarding.View{
		PartnerRecord: ports.PartnerRecord{
			Partner: domain.Partner{ID: "ptn-1", Code: "ten_banco", Name: "Banco Aliado", Type: domain.TypeBank,
				Products: []string{"hogar"}, TechnicalContactEmail: "api@banco.co", Status: domain.StatusActive,
				Quota: domain.Quota{DailyLimit: 5000, RateLimit: 100, BurstLimit: 200}, UsagePlanID: "plan-1",
				RegisteredAt: approved, RegisteredBy: ops, ApprovedAt: &approved},
			Credentials: []domain.Credential{
				{ID: "crd-2", ClientID: "sol_2", SecretHint: "a1c9", Status: domain.CredentialActive, IssuedAt: t0,
					IssuedBy: ops, Reason: domain.ReasonRotation},
				{ID: "crd-1", ClientID: "sol_1", SecretHint: "f00d", Status: domain.CredentialRetiring, IssuedAt: approved,
					IssuedBy: ops, Reason: domain.ReasonApproval, ExpiresAt: &exp},
			},
			History: []domain.HistoryEntry{
				{ID: "pev-3", Type: domain.HistoryCredentialsRotated, OccurredAt: t0, Actor: ops,
					Details: map[string]any{"previous_credential_id": "crd-1", "client_id": "sol_2"}},
				{ID: "pev-2", Type: domain.HistoryCredentialsIssued, OccurredAt: approved, Actor: ops},
			},
		},
		Usage: &u,
	}
}

func serve(t *testing.T, onb *fakeOnboarding, rot *fakeRotations, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	h := httpadapter.NewRouter(httpadapter.WithObservability("partners"), httpadapter.WithPartners(onb, rot, verifier, "staging"))
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestOnlyOperationsReachTheRoutes(t *testing.T) {
	routes := [][2]string{
		{http.MethodGet, "/v1/partners"}, {http.MethodPost, "/v1/partners"}, {http.MethodGet, "/v1/partners/ptn-1"},
		{http.MethodPost, "/v1/partners/ptn-1/approval"}, {http.MethodPost, "/v1/partners/ptn-1/credential-rotations"},
	}
	for _, r := range routes {
		for token, want := range map[string]int{"": 401, "bad": 401, "advisor": 403, "partner": 403, "insured": 403} {
			onb, rot := &fakeOnboarding{view: activeView()}, &fakeRotations{}
			rec := serve(t, onb, rot, r[0], r[1], token, "{}")
			assert.Equal(t, want, rec.Code, "%s %s with %q", r[0], r[1], token)
			assert.Zero(t, onb.calls+rot.calls, "nothing runs without the operations role")
		}
	}
}

func TestHealth(t *testing.T) {
	rec := serve(t, &fakeOnboarding{}, &fakeRotations{}, http.MethodGet, "/health", "", "")
	assert.Equal(t, 200, rec.Code)
}

func TestListBody(t *testing.T) {
	avg := 42.0
	onb := &fakeOnboarding{views: []onboarding.View{activeView()},
		ind: onboarding.Indicators{ActivePartners: 1, AverageQuotaUsedPercent: &avg, NearLimitNames: []string{"X"}}}
	rec := serve(t, onb, &fakeRotations{}, http.MethodGet, "/v1/partners", "ops", "")
	require.Equal(t, 200, rec.Code)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	ind := body["indicators"].(map[string]any)
	assert.EqualValues(t, 1, ind["near_limit_partners"])
	item := body["items"].([]any)[0].(map[string]any)
	assert.Equal(t, "rotation_in_progress", item["credentials_status"])
	assert.Equal(t, "normal", item["usage_status"])
	assert.Equal(t, "day", item["quota"].(map[string]any)["period"])
	assert.EqualValues(t, 62.4, item["usage"].(map[string]any)["percent"])
}

func TestDetailNeverCarriesTheSecret(t *testing.T) {
	rec := serve(t, &fakeOnboarding{view: activeView()}, &fakeRotations{}, http.MethodGet, "/v1/partners/ptn-1", "ops", "")
	require.Equal(t, 200, rec.Code)
	assert.NotContains(t, rec.Body.String(), "client_secret")
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "staging", body["environment"])
	cr := body["credentials"].(map[string]any)
	assert.Equal(t, "x-api-key", cr["header"])
	assert.Equal(t, "sol_2", cr["active"].(map[string]any)["client_id"])
	assert.Equal(t, "sol_1", cr["previous"].(map[string]any)["client_id"])
	assert.NotNil(t, cr["first_issued_at"])
	assert.NotNil(t, cr["last_rotated_at"])
	hist := body["history"].([]any)
	assert.Equal(t, "in_progress", hist[0].(map[string]any)["status"])
	assert.Equal(t, "applied", hist[1].(map[string]any)["status"])
}

func TestPendingDetailHasNoCredentials(t *testing.T) {
	v := activeView()
	v.Partner.Status, v.Credentials, v.Usage = domain.StatusPendingApproval, nil, nil
	rec := serve(t, &fakeOnboarding{view: v}, &fakeRotations{}, http.MethodGet, "/v1/partners/ptn-1", "ops", "")
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Nil(t, body["credentials"])
	assert.Nil(t, body["usage"])
	assert.Equal(t, "none", body["usage_status"])
}

func TestRegister(t *testing.T) {
	onb := &fakeOnboarding{view: activeView()}
	rec := serve(t, onb, &fakeRotations{}, http.MethodPost, "/v1/partners", "ops",
		`{"name":"Banco","code":"ten_b","type":"bank","products":["hogar"],"technical_contact_email":"a@b.co",
		 "quota":{"daily_limit":10,"rate_limit":1,"burst_limit":2}}`)
	assert.Equal(t, 201, rec.Code)
	assert.Equal(t, domain.Quota{DailyLimit: 10, RateLimit: 1, BurstLimit: 2}, onb.register.Quota)
	assert.Equal(t, domain.Actor{Type: "operations", ID: "sub-ops", Label: "andres@solventa.co"}, onb.actor)

	rec = serve(t, onb, &fakeRotations{}, http.MethodPost, "/v1/partners", "ops-nom", `{`)
	assert.Equal(t, 400, rec.Code)
	assert.Contains(t, rec.Body.String(), "invalid_request")
}

func TestApproveShowsSecretOnce(t *testing.T) {
	onb := &fakeOnboarding{view: activeView()}
	rec := serve(t, onb, &fakeRotations{}, http.MethodPost, "/v1/partners/ptn-1/approval", "ops-nom", "")
	require.Equal(t, 201, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	secret := body["secret"].(map[string]any)
	assert.Equal(t, "SECRET-VALUE-0001", secret["client_secret"])
	assert.Equal(t, "x-api-key", secret["header"])
	assert.Equal(t, "sub-2", onb.actor.Label, "without e-mail nor user name the label is the sub")

	rec = serve(t, onb, &fakeRotations{}, http.MethodPost, "/v1/partners/"+strings.Repeat("x", 65)+"/approval", "ops", "")
	assert.Equal(t, 404, rec.Code)
}

func TestRotate(t *testing.T) {
	rot := &fakeRotations{}
	rec := serve(t, &fakeOnboarding{view: activeView()}, rot, http.MethodPost, "/v1/partners/ptn-1/credential-rotations", "ops", "")
	require.Equal(t, 201, rec.Code)
	assert.Zero(t, rot.grace, "no body: the default")
	assert.Contains(t, rec.Body.String(), "ROTATED-SECRET-01")

	serve(t, &fakeOnboarding{view: activeView()}, rot, http.MethodPost, "/v1/partners/ptn-1/credential-rotations", "ops",
		`{"grace_period_hours":48}`)
	assert.Equal(t, 48, rot.grace)
	serve(t, &fakeOnboarding{view: activeView()}, rot, http.MethodPost, "/v1/partners/ptn-1/credential-rotations", "ops",
		`{"grace_period_hours":0}`)
	assert.Equal(t, -1, rot.grace, "an explicit 0 is invalid, not the default")

	rec = serve(t, &fakeOnboarding{view: activeView()}, rot, http.MethodPost, "/v1/partners/ptn-1/credential-rotations", "ops", `[`)
	assert.Equal(t, 400, rec.Code)
	rec = serve(t, &fakeOnboarding{view: activeView()}, rot, http.MethodPost,
		"/v1/partners/"+strings.Repeat("x", 65)+"/credential-rotations", "ops", "")
	assert.Equal(t, 404, rec.Code)
}

func TestErrorMapping(t *testing.T) {
	for err, want := range map[error][2]any{
		&domain.ValidationError{Fields: []domain.FieldError{{Field: "name", Code: "required"}}}: {400, "validation_failed"},
		domain.ErrPartnerNotFound:    {404, "partner_not_found"},
		domain.ErrCodeTaken:          {409, "partner_code_taken"},
		domain.ErrNotPending:         {409, "partner_not_pending"},
		domain.ErrNotActive:          {409, "partner_not_active"},
		domain.ErrRotationPending:    {409, "rotation_in_progress"},
		domain.ErrGatewayUnavailable: {503, "gateway_unavailable"},
		context.DeadlineExceeded:     {503, "service_unavailable"},
	} {
		rec := serve(t, &fakeOnboarding{err: err}, &fakeRotations{err: err}, http.MethodPost,
			"/v1/partners/ptn-1/credential-rotations", "ops", "")
		assert.Equal(t, want[0], rec.Code, err.Error())
		assert.Contains(t, rec.Body.String(), want[1].(string))
	}
	for _, path := range []string{"/v1/partners", "/v1/partners/ptn-1"} {
		rec := serve(t, &fakeOnboarding{err: domain.ErrPartnerNotFound}, &fakeRotations{}, http.MethodGet, path, "ops", "")
		assert.Equal(t, 404, rec.Code)
	}
	for _, path := range []string{"/v1/partners", "/v1/partners/ptn-1/approval"} {
		rec := serve(t, &fakeOnboarding{err: domain.ErrCodeTaken}, &fakeRotations{}, http.MethodPost, path, "ops", "{}")
		assert.Equal(t, 409, rec.Code)
	}
	// The rotation succeeded but the detail could not be read.
	rec := serve(t, &fakeOnboarding{err: context.Canceled}, &fakeRotations{}, http.MethodPost,
		"/v1/partners/ptn-1/credential-rotations", "ops", "")
	assert.Equal(t, 503, rec.Code)
}
