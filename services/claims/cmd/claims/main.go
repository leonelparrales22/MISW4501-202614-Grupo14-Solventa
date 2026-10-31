// Package main is the entry point of the claims service.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	corepolicies "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/policies"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/observability"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/pdf"
	policiesadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/policies"
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
		records, supports := claimStores(context.Background())
		// HU-M2.1-01: the evidence of the phone lives in the bucket of the
		// environment, and the filing checks every key against it.
		evidences := evidenceStore(context.Background())
		opts = append(opts,
			httpadapter.WithFilings(
				filing.NewService(records, evidences, systemClock{}), verifier),
			// HU-W2.1-01: the insured's claims of both channels, the detail
			// (with the evidence of the phone, when there is a store to sign
			// its reads) and the filing receipt.
			httpadapter.WithClaimReads(
				filing.NewQueryService(records, pdf.NewReceiptRenderer()).WithObjects(evidences), verifier))
		if evidences != nil {
			opts = append(opts,
				httpadapter.WithEvidence(evidence.NewService(evidences, systemClock{}), verifier))
		}
		opts = append(opts, webFilings(records, supports, evidences, verifier)...)
		// HU-W2.2-01: the triage gives every claim in evaluation its priority
		// from a sweep of the claims, and its routes read it.
		priorities, interval, err := startTriage(context.Background(), records, os.Getenv)
		if err != nil {
			log.Fatalf("invalid configuration: %v", err)
		}
		log.Printf("triage (HU-W2.2-01): sweep every %s, policy %s", interval, priorities.Policy().Version)
		opts = append(opts, httpadapter.WithTriage(priorities, verifier))
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

// claimStores opens the database once and applies the migrations, or keeps
// everything in one in-memory store when there is no database configured. The
// mobile filing (HU-M2.2-01) and the web filing (HU-W2.1-01) share the claims,
// the filing numbers and the filing keys: one store serves both.
func claimStores(ctx context.Context) (ports.ClaimRecords, ports.SupportStore) {
	url := os.Getenv(envDatabaseURL)
	if url == "" {
		log.Printf("%s is not set: the filed claims live in memory and are lost when the service restarts",
			envDatabaseURL)
		cases := memory.NewCaseStore()
		return cases, cases
	}
	db, err := postgres.Open(ctx, url)
	if err != nil {
		log.Fatalf("could not open the database: %v", err)
	}
	if err := postgres.Migrate(ctx, db); err != nil {
		log.Fatalf("could not migrate the database: %v", err)
	}
	log.Print("claims: PostgreSQL")
	return postgres.NewClaimRecordStore(db), postgres.NewSupportStore(db)
}

// webFilings mounts the web filing with supports (HU-W2.1-01) when the policy
// core and the object store are configured. Without the core nothing is
// mounted: nobody could tell which policies are the insured's (fail-closed).
// Without the store the supports have nowhere to go.
func webFilings(records ports.ClaimRecords, supports ports.SupportStore, objects ports.ObjectStore,
	verifier security.Verifier) []httpadapter.Option {
	cfg, err := loadPolicyCoreConfig(os.Getenv)
	if err != nil {
		log.Fatalf("invalid configuration: %v", err)
	}
	if cfg.url == "" {
		log.Printf("%s is not set: the web filing of the claim (HU-W2.1-01) is not mounted", envPolicyCoreURL)
		return nil
	}
	if objects == nil {
		log.Printf("%s is not set: the web filing of the claim (HU-W2.1-01) is not mounted, its supports have no store",
			envEvidenceBucket)
		return nil
	}
	core := corepolicies.NewClient(corepolicies.Config{
		BaseURL: cfg.url, Timeout: cfg.timeout, Breaker: resilience.DefaultConfig(),
	})
	directory := policiesadapter.NewDirectory(core)
	log.Printf("web filing (HU-W2.1-01): policy core at %s, timeout %s", cfg.url, cfg.timeout)
	return []httpadapter.Option{httpadapter.WithWebFilings(
		filing.NewWebService(records, supports, directory, systemClock{}),
		filing.NewSupportService(supports, objects, systemClock{}),
		verifier)}
}

// evidenceStore opens the bucket where the captured evidence and the supports
// of the web filing go. Without
// the bucket the evidence of HU-M2.1-01 is not mounted and a filing that
// declares evidence is refused, so the rest of the service keeps answering; a
// bucket that is configured and cannot be reached is a misconfiguration and
// the service does not start with it.
func evidenceStore(ctx context.Context) ports.ObjectStore {
	bucket := os.Getenv(envEvidenceBucket)
	if bucket == "" {
		log.Printf("%s is not set: the evidence of the claim (HU-M2.1-01) is not mounted",
			envEvidenceBucket)
		return nil
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
