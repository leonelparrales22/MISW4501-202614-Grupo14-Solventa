package endorsements_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/endorsements"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/rating"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/renewal"
)

// HU-W1.4-02: the review of the operations analyst over the fakes of the
// stores, the registry and claims.

var andres = domain.Actor{Subject: "sub-andres", Name: "Andrés Ruiz", Email: "operaciones@example.com"}

func operations() context.Context {
	return security.WithClaims(context.Background(), security.Claims{Subject: andres.Subject, Name: andres.Name,
		Email: andres.Email, Roles: []security.Role{security.RoleOperations}})
}

type reviewEnv struct {
	*env
	store     *fakeStore
	documents *fakeDocuments
	claims    fakeClaims
	renewals  fakeRenewals
	offers    fakeOffers
	woken     int
}

// originalOffer is the offer pol-carolina was bought from: a customer of 41
// with an average risk, for 12 months.
var originalOffer = domain.PersonalizedOffer{ID: "off-carolina", CustomerID: "cust-carolina", AgeAtQuote: 41,
	Terms: domain.CoverageTerms{InsuredAmount: 68_000_000, TermMonths: 12}, Profile: domain.ProfileSnapshot{Score: 0.5, Category: "standard"}}

func newReviewEnv() *reviewEnv {
	e := &reviewEnv{env: newEnv(), documents: &fakeDocuments{}, claims: fakeClaims{open: map[string]int{}},
		renewals: fakeRenewals{rows: map[string]domain.Renewal{}},
		offers:   fakeOffers{rows: map[string]domain.PersonalizedOffer{originalOffer.ID: originalOffer}}}
	e.origins.rows["pol-carolina"] = renewal.Origin{PartnerID: "aliado-banco-1", OfferID: originalOffer.ID}
	e.store = newFakeStore(e.requests)
	return e
}

func (e *reviewEnv) review(t *testing.T) *endorsements.Review {
	t.Helper()
	terms := endorsements.NewTerms(e.origins, map[string]string{"aliado-banco-1": "Banco Aliado"}).WithEndorsements(e.store).
		WithCancellations(e.cancellations)
	r, err := endorsements.NewReview(endorsements.ReviewConfig{Policies: e.policies, Requests: e.requests, Store: e.store,
		Terms: terms, Documents: e.documents, Claims: e.claims, Renewals: e.renewals,
		Pricer: endorsements.Pricer{Origins: e.origins, Offers: e.offers, Rater: rating.New()},
		Clock:  e.clock, Wake: func() { e.woken++ }})
	require.NoError(t, err)
	return r
}

// file files a request of carolina on pol-carolina.
func (e *reviewEnv) file(t *testing.T, in domain.ChangeInput) domain.ChangeRequest {
	t.Helper()
	r, err := e.service(t).File(insured("sub-carolina", "Carolina Pérez Ruiz"), "pol-carolina", in)
	require.NoError(t, err)
	return r
}

func contactData() domain.ChangeInput {
	return domain.ChangeInput{Type: "personal_data", Channel: "web",
		PersonalData: &domain.ContactInput{Phone: ptr("3012345678"), Address: ptr("Calle 100 # 15-20, Bogotá")}}
}

func listedCodes(v domain.Validations) []domain.ValidationCode {
	out := []domain.ValidationCode{}
	for _, r := range v {
		out = append(out, r.Code)
	}
	return out
}

func TestNewReviewNeedsItsDependencies(t *testing.T) {
	_, err := endorsements.NewReview(endorsements.ReviewConfig{})
	assert.Error(t, err)
}

