package postgres_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

var quoteAt = time.Date(2026, 10, 25, 15, 0, 0, 123_456_000, time.UTC)

func sampleQuote(id string) domain.Quote {
	return domain.Quote{
		ID: id, CustomerID: "cus-1", AdvisorID: "adv-1",
		Applicant: domain.Applicant{FullName: "Laura Gómez", DocumentID: "1020304050", Email: "laura@example.com"},
		BirthDate: time.Date(1985, 3, 2, 0, 0, 0, 0, time.UTC), InsuredAmount: 200_000_000, TermMonths: 120,
		Premium: 98_450.5, Currency: "COP",
		Coverages: []domain.Coverage{
			{Code: domain.CoverageDeath, InsuredAmount: 200_000_000}, {Code: domain.CoverageDisability, InsuredAmount: 100_000_000},
		},
		RatingVersion: "rating-1", Mode: domain.ModeProvisional, ProfileSource: domain.SourceCache, ProfileScore: 0.875,
		ProfileCategory: "standard", ProfileDate: quoteAt.Add(-time.Hour), UnavailableSource: "open_finance",
		CreatedAt: quoteAt, ExpiresAt: quoteAt.Add(domain.QuoteValidity),
	}
}

func TestQuoteStoreKeepsTheQuoteAsItWasQuoted(t *testing.T) {
	requireOfferDocker(t)
	store := postgres.NewQuoteStore(newDB(t))
	ctx := context.Background()
	q := sampleQuote("q-1")

	require.NoError(t, store.Create(ctx, q))
	got, err := store.Get(ctx, "q-1")

	require.NoError(t, err)
	assert.Equal(t, q, got)
	_, err = store.Get(ctx, "nope")
	assert.ErrorIs(t, err, ports.ErrQuoteNotFound)
	_, err = store.Get(ctx, "")
	assert.ErrorIs(t, err, ports.ErrQuoteNotFound)
	_, err = store.Get(ctx, "a\x00b")
	assert.ErrorIs(t, err, ports.ErrQuoteNotFound, "a NUL byte is a missing quote, not a server error")
}

func TestAQuoteIsImmutable(t *testing.T) {
	requireOfferDocker(t)
	db := newDB(t)
	require.NoError(t, postgres.NewQuoteStore(db).Create(context.Background(), sampleQuote("q-1")))

	_, err := db.Exec(`UPDATE assisted_quote SET premium_minor = 1`)
	assert.ErrorContains(t, err, "append-only")
	_, err = db.Exec(`DELETE FROM assisted_quote`)
	assert.ErrorContains(t, err, "append-only")
}

func TestFormalizationIsRecordedOncePerQuoteAndIsAppendOnly(t *testing.T) {
	requireOfferDocker(t)
	db := newDB(t)
	ctx := context.Background()
	require.NoError(t, postgres.NewQuoteStore(db).Create(ctx, sampleQuote("q-1")))
	store := postgres.NewFormalizationStore(db)
	f := domain.Formalization{
		ID: "frm-q-1", QuoteID: "q-1", OfferID: "off-q-q-1", CustomerID: "cus-1", AdvisorID: "adv-1", AdvisorEmail: "laura@example.com",
		Consent:      domain.ConsentAcceptance{Method: domain.ConsentRecordedCall, AcceptedAt: quoteAt.Add(30 * time.Second), Reference: "GRAB-2026-88910"},
		FormalizedAt: quoteAt.Add(time.Minute),
	}

	stored, created, err := store.Create(ctx, f)
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, f, stored)

	other := f
	other.ID, other.AdvisorID = "frm-other", "adv-2"
	stored, created, err = store.Create(ctx, other)
	require.NoError(t, err)
	assert.False(t, created, "CA-6: the second formalization of the quote is not recorded")
	assert.Equal(t, f, stored, "it gets the winner")

	_, err = store.GetByQuote(ctx, "nope")
	assert.ErrorIs(t, err, ports.ErrFormalizationNotFound)
	_, err = db.Exec(`UPDATE formalization SET advisor_id = 'x'`)
	assert.ErrorContains(t, err, "append-only")
	_, err = db.Exec(`DELETE FROM formalization`)
	assert.ErrorContains(t, err, "append-only")
}

func TestFormalizationNeedsTheReferenceOfARecordedCallButNotOfTheCode(t *testing.T) {
	requireOfferDocker(t)
	db := newDB(t)
	ctx := context.Background()
	require.NoError(t, postgres.NewQuoteStore(db).Create(ctx, sampleQuote("q-1")))
	require.NoError(t, postgres.NewQuoteStore(db).Create(ctx, sampleQuote("q-2")))
	store := postgres.NewFormalizationStore(db)
	formalization := func(quote string, c domain.ConsentAcceptance) domain.Formalization {
		return domain.Formalization{ID: "frm-" + quote, QuoteID: quote, OfferID: "off-" + quote, CustomerID: "cus-1", AdvisorID: "adv-1",
			Consent: c, FormalizedAt: quoteAt}
	}

	_, _, err := store.Create(ctx, formalization("q-1", domain.ConsentAcceptance{Method: domain.ConsentRemoteSignature, AcceptedAt: quoteAt}))
	assert.Error(t, err, "a signature without its reference is not a proof")

	stored, created, err := store.Create(ctx, formalization("q-2", domain.ConsentAcceptance{Method: domain.ConsentConfirmationCode, AcceptedAt: quoteAt}))
	require.NoError(t, err)
	assert.True(t, created)
	assert.Empty(t, stored.Consent.Reference)
}

