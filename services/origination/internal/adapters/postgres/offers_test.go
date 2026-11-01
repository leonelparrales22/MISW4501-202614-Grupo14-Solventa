package postgres_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// requireOfferDocker fails the test when REQUIRE_DOCKER=1 and Docker is not
// healthy, and skips it otherwise, so the certification run never passes
// without the tests that need Docker.
func requireOfferDocker(t *testing.T) {
	t.Helper()
	provider, err := testcontainers.NewDockerProvider()
	if err == nil {
		defer func() { _ = provider.Close() }()
		err = provider.Health(context.Background())
	}
	if err == nil {
		return
	}
	if os.Getenv("REQUIRE_DOCKER") == "1" {
		t.Fatalf("Docker is required (REQUIRE_DOCKER=1) but is not healthy: %v", err)
	}
	t.Skipf("Docker is not available: %v", err)
}

func newOfferStore(t *testing.T) (*postgres.OfferStore, *sql.DB) {
	t.Helper()
	requireOfferDocker(t)
	db := newDB(t)
	return postgres.NewOfferStore(db), db
}

var offerAt = time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)

func storedOffer(id string, mode domain.OfferMode, created time.Time) domain.PersonalizedOffer {
	o := domain.PersonalizedOffer{
		ID: id, CustomerID: "cust-1", Channel: domain.OfferChannelEmbedded, QuoteID: "", ConsentID: "cns-1",
		PartnerID: "partner-1", ApplicationID: "app-1",
		Applicant:  domain.Applicant{FullName: "Ana María Pérez", DocumentID: "1020304050", Email: "ana@example.com"},
		BirthDate:  time.Date(1985, 3, 14, 0, 0, 0, 0, time.UTC),
		AgeAtQuote: 41,
		Terms: domain.CoverageTerms{InsuredAmount: 250_000_000, TermMonths: 120, Coverages: []domain.Coverage{
			{Code: domain.CoverageDeath, InsuredAmount: 250_000_000},
			{Code: domain.CoverageDisability, InsuredAmount: 250_000_000},
		}},
		Premium: 137_600, Currency: "COP", Conditions: []string{"validity_24h", "monthly_premium"},
		Mode: mode, ProfileSource: domain.SourceExternal,
		Profile: domain.ProfileSnapshot{
			Score: 0.4, Category: "standard", CalculatedAt: created.Add(-time.Minute), Partial: true,
			Sources: []domain.SourceOutcome{
				{Source: "open_finance", Status: domain.SourceStatusOK, Latency: 80 * time.Millisecond,
					ConsultedAt: time.Date(2026, 10, 17, 12, 0, 0, 120_000_000, time.UTC), Value: &domain.SourceValue{Score: 0.82, Category: "preferred"}},
				{Source: "kyc", Status: domain.SourceStatusTimeout, Latency: 700 * time.Millisecond},
			},
		},
		Rules:        []domain.AppliedRule{{Code: "base_monthly_rate", Value: 0.0003}, {Code: "rounding_step", Value: 100}},
		RaterVersion: "v1", CreatedAt: created, ExpiresAt: created.Add(domain.OfferValidity),
	}
	if mode == domain.ModeProvisional {
		o.ProfileSource = domain.SourceCache
		o.ProvisionalReason = domain.ProvisionalCachedProfile
		o.ProfileDate = created.Add(-48 * time.Hour)
		o.UnavailableSource = "open_finance"
	}
	return o
}

func TestOfferStoreRoundTripsTheWholeOffer(t *testing.T) {
	s, _ := newOfferStore(t)
	want := storedOffer("off-1", domain.ModeProvisional, offerAt)
	want.QuoteID = "quote-9"

	stored, created, err := s.Create(context.Background(), want)

	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, want, stored, "profile copy, rules, terms and version are kept exactly")
	got, err := s.Get(context.Background(), "off-1")
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestOfferStoreCreateIsIdempotentAndNeverOverwrites(t *testing.T) {
	s, _ := newOfferStore(t)
	first := storedOffer("off-1", domain.ModeNormal, offerAt)
	_, _, err := s.Create(context.Background(), first)
	require.NoError(t, err)
	other := first
	other.Premium = 1

	stored, created, err := s.Create(context.Background(), other)

	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, first.Premium, stored.Premium)
}

func TestOfferStoreGetUnknown(t *testing.T) {
	s, _ := newOfferStore(t)

	_, err := s.Get(context.Background(), "off-nope")

	assert.ErrorIs(t, err, ports.ErrOfferNotFound)
}

