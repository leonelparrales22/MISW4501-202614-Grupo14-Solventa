// Package main is the entry point of the claims service.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/observability"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/filing"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

const serviceName = "claims"

// version is set at build time; "dev" when it is not.
var version = "dev"

// envDatabaseURL is the service's own database. Without it the filed claims
// live in memory and are lost when the service restarts.
const envDatabaseURL = "DATABASE_URL"

func main() {
	// JSON logs with correlation and trace ids, plus OpenTelemetry traces and
	// metrics; the standard log package is routed through the same handler.
	shutdownTelemetry, err := observability.Setup(context.Background(),
		observability.ConfigFromEnv(os.Getenv, serviceName, version))
	if err != nil {
		log.Fatalf("could not set up the observability: %v", err)
	}

	opts := []httpadapter.Option{httpadapter.WithObservability(serviceName)}

	// The filing hangs from the human routes: without the user pool there is
	// no way to tell whose claim it is.
	if os.Getenv(security.EnvIssuer) != "" {
		auth, err := security.ConfigFromEnv(os.Getenv)
		if err != nil {
			log.Fatalf("invalid configuration: %v", err)
		}
		verifier, err := security.NewJWKSVerifier(context.Background(), auth)
		if err != nil {
			log.Fatalf("could not set up the token verification: %v", err)
		}
		opts = append(opts, httpadapter.WithFilings(
			filing.NewService(claimStore(context.Background()), systemClock{}), verifier))
	} else {
		log.Printf("%s is not set: the filing of the claim (HU-M2.2-01) is not mounted", security.EnvIssuer)
	}

	addr := ":" + orDefault(os.Getenv("PORT"), "8080")
	srv := &http.Server{
		Addr:              addr,
		Handler:           httpadapter.NewRouter(opts...),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	log.Printf("claims listening on %s", addr)
	err = srv.ListenAndServe()
	// Flush the telemetry before exiting: log.Fatal would skip the deferred calls.
	_ = shutdownTelemetry(context.Background())
	log.Fatal(err)
}

// claimStore opens the database and applies the migrations, or keeps the
// claims in memory when there is no database configured.
func claimStore(ctx context.Context) ports.ClaimRepository {
	url := os.Getenv(envDatabaseURL)
	if url == "" {
		log.Printf("%s is not set: the filed claims live in memory and are lost when the service restarts",
			envDatabaseURL)
		return memory.NewClaimStore()
	}
	db, err := postgres.Open(ctx, url)
	if err != nil {
		log.Fatalf("could not open the database: %v", err)
	}
	if err := postgres.Migrate(ctx, db); err != nil {
		log.Fatalf("could not migrate the database: %v", err)
	}
	log.Print("claims: PostgreSQL")
	return postgres.NewClaimStore(db)
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

func orDefault(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}