func TestOnlyOperationsReviewsTheRequests(t *testing.T) {
	e := newReviewEnv()
	r := e.file(t, beneficiaries())
	review := e.review(t)
	reads := e.policies.reads
	for name, ctx := range map[string]context.Context{
		"insured":  insured("sub-carolina", "Carolina Pérez"),
		"advisor":  withRole(security.RoleAdvisor),
		"no token": context.Background(),
	} {
		_, err := review.Inbox(ctx, domain.EndorsementInboxFilter{})
		assert.Error(t, err, name)
		_, err = review.Detail(ctx, r.ID)
		assert.Error(t, err, name)
		_, err = review.Review(ctx, r.ID)
		assert.Error(t, err, name)
		_, err = review.Apply(ctx, r.ID)
		assert.Error(t, err, name)
		_, err = review.Reject(ctx, r.ID, "No procede el cambio solicitado")
		assert.Error(t, err, name)
		_, err = review.Return(ctx, r.ID, "No procede el cambio solicitado")
		assert.Error(t, err, name)
		_, _, err = review.History(ctx, "pol-carolina")
		assert.Error(t, err, name)
	}
	assert.Equal(t, reads, e.policies.reads, "a forbidden caller reads nothing")
	assert.Equal(t, domain.ChangeRequestFiled, e.requests.rows[r.ID].Status)
}

func TestTheInboxIsReadAtTheTimeOfTheServiceWithItsDefaults(t *testing.T) {
	e := newReviewEnv()
	filed := e.file(t, beneficiaries())

	page, err := e.review(t).Inbox(operations(), domain.EndorsementInboxFilter{})

	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, filed.ID, page.Items[0].Request.ID)
	assert.Equal(t, 1, page.Page)
	assert.Equal(t, domain.DefaultInboxPageSize, page.PageSize)
	_, err = e.review(t).Inbox(operations(), domain.EndorsementInboxFilter{PageSize: domain.MaxInboxPageSize + 1})
	assert.ErrorIs(t, err, domain.ErrInvalidInboxQuery)
}

func TestDetailShowsChangeRowsPerType(t *testing.T) {
	e := newReviewEnv()
	review := e.review(t)
	rated, err := rating.New().Rate(context.Background(), ports.RatingInput{Age: 41, InsuredAmount: 120_000_000, TermMonths: 12,
		Profile: domain.RiskProfile{CustomerID: "cust-carolina", Score: 0.5, Category: "standard"}})
	require.NoError(t, err)
	for _, tc := range []struct {
		name    string
		in      domain.ChangeInput
		rows    int
		listed  []domain.ValidationCode
		premium *endorsements.PremiumChange
	}{
		{"beneficiaries", beneficiaries(), 2, []domain.ValidationCode{domain.ValidationSharesTotal100, domain.ValidationOnerousIntact,
			domain.ValidationIdentityDocuments, domain.ValidationNoOpenClaim, domain.ValidationRequesterIsHolder}, nil},
		{"insured amount with the premium of the engine", insuredAmount(120_000_000), 1, []domain.ValidationCode{
			domain.ValidationInsuredAmountWithinLimits, domain.ValidationNoOpenClaim, domain.ValidationRequesterIsHolder},
			&endorsements.PremiumChange{Previous: 142_300, New: rated.Premium, Priced: true, Currency: "COP", Version: "v1"}},
		{"personal data", contactData(), 2, []domain.ValidationCode{domain.ValidationPersonalDataValid, domain.ValidationRequesterIsHolder}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := e.file(t, tc.in)

			d, err := review.Detail(operations(), r.ID)

			require.NoError(t, err)
			assert.Len(t, d.Request.Changes(), tc.rows)
			assert.Equal(t, endorsements.SourcePreview, d.Source)
			assert.Equal(t, tc.listed, listedCodes(d.Validations))
			assert.Equal(t, tc.premium, d.Premium)
			assert.Equal(t, domain.ChangeRequestFiled, e.requests.rows[r.ID].Status, "the preview writes nothing")
		})
	}
}

func TestDetailShowsPolicyPanel(t *testing.T) {
	e := newReviewEnv()
	review := e.review(t)
	applied := e.file(t, beneficiaries())
	_, err := review.Apply(operations(), applied.ID)
	require.NoError(t, err)
	next := e.file(t, contactData())

	d, err := review.Detail(operations(), next.ID)

	require.NoError(t, err)
	assert.Equal(t, "pol-carolina", d.Policy.ID)
	assert.True(t, d.InForce)
	assert.Equal(t, 1, d.Endorsements.Count)
	require.NotNil(t, d.Endorsements.Last)
	assert.Equal(t, domain.ChangeBeneficiaries, d.Endorsements.Last.Type)
	assert.Len(t, d.Terms.Beneficiaries, 2, "the panel shows the terms with the last endorsement")
	assert.Equal(t, "Banco Aliado", d.Terms.Onerous.Name)

	ad, err := review.Detail(operations(), applied.ID)
	require.NoError(t, err)
	assert.Equal(t, endorsements.SourceAttempt, ad.Source, "an applied request shows the validations it passed")
	require.NotNil(t, ad.Record.Endorsement)
	assert.Equal(t, andres, ad.Record.Endorsement.By)
}

