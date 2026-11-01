// Package main is the entry point of the audit service.
//
// It always exposes GET /health (ADR-02). With DATABASE_URL it also owns the
// explicit consent of the customer (HU-W1.2-01): the partner channel records
// it and origination verifies it before it queries any source. The human
// evidence lookup and the insured's own consents (HU-M1.3-01) are mounted when
// the Cognito verification (AUTH_*) is set. With EVENTS_QUEUE_URL it consumes
// the CustomerLinked events that tell it who the holder of the consents is.
// HU-M1.3-02: the insured revokes a consent (with AUTH_*) and, with
// OPEN_FINANCE_URL, a worker notifies every entity within the deadline.
// HU-W4.2-02: the compliance portal (operations role) reads, revokes and
// retries the revocation of a consent with the same process (with AUTH_*).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	ofclient "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/openfinance"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/events"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/events/sqsconsumer"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/observability"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/adapters/messaging"
	openfinanceadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/adapters/openfinance"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/adapters/pdf"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/consents"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/decisions"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/partnercredentials"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/ports"
)

const serviceName = "audit"

// version is stamped on the telemetry; the build can set it with -ldflags.
var version = "dev"

// Environment variables of the service.
const (
	// envDatabaseURL turns the consent routes on.
	envDatabaseURL = "DATABASE_URL"
	// envEventsQueueURL is the SQS queue of audit (modules/messaging); with it
	// the CustomerLinked events are consumed (HU-M1.3-01).
	envEventsQueueURL = "EVENTS_QUEUE_URL"
	// envPartnerDisplayNames is the JSON catalog of the display names of the
	// entities, by partner code (decision D-2 a).
	envPartnerDisplayNames = "PARTNER_DISPLAY_NAMES"
	// envOpenFinanceURL is the Open Finance provider (the stub in local and
	// staging); with it the revocation notices are sent (HU-M1.3-02).
	envOpenFinanceURL = "OPEN_FINANCE_URL"
	// envOpenFinanceTimeoutMS is the hard cut of one call, in milliseconds.
	envOpenFinanceTimeoutMS = "OPEN_FINANCE_TIMEOUT_MS"
	// envNoticeDeadlineSeconds is how long the entities have to confirm a
	// revocation (RN-02: 300); the tests shorten it.
	envNoticeDeadlineSeconds = "REVOCATION_NOTICE_DEADLINE_SECONDS"
	// envNoticePollIntervalMS is how often the notice worker looks for due
	// notices, besides being woken up by each revocation.
	envNoticePollIntervalMS = "REVOCATION_NOTICE_POLL_INTERVAL_MS"
)

// Defaults of the revocation (HU-M1.3-02).
const (
	defaultOpenFinanceTimeout = 700 * time.Millisecond
	defaultNoticeDeadline     = 300 * time.Second
	defaultNoticePollInterval = time.Second
)

// noQueueNotice is logged when EVENTS_QUEUE_URL is empty.
const noQueueNotice = envEventsQueueURL + " is not set: account links (CustomerLinked) are not consumed; insured users get 403 holder_not_linked. " +
	"The partner credential changes (HU-W3.1-01) are not recorded either"

