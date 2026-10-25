package main

import (
	"fmt"
	"strconv"
	"time"

	corepolicies "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/policies"
)

// Variables of the policy core the web filing asks (HU-W2.1-01).
const (
	envPolicyCoreURL     = "POLICY_CORE_URL"
	envPolicyCoreTimeout = "POLICY_CORE_TIMEOUT_MS"
)

// policyCoreConfig is where the policy core is and how long a call may take.
// An empty url leaves the web filing out (fail-closed: without the core
// nobody can tell which policies are the insured's).
type policyCoreConfig struct {
	url     string
	timeout time.Duration
}

// loadPolicyCoreConfig reads the policy core configuration. The timeout is
// 700 ms unless POLICY_CORE_TIMEOUT_MS says otherwise; it must be a positive
// number of milliseconds.
func loadPolicyCoreConfig(getenv func(string) string) (policyCoreConfig, error) {
	cfg := policyCoreConfig{url: getenv(envPolicyCoreURL), timeout: corepolicies.DefaultTimeout}
	if raw := getenv(envPolicyCoreTimeout); raw != "" {
		ms, err := strconv.Atoi(raw)
		if err != nil || ms <= 0 {
			return policyCoreConfig{}, fmt.Errorf("%s must be a positive number of milliseconds, got %q",
				envPolicyCoreTimeout, raw)
		}
		cfg.timeout = time.Duration(ms) * time.Millisecond
	}
	return cfg, nil
}
