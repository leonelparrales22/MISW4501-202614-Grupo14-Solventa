package formalization_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/formalization"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/issuance"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

var t0 = time.Date(2026, 10, 25, 15, 0, 0, 0, time.UTC)

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

// sender keeps the codes it was asked to deliver.
type sender struct {
	mu    sync.Mutex
	sent  []domain.ConsentCodeNotice
	fails error
}

func (s *sender) SendConsentCode(_ context.Context, n domain.ConsentCodeNotice) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fails != nil {
		return s.fails
	}
	s.sent = append(s.sent, n)
	return nil
}

func (s *sender) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sent)
}

func (s *sender) last(t *testing.T) domain.ConsentCodeNotice {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	require.NotEmpty(t, s.sent, "no code was sent")
	return s.sent[len(s.sent)-1]
}

type env struct {
	svc         *formalization.Service
	clock       *clock
	quotes      *memory.QuoteStore
	offers      *memory.PersonalizedOfferStore
	acceptances *memory.AcceptanceStore
	customers   *memory.CustomerStore
	sender      *sender
	started     int
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{
		clock: &clock{t: t0}, quotes: memory.NewQuoteStore(), offers: memory.NewPersonalizedOfferStore(),
		acceptances: memory.NewAcceptanceStore(), customers: memory.NewCustomerStore(), sender: &sender{},
	}
	clk := e.clock
	router := memory.NewOfferRouter(memory.NewOfferStub(clk), e.offers)
	e.svc = formalization.New(formalization.Deps{
		Quotes: e.quotes, Customers: e.customers, Offers: e.offers, Formalized: memory.NewFormalizationStore(), Challenges: memory.NewConsentChallengeStore(),
		Sender: e.sender, Acceptances: issuance.NewAcceptanceService(router, e.acceptances, clk, nil), Clock: clk,
		OnFormalized: func() { e.started++ },
	})
	return e
}

func advisor(subject string) context.Context {
	return security.WithClaims(context.Background(), security.Claims{
		Subject: subject, Email: subject + "@example.com", Roles: []security.Role{security.RoleAdvisor},
	})
}

// byCode is the input of an acceptance confirmed with the code the customer received.
func byCode(code string) formalization.Input {
	return formalization.Input{Acceptance: domain.ConsentAcceptance{Method: domain.ConsentConfirmationCode, AcceptedAt: t0}, Code: code}
}

// byCall is the input of an acceptance on a recorded call.
func byCall(reference string, at time.Time) formalization.Input {
	return formalization.Input{Acceptance: domain.ConsentAcceptance{Method: domain.ConsentRecordedCall, AcceptedAt: at, Reference: reference}}
}

func (e *env) seedQuote(t *testing.T, id string) domain.Quote {
	t.Helper()
	q := domain.Quote{
		ID: id, CustomerID: "cus-1", AdvisorID: "adv-1",
		Applicant: domain.Applicant{FullName: "Laura Gómez", DocumentID: "1020304050", Email: "laura@example.com"},
		BirthDate: time.Date(1985, 3, 2, 0, 0, 0, 0, time.UTC), InsuredAmount: 200_000_000, TermMonths: 120,
		Premium: 98_450, Currency: "COP", Coverages: []domain.Coverage{{Code: "life", InsuredAmount: 200_000_000}},
		RatingVersion: "rating-1", Mode: domain.ModeNormal, ProfileSource: domain.SourceExternal, ProfileScore: 0.4,
		ProfileCategory: "standard", ProfileDate: t0.Add(-time.Hour), CreatedAt: t0, ExpiresAt: t0.Add(domain.QuoteValidity),
	}
	require.NoError(t, e.quotes.Create(context.Background(), q))
	return q
}

// codeFor asks for the code and returns what the customer received.
func (e *env) codeFor(t *testing.T, ctx context.Context, quoteID string) string {
	t.Helper()
	_, err := e.svc.RequestConsent(ctx, quoteID)
	require.NoError(t, err)
	return e.sender.last(t).Code
}

