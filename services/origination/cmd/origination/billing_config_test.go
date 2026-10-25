package main

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/billing"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

func billingEnv() map[string]string {
	m := contractEnv()
	m["PAYMENT_GATEWAY_URL"] = "http://payment-gateway:9040"
	m["DATABASE_URL"] = "postgres://origination@postgres:5432/origination"
	m["AUTH_ISSUER"] = "https://cognito-idp.us-east-2.amazonaws.com/pool"
	m["AUTH_CLIENT_IDS"] = "web,mobile"
	return m
}

func TestBillingIsOffWithoutAPaymentGateway(t *testing.T) {
	cfg, err := loadConfig(env(contractEnv()))

	require.NoError(t, err)
	assert.False(t, cfg.billing.enabled)
}

func TestBillingConfigDefaultsMatchTheValidatedExperiment(t *testing.T) {
	cfg, err := loadConfig(env(billingEnv()))

	require.NoError(t, err)
	b := cfg.billing
	assert.True(t, b.enabled)
	assert.Equal(t, "http://payment-gateway:9040", b.gatewayURL)
	assert.Equal(t, 700*time.Millisecond, b.gatewayTimeout, "decision D-9")
	assert.Equal(t, 500*time.Millisecond, b.retry.Base, "decision D-4 A")
	assert.Equal(t, time.Minute, b.retry.Max)
	assert.Equal(t, 24*time.Hour, b.retry.Window, "RETRY_WINDOW_SECONDS of ADR-01")
	assert.Equal(t, time.Second, b.worker.PollInterval)
	assert.Equal(t, 30*time.Second, b.worker.ClaimLease, "decision D-8")
	assert.Equal(t, b.retry, b.worker.Retry)
}

func TestBillingConfigOverrides(t *testing.T) {
	m := billingEnv()
	m["PAYMENT_GATEWAY_TIMEOUT_MS"] = "300"
	m["RETRY_WINDOW_SECONDS"] = "10"
	m["CHARGE_RETRY_BASE_MS"] = "100"
	m["CHARGE_RETRY_MAX_MS"] = "2000"
	m["CHARGE_POLL_INTERVAL_MS"] = "200"
	m["CHARGE_CLAIM_LEASE_MS"] = "5000"

	cfg, err := loadConfig(env(m))

	require.NoError(t, err)
	b := cfg.billing
	assert.Equal(t, 300*time.Millisecond, b.gatewayTimeout)
	assert.Equal(t, 10*time.Second, b.retry.Window)
	assert.Equal(t, 100*time.Millisecond, b.retry.Base)
	assert.Equal(t, 2*time.Second, b.retry.Max)
	assert.Equal(t, 200*time.Millisecond, b.worker.PollInterval)
	assert.Equal(t, 5*time.Second, b.worker.ClaimLease)
}

func TestBillingConfigRejectsAMissingDatabaseOrEmail(t *testing.T) {
	noDB := billingEnv()
	delete(noDB, "DATABASE_URL")
	_, err := loadConfig(env(noDB))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DATABASE_URL is required", "decision D-2b: the charge is always durable")

	noEmail := billingEnv()
	delete(noEmail, "EMAIL_URL")
	_, err = loadConfig(env(noEmail))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "EMAIL_URL is required", "decision D-16 b: a paid policy always gets its document")
}

func TestBillingConfigRejectsAGatewayWithoutAuth(t *testing.T) {
	noAuth := billingEnv()
	delete(noAuth, "AUTH_ISSUER")
	delete(noAuth, "AUTH_CLIENT_IDS")

	_, err := loadConfig(env(noAuth))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "AUTH_ISSUER is required when PAYMENT_GATEWAY_URL is set",
		"m-3 (a): saved charges and paid activations are never left unprocessed")

	cfg, err := loadConfig(env(requiredEnv()))
	require.NoError(t, err, "without a gateway nothing changes: no AUTH_ISSUER needed")
	assert.False(t, cfg.billing.enabled)
	assert.False(t, cfg.acceptances.enabled)
}