// noOpenFinanceNotice is logged when OPEN_FINANCE_URL is empty.
const noOpenFinanceNotice = envOpenFinanceURL + " is not set: revocations are recorded and block the queries, but no entity is " +
	"notified; their notices end as not confirmed when the deadline passes"

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

	// The background work (consumer, notice worker) stops with this context.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

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
		consentStore, eventStore := postgres.NewConsentStore(db), postgres.NewConsentEventStore(db)
		// Verify answers with the derived status: a revoked consent is not
		// active from the instant it is recorded (HU-M1.3-02 criterion 3).
		svc := consents.NewService(consentStore, systemClock{}, consents.WithEvents(eventStore))
		holders := consents.NewHolderService(consentStore, eventStore, postgres.NewHolderLinkStore(db),
			systemClock{}, cfg.partnerNames)
		noticeStore, revocationStore := postgres.NewNoticeStore(db), postgres.NewRevocationStore(db)
		revocations := consents.NewRevocationService(consents.RevocationDeps{
			Consents: consentStore, Events: eventStore, Revocations: revocationStore, Notices: noticeStore,
			Receipts: pdf.New(), Clock: systemClock{}, Names: cfg.partnerNames, NoticeDeadline: cfg.noticeDeadline,
		})
		worker, err := newNoticeWorker(cfg, noticeStore)
		if err != nil {
			log.Fatalf("could not set up the revocation notice worker: %v", err)
		}
		if worker != nil {
			revocations.OnNewNotices(worker.Wake)
			go worker.Run(ctx)
		}
		log.Printf("consents (HU-W1.2-01): append-only records in PostgreSQL; terms %s", svc.CurrentTerms().Version)
		var verifier security.Verifier
		if cfg.auth.enabled {
			jwks, err := security.NewJWKSVerifier(context.Background(), cfg.auth.config)
			if err != nil {
				log.Fatalf("could not load the user pool keys: %v", err)
			}
			verifier = jwks
		}
		opts = append(opts, consentOptions(svc, holders, revocations, verifier)...)
		compliance := consents.NewComplianceService(consents.ComplianceDeps{
			Revocations: revocations, Consents: consentStore, Events: eventStore, Records: revocationStore,
			List: postgres.NewComplianceListStore(db), Clock: systemClock{}, Names: cfg.partnerNames,
		})
		opts = append(opts, complianceOptions(compliance, verifier)...)
		opts = append(opts, decisionOptions(decisions.NewService(postgres.NewDecisionStore(db), systemClock{}), verifier)...)
		// HU-W3.1-01: the audit record of the partner credentials, fed by the
		// events of partners from the same queue.
		credentials := partnercredentials.NewService(postgres.NewPartnerCredentialStore(db))
		consumer, err := newEventConsumer(ctx, cfg, holders, credentials)
		if err != nil {
			log.Fatalf("could not set up the consumer of the audit queue: %v", err)
		}
		if consumer != nil {
			go consumer.Run(ctx)
		}
	} else {
		log.Printf("%s is not set: the consent routes (HU-W1.2-01, HU-M1.3-01) are not mounted and no event is consumed", envDatabaseURL)
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
	// Stop the background work and flush the telemetry before exiting:
	// log.Fatal would skip the deferred calls.
	cancel()
	_ = shutdownTelemetry(context.Background())
	log.Fatal(err)
}

// consentOptions mounts the consent routes: always the partner channel and the
// internal verification (HU-W1.2-01); with a verifier, also the human routes:
// the evidence lookup (operations, advisor), the insured's own consents
// (HU-M1.3-01) and their revocation with its receipt (HU-M1.3-02).
func consentOptions(svc *consents.Service, holders *consents.HolderService, revocations *consents.RevocationService,
	verifier security.Verifier) []httpadapter.Option {
	opts := []httpadapter.Option{httpadapter.WithConsents(svc)}
	if verifier == nil {
		log.Printf("%s is not set: the human consent routes (evidence lookup HU-W1.2-01, insured consents HU-M1.3-01, "+
			"revocation HU-M1.3-02) are not mounted", security.EnvIssuer)
		return opts
	}
	log.Printf("insured consents (HU-M1.3-01): GET /v1/consents and GET /v1/consents/{consent_id} for the insured role")
	log.Printf("revocation (HU-M1.3-02): POST /v1/consents/{consent_id}/revocations and GET /v1/consents/{consent_id}/revocation-receipt " +
		"for the insured role")
	return append(opts, httpadapter.WithConsentReads(svc, verifier), httpadapter.WithHolderConsents(holders, verifier),
		httpadapter.WithHolderRevocations(holders, revocations, verifier))
}

// complianceService is what the compliance portal uses: the revocation
// (HU-W4.2-02) and the list of consents (HU-W4.2-01).
type complianceService interface {
	httpadapter.ComplianceRevocationService
	httpadapter.ComplianceConsentLister
}

