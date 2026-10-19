package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/profiling"
)

func TestLoadConfigAuditDefaults(t *testing.T) {
	cfg, err := loadConfig(env(requiredEnv()))

	require.NoError(t, err)
	assert.Empty(t, cfg.audit.url, "without AUDIT_URL the caller still asserts the consent")
	assert.Equal(t, 300*time.Millisecond, cfg.audit.timeout)
}

func TestLoadConfigAuditOverrides(t *testing.T) {
	m := requiredEnv()
	m["AUDIT_URL"] = "http://audit:8080"
	m["AUDIT_TIMEOUT_MS"] = "150"

	cfg, err := loadConfig(env(m))

	require.NoError(t, err)
	assert.Equal(t, "http://audit:8080", cfg.audit.url)
	assert.Equal(t, 150*time.Millisecond, cfg.audit.timeout)
}

func TestLoadConfigRejectsAnInvalidAuditTimeout(t *testing.T) {
	for _, bad := range []string{"fast", "0", "-5"} {
		m := requiredEnv()
		m["AUDIT_TIMEOUT_MS"] = bad

		_, err := loadConfig(env(m))

		assert.ErrorContains(t, err, "AUDIT_TIMEOUT_MS", bad)
	}
}

func TestConsentCheckedResolverIsUnwrappedWithoutAuditURL(t *testing.T) {
	resolver := profiling.NewResolver(nil, nil, systemClock{})

	got := consentCheckedResolver(auditConfig{}, resolver)

	assert.Same(t, resolver, got)
}

func TestConsentCheckedResolverIsWrappedWithAuditURL(t *testing.T) {
	resolver := profiling.NewResolver(nil, nil, systemClock{})

	got := consentCheckedResolver(auditConfig{url: "http://audit:8080", timeout: time.Second}, resolver)

	assert.IsType(t, &profiling.ConsentCheckedResolver{}, got)
}
