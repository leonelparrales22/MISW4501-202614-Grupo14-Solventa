// Package main is the entry point of the origination service.
package main

import (
	"context"
	"fmt"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/email"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/kyc"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/opendata"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/openfinance"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/observability"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	emailadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/email"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/filestore"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/http"
	kycadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/kyc"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/links"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/memory"
	odadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/opendata"
	ofadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/openfinance"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/pdf"
	redisadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/redis"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/issuance"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/orchestrator"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/profiling"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/rating"
)

const serviceName = "origination"

func main() {
	// JSON logs with correlation and trace ids, plus OpenTelemetry traces and
	// metrics; the standard log package is routed through the same handler.
	shutdownTelemetry, err := observability.Setup(context.Background(), observability.ConfigFromEnv(os.Getenv, serviceName, version))
	if err != nil {
		log.Fatalf("could not set up the observability: %v", err)
	}

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
		resolverOptions(cfg)...,
	)

	// WithObservability must come first: chi only accepts middlewares before
	// the routes are registered.
	opts := []httpadapter.Option{httpadapter.WithObservability(serviceName)}
	if cfg.acceptances.enabled {
		verifier, err := security.NewJWKSVerifier(context.Background(), cfg.acceptances.auth)
		if err != nil {
			log.Fatalf("could not load the user pool keys: %v", err)
		}
		var starter ports.IssuanceStarter = memory.NewIssuanceStub()
		if cfg.contracts.enabled {
			contract, err := newContractWiring(context.Background(), cfg.contracts)
			if err != nil {
				log.Fatalf("could not set up the contractual document: %v", err)
			}
			starter = memory.NewActivationStub(contract.service, systemClock{})
			opts = append(opts, httpadapter.WithContractDocuments(contract.reader, verifier))
		} else {
			log.Printf("%s is not set: the contractual document and its notification (HU-W1.3-03) are not wired", envEmailURL)
		}
		opts = append(opts,
			httpadapter.WithAcceptances(newAcceptanceService(context.Background(), cfg.acceptances, starter), verifier),
			httpadapter.WithAssistedQuotes(newAssistedQuoteService(resolver), verifier))
	} else {
		log.Printf("%s is not set: the human routes (offer acceptance HU-W1.3-01, assisted quote HU-W1.1-01, contract documents HU-W1.3-03) are not mounted", security.EnvIssuer)
	}

	srv := &http.Server{
		Addr:              ":" + cfg.port,
		Handler:           httpadapter.NewRouter(resolver, opts...),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	log.Printf("origination listening on %s", srv.Addr)
	err = srv.ListenAndServe()
	// Flush the telemetry before exiting: log.Fatal would skip the deferred calls.
	_ = shutdownTelemetry(context.Background())
	log.Fatal(err)
}

// version is stamped on the telemetry; the build can set it with -ldflags.
var version = "dev"

// resolverOptions returns the options of the Resolver: the reuse of a complete
// cached profile (HU-W1.2-02) unless PROFILE_REUSE_ENABLED=false.
func resolverOptions(cfg config) []profiling.Option {
	if !cfg.profileReuse {
		log.Printf("PROFILE_REUSE_ENABLED=false: the sources are always queried first")
		return nil
	}
	return []profiling.Option{profiling.WithCachedProfileReuse()}
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

	// Provisional actuarial weights by source name (HU-W1.2-02).
	scoreWeights map[string]float64
	// profileReuse enables the reuse of a complete cached profile (HU-W1.2-02).
	profileReuse bool

	// Offer acceptance (HU-W1.3-01).
	acceptances acceptanceConfig

	// Contractual document and notification (HU-W1.3-03).
	contracts contractConfig
}

// acceptanceConfig configures the offer acceptance (HU-W1.3-01). Its routes
// are human routes, so they are mounted only when the Cognito verification
// (AUTH_*) is configured.
type acceptanceConfig struct {
	enabled       bool
	auth          security.Config
	processor     issuance.ProcessorConfig
	flakyFailures int
	// reviewScoreThreshold is the profile score above which the provisional
	// underwriting routes a normal offer to the advisor (decision #40).
	reviewScoreThreshold float64
}

