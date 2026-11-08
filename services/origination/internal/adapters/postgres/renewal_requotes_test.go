package postgres_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

var requotedAt = policyEnds.Add(-10 * 24 * time.Hour).Truncate(time.Microsecond)

func sampleRequote(id string, p domain.Policy, at time.Time) domain.RenewalRequote {
	return domain.RenewalRequote{
		ID: id, PolicyID: p.ID, PeriodEnd: p.CoverageEnd, AdvisorID: "sub-advisor",
		Terms:   domain.CoverageTerms{InsuredAmount: 68_000_000, TermMonths: 12, Coverages: p.Coverages},
		Premium: 149_800, Currency: "COP", DiscountRate: 0.08, Requoted: true, RatingVersion: "v1",
		AppliedRules: []domain.AppliedRule{{Code: "base_monthly_rate", Value: 0.00045}, {Code: "rounding_step", Value: 100}},
		Mode:         domain.ModeProvisional, ProfileSource: domain.SourceCache, ProfileNotice: domain.ProfileNoticeLastProfileInUse,
		ProfileScore: 0.31, ProfileCategory: "standard", ProfileDate: at.Add(-20 * 24 * time.Hour), UnavailableSource: "open_finance",
		Sources: []domain.SourceOutcome{
			{Source: "open_finance", Status: domain.SourceStatusTimeout, Latency: 120 * time.Millisecond, ConsultedAt: at},
			{Source: "open_data", Status: domain.SourceStatusOK, Latency: 88 * time.Millisecond, ConsultedAt: at,
				Value: &domain.SourceValue{Score: 0.2, Category: "preferred"}},
		},
		ProfileCustomerID: "cust-1", OriginChannel: domain.OfferChannelEmbedded,
		ConsentBasis: domain.ConsentBasisAuditVerified, ConsentID: "con-1",
		CreatedAt: at, ExpiresAt: domain.RenewalRequoteExpiry(at),
	}
}

// newRenewalDB returns a migrated database with the policy pol-1.
func newRenewalDB(t *testing.T) (*sql.DB, domain.Policy) {
	t.Helper()
	requireOfferDocker(t)
	db := newDB(t)
	p := samplePolicy("pol-1", "sub-1", policyEnds)
	require.NoError(t, postgres.NewPolicyStore(db).Create(context.Background(), p))
	return db, p
}

func TestRenewalRequoteStoreKeepsTheSnapshotAsItWasMade(t *testing.T) {
	db, p := newRenewalDB(t)
	s := postgres.NewRenewalRequoteStore(db)
	ctx := context.Background()
	want := sampleRequote("rq-1", p, requotedAt)

	require.NoError(t, s.Create(ctx, want))
	got, err := s.Get(ctx, "rq-1")

	require.NoError(t, err)
	assert.Equal(t, want, got)
	_, err = s.Get(ctx, "rq-404")
	assert.ErrorIs(t, err, ports.ErrRenewalRequoteNotFound)
}

func TestRenewalRequoteStoreKeepsAnAffirmedConsentAndARequoteWithoutProfile(t *testing.T) {
	db, p := newRenewalDB(t)
	s := postgres.NewRenewalRequoteStore(db)
	ctx := context.Background()

	affirmed := sampleRequote("rq-assisted", p, requotedAt)
	affirmed.OriginChannel, affirmed.ConsentID = domain.OfferChannelAssisted, ""
	affirmed.ConsentBasis, affirmed.ConsentAffirmedBy, affirmed.ConsentAffirmedAt = domain.ConsentBasisAdvisorAffirmed, "sub-advisor", requotedAt
	require.NoError(t, s.Create(ctx, affirmed))
	got, err := s.Get(ctx, "rq-assisted")
	require.NoError(t, err)
	assert.Equal(t, affirmed, got)

	same := sampleRequote("rq-same", p, requotedAt)
	same.Requoted, same.RatingVersion, same.AppliedRules, same.Sources = false, "", nil, nil
	same.Mode, same.ProfileSource, same.ProfileNotice, same.ProfileScore, same.ProfileCategory = "", "", "", 0, ""
	same.ProfileDate, same.UnavailableSource, same.ProfileCustomerID, same.OriginChannel = time.Time{}, "", "", ""
	same.ConsentBasis, same.ConsentID = "", ""
	require.NoError(t, s.Create(ctx, same), "no external data consulted: no consent basis")
	got, err = s.Get(ctx, "rq-same")
	require.NoError(t, err)
	assert.Equal(t, same, got)
}

func TestAnAffirmedConsentWithoutWhoOrWhenIsRefused(t *testing.T) {
	db, p := newRenewalDB(t)
	s := postgres.NewRenewalRequoteStore(db)
	ctx := context.Background()

	noWho := sampleRequote("rq-no-who", p, requotedAt)
	noWho.ConsentBasis, noWho.ConsentAffirmedAt = domain.ConsentBasisAdvisorAffirmed, requotedAt
	assert.Error(t, s.Create(ctx, noWho), "the CHECK needs who affirmed it")

	noWhen := sampleRequote("rq-no-when", p, requotedAt)
	noWhen.ConsentBasis, noWhen.ConsentAffirmedBy = domain.ConsentBasisAdvisorAffirmed, "sub-advisor"
	assert.Error(t, s.Create(ctx, noWhen), "the CHECK needs when")

	unknown := sampleRequote("rq-unknown", p, requotedAt)
	unknown.ConsentBasis = "implicit"
	assert.Error(t, s.Create(ctx, unknown))

	_, err := s.Get(ctx, "rq-no-who")
	assert.ErrorIs(t, err, ports.ErrRenewalRequoteNotFound)
}

func TestTheLatestRequoteOfThePeriodIsTheLastOneMade(t *testing.T) {
	db, p := newRenewalDB(t)
	s := postgres.NewRenewalRequoteStore(db)
	ctx := context.Background()

	_, err := s.Latest(ctx, p.ID, p.CoverageEnd)
	assert.ErrorIs(t, err, ports.ErrRenewalRequoteNotFound)

	require.NoError(t, s.Create(ctx, sampleRequote("rq-b", p, requotedAt)))
	require.NoError(t, s.Create(ctx, sampleRequote("rq-a", p, requotedAt.Add(time.Minute))))
	other := samplePolicy("pol-2", "sub-1", policyEnds)
	require.NoError(t, postgres.NewPolicyStore(db).Create(ctx, other))
	require.NoError(t, s.Create(ctx, sampleRequote("rq-z", other, requotedAt.Add(time.Hour))))

	got, err := s.Latest(ctx, p.ID, p.CoverageEnd)
	require.NoError(t, err)
	assert.Equal(t, "rq-a", got.ID)
	_, err = s.Latest(ctx, p.ID, p.CoverageEnd.AddDate(1, 0, 0))
	assert.ErrorIs(t, err, ports.ErrRenewalRequoteNotFound, "another period has none")
}

func TestARenewalRequoteIsImmutable(t *testing.T) {
	db, p := newRenewalDB(t)
	ctx := context.Background()
	require.NoError(t, postgres.NewRenewalRequoteStore(db).Create(ctx, sampleRequote("rq-1", p, requotedAt)))

	_, err := db.ExecContext(ctx, `UPDATE renewal_requote SET premium_minor = 1`)
	assert.Error(t, err)
	_, err = db.ExecContext(ctx, `DELETE FROM renewal_requote`)
	assert.Error(t, err)
	_, err = db.ExecContext(ctx, `TRUNCATE renewal_requote CASCADE`)
	assert.Error(t, err)
}
