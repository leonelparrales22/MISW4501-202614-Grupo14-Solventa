package main

import (
	"log"
	"time"

	corepolicies "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/policies"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/policycore"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/wallet"
)

// Variables of the policy core the wallet reads (HU-M1.2-01). They are the
// same names the claims service uses for the same dependency.
const (
	envPolicyCoreURL     = "POLICY_CORE_URL"
	envPolicyCoreTimeout = "POLICY_CORE_TIMEOUT_MS"
)

// policyCoreConfig is where the policy core is and how long a call may take.
// An empty url lists only the policies origination issued.
type policyCoreConfig struct {
	url     string
	timeout time.Duration
}

// loadPolicyCoreConfig reads the policy core configuration. The timeout is
// 700 ms unless POLICY_CORE_TIMEOUT_MS says otherwise.
func loadPolicyCoreConfig(getenv func(string) string, cfg *policyCoreConfig) error {
	cfg.url = getenv(envPolicyCoreURL)
	ms, err := intVar(getenv, envPolicyCoreTimeout, int(corepolicies.DefaultTimeout/time.Millisecond))
	if err != nil {
		return err
	}
	cfg.timeout = time.Duration(ms) * time.Millisecond
	return nil
}

// newPolicyPortfolio builds the policy core the wallet reads, or nil when
// POLICY_CORE_URL is not set.
func newPolicyPortfolio(cfg policyCoreConfig) ports.PolicyPortfolio {
	if cfg.url == "" {
		log.Printf("%s is not set: the wallet (HU-M1.2-01) lists only the policies origination issued", envPolicyCoreURL)
		return nil
	}
	log.Printf("wallet (HU-M1.2-01): policy core at %s, timeout %s", cfg.url, cfg.timeout)
	return policycore.NewPortfolio(corepolicies.NewClient(corepolicies.Config{
		BaseURL: cfg.url, Timeout: cfg.timeout, Breaker: resilience.DefaultConfig(),
	}))
}

// walletOptions mounts GET /v1/policies over the sources that are configured:
// the policies origination issued (renewals, nil without the premium charge)
// and the policy core (portfolio, nil without POLICY_CORE_URL). Without either
// nothing is mounted.
func walletOptions(renewals wallet.RenewalLister, portfolio ports.PolicyPortfolio,
	verifier security.Verifier) []httpadapter.Option {
	svc, err := wallet.NewService(renewals, portfolio, systemClock{})
	if err != nil {
		log.Printf("the wallet (HU-M1.2-01) is not mounted: %v", err)
		return nil
	}
	return []httpadapter.Option{httpadapter.WithPolicyWallet(svc, verifier)}
}