// newAcceptanceService wires the offer acceptance over the in-memory adapters
// (PENDING: PostgreSQL, HU-W1.2-03 offers, the underwriting engine, the
// HU-W1.1-02 inbox and the HU-W1.3-02 issuance) and starts the processor that
// runs underwriting asynchronously and resumes failed acceptances.
func newAcceptanceService(ctx context.Context, cfg acceptanceConfig, starter ports.IssuanceStarter) *issuance.AcceptanceService {
	clock := systemClock{}
	offers := memory.NewOfferStub(clock)
	acceptances := memory.NewAcceptanceStore()
	processor := issuance.NewProcessor(offers, acceptances,
		memory.NewUnderwriterStub(clock, cfg.flakyFailures, cfg.reviewScoreThreshold), memory.NewReviewQueueStub(), starter,
		clock, cfg.processor)
	go processor.Run(ctx)
	log.Printf("offer acceptance: in-memory adapters (pending PostgreSQL and the real dependencies), poll every %s, retry from %s up to %s, profile scores above %g go to the advisor",
		cfg.processor.PollInterval, cfg.processor.RetryBase, cfg.processor.RetryMax, cfg.reviewScoreThreshold)
	return issuance.NewAcceptanceService(offers, acceptances, clock, processor.Notify)
}

// contractWiring is the contractual document of HU-W1.3-03, ready to serve.
type contractWiring struct {
	service *issuance.ContractService
	reader  *issuance.ContractReader
}

// newContractWiring wires the contractual issuance (HU-W1.3-03): the PDF
// generator, the document directory, the signed links, the e-mail provider
// and the in-memory repository and audit log (PENDING: PostgreSQL and the
// audit service). It starts the worker that issues the documents.
func newContractWiring(ctx context.Context, cfg contractConfig) (contractWiring, error) {
	store, err := filestore.New(cfg.documentsDir)
	if err != nil {
		return contractWiring{}, err
	}
	signer, err := links.NewSigner(cfg.linkSecret)
	if err != nil {
		return contractWiring{}, err
	}
	clock := systemClock{}
	repo, audit := memory.NewContractStore(), memory.NewIssuanceAuditLog()
	notifier := emailadapter.NewNotifier(email.NewClient(email.Config{
		BaseURL: cfg.emailURL, Timeout: cfg.emailTimeout, Breaker: resilience.DefaultConfig(),
	}))
	service := issuance.NewContractService(issuance.ContractDeps{
		Repo: repo, Generator: pdf.New(), Store: store, Signer: signer, Notifier: notifier, Audit: audit, Clock: clock,
	}, cfg.service)
	go service.Run(ctx)
	log.Printf("contractual document: PDF in %s, e-mail via %s, links valid %s, in-memory repository and audit log (pending PostgreSQL and the audit service)",
		store.Dir(), cfg.emailURL, cfg.service.LinkTTL)
	return contractWiring{
		service: service,
		reader:  issuance.NewContractReader(repo, store, audit, signer, clock),
	}, nil
}

// newAssistedQuoteService wires the advisor quote (HU-W1.1-01) over the
// in-memory customer and quote stores (PENDING: PostgreSQL), the shared
// profiling Resolver and the rating engine.
func newAssistedQuoteService(resolver *profiling.Resolver) *orchestrator.AssistedQuoteService {
	log.Printf("assisted quote: in-memory customer and quote stores (pending PostgreSQL), rating %s", rating.Version)
	return orchestrator.NewAssistedQuoteService(memory.NewCustomerStore(), memory.NewQuoteStore(), resolver, rating.New(), systemClock{})
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
	if b := cfg.breaker; b.FailureRatio > 0 {
		log.Printf("circuit breaker per source: opens at %.0f%% failed calls with at least %d calls in %s or after %d failures in a row, open %s, closes after %d probes",
			b.FailureRatio*100, b.MinRequests, b.Window, b.ConsecutiveFailureLimit, b.OpenTimeout, b.HalfOpenSuccesses)
	} else {
		log.Printf("circuit breaker per source: opens after %d consecutive failures, open %s, closes after %d probes",
			b.ConsecutiveFailures, b.OpenTimeout, b.HalfOpenSuccesses)
	}
	return profiling.NewParallelSource(sources, profiling.ParallelConfig{
		Budget:   cfg.sourceBudget,
		Deadline: cfg.profilingDeadline,
		Required: required,
		Weights:  cfg.scoreWeights,
	})
}