func TestFormalizeStartsTheIssuanceAndRecordsWhoWhenAndFromWhichQuote(t *testing.T) {
	e := newEnv(t)
	e.seedQuote(t, "q-1")
	ctx := advisor("adv-1")
	code := e.codeFor(t, ctx, "q-1")
	e.clock.advance(2 * time.Minute)

	res, err := e.svc.Formalize(ctx, "q-1", byCode(code))

	require.NoError(t, err)
	assert.True(t, res.Created)
	assert.Equal(t, "adv-1", res.Formalization.AdvisorID, "CA-4: who")
	assert.Equal(t, t0.Add(2*time.Minute), res.Formalization.FormalizedAt, "CA-4: when")
	assert.Equal(t, "q-1", res.Formalization.QuoteID, "CA-4: from which quote")
	assert.Equal(t, "adv-1@example.com", res.Formalization.AdvisorEmail)
	assert.Equal(t, domain.ConsentConfirmationCode, res.Formalization.Consent.Method, "CA-5")
	assert.Equal(t, t0, res.Formalization.Consent.AcceptedAt, "CA-5: when the customer accepted")
	assert.Equal(t, domain.ChannelAdvisor, res.Acceptance.Channel, "CA-3: the acceptance starts the issuance")
	assert.Equal(t, domain.AcceptanceRegistered, res.Acceptance.Status)
	assert.Empty(t, res.Acceptance.CustomerID, "a customer without an account owns nothing yet")
	assert.Equal(t, 1, e.started)

	offer, err := e.offers.Get(context.Background(), res.Formalization.OfferID)
	require.NoError(t, err)
	assert.Equal(t, domain.OfferChannelAssisted, offer.Channel)
	assert.Equal(t, "q-1", offer.QuoteID)
	assert.InDelta(t, 98_450, offer.Premium, 0.001)
	assert.Equal(t, 120, offer.Terms.TermMonths)
}

func TestTheAcceptanceBelongsToTheCustomerAccountWhenTheyHaveOne(t *testing.T) {
	e := newEnv(t)
	e.seedQuote(t, "q-1")
	_, err := e.customers.Save(context.Background(), domain.Customer{
		DocumentID: "1020304050", FullName: "Laura Gómez", Email: "laura@example.com",
		BirthDate: time.Date(1985, 3, 2, 0, 0, 0, 0, time.UTC), CognitoSub: "sub-laura",
	})
	require.NoError(t, err)
	ctx := advisor("adv-1")
	code := e.codeFor(t, ctx, "q-1")

	res, err := e.svc.Formalize(ctx, "q-1", byCode(code))

	require.NoError(t, err)
	assert.Equal(t, "sub-laura", res.Acceptance.CustomerID, "the customer follows the charge from their own account")
}

func TestFormalizeWithARecordedCallNeedsNoCodeButKeepsTheReference(t *testing.T) {
	e := newEnv(t)
	e.seedQuote(t, "q-1")
	e.clock.advance(time.Hour)

	res, err := e.svc.Formalize(advisor("adv-1"), "q-1", byCall("GRAB-2026-88910", t0.Add(30*time.Minute)))

	require.NoError(t, err, "CA-5: a recorded call backs the consent without a code")
	assert.Equal(t, domain.ConsentRecordedCall, res.Formalization.Consent.Method)
	assert.Equal(t, "GRAB-2026-88910", res.Formalization.Consent.Reference)
	assert.Equal(t, t0.Add(30*time.Minute), res.Formalization.Consent.AcceptedAt)
	assert.Zero(t, e.sender.count(), "no code was sent")
	assert.Equal(t, domain.ChannelAdvisor, res.Acceptance.Channel)
}

