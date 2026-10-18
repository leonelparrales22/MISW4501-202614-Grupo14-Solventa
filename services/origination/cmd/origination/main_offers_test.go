package main

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/profiling"
)

func TestLoadConfigOfferDefaults(t *testing.T) {
	cfg, err := loadConfig(env(requiredEnv()))

	require.NoError(t, err)
	assert.Empty(t, cfg.offers.databaseURL)
	assert.Equal(t, time.Minute, cfg.offers.requotePollInterval)
	assert.Equal(t, 50, cfg.offers.requoteBatch)
	assert.Equal(t, "http://localhost:4200", cfg.offers.linkBaseURL)
}

func TestLoadConfigOfferOverrides(t *testing.T) {
	m := requiredEnv()
	m["DATABASE_URL"] = "postgres://o@db/origination"
	m["OFFER_REQUOTE_POLL_INTERVAL_MS"] = "250"
	m["OFFER_REQUOTE_BATCH"] = "10"
	m["OFFER_LINK_BASE_URL"] = "https://app.solventa.test"

	cfg, err := loadConfig(env(m))

	require.NoError(t, err)
	assert.Equal(t, "postgres://o@db/origination", cfg.offers.databaseURL)
	assert.Equal(t, 250*time.Millisecond, cfg.offers.requotePollInterval)
	assert.Equal(t, 10, cfg.offers.requoteBatch)
	assert.Equal(t, "https://app.solventa.test", cfg.offers.linkBaseURL)
}

func TestLoadConfigRejectsInvalidOfferValues(t *testing.T) {
	for _, name := range []string{"OFFER_REQUOTE_POLL_INTERVAL_MS", "OFFER_REQUOTE_BATCH"} {
		for _, bad := range []string{"soon", "0", "-1"} {
			m := requiredEnv()
			m[name] = bad

			_, err := loadConfig(env(m))

			assert.ErrorContains(t, err, name, bad)
		}
	}
}

func TestNewOfferWiringWithoutDatabaseOrAuditKeepsOffersInMemoryAndFailsClosed(t *testing.T) {
	cfg, err := loadConfig(env(requiredEnv()))
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w, err := newOfferWiring(ctx, cfg, profiling.NewResolver(nil, nil, systemClock{}))

	require.NoError(t, err)
	require.NotNil(t, w.router)
	require.NotNil(t, w.acceptances)
	_, genErr := w.service.Generate(ctx, domain.EmbeddedOfferRequest{
		CustomerID: "c1", ConsentID: "cns-1", PartnerID: "p", ApplicationID: "a", DocumentID: "1020304050",
		FullName: "Ana Pérez", Email: "ana@example.com", BirthDate: "1985-03-14", InsuredAmount: 250_000_000, TermMonths: 120,
	})
	assert.ErrorContains(t, genErr, "consent", "without AUDIT_URL the consent cannot be verified")
}

func TestNewOfferWiringWithAuditStartsTheRequoteWorker(t *testing.T) {
	m := requiredEnv()
	m["AUDIT_URL"] = "http://audit.invalid:8080"
	m["OFFER_REQUOTE_POLL_INTERVAL_MS"] = "20"
	cfg, err := loadConfig(env(m))
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())

	w, err := newOfferWiring(ctx, cfg, profiling.NewResolver(nil, nil, systemClock{}))
	cancel()

	require.NoError(t, err)
	assert.NotNil(t, w.service)
}

func TestNewOfferWiringFailsWithAnUnreachableDatabase(t *testing.T) {
	m := requiredEnv()
	m["DATABASE_URL"] = "postgres://o:x@127.0.0.1:1/origination?sslmode=disable&connect_timeout=1"
	cfg, err := loadConfig(env(m))
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	_, err = newOfferWiring(ctx, cfg, profiling.NewResolver(nil, nil, systemClock{}))

	assert.Error(t, err)
}

func TestRequoteNotifierNeedsTheEmailProvider(t *testing.T) {
	assert.Nil(t, newRequoteNotifier(config{}))

	cfg := config{}
	cfg.contracts.emailURL = "http://email:8030"
	cfg.offers.linkBaseURL = "https://app"
	assert.NotNil(t, newRequoteNotifier(cfg))
}

func TestAcceptanceServiceSharesTheOfferRouterAndTheAcceptanceStoreItIsGiven(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	router := memory.NewOfferRouter(memory.NewOfferStub(systemClock{}), memory.NewPersonalizedOfferStore())
	store := memory.NewAcceptanceStore()
	cfg := acceptanceConfig{reviewScoreThreshold: memory.DefaultReviewScoreThreshold, offers: router, acceptanceStore: store}
	svc, _ := newAcceptanceService(ctx, cfg, memory.NewIssuanceStub(), nil)

	asInsured := security.WithClaims(ctx, security.Claims{Subject: "sub-a", Roles: []security.Role{security.RoleInsured}})
	a, created, err := svc.Accept(asInsured, "demo-active-shared", domain.ChannelWeb)

	require.NoError(t, err)
	assert.True(t, created)
	got, err := store.GetByOffer(ctx, "demo-active-shared")
	require.NoError(t, err)
	assert.Equal(t, a.ID, got.ID, "the requote worker reads the same acceptances")
}

// The human offer routes (read and renewal) must be mounted next to the
// acceptance ones; main wires them with the same verifier.
func TestMainMountsTheHumanOfferRoutes(t *testing.T) {
	src, err := os.ReadFile("main.go")
	require.NoError(t, err)

	assert.Contains(t, string(src), "httpadapter.WithOffers(offers.service, verifier)")
	assert.Contains(t, string(src), "httpadapter.WithEmbeddedOffers(offers.service)")
}