// loadConfig reads the configuration from environment variables. REDIS_ADDR
// and OPEN_FINANCE_URL are required; the rest default to the values validated
// by experiment EC-D01 and the 30-day profile validity. The parallel profiling
// (HA-L01) defaults to a 120 ms budget per source, a 700 ms total cut, a pool
// of 200 connections per host and Open Finance as a required source (D-2);
// the circuit breaker of each source opens by failure ratio (D-3 c).
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
	if err := loadAcceptanceConfig(getenv, &cfg.acceptances); err != nil {
		return config{}, err
	}
	if err := loadContractConfig(getenv, cfg.port, &cfg.contracts); err != nil {
		return config{}, err
	}
	return cfg, nil
}

// loadAcceptanceConfig reads the offer acceptance variables (HU-W1.3-01). The
// routes are enabled when AUTH_ISSUER is set; then the rest of AUTH_* must be
// valid too.
func loadAcceptanceConfig(getenv func(string) string, cfg *acceptanceConfig) error {
	if getenv(security.EnvIssuer) != "" {
		auth, err := security.ConfigFromEnv(getenv)
		if err != nil {
			return err
		}
		cfg.enabled, cfg.auth = true, auth
	}

	var err error
	var pollMs, attemptMs, baseMs, maxMs int
	if pollMs, err = intVar(getenv, "ACCEPTANCE_POLL_INTERVAL_MS", int(issuance.DefaultPollInterval/time.Millisecond)); err != nil {
		return err
	}
	if attemptMs, err = intVar(getenv, "ACCEPTANCE_ATTEMPT_TIMEOUT_MS", int(issuance.DefaultAttemptTimeout/time.Millisecond)); err != nil {
		return err
	}
	if baseMs, err = intVar(getenv, "ACCEPTANCE_RETRY_BASE_MS", int(issuance.DefaultRetryBase/time.Millisecond)); err != nil {
		return err
	}
	if maxMs, err = intVar(getenv, "ACCEPTANCE_RETRY_MAX_MS", int(issuance.DefaultRetryMax/time.Millisecond)); err != nil {
		return err
	}
	if maxMs < baseMs {
		return fmt.Errorf("ACCEPTANCE_RETRY_MAX_MS (%d) must not be lower than ACCEPTANCE_RETRY_BASE_MS (%d)", maxMs, baseMs)
	}
	if cfg.flakyFailures, err = nonNegativeIntVar(getenv, "UNDERWRITING_STUB_FLAKY_FAILURES", memory.DefaultFlakyFailures); err != nil {
		return err
	}
	if cfg.reviewScoreThreshold, err = floatVar(getenv, "UNDERWRITING_REVIEW_SCORE_THRESHOLD", memory.DefaultReviewScoreThreshold); err != nil {
		return err
	}
	cfg.processor = issuance.ProcessorConfig{
		PollInterval:   time.Duration(pollMs) * time.Millisecond,
		AttemptTimeout: time.Duration(attemptMs) * time.Millisecond,
		RetryBase:      time.Duration(baseMs) * time.Millisecond,
		RetryMax:       time.Duration(maxMs) * time.Millisecond,
	}
	return nil
}

// envEmailURL names the variable that turns the contractual document on.
const envEmailURL = "EMAIL_URL"

// contractConfig configures the contractual document and its notification
// (HU-W1.3-03). It is enabled when EMAIL_URL is set; then the signing secret
// and the document directory are required too.
type contractConfig struct {
	enabled      bool
	emailURL     string
	emailTimeout time.Duration
	documentsDir string
	linkSecret   string
	service      issuance.ContractConfig
}