func TestTheAcceptanceIsValidatedFieldByField(t *testing.T) {
	e := newEnv(t)
	e.seedQuote(t, "q-1")
	e.clock.advance(time.Hour)
	now := t0.Add(time.Hour)
	cases := map[string]struct {
		in    formalization.Input
		field string
		code  string
	}{
		"no method":        {formalization.Input{Acceptance: domain.ConsentAcceptance{AcceptedAt: now, Reference: "r"}}, "acceptance.method", domain.CodeRequired},
		"unknown method":   {formalization.Input{Acceptance: domain.ConsentAcceptance{Method: "smoke", AcceptedAt: now, Reference: "r"}}, "acceptance.method", domain.CodeInvalidFormat},
		"no date":          {formalization.Input{Acceptance: domain.ConsentAcceptance{Method: domain.ConsentRecordedCall, Reference: "r"}}, "acceptance.accepted_at", domain.CodeRequired},
		"in the future":    {byCall("r", now.Add(time.Hour)), "acceptance.accepted_at", domain.CodeOutOfRange},
		"before the quote": {byCall("r", t0.Add(-time.Minute)), "acceptance.accepted_at", domain.CodeOutOfRange},
		"no reference":     {byCall("  ", now), "acceptance.reference", domain.CodeRequired},
		"bad reference":    {byCall("grab 1; drop", now), "acceptance.reference", domain.CodeInvalidFormat},
		"long reference":   {byCall(strings.Repeat("a", domain.MaxConsentReferenceLength+1), now), "acceptance.reference", domain.CodeInvalidFormat},
	}
	for name, c := range cases {
		_, err := e.svc.Formalize(advisor("adv-1"), "q-1", c.in)
		var invalid *domain.ValidationError
		require.ErrorAs(t, err, &invalid, name)
		assert.Contains(t, invalid.Fields, domain.FieldError{Field: c.field, Code: c.code}, name)
	}
	_, err := e.svc.Get(advisor("adv-1"), "q-1")
	assert.ErrorIs(t, err, ports.ErrFormalizationNotFound, "nothing was formalized")
}

func TestTheCodeIsSentToTheCustomerAndTheAdvisorSeesItMasked(t *testing.T) {
	e := newEnv(t)
	e.seedQuote(t, "q-1")

	req, err := e.svc.RequestConsent(advisor("adv-1"), "q-1")

	require.NoError(t, err)
	assert.Equal(t, "la***@example.com", req.SentTo)
	assert.Equal(t, t0.Add(domain.ConsentCodeTTL), req.ExpiresAt)
	sent := e.sender.last(t)
	assert.Equal(t, "laura@example.com", sent.To)
	assert.True(t, domain.ValidConsentCode(sent.Code))
	assert.Equal(t, "consent-code-q-1-1", sent.IdempotencyKey)
}

func TestFormalizeNeedsTheCodeTheCustomerReceived(t *testing.T) {
	e := newEnv(t)
	e.seedQuote(t, "q-1")
	ctx := advisor("adv-1")

	_, err := e.svc.Formalize(ctx, "q-1", byCode("123456"))
	assert.ErrorIs(t, err, formalization.ErrConsentCodeRequired, "no code was requested")

	code := e.codeFor(t, ctx, "q-1")
	wrong := "000000"
	if code == wrong {
		wrong = "111111"
	}
	_, err = e.svc.Formalize(ctx, "q-1", byCode(wrong))
	assert.ErrorIs(t, err, formalization.ErrConsentCodeInvalid)
	_, err = e.svc.Get(ctx, "q-1")
	assert.ErrorIs(t, err, ports.ErrFormalizationNotFound, "a wrong code formalizes nothing")
	assert.Zero(t, e.started)
}

func TestTheCodeExpiresAndAFreshOneReplacesIt(t *testing.T) {
	e := newEnv(t)
	e.seedQuote(t, "q-1")
	ctx := advisor("adv-1")
	old := e.codeFor(t, ctx, "q-1")
	e.clock.advance(domain.ConsentCodeTTL)

	_, err := e.svc.Formalize(ctx, "q-1", byCode(old))
	assert.ErrorIs(t, err, formalization.ErrConsentCodeExpired)

	fresh := e.codeFor(t, ctx, "q-1")
	_, err = e.svc.Formalize(ctx, "q-1", byCode(fresh))
	require.NoError(t, err)
}

func TestTheCodeIsLockedAfterTooManyAttemptsAndCannotBeGuessed(t *testing.T) {
	e := newEnv(t)
	e.seedQuote(t, "q-1")
	ctx := advisor("adv-1")
	code := e.codeFor(t, ctx, "q-1")
	wrong := "000000"
	if code == wrong {
		wrong = "111111"
	}
	for i := 0; i < domain.MaxConsentCodeAttempts; i++ {
		_, err := e.svc.Formalize(ctx, "q-1", byCode(wrong))
		require.ErrorIs(t, err, formalization.ErrConsentCodeInvalid)
	}

	_, err := e.svc.Formalize(ctx, "q-1", byCode(code))

	assert.ErrorIs(t, err, formalization.ErrConsentCodeLocked, "even the right code is refused once the attempts are used")
}

