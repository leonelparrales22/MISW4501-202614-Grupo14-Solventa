// Package main is the entry point of the notifications service: the
// notification center of the insured and the pushes of the automatic
// parametric payments (HU-M3.1-01).
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	pushclient "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/push"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/events"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/events/sqsconsumer"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/observability"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/adapters/messaging"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/adapters/pushprovider"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/application"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/push"
)

const serviceName = "notifications"

var version = "dev"

// Environment variables of the service.
const (
	// envDatabaseURL is the service's own PostgreSQL (a secret, never logged).
	envDatabaseURL = "DATABASE_URL"
	// envEventsQueueURL is the SQS queue of the service, subscribed to the
	// claims topic.
	envEventsQueueURL = "EVENTS_QUEUE_URL"
	// envPushProviderURL is the push provider (stubs/push locally).
	envPushProviderURL       = "PUSH_PROVIDER_URL"
	envPushProviderTimeoutMS = "PUSH_PROVIDER_TIMEOUT_MS"
	envPushPollIntervalMS    = "PUSH_POLL_INTERVAL_MS"
	envPushRetryBaseMS       = "PUSH_RETRY_BASE_MS"
	envPushRetryMaxMS        = "PUSH_RETRY_MAX_MS"
)

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
		store := postgres.NewStore(db)
		worker, err := newPushWorker(cfg, store)
		if err != nil {
			log.Fatalf("could not set up the push worker: %v", err)
		}
		var wake func()
		if worker != nil {
			wake = worker.Wake
			go worker.Run(ctx)
		}
		svc, err := application.NewService(store, systemClock{}, wake)
		if err != nil {
			log.Fatalf("could not set up the notification center: %v", err)
		}
		opts = append(opts, centerOptions(ctx, cfg, svc)...)
		consumer, err := newEventConsumer(ctx, cfg, svc)
		if err != nil {
			log.Fatalf("could not set up the consumer of the notifications queue: %v", err)
		}
		if consumer != nil {
			go consumer.Run(ctx)
		}
	} else {
		log.Printf("%s is not set: the notification center (HU-M3.1-01) is not mounted and no event is consumed", envDatabaseURL)
	}

	srv := &http.Server{
		Addr:              ":" + cfg.port,
		Handler:           httpadapter.NewRouter(opts...),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	log.Printf("notifications listening on %s", srv.Addr)
	err = srv.ListenAndServe()
	cancel()
	// Flush the telemetry before exiting: log.Fatal would skip the deferred calls.
	_ = shutdownTelemetry(context.Background())
	log.Fatal(err)
}

// centerOptions mounts the human routes when the Cognito verification is
// configured.
func centerOptions(ctx context.Context, cfg config, svc httpadapter.CenterService) []httpadapter.Option {
	if !cfg.auth.enabled {
		log.Printf("%s is not set: the notification center routes (HU-M3.1-01) are not mounted", security.EnvIssuer)
		return nil
	}
	verifier, err := security.NewJWKSVerifier(ctx, cfg.auth.config)
	if err != nil {
		log.Fatalf("could not load the user pool keys: %v", err)
	}
	log.Printf("notification center (HU-M3.1-01): GET /v1/notifications, GET /v1/notifications/{id}, " +
		"POST /v1/notifications/{id}/read, POST /v1/notifications/read-all and PUT /v1/devices/{id} for the insured role")
	return []httpadapter.Option{httpadapter.WithCenter(svc, verifier)}
}

// newPushWorker builds the worker that sends the pushes, or nil without a
// push provider: then the notifications are recorded and wait in the center.
func newPushWorker(cfg config, store *postgres.Store) (*push.Worker, error) {
	if cfg.pushProviderURL == "" {
		log.Printf("%s is not set: no push is sent; the notifications wait in the center (HU-M3.1-01, criterion 7)",
			envPushProviderURL)
		return nil, nil
	}
	client := pushclient.NewClient(pushclient.Config{
		BaseURL: cfg.pushProviderURL, Timeout: cfg.pushProviderTimeout, Breaker: resilience.DefaultConfig(),
	})
	worker, err := push.NewWorker(store, pushprovider.NewSender(client), push.Config{
		PollInterval: cfg.pushPollInterval, RetryBase: cfg.pushRetryBase, RetryMax: cfg.pushRetryMax,
	})
	if err != nil {
		return nil, err
	}
	log.Printf("pushes (HU-M3.1-01): to %s, polling every %s, retries from %s up to %s for 24h",
		cfg.pushProviderURL, cfg.pushPollInterval, cfg.pushRetryBase, cfg.pushRetryMax)
	return worker, nil
}