// loadContractConfig reads the HU-W1.3-03 variables. publicPort is the port of
// the service, used for the default public URL of the document links.
func loadContractConfig(getenv func(string) string, publicPort string, cfg *contractConfig) error {
	cfg.emailURL = getenv(envEmailURL)
	if cfg.emailURL == "" {
		return nil
	}
	cfg.enabled = true
	cfg.documentsDir = getenv("DOCUMENTS_DIR")
	cfg.linkSecret = getenv("DOCUMENT_LINK_SECRET")
	if cfg.documentsDir == "" {
		return fmt.Errorf("DOCUMENTS_DIR is required when %s is set", envEmailURL)
	}
	if len(cfg.linkSecret) < links.MinSecretLength {
		return fmt.Errorf("DOCUMENT_LINK_SECRET (at least %d characters) is required when %s is set", links.MinSecretLength, envEmailURL)
	}

	var err error
	var pollMs, attemptMs, baseMs, maxMs, emailMs, linkHours int
	if pollMs, err = intVar(getenv, "CONTRACT_POLL_INTERVAL_MS", int(issuance.DefaultContractPollInterval/time.Millisecond)); err != nil {
		return err
	}
	if attemptMs, err = intVar(getenv, "CONTRACT_ATTEMPT_TIMEOUT_MS", int(issuance.DefaultContractAttemptTimeout/time.Millisecond)); err != nil {
		return err
	}
	if baseMs, err = intVar(getenv, "CONTRACT_RETRY_BASE_MS", int(issuance.DefaultContractRetryBase/time.Millisecond)); err != nil {
		return err
	}
	if maxMs, err = intVar(getenv, "CONTRACT_RETRY_MAX_MS", int(issuance.DefaultContractRetryMax/time.Millisecond)); err != nil {
		return err
	}
	if maxMs < baseMs {
		return fmt.Errorf("CONTRACT_RETRY_MAX_MS (%d) must not be lower than CONTRACT_RETRY_BASE_MS (%d)", maxMs, baseMs)
	}
	if cfg.service.MaxAttempts, err = intVar(getenv, "CONTRACT_MAX_ATTEMPTS", issuance.DefaultContractMaxAttempts); err != nil {
		return err
	}
	if emailMs, err = intVar(getenv, "EMAIL_TIMEOUT_MS", int(email.DefaultTimeout/time.Millisecond)); err != nil {
		return err
	}
	if linkHours, err = intVar(getenv, "DOCUMENT_LINK_TTL_HOURS", int(issuance.DefaultDocumentLinkTTL/time.Hour)); err != nil {
		return err
	}
	cfg.emailTimeout = time.Duration(emailMs) * time.Millisecond
	cfg.service.PollInterval = time.Duration(pollMs) * time.Millisecond
	cfg.service.AttemptTimeout = time.Duration(attemptMs) * time.Millisecond
	cfg.service.RetryBase = time.Duration(baseMs) * time.Millisecond
	cfg.service.RetryMax = time.Duration(maxMs) * time.Millisecond
	cfg.service.LinkTTL = time.Duration(linkHours) * time.Hour
	cfg.service.PublicBaseURL = orDefault(getenv("DOCUMENTS_PUBLIC_BASE_URL"), "http://localhost:"+publicPort)
	return nil
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
	if err := loadScoreWeights(getenv, cfg); err != nil {
		return err
	}
	if cfg.profileReuse, err = boolVar(getenv, "PROFILE_REUSE_ENABLED", true); err != nil {
		return err
	}
	return loadBreakerRatio(getenv, &cfg.breaker)
}