func TestApplyAppliesWhenEveryValidationHolds(t *testing.T) {
	e := newReviewEnv()
	review := e.review(t)
	r := e.file(t, beneficiaries())
	_, err := review.Review(operations(), r.ID)
	require.NoError(t, err)

	got, err := review.Apply(operations(), r.ID)

	require.NoError(t, err)
	assert.Equal(t, domain.OutcomeApplied, got.Outcome)
	require.NotNil(t, got.Endorsement)
	assert.Equal(t, "END-2026-0001", got.Endorsement.ID)
	assert.Equal(t, 1, got.Endorsement.Version)
	assert.Equal(t, andres, got.Endorsement.By)
	assert.Equal(t, domain.ChangeRequestApplied, got.Request.Status)
	assert.Len(t, got.Validations, 5)
	assert.Equal(t, []string{"1140223001", "1142998310"}, e.documents.asked, "the two new documents were checked")
	assert.Equal(t, 1, e.woken, "the e-mail is sent at once")
	latest, err := e.store.LatestTerms(context.Background(), "pol-carolina")
	require.NoError(t, err)
	assert.Len(t, latest.Terms.Beneficiaries, 2)
	assert.InDelta(t, 142_300, latest.Terms.Premium, 0, "beneficiaries keep the premium")

	again, err := review.Apply(operations(), r.ID)
	require.NoError(t, err)
	assert.Equal(t, got.Endorsement.ID, again.Endorsement.ID, "a double click never creates two endorsements")
	assert.Len(t, e.store.endorsements, 1)
}

func TestApplyANewInsuredValuePricesItWithTheEngine(t *testing.T) {
	e := newReviewEnv()
	review := e.review(t)
	r := e.file(t, insuredAmount(120_000_000))

	got, err := review.Apply(operations(), r.ID)

	require.NoError(t, err)
	rated, err := rating.New().Rate(context.Background(), ports.RatingInput{Age: 41, InsuredAmount: 120_000_000, TermMonths: 12,
		Profile: domain.RiskProfile{CustomerID: "cust-carolina", Score: 0.5, Category: "standard"}})
	require.NoError(t, err)
	assert.InDelta(t, rated.Premium, got.Endorsement.Terms.Premium, 0, "the premium of the engine with the inputs of the issuance")
	assert.InDelta(t, 142_300, got.Endorsement.PreviousPremium, 0)
	assert.Equal(t, rated.Coverages, got.Endorsement.Terms.Coverages)
	assert.InDelta(t, 120_000_000, got.Endorsement.Terms.InsuredAmount, 0)

	d, err := review.Detail(operations(), r.ID)
	require.NoError(t, err)
	assert.Equal(t, &endorsements.PremiumChange{Previous: 142_300, New: rated.Premium, Priced: true, Currency: "COP"}, d.Premium,
		"an applied request shows the premium it left, not a new price")
}

func TestTheAgeOfTheIssuanceComesFromTheBirthDateWhenTheOfferHasNone(t *testing.T) {
	e := newReviewEnv()
	o := originalOffer
	o.AgeAtQuote, o.BirthDate, o.CreatedAt = 0, time.Date(1985, 5, 1, 0, 0, 0, 0, time.UTC), now.AddDate(0, -2, 0)
	e.offers.rows[o.ID] = o

	rated, priced, err := endorsements.Pricer{Origins: e.origins, Offers: e.offers, Rater: rating.New()}.Price(context.Background(),
		e.policies.rows["pol-carolina"], 120_000_000)

	require.NoError(t, err)
	assert.True(t, priced)
	want, err := rating.New().Rate(context.Background(), ports.RatingInput{Age: 41, InsuredAmount: 120_000_000, TermMonths: 12,
		Profile: domain.RiskProfile{CustomerID: "cust-carolina", Score: 0.5, Category: "standard"}})
	require.NoError(t, err)
	assert.InDelta(t, want.Premium, rated.Premium, 0)
}