// complianceOptions mounts the routes of the compliance portal, human routes
// for the operations role: the revocation (HU-W4.2-02) and the list of
// consents (HU-W4.2-01). They need a verifier, so without AUTH_ISSUER they are
// not mounted.
func complianceOptions(svc complianceService, verifier security.Verifier) []httpadapter.Option {
	if verifier == nil {
		log.Printf("%s is not set: the revocation routes of the compliance portal (HU-W4.2-02) are not mounted, "+
			"nor the list of consents (HU-W4.2-01)", security.EnvIssuer)
		return nil
	}
	log.Printf("compliance revocation (HU-W4.2-02): GET /v1/audit/consents/{consent_id}/revocation, " +
		"POST /v1/audit/consents/{consent_id}/revocations and POST /v1/audit/consents/{consent_id}/revocation-retries for the operations role")
	log.Printf("compliance list (HU-W4.2-01): GET /v1/audit/consents for the operations role")
	return []httpadapter.Option{httpadapter.WithComplianceRevocations(svc, verifier), httpadapter.WithComplianceConsents(svc, verifier)}
}

// newNoticeWorker builds the worker that notifies the entities of each
// revocation through the Open Finance provider (HU-M1.3-02). It returns nil,
// and logs a notice, without OPEN_FINANCE_URL.
func newNoticeWorker(cfg config, store ports.NoticeStore) (*consents.NoticeWorker, error) {
	if cfg.openFinanceURL == "" {
		log.Print(noOpenFinanceNotice)
		return nil, nil
	}
	client := ofclient.NewClient(ofclient.Config{
		BaseURL: cfg.openFinanceURL, Timeout: cfg.openFinanceTimeout, Breaker: resilience.DefaultConfig(),
	})
	worker, err := consents.NewNoticeWorker(store, openfinanceadapter.NewNotifier(client),
		consents.NoticeWorkerConfig{PollInterval: cfg.noticePollInterval})
	if err != nil {
		return nil, err
	}
	log.Printf("revocation notices (HU-M1.3-02): to %s within %s, polling every %s", cfg.openFinanceURL, cfg.noticeDeadline,
		cfg.noticePollInterval)
	return worker, nil
}

// decisionOptions mounts the underwriting decision routes (HU-W4.1-01): always
// the internal recording origination calls; with a verifier, also the analyst's
// lookup (operations role).
func decisionOptions(svc *decisions.Service, verifier security.Verifier) []httpadapter.Option {
	opts := []httpadapter.Option{httpadapter.WithDecisionRecords(svc)}
	if verifier == nil {
		log.Printf("%s is not set: the decision lookup (HU-W4.1-01) is not mounted", security.EnvIssuer)
		return opts
	}
	log.Printf("decision audit (HU-W4.1-01): POST /v1/audit/decisions (internal) and GET /v1/audit/decisions/{offer_id} for operations")
	return append(opts, httpadapter.WithDecisionReads(svc, verifier))
}

