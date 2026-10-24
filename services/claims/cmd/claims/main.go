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
	s3adapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/s3"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/evidence"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/filing"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
)

const serviceName = "claims"

// version is set at build time; "dev" when it is not.
var version = "dev"

// envDatabaseURL is the service's own database. Without it the filed claims
// live in memory and are lost when the service restarts.
const envDatabaseURL = "DATABASE_URL"

// Variables of the evidence store (HU-M2.1-01): the bucket of the environment
// and, only for a local run, the endpoint of an S3 emulator.
const (
	envEvidenceBucket = "EVIDENCE_BUCKET"
	envEvidenceURL    = "EVIDENCE_ENDPOINT_URL"
)

// evidenceCheckTimeout bounds the check of the evidence bucket at start.
const evidenceCheckTimeout = 10 * time.Second

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
		store := evidenceStore(context.Background())
		opts = append(opts,
			httpadapter.WithFilings(
				filing.NewService(claimStore(context.Background()), store, systemClock{}), verifier),
			httpadapter.WithEvidence(evidence.NewService(store, systemClock{}), verifier))
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

// evidenceStore opens the bucket where the captured evidence goes. The
// filing checks every key against it, so the service does not start without
// it: a case that names evidence nobody can open is not a filing.
func evidenceStore(ctx context.Context) ports.EvidenceStore {
	bucket := os.Getenv(envEvidenceBucket)
	if bucket == "" {
		log.Fatalf("%s is required when %s is set: the evidence of the claim is kept there",
			envEvidenceBucket, security.EnvIssuer)
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		log.Fatalf("could not load the AWS configuration for %s: %v", envEvidenceBucket, err)
	}
	client := awss3.NewFromConfig(cfg, func(o *awss3.Options) {
		// Only a local run points somewhere else; an emulator addresses the
		// bucket by path, not by subdomain.
		if endpoint := os.Getenv(envEvidenceURL); endpoint != "" {
			o.BaseEndpoint = &endpoint
			o.UsePathStyle = true
		}
	})
	store, err := s3adapter.New(client, bucket)
	if err != nil {
		log.Fatalf("could not open the evidence store: %v", err)
	}
	checkCtx, cancel := context.WithTimeout(ctx, evidenceCheckTimeout)
	defer cancel()
	if err := store.Check(checkCtx); err != nil {
		log.Fatalf("could not reach the evidence store: %v", err)
	}
	log.Printf("evidence: s3://%s", store.Bucket())
	return store
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

func orDefault(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}