func TestOnlyAFewCodesAreSentForAQuote(t *testing.T) {
	e := newEnv(t)
	e.seedQuote(t, "q-1")
	ctx := advisor("adv-1")
	for i := 0; i < domain.MaxConsentCodeSends; i++ {
		_, err := e.svc.RequestConsent(ctx, "q-1")
		require.NoError(t, err)
	}

	_, err := e.svc.RequestConsent(ctx, "q-1")

	assert.ErrorIs(t, err, formalization.ErrConsentCodeLimit)
}

func TestAnExpiredQuoteCannotBeFormalizedNorAskedForACode(t *testing.T) {
	e := newEnv(t)
	e.seedQuote(t, "q-1")
	ctx := advisor("adv-1")
	code := e.codeFor(t, ctx, "q-1")
	e.clock.advance(domain.QuoteValidity)

	_, err := e.svc.Formalize(ctx, "q-1", byCode(code))
	var expired *formalization.QuoteExpiredError
	require.ErrorAs(t, err, &expired, "CA-2")
	assert.Equal(t, t0.Add(domain.QuoteValidity), expired.ExpiresAt)

	_, err = e.svc.RequestConsent(ctx, "q-1")
	assert.ErrorAs(t, err, &expired)
	assert.Zero(t, e.started)
}

func TestAnUnknownQuoteIsNotFound(t *testing.T) {
	e := newEnv(t)

	_, err := e.svc.RequestConsent(advisor("adv-1"), "nope")
	assert.ErrorIs(t, err, ports.ErrQuoteNotFound)
	_, err = e.svc.Formalize(advisor("adv-1"), "nope", byCode("123456"))
	assert.ErrorIs(t, err, ports.ErrQuoteNotFound)
}

func TestRepeatingAFormalizationReturnsTheSameResultWithoutAnotherAcceptance(t *testing.T) {
	e := newEnv(t)
	e.seedQuote(t, "q-1")
	ctx := advisor("adv-1")
	code := e.codeFor(t, ctx, "q-1")
	first, err := e.svc.Formalize(ctx, "q-1", byCode(code))
	require.NoError(t, err)

	again, err := e.svc.Formalize(ctx, "q-1", byCode(code))

	require.NoError(t, err)
	assert.False(t, again.Created)
	assert.Equal(t, first.Acceptance.ID, again.Acceptance.ID)
	assert.Equal(t, first.Formalization, again.Formalization)
}

func TestAnotherAdvisorCannotFormalizeAQuoteAlreadyFormalized(t *testing.T) {
	e := newEnv(t)
	e.seedQuote(t, "q-1")
	code := e.codeFor(t, advisor("adv-1"), "q-1")
	_, err := e.svc.Formalize(advisor("adv-1"), "q-1", byCode(code))
	require.NoError(t, err)

	_, err = e.svc.Formalize(advisor("adv-2"), "q-1", byCode(code))
	assert.ErrorIs(t, err, formalization.ErrAlreadyFormalized, "CA-6")
	_, err = e.svc.RequestConsent(advisor("adv-2"), "q-1")
	assert.ErrorIs(t, err, formalization.ErrAlreadyFormalized)
}

