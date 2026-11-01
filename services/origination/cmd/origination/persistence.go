package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/outbox"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// Notices of where origination keeps its data (fix C5, decision D-2 a). There
// is a single switch, DATABASE_URL: everything goes to PostgreSQL together or
// everything stays in memory, never a mix.
const (
	storesInPostgres = "origination data in PostgreSQL: personalized and adjusted offers, acceptances, the advisor inbox and its decisions, customers, the advisor quotes and their formalizations, and the outbox of domain events (HU-W1.2-03, HU-W1.3-01, HU-W1.1-02, HU-M1.1-01, HU-M1.3-01, HU-W1.1-01, HU-W1.1-03)"
	storesInMemory   = envDatabaseURL + " is not set: personalized and adjusted offers, acceptances, the advisor inbox and its decisions, customers, the advisor quotes and their formalizations, and the outbox of domain events are kept in memory and lost on restart"
)

// stores are the stores of origination's own data.
type stores struct {
	// db is the single pool of the process (decision D-4 a), shared by every
	// store below and by the premium charge; nil without DATABASE_URL.
	db *sql.DB
	// demo serves the demonstration offers (demo-*), always in memory (#77 B).
	demo        *memory.OfferStub
	offers      ports.PersonalizedOfferStore
	adjusted    ports.AdjustedOfferStore
	acceptances ports.AcceptanceRepository
	reviews     ports.ReviewRepository
	customers   ports.CustomerRepository
	// quotes, formalizations and challenges are the advisor quotes and the
	// formalization of their purchase (HU-W1.1-01, HU-W1.1-03).
	quotes       ports.QuoteRepository
	formalized   ports.FormalizationStore
	consentCodes ports.ConsentChallengeStore
	// outbox holds the domain events written with the customers
	// (CustomerLinked, HU-M1.3-01); the relay publishes them.
	outbox outbox.Store
}

// newStores opens origination's stores. With a database URL it opens a single
// pool, applies the migrations once and builds every store over it: the
// personalized offers (HU-W1.2-03), the adjusted offers, the acceptances
// (HU-W1.3-01), the advisor inbox and its decisions (HU-W1.1-02) and the
// customers (HU-M1.1-01), which no longer depend on the payment gateway, and
// the outbox of their domain events (HU-M1.3-01). Without it every store is in
// memory and a single notice says so.
func newStores(ctx context.Context, databaseURL string, clock ports.Clock) (stores, error) {
	demo := memory.NewOfferStub(clock)
	if databaseURL == "" {
		log.Print(storesInMemory)
		customers := memory.NewCustomerStore()
		return stores{
			demo: demo, offers: memory.NewPersonalizedOfferStore(), adjusted: demo,
			acceptances: memory.NewAcceptanceStore(), reviews: memory.NewReviewQueueStub(), customers: customers,
			outbox: customers.Outbox(),
			quotes: memory.NewQuoteStore(), formalized: memory.NewFormalizationStore(), consentCodes: memory.NewConsentChallengeStore(),
		}, nil
	}
	db, err := postgres.Open(ctx, databaseURL)
	if err != nil {
		return stores{}, err
	}
	if err := postgres.Migrate(ctx, db); err != nil {
		_ = db.Close()
		return stores{}, fmt.Errorf("migrate origination's database: %w", err)
	}
	log.Print(storesInPostgres)
	return stores{
		db: db, demo: demo, offers: postgres.NewOfferStore(db), adjusted: postgres.NewAdjustedOfferStore(db),
		acceptances: postgres.NewAcceptanceStore(db), reviews: postgres.NewReviewStore(db), customers: postgres.NewCustomerStore(db),
		outbox: postgres.NewOutboxStore(db),
		quotes: postgres.NewQuoteStore(db), formalized: postgres.NewFormalizationStore(db), consentCodes: postgres.NewConsentChallengeStore(db),
	}, nil
}

// router serves every offer id to the acceptance, the advisor inbox and the
// charge: the demo offers from the stub, the adjusted offers from their store
// and the rest from the personalized offers.
func (s stores) router() *memory.OfferRouter {
	return memory.NewOfferRouterWithAdjusted(s.demo, s.offers, s.adjusted)
}