func TestOfferTableRejectsUpdateDeleteAndTruncate(t *testing.T) {
	s, db := newOfferStore(t)
	_, _, err := s.Create(context.Background(), storedOffer("off-1", domain.ModeNormal, offerAt))
	require.NoError(t, err)
	_, err = s.Claim(context.Background(), "off-1", "sub-a")
	require.NoError(t, err)

	for _, stmt := range []string{
		"UPDATE offer SET premium_minor = 1",
		"DELETE FROM offer",
		"TRUNCATE offer, offer_owner", // passes the foreign key check; the trigger must stop it
		"UPDATE offer_owner SET owner_id = 'sub-evil'",
		"DELETE FROM offer_owner",
	} {
		_, err := db.ExecContext(context.Background(), stmt)
		require.Error(t, err, stmt)
		assert.Contains(t, err.Error(), "append-only", stmt)
	}
	_, err = db.ExecContext(context.Background(), "TRUNCATE offer")
	assert.Error(t, err, "also refused by the foreign key of offer_owner")
	got, _ := s.Get(context.Background(), "off-1")
	assert.Equal(t, 137_600.0, got.Premium)
	assert.Equal(t, "sub-a", got.OwnerID)
}

func TestOfferStoreClaimKeepsTheFirstOwner(t *testing.T) {
	s, _ := newOfferStore(t)
	_, _, _ = s.Create(context.Background(), storedOffer("off-1", domain.ModeNormal, offerAt))

	a, err := s.Claim(context.Background(), "off-1", "sub-a")
	require.NoError(t, err)
	b, err := s.Claim(context.Background(), "off-1", "sub-b")
	require.NoError(t, err)

	assert.Equal(t, "sub-a", a.OwnerID)
	assert.Equal(t, "sub-a", b.OwnerID)
}

func TestOfferStoreClaimOfAnUnknownOffer(t *testing.T) {
	s, _ := newOfferStore(t)

	_, err := s.Claim(context.Background(), "off-nope", "sub-a")

	assert.ErrorIs(t, err, ports.ErrOfferNotFound)
}

func TestOfferStoreConcurrentClaimsHaveASingleWinner(t *testing.T) {
	s, _ := newOfferStore(t)
	_, _, _ = s.Create(context.Background(), storedOffer("off-1", domain.ModeNormal, offerAt))

	var wg sync.WaitGroup
	owners := make([]string, 16)
	for i := range owners {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o, err := s.Claim(context.Background(), "off-1", fmt.Sprintf("sub-%d", i))
			assert.NoError(t, err)
			owners[i] = o.OwnerID
		}()
	}
	wg.Wait()

	for _, o := range owners {
		assert.Equal(t, owners[0], o)
		assert.NotEmpty(t, o)
	}
}

func TestOfferStoreSupersedeLinksTheOffersAndInheritsTheOwner(t *testing.T) {
	s, _ := newOfferStore(t)
	ctx := context.Background()
	_, _, _ = s.Create(ctx, storedOffer("off-1", domain.ModeProvisional, offerAt))
	_, _ = s.Claim(ctx, "off-1", "sub-a")
	repl := storedOffer("off-2", domain.ModeNormal, offerAt.Add(time.Hour))

	created, err := s.Supersede(ctx, "off-1", repl)

	require.NoError(t, err)
	assert.True(t, created)
	old, _ := s.Get(ctx, "off-1")
	assert.Equal(t, "off-2", old.SupersededBy)
	assert.Equal(t, repl.CreatedAt, old.SupersededAt)
	assert.Equal(t, domain.OfferSuperseded, old.Status(offerAt.Add(time.Hour)))
	assert.True(t, old.AsOffer().IsExpired(offerAt.Add(time.Hour)))
	got, _ := s.Get(ctx, "off-2")
	assert.Equal(t, "off-1", got.Supersedes)
	assert.Equal(t, "sub-a", got.OwnerID)
}

func TestOfferStoreConcurrentSupersedesCreateASingleReplacement(t *testing.T) {
	s, db := newOfferStore(t)
	ctx := context.Background()
	_, _, _ = s.Create(ctx, storedOffer("off-1", domain.ModeProvisional, offerAt))

	var wg sync.WaitGroup
	var created atomic.Int32
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := s.Supersede(ctx, "off-1", storedOffer(fmt.Sprintf("off-new-%d", i), domain.ModeNormal, offerAt.Add(time.Hour)))
			assert.NoError(t, err)
			if ok {
				created.Add(1)
			}
		}()
	}
	wg.Wait()

	assert.EqualValues(t, 1, created.Load())
	var n int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM offer WHERE supersedes = 'off-1'`).Scan(&n))
	assert.Equal(t, 1, n)
	var total int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM offer`).Scan(&total))
	assert.Equal(t, 2, total, "the losers left nothing behind")
}

func TestOfferStoreSupersedeOfAnUnknownOffer(t *testing.T) {
	s, _ := newOfferStore(t)

	_, err := s.Supersede(context.Background(), "off-nope", storedOffer("off-2", domain.ModeNormal, offerAt))

	assert.ErrorIs(t, err, ports.ErrOfferNotFound)
}

