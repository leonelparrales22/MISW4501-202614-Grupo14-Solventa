package main

import (
	"log"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
	auditadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/audit"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/profiling"
)

// auditConfig configures the verification of the explicit consent with the
// audit service (HU-W1.2-01).
type auditConfig struct {
	// url is the base URL of audit (AUDIT_URL). Empty: the caller still asserts
	// consent_active.
	url     string
	timeout time.Duration
}

// loadAuditConfig reads AUDIT_URL and AUDIT_TIMEOUT_MS (300 ms by default).
func loadAuditConfig(getenv func(string) string, cfg *auditConfig) error {
	cfg.url = getenv("AUDIT_URL")
	ms, err := intVar(getenv, "AUDIT_TIMEOUT_MS", int(auditadapter.DefaultTimeout/time.Millisecond))
	if err != nil {
		return err
	}
	cfg.timeout = time.Duration(ms) * time.Millisecond
	return nil
}

// consentCheckedResolver returns the resolver the profile routes use. With
// AUDIT_URL the consent is verified with audit before any source is queried
// and fails closed when audit does not answer (HU-W1.2-01 CA-3); without it
// the resolver is returned unwrapped and the caller still asserts
// consent_active.
func consentCheckedResolver(cfg auditConfig, resolver *profiling.Resolver) httpadapter.ProfileResolver {
	if cfg.url == "" {
		log.Printf("AUDIT_URL is not set: the caller still asserts consent_active (HU-W1.2-01 not enforced)")
		return resolver
	}
	verifier := auditadapter.NewVerifier(auditadapter.Config{
		BaseURL: cfg.url, Timeout: cfg.timeout, Breaker: resilience.DefaultConfig(),
	})
	log.Printf("consent (HU-W1.2-01): verified with audit at %s, timeout %s, fail-closed", cfg.url, cfg.timeout)
	return profiling.NewConsentCheckedResolver(resolver, verifier)
}
