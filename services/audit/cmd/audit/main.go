// Package main is the entry point of the audit service.
//
// It always exposes GET /health (ADR-02). With DATABASE_URL it also owns the
// explicit consent of the customer (HU-W1.2-01): the partner channel records
// it and origination verifies it before it queries any source. The human
// evidence lookup is mounted when the Cognito verification (AUTH_*) is set.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/observability"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/consents"
)

const serviceName = "audit"

// version is stamped on the telemetry; the build can set it with -ldflags.
var version = "dev"

// envDatabaseURL names the variable that turns the consent routes on.
const envDatabaseURL = "DATABASE_URL"

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

	// WithObservability must come first: chi only accepts middlewares before
	// the routes are registered.
	opts := []httpadapter.Option{httpadapter.WithObservability(serviceName)}
	if cfg.databaseURL != "" {
		db, err := postgres.Open(context.Background(), cfg.databaseURL)
		if err != nil {
			log.Fatalf("could not open the database: %v", err)
		}
		defer func() { _ = db.Close() }()
		if err := postgres.Migrate(context.Background(), db); err != nil {
			log.Fatalf("could not migrate the database: %v", err)
		}
		svc := consents.NewService(postgres.NewConsentStore(db), systemClock{})
		opts = append(opts, httpadapter.WithConsents(svc))
		log.Printf("consents (HU-W1.2-01): append-only records in PostgreSQL; terms %s", svc.CurrentTerms().Version)
		if cfg.auth.enabled {
			verifier, err := security.NewJWKSVerifier(context.Background(), cfg.auth.config)
			if err != nil {
				log.Fatalf("could not load the user pool keys: %v", err)
			}
			opts = append(opts, httpadapter.WithConsentReads(svc, verifier))
		} else {
			log.Printf("%s is not set: the consent evidence lookup (operations, advisor) is not mounted", security.EnvIssuer)
		}
	} else {
		log.Printf("%s is not set: the consent routes (HU-W1.2-01) are not mounted", envDatabaseURL)
	}

	srv := &http.Server{
		Addr:              ":" + cfg.port,
		Handler:           httpadapter.NewRouter(opts...),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	log.Printf("audit listening on %s", srv.Addr)
	err = srv.ListenAndServe()
	// Flush the telemetry before exiting: log.Fatal would skip the deferred calls.
	_ = shutdownTelemetry(context.Background())
	log.Fatal(err)
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

type config struct {
	port string
	// databaseURL is audit's own PostgreSQL (a secret: it comes from the
	// environment or Secrets Manager and is never logged). Empty leaves the
	// consent routes out.
	databaseURL string
	auth        authConfig
}

// authConfig is the Cognito verification of the human routes.
type authConfig struct {
	enabled bool
	config  security.Config
}

// loadConfig reads the configuration from environment variables. Everything is
// optional: without DATABASE_URL only /health is served, and without
// AUTH_ISSUER the human evidence lookup is not mounted. When AUTH_ISSUER is
// set the rest of AUTH_* must be valid too.
func loadConfig(getenv func(string) string) (config, error) {
	cfg := config{port: getenv("PORT"), databaseURL: getenv(envDatabaseURL)}
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
	return cfg, nil
}