// newEventConsumer builds the consumer of the audit queue with the
// credentials and the region of the environment (the task role and
// AWS_REGION in ECS; AWS_ENDPOINT_URL_SQS points to LocalStack in local runs).
// It returns nil, and logs a notice, without EVENTS_QUEUE_URL. Besides
// CustomerLinked (HU-M1.3-01) it records the partner credential changes of
// partners (HU-W3.1-01).
func newEventConsumer(ctx context.Context, cfg config, holders *consents.HolderService,
	credentials messaging.PartnerCredentialRecorder) (*sqsconsumer.Consumer, error) {
	if cfg.eventsQueueURL == "" {
		log.Print(noQueueNotice)
		return nil, nil
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("load the AWS configuration for %s: %w", envEventsQueueURL, err)
	}
	router := events.NewRouter()
	if err := router.Register(messaging.EventTypeCustomerLinked, messaging.NewCustomerLinkedHandler(holders)); err != nil {
		return nil, err
	}
	consumed := []string{messaging.EventTypeCustomerLinked}
	credentialHandler := messaging.NewPartnerCredentialsHandler(credentials)
	for _, t := range messaging.PartnerCredentialEventTypes {
		if err := router.Register(t, credentialHandler); err != nil {
			return nil, err
		}
		consumed = append(consumed, t)
	}
	consumer, err := sqsconsumer.New(sqs.NewFromConfig(awsCfg), cfg.eventsQueueURL, router, sqsconsumer.Options{})
	if err != nil {
		return nil, err
	}
	log.Printf("domain events: consuming %s from %s", strings.Join(consumed, ", "), cfg.eventsQueueURL)
	return consumer, nil
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
	// eventsQueueURL is the SQS queue of audit; empty leaves the consumer out.
	eventsQueueURL string
	// partnerNames are the display names of the entities (decision D-2 a).
	partnerNames domain.EntityNames
	// openFinanceURL is the provider the revocation notices go to; empty
	// leaves the notice worker out (HU-M1.3-02).
	openFinanceURL     string
	openFinanceTimeout time.Duration
	noticeDeadline     time.Duration
	noticePollInterval time.Duration
}

// authConfig is the Cognito verification of the human routes.
type authConfig struct {
	enabled bool
	config  security.Config
}

// partnerCodePattern is the shape of a partner code in the catalog.
var partnerCodePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// loadConfig reads the configuration from environment variables. Everything is
// optional: without DATABASE_URL only /health is served, and without
// AUTH_ISSUER the human routes are not mounted. When AUTH_ISSUER is set the
// rest of AUTH_* must be valid too, and PARTNER_DISPLAY_NAMES, when set, must
// be a JSON object of partner code to name.
func loadConfig(getenv func(string) string) (config, error) {
	cfg := config{port: getenv("PORT"), databaseURL: getenv(envDatabaseURL), eventsQueueURL: getenv(envEventsQueueURL)}
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
	names, err := parsePartnerNames(getenv(envPartnerDisplayNames))
	if err != nil {
		return config{}, err
	}
	cfg.partnerNames = names
	cfg.openFinanceURL = getenv(envOpenFinanceURL)
	for _, d := range []struct {
		name string
		unit time.Duration
		def  time.Duration
		dst  *time.Duration
	}{
		{envOpenFinanceTimeoutMS, time.Millisecond, defaultOpenFinanceTimeout, &cfg.openFinanceTimeout},
		{envNoticeDeadlineSeconds, time.Second, defaultNoticeDeadline, &cfg.noticeDeadline},
		{envNoticePollIntervalMS, time.Millisecond, defaultNoticePollInterval, &cfg.noticePollInterval},
	} {
		v, err := positiveDuration(getenv(d.name), d.unit, d.def)
		if err != nil {
			return config{}, fmt.Errorf("%s: %w", d.name, err)
		}
		*d.dst = v
	}
	return cfg, nil
}

// positiveDuration reads a positive integer count of unit; empty is def.
func positiveDuration(raw string, unit, def time.Duration) (time.Duration, error) {
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("must be a positive integer, got %q", raw)
	}
	return time.Duration(n) * unit, nil
}

// parsePartnerNames reads the catalog of display names; empty is an empty
// catalog (the codes are shown as they are).
func parsePartnerNames(raw string) (domain.EntityNames, error) {
	names := domain.EntityNames{}
	if raw == "" {
		return names, nil
	}
	if err := json.Unmarshal([]byte(raw), &names); err != nil {
		return nil, fmt.Errorf("%s must be a JSON object of partner code to display name: %w", envPartnerDisplayNames, err)
	}
	for code, name := range names {
		if !partnerCodePattern.MatchString(code) || name == "" {
			return nil, fmt.Errorf("%s has an invalid entry for %q: codes are letters, digits, '-' or '_' and names are not empty",
				envPartnerDisplayNames, code)
		}
	}
	return names, nil
}
