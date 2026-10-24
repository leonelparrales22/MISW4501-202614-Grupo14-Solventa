package orchestrator_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/orchestrator"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/profiling"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/rating"
)

var start = time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type fakeProfiles struct {
	res     domain.ProfileResolution
	err     error
	calls   []string
	consent []bool
}

func (f *fakeProfiles) Resolve(_ context.Context, id string, consent bool) (domain.ProfileResolution, error) {
	f.calls = append(f.calls, id)
	f.consent = append(f.consent, consent)
	if f.err != nil {
		return domain.ProfileResolution{}, f.err
	}
	res := f.res
	res.Profile.CustomerID = id
	return res, nil
}

func normalProfile() domain.ProfileResolution {
	return domain.ProfileResolution{
		Profile: domain.RiskProfile{Score: 0.4, Category: "standard", CalculatedAt: start.Add(-time.Hour)},
		Mode:    domain.ModeNormal, Source: domain.SourceExternal,
	}
}

type env struct {
	svc       *orchestrator.AssistedQuoteService
	clock     *clock
	profiles  *fakeProfiles
	customers *memory.CustomerStore
	quotes    *memory.QuoteStore
}

func newEnv() *env {
	e := &env{clock: &clock{t: start}, profiles: &fakeProfiles{res: normalProfile()},
		customers: memory.NewCustomerStore(), quotes: memory.NewQuoteStore()}
	e.svc = orchestrator.NewAssistedQuoteService(e.customers, e.quotes, e.profiles, rating.New(), e.clock)
	return e
}

func yes() *bool { b := true; return &b }

func request() domain.QuoteRequest {
	return domain.QuoteRequest{
		DocumentID: "1032456789", FullName: "Ana Pérez", Email: "ana@example.com",
		BirthDate: "1990-05-20", InsuredAmount: 250_000_000, TermMonths: 180, ConsentActive: yes(),
	}
}

func TestCreatePricesWithTheProfileAndPersistsTheQuoteFor24Hours(t *testing.T) {
	e := newEnv()

	q, err := e.svc.Create(context.Background(), "advisor-1", request())

	require.NoError(t, err)
	assert.NotEmpty(t, q.ID)
	assert.Equal(t, "advisor-1", q.AdvisorID)
	assert.Equal(t, domain.ModeNormal, q.Mode)
	assert.Equal(t, domain.SourceExternal, q.ProfileSource)
	assert.InDelta(t, 0.4, q.ProfileScore, 1e-9)
	assert.Positive(t, q.Premium)
	assert.Equal(t, rating.Version, q.RatingVersion)
	assert.Len(t, q.Coverages, 2)
	assert.Equal(t, start, q.CreatedAt)
	assert.Equal(t, start.Add(24*time.Hour), q.ExpiresAt, "criterion 4: 24 hours of validity")
	assert.Equal(t, []bool{true}, e.profiles.consent, "the advisor-affirmed consent reaches the profiling")

	stored, err := e.svc.Get(context.Background(), q.ID)
	require.NoError(t, err)
	assert.Equal(t, q, stored, "criterion 4: the quote can be read later by its id")
}

func TestCreateUsesTheRiskProfileOfTheCustomer(t *testing.T) {
	e := newEnv()
	safe, err := e.svc.Create(context.Background(), "a", request())
	require.NoError(t, err)

	e.profiles.res.Profile.Score = 0.9
	other := request()
	other.DocumentID = "9999999999"
	risky, err := e.svc.Create(context.Background(), "a", other)
	require.NoError(t, err)

	assert.Greater(t, risky.Premium, safe.Premium, "the price is not a standard age-and-amount tariff")
}

func TestCreateWithInvalidFieldsGeneratesNothing(t *testing.T) {
	e := newEnv()
	req := request()
	req.Email = "not-an-email"
	req.TermMonths = 0

	_, err := e.svc.Create(context.Background(), "a", req)

	var invalid *domain.ValidationError
	require.ErrorAs(t, err, &invalid)
	assert.ElementsMatch(t, []domain.FieldError{
		{Field: "email", Code: domain.CodeInvalidFormat}, {Field: "term_months", Code: domain.CodeRequired},
	}, invalid.Fields)
	assert.Empty(t, e.profiles.calls, "the profiling is not consulted for an invalid request")
	_, err = e.customers.GetByDocument(context.Background(), "1032456789")
	assert.ErrorIs(t, err, ports.ErrCustomerNotFound, "no customer is registered either")
}

func TestCreateWithoutConsentIsRefusedBeforeAnythingIsStored(t *testing.T) {
	e := newEnv()
	req := request()
	no := false
	req.ConsentActive = &no

	_, err := e.svc.Create(context.Background(), "a", req)

	assert.ErrorIs(t, err, orchestrator.ErrConsentRequired)
	assert.Empty(t, e.profiles.calls)
	_, err = e.customers.GetByDocument(context.Background(), "1032456789")
	assert.ErrorIs(t, err, ports.ErrCustomerNotFound)
}

func TestCreateReportsAConsentRefusedByTheProfiling(t *testing.T) {
	e := newEnv()
	e.profiles.err = profiling.ErrConsentRequired

	_, err := e.svc.Create(context.Background(), "a", request())

	assert.ErrorIs(t, err, orchestrator.ErrConsentRequired)
}