func TestApplyNotAppliedRecordsAttemptAndKeepsPolicy(t *testing.T) {
	e := newReviewEnv()
	review := e.review(t)
	in := beneficiaries()
	in.Beneficiaries[1].DocumentNumber = "1142998000"
	r := e.file(t, in)

	got, err := review.Apply(operations(), r.ID)

	require.NoError(t, err)
	assert.Equal(t, domain.OutcomeNotApplied, got.Outcome)
	require.NotNil(t, got.Attempt)
	assert.Equal(t, domain.ValidationIdentityDocuments, got.Attempt.Validations.FirstFailed())
	assert.Equal(t, andres, got.Attempt.By)
	assert.Equal(t, domain.ChangeRequestNotApplied, got.Request.Status)
	assert.Equal(t, domain.ChangeRequestInReview, got.Request.InsuredStatus(), "the insured sees it in review until it is returned")
	assert.Empty(t, e.store.endorsements, "the policy keeps its terms")
	assert.Zero(t, e.woken, "nothing to tell the insured yet")

	again, err := review.Apply(operations(), r.ID)
	require.NoError(t, err)
	assert.Equal(t, got.Attempt.ID, again.Attempt.ID, "applying it again answers the same attempt")
	d, err := review.Detail(operations(), r.ID)
	require.NoError(t, err)
	assert.Equal(t, endorsements.SourceAttempt, d.Source)

	returned, err := review.Return(operations(), r.ID, "Corrige el documento de Lucía y radica de nuevo")
	require.NoError(t, err)
	assert.Equal(t, domain.ChangeRequestRejected, returned.InsuredStatus())
	assert.Equal(t, "Corrige el documento de Lucía y radica de nuevo", returned.DecisionReason)
	assert.Equal(t, 1, e.woken)
	_, err = review.Apply(operations(), r.ID)
	var decided *endorsements.DecidedError
	assert.ErrorAs(t, err, &decided, "a returned request is closed")
}

// A-9: a policy cancelled for non-payment within its dates is not in force,
// as one whose coverage ended: the detail says so and applying records the
// attempt with policy_in_force failed and leaves the policy as it was.
func TestACancelledPolicyIsNotAppliedAsOneNotInForce(t *testing.T) {
	e := newReviewEnv()
	review := e.review(t)
	r := e.file(t, contactData())
	e.cancellations.cancelled["pol-carolina"] = true

	d, err := review.Detail(operations(), r.ID)
	require.NoError(t, err)
	assert.False(t, d.InForce, "the detail shows it not in force")
	assert.Contains(t, listedCodes(d.Validations), domain.ValidationPolicyInForce)

	got, err := review.Apply(operations(), r.ID)

	require.NoError(t, err)
	assert.Equal(t, domain.OutcomeNotApplied, got.Outcome)
	require.NotNil(t, got.Attempt)
	assert.Equal(t, domain.ValidationPolicyInForce, got.Attempt.Validations.FirstFailed())
	assert.Empty(t, e.store.endorsements, "the policy keeps its terms")
}

func TestApplyUnverifiableChangesNothing(t *testing.T) {
	for _, tc := range []struct {
		name  string
		in    domain.ChangeInput
		setup func(*reviewEnv)
		want  []domain.ValidationCode
	}{
		{"KYC down", beneficiaries(), func(e *reviewEnv) { e.documents.unavailable = true },
			[]domain.ValidationCode{domain.ValidationIdentityDocuments}},
		{"claims down", insuredAmount(120_000_000), func(e *reviewEnv) { e.claims.err = errors.New("timeout") },
			[]domain.ValidationCode{domain.ValidationNoOpenClaim}},
		{"no original offer", insuredAmount(120_000_000), func(e *reviewEnv) { delete(e.offers.rows, originalOffer.ID) },
			[]domain.ValidationCode{domain.ValidationPremiumRecalculated}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newReviewEnv()
			r := e.file(t, tc.in)
			tc.setup(e)

			_, err := e.review(t).Apply(operations(), r.ID)

			var unverifiable *endorsements.UnverifiableError
			require.ErrorAs(t, err, &unverifiable)
			assert.ErrorIs(t, err, endorsements.ErrValidationUnverifiable)
			assert.Equal(t, tc.want, unverifiable.Codes)
			assert.Equal(t, domain.ChangeRequestFiled, e.requests.rows[r.ID].Status, "nothing changes")
			assert.Empty(t, e.store.attempts, "and nothing is recorded")
		})
	}
}