func TestConcurrentFormalizationsOfTheSameQuoteHaveASingleWinner(t *testing.T) {
	requireOfferDocker(t)
	db := newDB(t)
	ctx := context.Background()
	require.NoError(t, postgres.NewQuoteStore(db).Create(ctx, sampleQuote("q-1")))
	store := postgres.NewFormalizationStore(db)

	const advisors = 10
	var wg sync.WaitGroup
	createdBy := make([]bool, advisors)
	winners := make([]string, advisors)
	start := make(chan struct{})
	for i := 0; i < advisors; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			id := "adv-" + string(rune('a'+i))
			got, created, err := store.Create(ctx, domain.Formalization{
				ID: "frm-q-1", QuoteID: "q-1", OfferID: "off-q-q-1", CustomerID: "cus-1", AdvisorID: id,
				Consent:      domain.ConsentAcceptance{Method: domain.ConsentConfirmationCode, AcceptedAt: quoteAt},
				FormalizedAt: quoteAt,
			})
			assert.NoError(t, err)
			createdBy[i], winners[i] = created, got.AdvisorID
		}()
	}
	close(start)
	wg.Wait()

	count := 0
	for _, c := range createdBy {
		if c {
			count++
		}
	}
	assert.Equal(t, 1, count, "CA-6: exactly one advisor formalizes")
	for _, w := range winners {
		assert.Equal(t, winners[0], w, "everyone sees the same winner")
	}
}

func TestConsentChallengeKeepsTheCountersAcrossNewCodes(t *testing.T) {
	requireOfferDocker(t)
	db := newDB(t)
	ctx := context.Background()
	require.NoError(t, postgres.NewQuoteStore(db).Create(ctx, sampleQuote("q-1")))
	store := postgres.NewConsentChallengeStore(db)
	challenge := func(hash string) domain.ConsentChallenge {
		return domain.ConsentChallenge{QuoteID: "q-1", AdvisorID: "adv-1", CodeHash: hash, CreatedAt: quoteAt, ExpiresAt: quoteAt.Add(domain.ConsentCodeTTL)}
	}

	_, err := store.Get(ctx, "q-1")
	assert.ErrorIs(t, err, ports.ErrConsentChallengeNotFound)
	_, err = store.RegisterAttempt(ctx, "q-1")
	assert.ErrorIs(t, err, ports.ErrConsentChallengeNotFound)

	first, err := store.Issue(ctx, challenge("h1"))
	require.NoError(t, err)
	assert.Equal(t, 1, first.Sends)
	attempts, err := store.RegisterAttempt(ctx, "q-1")
	require.NoError(t, err)
	assert.Equal(t, 1, attempts)

	second, err := store.Issue(ctx, challenge("h2"))
	require.NoError(t, err)
	assert.Equal(t, 2, second.Sends)
	assert.Equal(t, 1, second.Attempts, "a new code does not reset the attempts")
	got, err := store.Get(ctx, "q-1")
	require.NoError(t, err)
	assert.Equal(t, "h2", got.CodeHash, "the new code replaces the previous one")
	assert.Equal(t, quoteAt.Add(domain.ConsentCodeTTL), got.ExpiresAt)
}

func TestConsentChallengeStopsAtTheLimitOfCodes(t *testing.T) {
	requireOfferDocker(t)
	db := newDB(t)
	ctx := context.Background()
	require.NoError(t, postgres.NewQuoteStore(db).Create(ctx, sampleQuote("q-1")))
	store := postgres.NewConsentChallengeStore(db)
	c := domain.ConsentChallenge{QuoteID: "q-1", AdvisorID: "adv-1", CodeHash: "h", CreatedAt: quoteAt, ExpiresAt: quoteAt.Add(time.Minute)}

	for i := 0; i < domain.MaxConsentCodeSends; i++ {
		_, err := store.Issue(ctx, c)
		require.NoError(t, err)
	}
	_, err := store.Issue(ctx, c)

	assert.ErrorIs(t, err, ports.ErrConsentCodeLimit)
}

func TestAcceptanceStoreKeepsTheAdvisorChannel(t *testing.T) {
	requireOfferDocker(t)
	store := postgres.NewAcceptanceStore(newDB(t))
	ctx := context.Background()

	_, _, err := store.Create(ctx, domain.Acceptance{
		ID: "acc-1", OfferID: "off-q-q-1", CustomerID: "sub-1", Channel: domain.ChannelAdvisor, AcceptedAt: quoteAt,
		Status: domain.AcceptanceRegistered, NextAttemptAt: quoteAt, UpdatedAt: quoteAt,
	})
	require.NoError(t, err)

	got, err := store.Get(ctx, "acc-1")
	require.NoError(t, err)
	assert.Equal(t, domain.ChannelAdvisor, got.Channel)
}