// newEventConsumer builds the consumer of the notifications queue, or nil
// without EVENTS_QUEUE_URL.
func newEventConsumer(ctx context.Context, cfg config, svc messaging.PayoutRecorder) (*sqsconsumer.Consumer, error) {
	if cfg.eventsQueueURL == "" {
		log.Printf("%s is not set: the automatic payments (ParametricPayoutCompleted) are not consumed", envEventsQueueURL)
		return nil, nil
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("load the AWS configuration for %s: %w", envEventsQueueURL, err)
	}
	router, err := eventRouter(svc)
	if err != nil {
		return nil, err
	}
	consumer, err := sqsconsumer.New(sqs.NewFromConfig(awsCfg), cfg.eventsQueueURL, router, sqsconsumer.Options{})
	if err != nil {
		return nil, err
	}
	log.Printf("domain events: consuming %s from %s", messaging.EventTypeParametricPayoutCompleted, cfg.eventsQueueURL)
	return consumer, nil
}

// eventRouter routes the events of the notifications queue to their handlers.
func eventRouter(svc messaging.PayoutRecorder) (*events.Router, error) {
	router := events.NewRouter()
	if err := router.Register(messaging.EventTypeParametricPayoutCompleted, messaging.NewParametricPayoutHandler(svc)); err != nil {
		return nil, err
	}
	return router, nil
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

type config struct {
	port        string
	databaseURL string
	auth        authConfig
	// eventsQueueURL is the SQS queue of the service; empty leaves the
	// consumer out.
	eventsQueueURL string
	// pushProviderURL is where the pushes go; empty sends none.
	pushProviderURL     string
	pushProviderTimeout time.Duration
	pushPollInterval    time.Duration
	pushRetryBase       time.Duration
	pushRetryMax        time.Duration
}

// authConfig is the Cognito verification of the human routes.
type authConfig struct {
	enabled bool
	config  security.Config
}

// loadConfig reads the configuration from environment variables. Everything is
// optional: without DATABASE_URL only /health is served, without AUTH_ISSUER
// the human routes are not mounted, without EVENTS_QUEUE_URL no event is
// consumed and without PUSH_PROVIDER_URL no push is sent.
func loadConfig(getenv func(string) string) (config, error) {
	cfg := config{port: getenv("PORT"), databaseURL: getenv(envDatabaseURL), eventsQueueURL: getenv(envEventsQueueURL),
		pushProviderURL: getenv(envPushProviderURL)}
	if cfg.port == "" {
		cfg.port = "8080"
	}
	if getenv(security.EnvIssuer) != "" {
		auth, err := security.ConfigFromEnv(getenv)
		if err != nil {
			return config{}, fmt.Errorf("invalid %s configuration: %w", security.EnvIssuer, err)
		}
		cfg.auth = authConfig{enabled: true, config: auth}
	}
	for _, d := range []struct {
		name string
		def  time.Duration
		dst  *time.Duration
	}{
		{envPushProviderTimeoutMS, pushclient.DefaultTimeout, &cfg.pushProviderTimeout},
		{envPushPollIntervalMS, push.DefaultPollInterval, &cfg.pushPollInterval},
		{envPushRetryBaseMS, push.DefaultRetryBase, &cfg.pushRetryBase},
		{envPushRetryMaxMS, push.DefaultRetryMax, &cfg.pushRetryMax},
	} {
		v, err := positiveMillis(getenv(d.name), d.def)
		if err != nil {
			return config{}, fmt.Errorf("%s: %w", d.name, err)
		}
		*d.dst = v
	}
	if cfg.pushRetryMax < cfg.pushRetryBase {
		return config{}, fmt.Errorf("%s must not be lower than %s", envPushRetryMaxMS, envPushRetryBaseMS)
	}
	return cfg, nil
}

// positiveMillis reads a positive integer count of milliseconds; empty is def.
func positiveMillis(raw string, def time.Duration) (time.Duration, error) {
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("must be a positive integer, got %q", raw)
	}
	return time.Duration(n) * time.Millisecond, nil
}