func TestAFailureWinsOverAnUnverifiableValidation(t *testing.T) {
	e := newReviewEnv()
	r := e.file(t, insuredAmount(120_000_000))
	e.renewals.rows["pol-carolina"] = domain.Renewal{Status: domain.RenewalAccepted}
	e.claims.err = errors.New("timeout")

	got, err := e.review(t).Apply(operations(), r.ID)

	require.NoError(t, err)
	assert.Equal(t, domain.OutcomeNotApplied, got.Outcome)
	assert.Equal(t, domain.ValidationNoRenewalInProgress, got.Attempt.Validations.FirstFailed(), "a renewal accepted and not active")
}

func TestAnotherEndorsementOfThePolicyWinsTheVersion(t *testing.T) {
	e := newReviewEnv()
	review := e.review(t)
	r := e.file(t, contactData())
	other := e.file(t, insuredAmount(120_000_000))
	e.store.beforeApply = func() {
		e.store.beforeApply = nil
		_, err := review.Apply(operations(), other.ID)
		require.NoError(t, err)
	}

	_, err := review.Apply(operations(), r.ID)

	assert.ErrorIs(t, err, ports.ErrPolicyChanged)
	assert.Equal(t, domain.ChangeRequestFiled, e.requests.rows[r.ID].Status, "nothing changes: the next attempt decides")
	got, err := review.Apply(operations(), r.ID)
	require.NoError(t, err)
	assert.Equal(t, 2, got.Endorsement.Version)
	assert.InDelta(t, 120_000_000, got.Endorsement.Terms.InsuredAmount, 0, "it starts from the terms of the winner")
}

func TestAnotherAnalystDecidedFirst(t *testing.T) {
	e := newReviewEnv()
	review := e.review(t)
	r := e.file(t, beneficiaries())
	e.store.beforeApply = func() {
		e.store.beforeApply = nil
		_, err := review.Reject(operations(), r.ID, "El parentesco no permite la designación")
		require.NoError(t, err)
	}

	_, err := review.Apply(operations(), r.ID)

	var decided *endorsements.DecidedError
	require.ErrorAs(t, err, &decided)
	assert.ErrorIs(t, err, ports.ErrRequestAlreadyDecided)
	assert.Equal(t, domain.ChangeRequestRejected, decided.Request.Status, "with the request as the winner left it")
	_, err = review.Reject(operations(), r.ID, "El parentesco no permite la designación")
	assert.ErrorAs(t, err, &decided)
	_, err = review.Review(operations(), r.ID)
	assert.ErrorAs(t, err, &decided)
}

