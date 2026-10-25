package main

import (
	"context"
	"log"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/email"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
	auditadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/audit"
	emailadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/email"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/orchestrator"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/profiling"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/rating"
)

// defaultOfferLinkBaseURL is where the e-mail links to the new offer point when
// OFFER_LINK_BASE_URL is not set: the development server of the web front end.
const defaultOfferLinkBaseURL = "http://localhost:4200"

// offerConfig configures the personalized offer (HU-W1.2-03).
type offerConfig struct {
	// databaseURL (DATABASE_URL) persists the offers; without it they live in
	// memory and are lost on restart.
	databaseURL string
	// requotePollInterval and requoteBatch tune the worker that replaces the
	// provisional offers when the external source is back.
	requotePollInterval time.Duration
	requoteBatch        int
	// linkBaseURL is the address of the web front end, for the e-mail links.
	linkBaseURL string
}

// loadOfferConfig reads OFFER_REQUOTE_POLL_INTERVAL_MS (60 s by default),
// OFFER_REQUOTE_BATCH (50) and OFFER_LINK_BASE_URL.
func loadOfferConfig(getenv func(string) string, cfg *offerConfig) error {
	cfg.databaseURL = getenv(envDatabaseURL)
	pollMs, err := intVar(getenv, "OFFER_REQUOTE_POLL_INTERVAL_MS", int(orchestrator.DefaultRequotePollInterval/time.Millisecond))
	if err != nil {
		return err
	}
	if cfg.requoteBatch, err = intVar(getenv, "OFFER_REQUOTE_BATCH", orchestrator.DefaultRequoteBatch); err != nil {
		return err
	}
	cfg.requotePollInterval = time.Duration(pollMs) * time.Millisecond
	cfg.linkBaseURL = orDefault(getenv("OFFER_LINK_BASE_URL"), defaultOfferLinkBaseURL)
	return nil
}

// offerWiring is the personalized offer (HU-W1.2-03), ready to serve.
type offerWiring struct {
	// router serves every offer id to the acceptance, the advisor inbox and the
	// charge: the demo offers from the stub, the adjusted offers from their
	// store and the rest from the personalized offers.
	router  *memory.OfferRouter
	service *orchestrator.OfferService
	// acceptances is the store the acceptance service writes and the requote
	// worker reads; reviews is the advisor inbox over the same database.
	acceptances ports.AcceptanceRepository
	reviews     ports.ReviewRepository
}

// newOfferWiring builds the personalized offer service over origination's
// stores (newStores: PostgreSQL with DATABASE_URL, memory otherwise). It starts
// the requote worker when the consent can be verified (AUDIT_URL), since a
// provisional offer must not be replaced for a customer whose consent is
// unknown.
func newOfferWiring(ctx context.Context, cfg config, resolver *profiling.Resolver, st stores) offerWiring {
	clock := systemClock{}
	store := st.offers
	router := st.router()
	acceptances := st.acceptances

	var consents ports.ConsentVerifier
	var decisions ports.DecisionRecorder
	if cfg.audit.url != "" {
		decisions = auditadapter.NewRecorder(auditadapter.Config{
			BaseURL: cfg.audit.url, Timeout: cfg.audit.timeout, Breaker: resilience.DefaultConfig(),
		})
		consents = auditadapter.NewVerifier(auditadapter.Config{
			BaseURL: cfg.audit.url, Timeout: cfg.audit.timeout, Breaker: resilience.DefaultConfig(),
		})
	} else {
		log.Printf("AUDIT_URL is not set: no personalized offer can be generated, because its consent cannot be verified (HU-W1.2-03, fail-closed)")
	}
	rater := rating.New()
	service := orchestrator.NewOfferService(orchestrator.OfferDeps{
		Store: store, Offers: router, Consents: consents, Profiles: resolver, Rater: rater, Clock: clock, Decisions: decisions,
	})

	if consents != nil {
		worker := orchestrator.NewRequoteWorker(orchestrator.RequoteDeps{
			Store: store, Acceptances: acceptances, Consents: consents, Profiles: resolver, Rater: rater,
			Notifier: newRequoteNotifier(cfg), Clock: clock, Decisions: decisions,
		}, orchestrator.RequoteConfig{PollInterval: cfg.offers.requotePollInterval, Batch: cfg.offers.requoteBatch})
		go worker.Run(ctx)
		log.Printf("offer requote: provisional offers are re-examined every %s (batch %d)", cfg.offers.requotePollInterval, cfg.offers.requoteBatch)
	} else {
		log.Printf("AUDIT_URL is not set: the requote of provisional offers is not running (HU-W1.2-03)")
	}
	return offerWiring{router: router, service: service, acceptances: acceptances, reviews: st.reviews}
}

// newRequoteNotifier returns the e-mail notice of the requote, or nil without
// EMAIL_URL (the replacement still happens; the customer sees it when they
// return to the flow).
func newRequoteNotifier(cfg config) ports.RequoteNotifier {
	if cfg.contracts.emailURL == "" {
		log.Printf("%s is not set: the requote e-mails (HU-W1.2-03) are not sent", envEmailURL)
		return nil
	}
	client := email.NewClient(email.Config{
		BaseURL: cfg.contracts.emailURL, Timeout: cfg.contracts.emailTimeout, Breaker: resilience.DefaultConfig(),
	})
	return emailadapter.NewRequoteNotifier(client, cfg.offers.linkBaseURL)
}