func TestOfferStoreRenewReturnsTheExistingRenewalOnTheSecondCall(t *testing.T) {
	s, _ := newOfferStore(t)
	ctx := context.Background()
	_, _, _ = s.Create(ctx, storedOffer("off-1", domain.ModeNormal, offerAt))
	_, _ = s.Claim(ctx, "off-1", "sub-a")

	first, created, err := s.Renew(ctx, "off-1", storedOffer("off-2", domain.ModeNormal, offerAt.Add(25*time.Hour)))
	require.NoError(t, err)
	require.True(t, created)
	again, created, err := s.Renew(ctx, "off-1", storedOffer("off-3", domain.ModeNormal, offerAt.Add(26*time.Hour)))

	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, "off-2", again.ID)
	assert.Equal(t, "off-1", first.Renews)
	assert.Equal(t, "sub-a", first.OwnerID)
	old, _ := s.Get(ctx, "off-1")
	assert.Equal(t, "off-2", old.RenewedBy)
}

func TestOfferStoreConcurrentRenewalsConvergeOnOneOffer(t *testing.T) {
	s, _ := newOfferStore(t)
	ctx := context.Background()
	_, _, _ = s.Create(ctx, storedOffer("off-1", domain.ModeNormal, offerAt))

	var wg sync.WaitGroup
	ids := make([]string, 10)
	var created atomic.Int32
	for i := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o, ok, err := s.Renew(ctx, "off-1", storedOffer(fmt.Sprintf("off-r-%d", i), domain.ModeNormal, offerAt.Add(25*time.Hour)))
			assert.NoError(t, err)
			ids[i] = o.ID
			if ok {
				created.Add(1)
			}
		}()
	}
	wg.Wait()

	assert.EqualValues(t, 1, created.Load())
	for _, id := range ids {
		assert.Equal(t, ids[0], id)
	}
}

func TestOfferStoreRenewOfAnUnknownOffer(t *testing.T) {
	s, _ := newOfferStore(t)

	_, _, err := s.Renew(context.Background(), "off-nope", storedOffer("off-2", domain.ModeNormal, offerAt))

	assert.ErrorIs(t, err, ports.ErrOfferNotFound)
}

func TestOfferStoreListRequotable(t *testing.T) {
	s, _ := newOfferStore(t)
	ctx := context.Background()
	now := offerAt.Add(time.Hour)
	for _, o := range []domain.PersonalizedOffer{
		storedOffer("off-normal", domain.ModeNormal, offerAt),
		storedOffer("off-prov-b", domain.ModeProvisional, offerAt.Add(time.Minute)),
		storedOffer("off-prov-a", domain.ModeProvisional, offerAt),
		storedOffer("off-prov-old", domain.ModeProvisional, offerAt.Add(-25*time.Hour)),
		storedOffer("off-prov-done", domain.ModeProvisional, offerAt.Add(2*time.Minute)),
	} {
		_, _, err := s.Create(ctx, o)
		require.NoError(t, err)
	}
	_, err := s.Supersede(ctx, "off-prov-done", storedOffer("off-new", domain.ModeNormal, now))
	require.NoError(t, err)

	got, err := s.ListRequotable(ctx, now, 10)

	require.NoError(t, err)
	var ids []string
	for _, o := range got {
		ids = append(ids, o.ID)
	}
	assert.Equal(t, []string{"off-prov-a", "off-prov-b"}, ids)
	limited, _ := s.ListRequotable(ctx, now, 1)
	assert.Len(t, limited, 1)
	exp := storedOffer("off-prov-a", domain.ModeProvisional, offerAt).ExpiresAt
	before, _ := s.ListRequotable(ctx, exp.Add(-time.Microsecond), 10)
	at, _ := s.ListRequotable(ctx, exp, 10)
	idsOf := func(os []domain.PersonalizedOffer) (out []string) {
		for _, o := range os {
			out = append(out, o.ID)
		}
		return out
	}
	assert.Contains(t, idsOf(before), "off-prov-a")
	assert.NotContains(t, idsOf(at), "off-prov-a", "an offer is not requoted at its expiry")
}

func TestOfferTableChecksItsInvariants(t *testing.T) {
	s, db := newOfferStore(t)
	_, _, _ = s.Create(context.Background(), storedOffer("off-1", domain.ModeNormal, offerAt))

	_, err := db.ExecContext(context.Background(), `INSERT INTO offer_owner (offer_id, owner_id) VALUES ('off-ghost', 'x')`)

	assert.Error(t, err, "an owner needs an offer")
	bad := storedOffer("off-2", domain.ModeNormal, offerAt)
	bad.ProvisionalReason = domain.ProvisionalBaseProfile
	_, _, err = s.Create(context.Background(), bad)
	assert.Error(t, err, "only a provisional offer has a provisional reason")
}

func TestOfferStoreAnIdWithANulByteIsNotFoundNotAServerError(t *testing.T) {
	s, _ := newOfferStore(t)

	_, err := s.Get(context.Background(), "off-\x001")
	assert.ErrorIs(t, err, ports.ErrOfferNotFound)
	_, err = s.Claim(context.Background(), "off-\x001", "sub-a")
	assert.ErrorIs(t, err, ports.ErrOfferNotFound)
}