func TestTwoAdvisorsFormalizingAtOnceOnlyOneCompletes(t *testing.T) {
	e := newEnv(t)
	e.seedQuote(t, "q-1")
	code := e.codeFor(t, advisor("adv-1"), "q-1")

	const advisors = 8
	var wg sync.WaitGroup
	results := make([]error, advisors)
	created := make([]bool, advisors)
	start := make(chan struct{})
	for i := 0; i < advisors; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			res, err := e.svc.Formalize(advisor("adv-"+string(rune('a'+i))), "q-1", byCode(code))
			results[i], created[i] = err, res.Created
		}()
	}
	close(start)
	wg.Wait()

	wins, rejected := 0, 0
	for i := range results {
		switch {
		case results[i] == nil && created[i]:
			wins++
		case errors.Is(results[i], formalization.ErrAlreadyFormalized):
			rejected++
		default:
			t.Fatalf("advisor %d: unexpected result %v (created=%v)", i, results[i], created[i])
		}
	}
	assert.Equal(t, 1, wins, "CA-6: a single formalization completes")
	assert.Equal(t, advisors-1, rejected)

	acceptances, err := e.acceptances.GetByOffer(context.Background(), domain.FormalizationOfferID("q-1"))
	require.NoError(t, err)
	assert.Equal(t, domain.ChannelAdvisor, acceptances.Channel, "one policy: a single acceptance")
}

func TestOnlyAnAdvisorFormalizes(t *testing.T) {
	e := newEnv(t)
	e.seedQuote(t, "q-1")
	insured := security.WithClaims(context.Background(), security.Claims{Subject: "u", Roles: []security.Role{security.RoleInsured}})

	_, err := e.svc.RequestConsent(insured, "q-1")
	assert.ErrorIs(t, err, security.ErrForbidden)
	_, err = e.svc.Formalize(insured, "q-1", byCode("123456"))
	assert.ErrorIs(t, err, security.ErrForbidden)
	_, err = e.svc.Get(insured, "q-1")
	assert.ErrorIs(t, err, security.ErrForbidden)
	_, err = e.svc.Formalize(context.Background(), "q-1", byCode("123456"))
	assert.ErrorIs(t, err, security.ErrUnauthenticated)
}

func TestFormalizationStoppedBeforeTheAcceptanceIsFinishedByRepeatingIt(t *testing.T) {
	e := newEnv(t)
	q := e.seedQuote(t, "q-1")
	// The process stopped after recording the formalization: no offer, no acceptance.
	formalized := memory.NewFormalizationStore()
	_, _, err := formalized.Create(context.Background(), domain.Formalization{
		ID: domain.FormalizationID(q.ID), QuoteID: q.ID, OfferID: domain.FormalizationOfferID(q.ID), CustomerID: q.CustomerID,
		AdvisorID: "adv-1", Consent: domain.ConsentAcceptance{Method: domain.ConsentConfirmationCode, AcceptedAt: t0}, FormalizedAt: t0,
	})
	require.NoError(t, err)
	clk := e.clock
	svc := formalization.New(formalization.Deps{
		Quotes: e.quotes, Offers: e.offers, Formalized: formalized, Challenges: memory.NewConsentChallengeStore(), Sender: e.sender,
		Acceptances: issuance.NewAcceptanceService(memory.NewOfferRouter(memory.NewOfferStub(clk), e.offers), e.acceptances, clk, nil),
		Clock:       clk,
	})

	res, err := svc.Formalize(advisor("adv-1"), "q-1", byCode("000000"))

	require.NoError(t, err, "the repeat does not need the code again")
	assert.False(t, res.Created)
	assert.Equal(t, domain.ChannelAdvisor, res.Acceptance.Channel)
	_, err = e.offers.Get(context.Background(), q.ID)
	assert.ErrorIs(t, err, ports.ErrOfferNotFound, "the offer id is not the quote id")
	_, err = e.offers.Get(context.Background(), domain.FormalizationOfferID(q.ID))
	assert.NoError(t, err, "the offer was stored by the repeat")
}

func TestACodeThatCannotBeDeliveredIsReported(t *testing.T) {
	e := newEnv(t)
	e.seedQuote(t, "q-1")
	e.sender.fails = formalization.ErrCodeDeliveryUnavailable

	_, err := e.svc.RequestConsent(advisor("adv-1"), "q-1")

	assert.ErrorIs(t, err, formalization.ErrCodeDeliveryUnavailable)
}

func TestMaskEmail(t *testing.T) {
	assert.Equal(t, "la***@example.com", formalization.MaskEmail("laura@example.com"))
	assert.Equal(t, "a***@x.co", formalization.MaskEmail("a@x.co"))
	assert.Empty(t, formalization.MaskEmail("no-at"))
}