// TestBillingWithTheDocumentsInS3KeepsItsRequirements: the document store of
// AWS (DOCUMENTS_BUCKET, HA-D02 AWS phase) does not relax what the charge
// needs: its database, the e-mail provider and the human routes.
func TestBillingWithTheDocumentsInS3KeepsItsRequirements(t *testing.T) {
	inS3 := func() map[string]string {
		m := billingEnv()
		delete(m, "DOCUMENTS_DIR")
		m["DOCUMENTS_BUCKET"] = "solventa-stg-documents-368083581678"
		return m
	}
	cfg, err := loadConfig(env(inS3()))
	require.NoError(t, err)
	assert.True(t, cfg.billing.enabled)
	assert.Equal(t, "solventa-stg-documents-368083581678", cfg.documents.bucket)

	for missing, want := range map[string]string{
		"DATABASE_URL": "DATABASE_URL is required",
		"EMAIL_URL":    "EMAIL_URL is required",
		"AUTH_ISSUER":  "AUTH_ISSUER is required",
	} {
		t.Run("without "+missing, func(t *testing.T) {
			m := inS3()
			delete(m, missing)
			_, err := loadConfig(env(m))
			require.Error(t, err)
			assert.Contains(t, err.Error(), want)
		})
	}
}

func TestBillingConfigRejectsInvalidValues(t *testing.T) {
	for name, extra := range map[string]map[string]string{
		"timeout":          {"PAYMENT_GATEWAY_TIMEOUT_MS": "x"},
		"window":           {"RETRY_WINDOW_SECONDS": "0"},
		"base":             {"CHARGE_RETRY_BASE_MS": "-1"},
		"max":              {"CHARGE_RETRY_MAX_MS": "x"},
		"max below base":   {"CHARGE_RETRY_BASE_MS": "5000", "CHARGE_RETRY_MAX_MS": "1000"},
		"poll":             {"CHARGE_POLL_INTERVAL_MS": "0"},
		"lease":            {"CHARGE_CLAIM_LEASE_MS": "x"},
		"lease <= timeout": {"CHARGE_CLAIM_LEASE_MS": "700"},
	} {
		t.Run(name, func(t *testing.T) {
			m := billingEnv()
			for k, v := range extra {
				m[k] = v
			}
			_, err := loadConfig(env(m))
			assert.Error(t, err)
		})
	}
}

func TestDemoCoverageTermsActivateAValidCoverage(t *testing.T) {
	terms := demoCoverageTerms()
	assert.InDelta(t, 250_000_000, terms.InsuredAmount, 0.001)
	assert.Equal(t, 120, terms.TermMonths)
	require.Len(t, terms.Coverages, 2)

	c := domain.NewCharge(domain.Acceptance{ID: "acc-1"},
		domain.Offer{ID: "o-1", CustomerID: "cust-1", Premium: 89_500, Currency: "COP",
			Applicant: domain.Applicant{FullName: "Ana", Email: "a@x.co"}},
		terms, time.Now(), time.Time{})
	assert.NoError(t, c.ValidateActivation(time.Now()))
}

func TestWithoutBillingTheLogSaysWhy(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)

	logWithoutBilling(false)
	assert.Contains(t, buf.String(), "PAYMENT_GATEWAY_URL is not set")
	assert.NotContains(t, buf.String(), "no contractual document")

	buf.Reset()
	logWithoutBilling(true)
	assert.Contains(t, buf.String(), "no contractual document will be issued")
}

// The charge runs over the shared pool of newStores (decision D-4 a of the fix
// C5): without it, it is never wired.
func TestNewBillingFailsWithoutADatabase(t *testing.T) {
	cfg, err := loadConfig(env(billingEnv()))
	require.NoError(t, err)

	_, err = newBilling(context.Background(), cfg.billing, nil, nil, recordingConsumer())
	assert.ErrorContains(t, err, "DATABASE_URL")
}