func TestCreateInDegradedModeSaysSo(t *testing.T) {
	for name, tc := range map[string]struct {
		source domain.ProfileSource
		score  float64
	}{
		"cached profile":         {domain.SourceCache, 0.35},
		"base actuarial profile": {domain.SourceBaseActuarial, 0.5},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv()
			e.profiles.res = domain.ProfileResolution{
				Profile: domain.RiskProfile{Score: tc.score, Category: "standard"},
				Mode:    domain.ModeProvisional, Source: tc.source, UnavailableSource: domain.ExternalSourceOpenFinance,
			}

			q, err := e.svc.Create(context.Background(), "a", request())

			require.NoError(t, err, "criterion 5: the quote is generated even when the sources are down")
			assert.Equal(t, domain.ModeProvisional, q.Mode)
			assert.Equal(t, tc.source, q.ProfileSource)
			assert.Equal(t, domain.ExternalSourceOpenFinance, q.UnavailableSource)
			assert.Positive(t, q.Premium)
		})
	}
}

func TestQuoteExpiresAfter24Hours(t *testing.T) {
	e := newEnv()
	q, err := e.svc.Create(context.Background(), "a", request())
	require.NoError(t, err)

	e.clock.advance(23*time.Hour + 59*time.Minute)
	assert.True(t, q.CanFormalize(e.svc.Now()))

	e.clock.advance(time.Minute)
	stored, err := e.svc.Get(context.Background(), q.ID)
	require.NoError(t, err, "an expired quote can still be read to explain why")
	assert.Equal(t, domain.QuoteExpired, stored.Status(e.svc.Now()))
	assert.False(t, stored.CanFormalize(e.svc.Now()), "criterion 6: an expired quote cannot be formalized")
}

func TestAnExistingCustomerKeepsItsIDAndItsDataIsUpdated(t *testing.T) {
	e := newEnv()
	first, err := e.svc.Create(context.Background(), "a", request())
	require.NoError(t, err)

	again := request()
	again.Email = "ana.nueva@example.com"
	second, err := e.svc.Create(context.Background(), "a", again)
	require.NoError(t, err)

	assert.Equal(t, first.CustomerID, second.CustomerID, "the profile cache is keyed by the customer id")
	assert.NotEqual(t, first.ID, second.ID)
	c, err := e.svc.CustomerByDocument(context.Background(), "1032456789")
	require.NoError(t, err)
	assert.Equal(t, "ana.nueva@example.com", c.Email)
}

func TestCustomerByDocument(t *testing.T) {
	e := newEnv()
	_, err := e.svc.Create(context.Background(), "a", request())
	require.NoError(t, err)

	c, err := e.svc.CustomerByDocument(context.Background(), "1032456789")
	require.NoError(t, err)
	assert.Equal(t, "Ana Pérez", c.FullName)
	assert.Equal(t, time.Date(1990, 5, 20, 0, 0, 0, 0, time.UTC), c.BirthDate)

	_, err = e.svc.CustomerByDocument(context.Background(), "5555555555")
	assert.ErrorIs(t, err, ports.ErrCustomerNotFound)

	_, err = e.svc.CustomerByDocument(context.Background(), "x!")
	var invalid *domain.ValidationError
	assert.ErrorAs(t, err, &invalid)
}

func TestGetUnknownQuote(t *testing.T) {
	_, err := newEnv().svc.Get(context.Background(), "nope")
	assert.ErrorIs(t, err, ports.ErrQuoteNotFound)
}

type brokenQuotes struct{ err error }

func (b brokenQuotes) Create(context.Context, domain.Quote) error { return b.err }
func (b brokenQuotes) Get(context.Context, string) (domain.Quote, error) {
	return domain.Quote{}, b.err
}

type brokenCustomers struct{ err error }

func (b brokenCustomers) GetByDocument(context.Context, string) (domain.Customer, error) {
	return domain.Customer{}, b.err
}
func (b brokenCustomers) GetByEmail(context.Context, string) (domain.Customer, error) {
	return domain.Customer{}, b.err
}
func (b brokenCustomers) Save(context.Context, domain.Customer) (domain.Customer, error) {
	return domain.Customer{}, b.err
}
func (b brokenCustomers) Link(context.Context, domain.Customer, domain.CustomerLinked) (domain.Customer, error) {
	return domain.Customer{}, b.err
}

type brokenRater struct{}

func (brokenRater) Rate(context.Context, ports.RatingInput) (domain.Rating, error) {
	return domain.Rating{}, errors.New("rating down")
}

func TestCreatePropagatesInfrastructureFailures(t *testing.T) {
	boom := errors.New("boom")
	profiles := &fakeProfiles{res: normalProfile()}
	c := &clock{t: start}

	t.Run("customers", func(t *testing.T) {
		svc := orchestrator.NewAssistedQuoteService(brokenCustomers{boom}, memory.NewQuoteStore(), profiles, rating.New(), c)
		_, err := svc.Create(context.Background(), "a", request())
		assert.ErrorIs(t, err, boom)
	})
	t.Run("profiling", func(t *testing.T) {
		svc := orchestrator.NewAssistedQuoteService(memory.NewCustomerStore(), memory.NewQuoteStore(), &fakeProfiles{err: boom}, rating.New(), c)
		_, err := svc.Create(context.Background(), "a", request())
		assert.ErrorIs(t, err, boom)
	})
	t.Run("rating", func(t *testing.T) {
		svc := orchestrator.NewAssistedQuoteService(memory.NewCustomerStore(), memory.NewQuoteStore(), profiles, brokenRater{}, c)
		_, err := svc.Create(context.Background(), "a", request())
		assert.Error(t, err)
	})
	t.Run("quotes", func(t *testing.T) {
		svc := orchestrator.NewAssistedQuoteService(memory.NewCustomerStore(), brokenQuotes{boom}, profiles, rating.New(), c)
		_, err := svc.Create(context.Background(), "a", request())
		assert.ErrorIs(t, err, boom)
	})
}
