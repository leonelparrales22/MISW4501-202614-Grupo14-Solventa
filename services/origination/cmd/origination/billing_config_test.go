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
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

func billingEnv() map[string]string {
	m := contractEnv()
	m["PAYMENT_GATEWAY_URL"] = "http://payment-gateway:9040"
	m["DATABASE_URL"] = "postgres://origination@postgres:5432/origination"
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

	noEmail := requiredEnv()
	noEmail["PAYMENT_GATEWAY_URL"] = "http://payment-gateway:9040"
	noEmail["DATABASE_URL"] = "postgres://origination@postgres:5432/origination"
	_, err = loadConfig(env(noEmail))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "EMAIL_URL is required", "decision D-16 b: a paid policy always gets its document")
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
	terms, err := demoCoverageTerms(domain.Offer{})
	require.NoError(t, err)
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

func TestNewBillingFailsWithoutADatabase(t *testing.T) {
	cfg, err := loadConfig(env(billingEnv()))
	require.NoError(t, err)
	cfg.billing.databaseURL = "postgres://nobody:nothing@127.0.0.1:1/none?sslmode=disable&connect_timeout=1"

	_, err = newBilling(context.Background(), cfg.billing, recordingConsumer())
	assert.Error(t, err)
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

	wiring, err := newBilling(ctx, cfg.billing, sink)
	require.NoError(t, err)
	defer func() { _ = wiring.db.Close() }()

	require.NoError(t, wiring.starter.Start(ctx,
		domain.Acceptance{ID: "acc-1", OfferID: "demo-active-1", CustomerID: "cust-1", AcceptedAt: time.Now()},
		domain.Offer{ID: "demo-active-1", CustomerID: "cust-1", Premium: 89_500, Currency: "COP",
			Applicant: domain.Applicant{FullName: "Ana", DocumentID: "123", Email: "a@x.co"}}))
	require.Eventually(t, func() bool { return len(sink.received()) == 1 }, 10*time.Second, 20*time.Millisecond)
	ev := sink.received()[0]
	assert.Equal(t, "pol-acc-1", ev.PolicyID)
	assert.NoError(t, ev.Validate())

	caller := security.WithClaims(ctx, security.Claims{Subject: "cust-1", Roles: []security.Role{security.RoleInsured}})
	c, err := wiring.service.Get(caller, "acc-1")
	require.NoError(t, err)
	assert.Equal(t, domain.ChargeConfirmed, c.Status)
	assert.Eventually(t, func() bool {
		c, err := wiring.service.Get(caller, "acc-1")
		return err == nil && c.ActivationPublished()
	}, 5*time.Second, 20*time.Millisecond)
}
