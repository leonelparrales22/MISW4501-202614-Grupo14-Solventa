// Package main is the entry point of the origination service.
package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/kyc"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/opendata"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/openfinance"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/http"
	kycadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/kyc"
	odadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/opendata"
	ofadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/openfinance"
	redisadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/redis"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/profiling"
)

func main() {
	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		log.Fatalf("invalid configuration: %v", err)
	}

	redisClient := goredis.NewClient(&goredis.Options{Addr: cfg.redisAddr})
	defer func() { _ = redisClient.Close() }()

	resolver := profiling.NewResolver(
		newProfileSource(cfg, newHTTPClient(cfg.maxConnsPerHost)),
		redisadapter.NewProfileCache(redisClient, cfg.profileCacheTTL, 0),
		systemClock{},
	)

	srv := &http.Server{
		Addr:              ":" + cfg.port,
		Handler:           httpadapter.NewRouter(resolver),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	log.Printf("origination listening on %s", srv.Addr)
	log.Fatal(srv.ListenAndServe())
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

type config struct {
	port               string
	redisAddr          string
	openFinanceURL     string
	openFinanceTimeout time.Duration
	breaker            resilience.Config
	profileCacheTTL    time.Duration

	// Parallel profiling (HA-L01).
	openDataURL         string
	kycURL              string
	sourceBudget        time.Duration
	profilingDeadline   time.Duration
	maxConnsPerHost     int
	openFinanceRequired bool
}

// newHTTPClient builds the client shared by the external-source adapters: one
// pool of connections per host (EXP-L01 used 200). Each adapter bounds its own
// calls, so the client has no global timeout.
func newHTTPClient(maxConnsPerHost int) *http.Client {
	return &http.Client{Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          3 * maxConnsPerHost,
		MaxConnsPerHost:       maxConnsPerHost,
		MaxIdleConnsPerHost:   maxConnsPerHost,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}}
}

// newProfileSource builds the parallel profiling over the configured external
// sources, in priority order: Open Finance (always), Open Data and KYC (when
// their URL is set). Every source gets its own circuit breaker.
func newProfileSource(cfg config, httpClient *http.Client) *profiling.ParallelSource {
	sources := []profiling.NamedSource{{
		Name: domain.ExternalSourceOpenFinance,
		Source: ofadapter.NewSource(openfinance.NewClient(openfinance.Config{
			BaseURL: cfg.openFinanceURL, Timeout: cfg.openFinanceTimeout, Breaker: cfg.breaker, HTTPClient: httpClient,
		})),
	}}
	if cfg.openDataURL != "" {
		sources = append(sources, profiling.NamedSource{
			Name: domain.ExternalSourceOpenData,
			Source: odadapter.NewSource(opendata.NewClient(opendata.Config{
				BaseURL: cfg.openDataURL, Breaker: cfg.breaker, HTTPClient: httpClient,
			})),
		})
	} else {
		log.Printf("OPEN_DATA_URL is not set: Open Data is left out of the parallel profiling")
	}
	if cfg.kycURL != "" {
		sources = append(sources, profiling.NamedSource{
			Name: domain.ExternalSourceKYC,
			Source: kycadapter.NewSource(kyc.NewClient(kyc.Config{
				BaseURL: cfg.kycURL, Breaker: cfg.breaker, HTTPClient: httpClient,
			})),
		})
	} else {
		log.Printf("KYC_URL is not set: KYC is left out of the parallel profiling")
	}

	required := ""
	if cfg.openFinanceRequired {
		required = domain.ExternalSourceOpenFinance
	}
	log.Printf("parallel profiling: %d sources, budget=%s, deadline=%s, open finance required=%t",
		len(sources), cfg.sourceBudget, cfg.profilingDeadline, cfg.openFinanceRequired)
	return profiling.NewParallelSource(sources, profiling.ParallelConfig{
		Budget:   cfg.sourceBudget,
		Deadline: cfg.profilingDeadline,
		Required: required,
	})
}

