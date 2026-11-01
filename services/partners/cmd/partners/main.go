// Package main is the entry point of the partners service.
//
// It always exposes GET /health (ADR-02). HU-W3.1-01: with DATABASE_URL and
// the Cognito verification (AUTH_*) it mounts the operations routes that
// register and approve the commercial partners and rotate their credentials.
// The credentials are API keys in a usage plan per partner of the API
// Gateway (GATEWAY_REST_API_ID, GATEWAY_STAGE_NAME); the credential events go
// to the partners topic through the outbox (EVENTS_TOPIC_ARN).
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	sdkapigateway "github.com/aws/aws-sdk-go-v2/service/apigateway"
	"github.com/aws/aws-sdk-go-v2/service/sns"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/events/snspublisher"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/observability"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/outbox"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/partners/internal/adapters/apigateway"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/partners/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/partners/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/partners/internal/credentials"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/partners/internal/onboarding"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/partners/internal/ports"
)

const serviceName = "partners"

// version is stamped on the telemetry; the build can set it with -ldflags.
var version = "dev"

// Environment variables of the service.
const (
	envDatabaseURL        = "DATABASE_URL"
	envEventsTopicARN     = "EVENTS_TOPIC_ARN"
	envOutboxPollInterval = "OUTBOX_POLL_INTERVAL_MS"
	envGatewayRestAPIID   = "GATEWAY_REST_API_ID"
	envGatewayStageName   = "GATEWAY_STAGE_NAME"
	envGracePeriodHours   = "CREDENTIAL_GRACE_PERIOD_HOURS"
	envRevocationPollMS   = "CREDENTIAL_REVOCATION_POLL_INTERVAL_MS"
	envEnvironment        = "ENVIRONMENT"
)

// Defaults.
const (
	defaultOutboxPollMS     = 1000
	defaultRevocationPollMS = 60000
	defaultEnvironment      = "local"
)

type config struct {
	port           string
	databaseURL    string
	auth           *security.Config
	topicARN       string
	outboxPoll     time.Duration
	gatewayAPIID   string
	gatewayStage   string
	graceHours     int
	revocationPoll time.Duration
	environment    string
}

// loadConfig reads the configuration from environment variables. Everything
// is optional; invalid values stop the service.
func loadConfig(getenv func(string) string) (config, error) {
	cfg := config{
		port: getenv("PORT"), databaseURL: getenv(envDatabaseURL), topicARN: getenv(envEventsTopicARN),
		gatewayAPIID: getenv(envGatewayRestAPIID), gatewayStage: getenv(envGatewayStageName), environment: getenv(envEnvironment),
	}
	if cfg.port == "" {
		cfg.port = "8080"
	}
	if cfg.environment == "" {
		cfg.environment = defaultEnvironment
	}
	if getenv(security.EnvIssuer) != "" {
		auth, err := security.ConfigFromEnv(getenv)
		if err != nil {
			return config{}, fmt.Errorf("invalid %s configuration: %w", security.EnvIssuer, err)
		}
		cfg.auth = &auth
	}
	if (cfg.gatewayAPIID == "") != (cfg.gatewayStage == "") {
		return config{}, fmt.Errorf("%s and %s must be set together", envGatewayRestAPIID, envGatewayStageName)
	}
	var err error
	if cfg.graceHours, err = positiveInt(getenv, envGracePeriodHours, credentials.DefaultGracePeriodHours); err != nil {
		return config{}, err
	}
	ms, err := positiveInt(getenv, envOutboxPollInterval, defaultOutboxPollMS)
	if err != nil {
		return config{}, err
	}
	cfg.outboxPoll = time.Duration(ms) * time.Millisecond
	if ms, err = positiveInt(getenv, envRevocationPollMS, defaultRevocationPollMS); err != nil {
		return config{}, err
	}
	cfg.revocationPoll = time.Duration(ms) * time.Millisecond
	return cfg, nil
}

func positiveInt(getenv func(string) string, name string, def int) (int, error) {
	raw := getenv(name)
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer, got %q", name, raw)
	}
	return n, nil
}