// loadScoreWeights reads the provisional actuarial weights of each source
// (HU-W1.2-02); they are relative, so they need not add up to 1, and a weight
// of 0 leaves the source out of the score.
func loadScoreWeights(getenv func(string) string, cfg *config) error {
	cfg.scoreWeights = profiling.DefaultWeights()
	for source, name := range map[string]string{
		domain.ExternalSourceOpenFinance: "SCORE_WEIGHT_OPEN_FINANCE",
		domain.ExternalSourceOpenData:    "SCORE_WEIGHT_OPEN_DATA",
		domain.ExternalSourceKYC:         "SCORE_WEIGHT_KYC",
	} {
		w, err := ratioVar(getenv, name, cfg.scoreWeights[source])
		if err != nil {
			return err
		}
		cfg.scoreWeights[source] = w
	}
	return nil
}

// Defaults of the failure-ratio mode of the circuit breaker (decision D-3 c,
// adjusted after the validation of HA-L01).
const (
	defaultBreakerFailureRatio = 0.8
	// defaultBreakerMinRequests keeps a degraded but alive source from
	// reaching the ratio by chance in a window with few calls.
	defaultBreakerMinRequests = 50
	// defaultBreakerFailuresInARow is the safety net of the failure-ratio
	// mode: a source that hangs is isolated even when the traffic is too low
	// to reach BREAKER_MIN_REQUESTS calls within one window.
	defaultBreakerFailuresInARow = 30
)

// loadBreakerRatio reads the failure-ratio mode of the circuit breaker of each
// source (HA-L01, decision D-3 c). A call cut by the per-source budget counts
// as a failure, and the circuit opens when at least BREAKER_FAILURE_RATIO of
// the calls failed, with at least BREAKER_MIN_REQUESTS calls within a
// BREAKER_WINDOW_MS window, or after BREAKER_CONSECUTIVE_FAILURES failures in
// a row (30 by default in this mode, counted across windows): a source that is
// persistently slow or down is isolated with any traffic, while the normal
// latency tail and a degraded but alive source do not open the circuit.
// BREAKER_FAILURE_RATIO=0 goes back to the consecutive failures of EC-D01
// (BREAKER_CONSECUTIVE_FAILURES, 3 by default, read by loadConfig).
func loadBreakerRatio(getenv func(string) string, breaker *resilience.Config) error {
	ratio, err := ratioVar(getenv, "BREAKER_FAILURE_RATIO", defaultBreakerFailureRatio)
	if err != nil {
		return err
	}
	minRequests, err := uint32Var(getenv, "BREAKER_MIN_REQUESTS", defaultBreakerMinRequests)
	if err != nil {
		return err
	}
	windowMs, err := intVar(getenv, "BREAKER_WINDOW_MS", int(resilience.DefaultWindow/time.Millisecond))
	if err != nil {
		return err
	}
	breaker.FailureRatio = ratio
	breaker.MinRequests = minRequests
	breaker.Window = time.Duration(windowMs) * time.Millisecond
	if ratio > 0 {
		breaker.ConsecutiveFailureLimit = defaultBreakerFailuresInARow
		if getenv("BREAKER_CONSECUTIVE_FAILURES") != "" {
			// Already validated by loadConfig.
			breaker.ConsecutiveFailureLimit = breaker.ConsecutiveFailures
		}
	}
	return nil
}

func ratioVar(getenv func(string) string, name string, def float64) (float64, error) {
	raw := getenv(name)
	if raw == "" {
		return def, nil
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(v) || v < 0 || v > 1 {
		return 0, fmt.Errorf("%s must be a number between 0 and 1, got %q", name, raw)
	}
	return v, nil
}

// floatVar reads a number greater than 0 and at most 1, such as a profile
// score threshold.
func floatVar(getenv func(string) string, name string, def float64) (float64, error) {
	raw := getenv(name)
	if raw == "" {
		return def, nil
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(v) || v <= 0 || v > 1 {
		return 0, fmt.Errorf("%s must be a number greater than 0 and at most 1, got %q", name, raw)
	}
	return v, nil
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

func nonNegativeIntVar(getenv func(string) string, name string, def int) (int, error) {
	raw := getenv(name)
	if raw == "" {
		return def, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 0 {
		return 0, fmt.Errorf("%s must be a non-negative integer, got %q", name, raw)
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