// consumer records the coverage activations it receives.
type consumer struct {
	mu     sync.Mutex
	events []domain.CoverageActivated
}

func recordingConsumer() *consumer { return &consumer{} }

func (c *consumer) Receive(_ context.Context, ev domain.CoverageActivated) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, ev)
	return nil
}

func (c *consumer) received() []domain.CoverageActivated {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]domain.CoverageActivated(nil), c.events...)
}

func TestNewBillingMigratesChargesAndActivatesTheCoverage(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)
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
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "cobro-acc-1", r.Header.Get("Idempotency-Key"))
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"charge_id":"ch-1","status":"succeeded"}`))
	}))
	defer gateway.Close()
	m := billingEnv()
	m["PAYMENT_GATEWAY_URL"], m["DATABASE_URL"], m["CHARGE_POLL_INTERVAL_MS"] = gateway.URL, dbURL, "20"
	cfg, err := loadConfig(env(m))
	require.NoError(t, err)
	sink := recordingConsumer()

	// The single pool of the process, opened and migrated by newStores.
	st, err := newStores(ctx, cfg.offers.databaseURL, systemClock{})
	require.NoError(t, err)
	defer func() { _ = st.db.Close() }()
	wiring, err := newBilling(ctx, cfg.billing, st.db, nil, sink)
	require.NoError(t, err)

	require.NoError(t, wiring.starter.Start(ctx,
		domain.Acceptance{ID: "acc-1", OfferID: "demo-active-1", CustomerID: "cust-1", AcceptedAt: time.Now()},
		domain.Offer{ID: "demo-active-1", CustomerID: "cust-1", Premium: 89_500, Currency: "COP",
			Applicant: domain.Applicant{FullName: "Ana", DocumentID: "123", Email: "a@x.co"}}))

	// The charge waits: nothing is charged until the customer hands over a
	// payment method in screen E4 (HU-W1.3-02).
	insured := security.WithClaims(ctx, security.Claims{Subject: "cust-1", Roles: []security.Role{security.RoleInsured}})
	require.Never(t, func() bool { return len(sink.received()) > 0 }, 300*time.Millisecond, 50*time.Millisecond,
		"no coverage is activated before there is a payment method")

	_, err = wiring.service.AttachPaymentMethod(insured, "acc-1", domain.PaymentMethod{
		Kind: domain.PaymentCard, Token: "tok_7b3c1f9a", Brand: "visa", Last4: "1111", Installments: 1,
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool { return len(sink.received()) == 1 }, 10*time.Second, 20*time.Millisecond)
	ev := sink.received()[0]
	assert.Equal(t, "pol-acc-1", ev.PolicyID)
	assert.NoError(t, ev.Validate())

	c, err := wiring.service.Get(insured, "acc-1")
	require.NoError(t, err)
	assert.Equal(t, domain.ChargeConfirmed, c.Status)
	assert.Eventually(t, func() bool {
		c, err := wiring.service.Get(insured, "acc-1")
		return err == nil && c.ActivationPublished()
	}, 5*time.Second, 20*time.Millisecond)

	// Decision D-16: a personalized offer activates its own terms, so one
	// without them is refused instead of falling back to the demonstration ones.
	err = wiring.starter.Start(ctx,
		domain.Acceptance{ID: "acc-2", OfferID: "off-0123456789abcdef", CustomerID: "cust-1", AcceptedAt: time.Now()},
		domain.Offer{ID: "off-0123456789abcdef", CustomerID: "cust-1", Premium: 98_400, Currency: "COP",
			Applicant: domain.Applicant{FullName: "Ana", DocumentID: "123", Email: "a@x.co"}})
	assert.ErrorIs(t, err, billing.ErrIncompleteTerms)
}