func main() {
	shutdownTelemetry, err := observability.Setup(context.Background(), observability.ConfigFromEnv(os.Getenv, serviceName, version))
	if err != nil {
		log.Fatalf("could not set up the observability: %v", err)
	}
	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		log.Fatalf("invalid configuration: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	opts := []httpadapter.Option{httpadapter.WithObservability(serviceName)}
	if cfg.databaseURL != "" {
		db, err := postgres.Open(ctx, cfg.databaseURL)
		if err != nil {
			log.Fatalf("could not open the database: %v", err)
		}
		defer func() { _ = db.Close() }()
		if err := postgres.Migrate(ctx, db); err != nil {
			log.Fatalf("could not migrate the database: %v", err)
		}
		partnerOpts, err := wire(ctx, cfg, db)
		if err != nil {
			log.Fatalf("could not set up the partners: %v", err)
		}
		opts = append(opts, partnerOpts...)
	} else {
		log.Printf("%s is not set: the partner routes (HU-W3.1-01) are not mounted", envDatabaseURL)
	}

	srv := &http.Server{
		Addr:              ":" + cfg.port,
		Handler:           httpadapter.NewRouter(opts...),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	log.Printf("partners listening on %s", srv.Addr)
	err = srv.ListenAndServe()
	cancel()
	_ = shutdownTelemetry(context.Background())
	log.Fatal(err)
}

// wire builds the use cases over the database and starts the background work
// (outbox relay and revocation worker). It returns the routes to mount.
func wire(ctx context.Context, cfg config, db *sql.DB) ([]httpadapter.Option, error) {
	gateway, err := newGateway(ctx, cfg)
	if err != nil {
		return nil, err
	}
	relay, err := newEventRelay(ctx, cfg, postgres.NewOutboxStore(db))
	if err != nil {
		return nil, err
	}
	if relay != nil {
		go relay.Run(ctx)
	}
	var verifier security.Verifier
	if cfg.auth != nil {
		jwks, err := security.NewJWKSVerifier(ctx, *cfg.auth)
		if err != nil {
			return nil, fmt.Errorf("load the user pool keys: %w", err)
		}
		verifier = jwks
	}
	return partnerOptions(ctx, cfg, postgres.NewPartnerStore(db), gateway, verifier)
}

// partnerOptions builds the use cases, starts the revocation worker when there
// is a gateway and returns the routes, which need a verifier.
func partnerOptions(ctx context.Context, cfg config, store ports.PartnerStore, gateway ports.Gateway,
	verifier security.Verifier) ([]httpadapter.Option, error) {
	issuer := credentials.NewIssuer(gateway, credentials.RandomGenerator{}, cfg.environment)
	onb := onboarding.NewService(store, issuer, systemClock{})
	rot, err := credentials.NewService(store, issuer, systemClock{}, cfg.graceHours)
	if err != nil {
		return nil, err
	}
	if gateway != nil {
		worker, err := credentials.NewWorker(rot, cfg.revocationPoll)
		if err != nil {
			return nil, err
		}
		go worker.Run(ctx)
		log.Printf("credential revocation worker (HU-W3.1-01): every %s", cfg.revocationPoll)
	}
	if verifier == nil {
		log.Printf("%s is not set: the partner routes (HU-W3.1-01) are not mounted", security.EnvIssuer)
		return nil, nil
	}
	log.Printf("partners (HU-W3.1-01): GET/POST /v1/partners, GET /v1/partners/{partner_id}, POST …/approval and " +
		"POST …/credential-rotations for the operations role")
	return []httpadapter.Option{httpadapter.WithPartners(onb, rot, verifier, cfg.environment)}, nil
}

// newGateway builds the API Gateway adapter with the credentials and region of
// the environment (the task role in ECS; AWS_ENDPOINT_URL_API_GATEWAY points
// to LocalStack in local runs). Without GATEWAY_REST_API_ID it returns nil and
// the approval and the rotation answer 503 gateway_unavailable.
func newGateway(ctx context.Context, cfg config) (ports.Gateway, error) {
	if cfg.gatewayAPIID == "" {
		log.Printf("%s is not set: partners cannot be approved nor rotate credentials (503 gateway_unavailable)", envGatewayRestAPIID)
		return nil, nil
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("load the AWS configuration for %s: %w", envGatewayRestAPIID, err)
	}
	gw, err := apigateway.New(sdkapigateway.NewFromConfig(awsCfg), cfg.gatewayAPIID, cfg.gatewayStage)
	if err != nil {
		return nil, err
	}
	log.Printf("credentials (HU-W3.1-01): usage plans and api keys on %s/%s", cfg.gatewayAPIID, cfg.gatewayStage)
	return gw, nil
}

// newEventRelay publishes the outbox to the partners topic. Without
// EVENTS_TOPIC_ARN it returns nil and the events stay in the outbox.
func newEventRelay(ctx context.Context, cfg config, store outbox.Store) (*outbox.Relay, error) {
	if cfg.topicARN == "" {
		log.Printf("%s is not set: the credential events stay in the outbox and are not published", envEventsTopicARN)
		return nil, nil
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("load the AWS configuration for %s: %w", envEventsTopicARN, err)
	}
	publisher, err := snspublisher.New(sns.NewFromConfig(awsCfg), cfg.topicARN)
	if err != nil {
		return nil, err
	}
	relayCfg := outbox.DefaultConfig()
	relayCfg.Interval = cfg.outboxPoll
	relay, err := outbox.NewRelay(store, publisher, time.Now, relayCfg)
	if err != nil {
		return nil, err
	}
	log.Printf("domain events: outbox relay to %s every %s", cfg.topicARN, cfg.outboxPoll)
	return relay, nil
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }
