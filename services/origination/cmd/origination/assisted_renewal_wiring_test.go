package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/email"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/profiling"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/renewal"
)

// HU-W1.5-03: the assisted renewal is mounted with the online renewal and the
// human routes, and fails closed without audit.

type advisorTokens struct{}

func (advisorTokens) Verify(_ context.Context, raw string) (security.Claims, error) {
	return security.Claims{Subject: raw, Email: raw + "@example.com", Roles: []security.Role{security.RoleAdvisor}}, nil
}

type mailSink struct{}

func (mailSink) Send(context.Context, email.Message) error { return nil }

func onlineRenewalOver(ctx context.Context, t *testing.T, db *sql.DB) *renewal.Service {
	t.Helper()
	svc, err := newRenewalService(ctx, renewalConfig{rules: domain.DefaultRenewalPolicy, pollInterval: time.Hour}, db,
		profiling.NewResolver(nil, nil, systemClock{}), nil, func() {})
	require.NoError(t, err)
	return svc
}

func serve(opts []httpadapter.Option, method, path, token, body string) *httptest.ResponseRecorder {
	router := httpadapter.NewRouter(nil, append([]httpadapter.Option{httpadapter.WithObservability(serviceName)}, opts...)...)
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestTheAssistedRenewalIsMountedOnlyWithTheOnlineRenewalAndTheHumanRoutes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db := lazyDB(t)
	online := onlineRenewalOver(ctx, t, db)
	w := assistedRenewalWiring{renewals: online, db: db, resolver: profiling.NewResolver(nil, nil, systemClock{})}

	none, err := assistedRenewalOptions(assistedRenewalWiring{db: db}, advisorTokens{})
	require.NoError(t, err)
	assert.Empty(t, none, "without the online renewal (database and premium charge) nothing is mounted")
	none, err = assistedRenewalOptions(w, nil)
	require.NoError(t, err)
	assert.Empty(t, none, "without AUTH_ISSUER there are no human routes")

	buf := captureLog(t)
	with, err := assistedRenewalOptions(w, advisorTokens{})
	require.NoError(t, err)
	require.Len(t, with, 1)
	assert.Contains(t, buf.String(), "AUDIT_URL is not set")
	assert.Contains(t, buf.String(), "EMAIL_URL is not set")

	assert.Equal(t, http.StatusUnauthorized, serve(with, http.MethodGet, "/v1/policies/pol-1/assisted-renewal", "", "").Code,
		"the route is mounted behind the authentication")
	assert.Equal(t, http.StatusServiceUnavailable, serve(with, http.MethodGet, "/v1/policies/pol-1/assisted-renewal", "adv-1", "").Code,
		"it reaches the database, which is not there")

	_, err = assistedRenewalOptions(assistedRenewalWiring{renewals: online}, advisorTokens{})
	assert.ErrorContains(t, err, "DATABASE_URL")

	w.email = mailSink{}
	buf.Reset()
	_, err = assistedRenewalOptions(w, advisorTokens{})
	require.NoError(t, err)
	assert.NotContains(t, buf.String(), "EMAIL_URL is not set")
}

// Without AUDIT_URL the requote of a policy born in the embedded channel
// answers 503 and nothing is stored, as POST /v1/offers does.
func TestWithoutAuditTheRequoteOfAnEmbeddedPolicyFailsClosed(t *testing.T) {
	requireDocker(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pg, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("origination"), tcpostgres.WithUsername("origination"), tcpostgres.WithPassword("origination"),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").
			WithOccurrence(2).WithStartupTimeout(2*time.Minute)))
	testcontainers.CleanupContainer(t, pg)
	require.NoError(t, err)
	dbURL, err := pg.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	st, err := newStores(ctx, dbURL, systemClock{})
	require.NoError(t, err)
	defer func() { _ = st.db.Close() }()

	now := time.Now().UTC().Truncate(time.Microsecond)
	offer := domain.PersonalizedOffer{
		ID: "off-wiring", CustomerID: "partner-cust-1", Channel: domain.OfferChannelEmbedded, ConsentID: "cns-1",
		PartnerID: "partner-1", ApplicationID: "app-1",
		Applicant: domain.Applicant{FullName: "Ana Perez", DocumentID: "1020304050", Email: "ana@example.com"},
		BirthDate: time.Date(1985, 3, 14, 0, 0, 0, 0, time.UTC),
		Terms:     domain.CoverageTerms{InsuredAmount: 68_000_000, TermMonths: 12, Coverages: []domain.Coverage{{Code: domain.CoverageDeath, InsuredAmount: 68_000_000}}},
		Premium:   98_400, Currency: "COP", Mode: domain.ModeNormal, CreatedAt: now, ExpiresAt: now.Add(domain.OfferValidity),
	}
	_, _, err = st.offers.Create(ctx, offer)
	require.NoError(t, err)
	acc := domain.Acceptance{ID: "acc-wiring", OfferID: offer.ID, CustomerID: "sub-ana", Channel: domain.ChannelWeb, AcceptedAt: now}
	_, _, err = postgres.NewChargeStore(st.db).CreatePending(ctx, domain.NewCharge(acc,
		domain.Offer{ID: offer.ID, CustomerID: "sub-ana", Applicant: offer.Applicant, Premium: offer.Premium, Currency: "COP", ExpiresAt: offer.ExpiresAt},
		offer.Terms, now, now.Add(24*time.Hour)))
	require.NoError(t, err)
	require.NoError(t, postgres.NewPolicyStore(st.db).Create(ctx, domain.Policy{
		ID: "pol-wiring", AcceptanceID: "acc-wiring", CustomerID: "sub-ana", Holder: offer.Applicant, Premium: 98_400, Currency: "COP",
		InsuredAmount: 68_000_000, Coverages: offer.Terms.Coverages, CoverageStart: now.AddDate(-1, 0, 10), CoverageEnd: now.AddDate(0, 0, 10),
	}))

	online := onlineRenewalOver(ctx, t, st.db)
	opts, err := assistedRenewalOptions(assistedRenewalWiring{renewals: online, db: st.db,
		resolver: profiling.NewResolver(nil, nil, systemClock{}), offers: st.offers}, advisorTokens{})
	require.NoError(t, err)

	view := serve(opts, http.MethodGet, "/v1/policies/pol-wiring/assisted-renewal", "adv-1", "")
	require.Equal(t, http.StatusOK, view.Code, view.Body.String())
	var body map[string]any
	require.NoError(t, json.Unmarshal(view.Body.Bytes(), &body))
	assert.Equal(t, "audit", body["consent_mode"], "the policy was bought from an embedded offer")
	assert.Equal(t, true, body["can_requote"])

	rec := serve(opts, http.MethodPost, "/v1/policies/pol-wiring/assisted-renewal/requotes", "adv-1", `{}`)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "consent_unverifiable")
	var requotes int
	require.NoError(t, st.db.QueryRowContext(ctx, `SELECT count(*) FROM renewal_requote`).Scan(&requotes))
	assert.Zero(t, requotes)
}