func TestRejectAndReturnRequireObservations(t *testing.T) {
	e := newReviewEnv()
	review := e.review(t)
	r := e.file(t, beneficiaries())

	_, err := review.Reject(operations(), r.ID, "corto")
	assert.ErrorIs(t, err, domain.ErrEndorsementObservations)
	_, err = review.Return(operations(), r.ID, " ")
	assert.ErrorIs(t, err, domain.ErrEndorsementObservations)
	_, err = review.Return(operations(), r.ID, "No se puede devolver si no se aplicó")
	assert.ErrorIs(t, err, ports.ErrNotReturnable, "only a request not applied is returned")
	assert.Equal(t, domain.ChangeRequestFiled, e.requests.rows[r.ID].Status)

	got, err := review.Reject(operations(), r.ID, "  El parentesco no permite la designación  ")

	require.NoError(t, err)
	assert.Equal(t, domain.ChangeRequestRejected, got.Status)
	assert.Equal(t, "El parentesco no permite la designación", got.DecisionReason)
	assert.Equal(t, 1, e.woken)
	d, err := review.Detail(operations(), r.ID)
	require.NoError(t, err)
	assert.Equal(t, endorsements.SourceNone, d.Source, "rejected before anybody tried to apply it")
	assert.Empty(t, d.Validations)
	require.NotNil(t, d.Record.Last(domain.ActionRejection))
	assert.Equal(t, andres, d.Record.Last(domain.ActionRejection).By)
	_, err = review.Reject(operations(), "SOL-MOD-2026-9999", "El parentesco no permite la designación")
	assert.ErrorIs(t, err, ports.ErrChangeRequestNotFound)
	_, err = review.Detail(operations(), "otro")
	assert.ErrorIs(t, err, ports.ErrChangeRequestNotFound)
}

func TestReviewTakesTheRequestOnce(t *testing.T) {
	e := newReviewEnv()
	review := e.review(t)
	r := e.file(t, beneficiaries())

	got, err := review.Review(operations(), r.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.ChangeRequestInReview, got.Status)
	got, err = review.Review(operations(), r.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.ChangeRequestInReview, got.Status)

	rec, err := e.store.Record(context.Background(), r.ID)
	require.NoError(t, err)
	assert.Len(t, rec.Actions, 1, "taking it again records nothing")
}

func TestHistoryChronologicalWithAttempts(t *testing.T) {
	e := newReviewEnv()
	review := e.review(t)
	applied := e.file(t, contactData())
	_, err := review.Apply(operations(), applied.ID)
	require.NoError(t, err)
	e.clock.now = now.Add(time.Hour)
	in := beneficiaries()
	in.Beneficiaries[0].DocumentNumber = "1140223000"
	failed := e.file(t, in)
	_, err = review.Apply(operations(), failed.ID)
	require.NoError(t, err)

	p, got, err := review.History(operations(), "pol-carolina")

	require.NoError(t, err)
	assert.Equal(t, "pol-carolina", p.ID)
	require.Len(t, got, 2)
	assert.Equal(t, domain.OutcomeNotApplied, got[0].Outcome, "the newest first")
	assert.Equal(t, []domain.ValidationCode{domain.ValidationIdentityDocuments}, got[0].Failed)
	assert.Equal(t, domain.OutcomeApplied, got[1].Outcome)
	assert.Equal(t, andres, got[1].By)
	_, empty, err := review.History(operations(), "pol-diego")
	require.NoError(t, err)
	assert.Empty(t, empty)
	_, _, err = review.History(operations(), "pol-otra")
	assert.ErrorIs(t, err, ports.ErrPolicyNotFound)
}

func TestTheFailuresOfTheStoresAreReturned(t *testing.T) {
	down := errors.New("database down")
	for name, setup := range map[string]func(*reviewEnv){
		"renewals": func(e *reviewEnv) { e.renewals.err = down },
		"offers":   func(e *reviewEnv) { e.offers.err = down },
		"store":    func(e *reviewEnv) { e.store.err = down },
		// A-9: not knowing whether the policy was cancelled applies nothing.
		"cancellations": func(e *reviewEnv) { e.cancellations.err = down },
	} {
		t.Run(name, func(t *testing.T) {
			e := newReviewEnv()
			r := e.file(t, insuredAmount(120_000_000))
			setup(e)
			review := e.review(t)

			_, err := review.Apply(operations(), r.ID)
			assert.ErrorIs(t, err, down)
			_, err = review.Detail(operations(), r.ID)
			assert.ErrorIs(t, err, down)
			assert.Equal(t, domain.ChangeRequestFiled, e.requests.rows[r.ID].Status)
		})
	}
	e := newReviewEnv()
	e.store.err = down
	_, err := e.review(t).Inbox(operations(), domain.EndorsementInboxFilter{})
	assert.ErrorIs(t, err, down)
	_, _, err = e.review(t).History(operations(), "pol-carolina")
	assert.ErrorIs(t, err, down)
}
