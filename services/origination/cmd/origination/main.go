// Package main is the entry point of the origination service.
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/openfinance"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/http"
	ofadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/openfinance"
	redisadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/redis"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/profiling"
)

func main() {
	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		log.Fatalf("invalid configuration: %v", err)
	}

	redisClient := goredis.NewClient(&goredis.Options{Addr: cfg.redisAddr})
	defer func() { _ = redisClient.Close() }()

	ofClient := openfinance.NewClient(openfinance.Config{
		BaseURL: cfg.openFinanceURL,
		Timeout: cfg.openFinanceTimeout,
		Breaker: cfg.breaker,
	})
	resolver := profiling.NewResolver(
		ofadapter.NewSource(ofClient),
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
}

// loadConfig reads the configuration from environment variables. REDIS_ADDR
// and OPEN_FINANCE_URL are required; the rest default to the values validated
// by experiment EC-D01 and the 30-day profile validity.
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
	return cfg, nil
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