// loadConfig reads the configuration from environment variables. REDIS_ADDR
// and OPEN_FINANCE_URL are required; the rest default to the values validated
// by experiment EC-D01 and the 30-day profile validity. The parallel profiling
// (HA-L01) defaults to a 120 ms budget per source, a 700 ms total cut, a pool
// of 200 connections per host and Open Finance as a required source (D-2).
func loadConfig(getenv func(string) string) (config, error) {
	def := resilience.DefaultConfig()
	cfg := config{
		port:           orDefault(getenv("PORT"), "8080"),
		redisAddr:      getenv("REDIS_ADDR"),
		openFinanceURL: getenv("OPEN_FINANCE_URL"),
	}
	if cfg.redisAddr == "" {
		return config{}, fmt.Errorf("REDIS_ADDR is required")
	}
	if cfg.openFinanceURL == "" {
		return config{}, fmt.Errorf("OPEN_FINANCE_URL is required")
	}

	var err error
	var timeoutMs, openMs, ttlHours int
	var failures, successes uint32
	if timeoutMs, err = intVar(getenv, "OPEN_FINANCE_TIMEOUT_MS", int(openfinance.DefaultTimeout/time.Millisecond)); err != nil {
		return config{}, err
	}
	if failures, err = uint32Var(getenv, "BREAKER_CONSECUTIVE_FAILURES", def.ConsecutiveFailures); err != nil {
		return config{}, err
	}
	if openMs, err = intVar(getenv, "BREAKER_OPEN_TIMEOUT_MS", int(def.OpenTimeout/time.Millisecond)); err != nil {
		return config{}, err
	}
	if successes, err = uint32Var(getenv, "BREAKER_HALF_OPEN_SUCCESSES", def.HalfOpenSuccesses); err != nil {
		return config{}, err
	}
	if ttlHours, err = intVar(getenv, "PROFILE_CACHE_TTL_HOURS", int(redisadapter.DefaultTTL/time.Hour)); err != nil {
		return config{}, err
	}

	cfg.openFinanceTimeout = time.Duration(timeoutMs) * time.Millisecond
	cfg.breaker = resilience.Config{
		ConsecutiveFailures: failures,
		OpenTimeout:         time.Duration(openMs) * time.Millisecond,
		HalfOpenSuccesses:   successes,
	}
	cfg.profileCacheTTL = time.Duration(ttlHours) * time.Hour

	if err := loadParallelConfig(getenv, &cfg); err != nil {
		return config{}, err
	}
	return cfg, nil
}

// loadParallelConfig reads the variables of the parallel profiling (HA-L01).
func loadParallelConfig(getenv func(string) string, cfg *config) error {
	cfg.openDataURL = getenv("OPEN_DATA_URL")
	cfg.kycURL = getenv("KYC_URL")

	var err error
	var budgetMs, deadlineMs int
	if budgetMs, err = intVar(getenv, "SOURCE_BUDGET_MS", int(profiling.DefaultSourceBudget/time.Millisecond)); err != nil {
		return err
	}
	if deadlineMs, err = intVar(getenv, "PROFILING_DEADLINE_MS", int(profiling.DefaultProfilingDeadline/time.Millisecond)); err != nil {
		return err
	}
	if cfg.maxConnsPerHost, err = intVar(getenv, "HTTP_MAX_CONNS_PER_HOST", 200); err != nil {
		return err
	}
	if cfg.openFinanceRequired, err = boolVar(getenv, "OPEN_FINANCE_REQUIRED", true); err != nil {
		return err
	}
	cfg.sourceBudget = time.Duration(budgetMs) * time.Millisecond
	cfg.profilingDeadline = time.Duration(deadlineMs) * time.Millisecond
	// D-3 (b): a call cut by the per-source budget is not a provider failure,
	// so the budget alone never opens a circuit.
	cfg.breaker.IgnoreCallerCancellation = true
	return nil
}

func boolVar(getenv func(string) string, name string, def bool) (bool, error) {
	raw := getenv(name)
	if raw == "" {
		return def, nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false, got %q", name, raw)
	}
	return v, nil
}

func intVar(getenv func(string) string, name string, def int) (int, error) {
	raw := getenv(name)
	if raw == "" {
		return def, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer, got %q", name, raw)
	}
	return v, nil
}

func uint32Var(getenv func(string) string, name string, def uint32) (uint32, error) {
	raw := getenv(name)
	if raw == "" {
		return def, nil
	}
	v, err := strconv.ParseUint(raw, 10, 32)
	if err != nil || v == 0 {
		return 0, fmt.Errorf("%s must be a positive integer, got %q", name, raw)
	}
	return uint32(v), nil
}

func orDefault(v, d string) string {
	if v != "" {
		return v
	}
	return d
}
