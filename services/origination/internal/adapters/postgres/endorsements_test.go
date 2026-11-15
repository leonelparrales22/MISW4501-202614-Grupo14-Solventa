package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// HU-W1.4-02: the store of the endorsements over PostgreSQL.

var analyst = domain.Actor{Subject: "sub-andres", Name: "Andrés Ruiz", Email: "operaciones@example.com"}

type endorsementFixture struct {
	*changeRequestFixture
	endorsements *postgres.EndorsementStore
}

func newEndorsementFixture(t *testing.T) *endorsementFixture {
	t.Helper()
	f := newChangeRequestFixture(t)
	return &endorsementFixture{changeRequestFixture: f, endorsements: postgres.NewEndorsementStore(f.db)}
}

// termsAfter is what applying the request leaves: the policy pol-1 with the
// requested change.
func termsAfter(r domain.ChangeRequest) domain.EndorsedTerms {
	p := samplePolicy(r.PolicyID, "sub-carolina", filedAt.AddDate(0, 10, 0))
	current := domain.PolicyTerms{InsuredAmount: p.InsuredAmount, Currency: p.Currency, Beneficiaries: []domain.Beneficiary{},
		Onerous: &domain.OnerousBeneficiary{PartnerID: "aliado-banco-1", Name: "Banco Aliado", Basis: domain.OnerousBasisOutstandingBalance},
		Contact: domain.ContactData{Email: strPtr("carolina@example.com")}}
	rated := domain.Rating{Premium: 160_000, Coverages: []domain.Coverage{{Code: domain.CoverageDeath, InsuredAmount: 80_000_000}}}
	return domain.ApplyChange(domain.TermsOf(p, current), r, rated)
}

var passed = domain.Validations{{Code: domain.ValidationRequesterIsHolder, Outcome: domain.ValidationPassed,
	Params: map[string]string{"channel": "web"}}}

func (f *endorsementFixture) apply(t *testing.T, r domain.ChangeRequest, base int, at time.Time) (domain.Endorsement, error) {
	t.Helper()
	return f.endorsements.Apply(context.Background(), ports.AppliedEndorsement{Request: r, BaseVersion: base, Terms: termsAfter(r),
		PreviousPremium: 142_300.5, Validations: passed, By: analyst, At: at})
}

func (f *endorsementFixture) status(t *testing.T, id string) domain.ChangeRequest {
	t.Helper()
	r, err := f.store.Get(context.Background(), id)
	require.NoError(t, err)
	return r
}

func TestApplyStoresTheEndorsementAndThePolicyReadsItsTerms(t *testing.T) {
	f := newEndorsementFixture(t)
	ctx := context.Background()
	r := f.file(t, changeRequest("pol-1", domain.ChangeInsuredAmount, filedAt))
	started, err := f.endorsements.StartReview(ctx, r.ID, analyst, filedAt.Add(time.Hour))
	require.NoError(t, err)
	assert.True(t, started)

	e, err := f.apply(t, r, 0, filedAt.Add(2*time.Hour))

	require.NoError(t, err)
	assert.Equal(t, "END-2026-0001", e.ID)
	assert.Equal(t, 1, e.Version)
	assert.Equal(t, domain.ChangeRequestApplied, f.status(t, r.ID).Status)
	var amount, premium int64
	require.NoError(t, f.db.QueryRowContext(ctx, `SELECT insured_amount_minor, premium_minor FROM policy_current WHERE policy_id = 'pol-1'`).
		Scan(&amount, &premium))
	assert.Equal(t, int64(8_000_000_000), amount, "the view lays the endorsed insured value over the policy")
	assert.Equal(t, int64(16_000_000), premium)
	latest, err := f.endorsements.LatestTerms(ctx, "pol-1")
	require.NoError(t, err)
	assert.True(t, latest.Found && latest.Own)
	assert.Equal(t, 1, latest.Version)
	assert.InDelta(t, 80_000_000, latest.Terms.InsuredAmount, 0)
	rec, err := f.endorsements.Record(ctx, r.ID)
	require.NoError(t, err)
	require.NotNil(t, rec.Endorsement)
	assert.Equal(t, e.ID, rec.Endorsement.ID)
	assert.Equal(t, analyst, rec.Endorsement.By)
	require.NotNil(t, rec.LastAttempt)
	assert.Equal(t, domain.OutcomeApplied, rec.LastAttempt.Outcome)
	assert.Equal(t, passed, rec.LastAttempt.Validations)
	require.NotNil(t, rec.Last(domain.ActionReview))
	assert.Equal(t, analyst, rec.Last(domain.ActionReview).By)
	count, err := f.endorsements.Endorsements(ctx, "pol-1")
	require.NoError(t, err)
	assert.Equal(t, 1, count.Count)
	assert.Equal(t, e.ID, count.Last.ID)
}

func TestTwoEndorsementsNeverStartFromTheSameVersion(t *testing.T) {
	f := newEndorsementFixture(t)
	amount := f.file(t, changeRequest("pol-1", domain.ChangeInsuredAmount, filedAt))
	contact := f.file(t, changeRequest("pol-1", domain.ChangePersonalData, filedAt))
	_, err := f.apply(t, amount, 0, filedAt.Add(time.Hour))
	require.NoError(t, err)

	_, err = f.apply(t, contact, 0, filedAt.Add(time.Hour))

	assert.ErrorIs(t, err, ports.ErrPolicyChanged)
	assert.Equal(t, domain.ChangeRequestFiled, f.status(t, contact.ID).Status, "nothing changed")
	e, err := f.apply(t, contact, 1, filedAt.Add(2*time.Hour))
	require.NoError(t, err)
	assert.Equal(t, "END-2026-0002", e.ID, "the losing attempt gave its number back")
	assert.Equal(t, 2, e.Version)
}

func TestEveryDecisionClaimsTheRequest(t *testing.T) {
	f := newEndorsementFixture(t)
	ctx := context.Background()
	at := filedAt.Add(time.Hour)
	applied := f.file(t, changeRequest("pol-1", domain.ChangeInsuredAmount, filedAt))
	_, err := f.apply(t, applied, 0, at)
	require.NoError(t, err)

	_, err = f.apply(t, applied, 1, at)
	assert.ErrorIs(t, err, ports.ErrRequestAlreadyDecided)
	_, err = f.endorsements.StartReview(ctx, applied.ID, analyst, at)
	assert.ErrorIs(t, err, ports.ErrRequestAlreadyDecided)
	assert.ErrorIs(t, f.endorsements.Reject(ctx, applied.ID, "No procede el cambio", analyst, at), ports.ErrRequestAlreadyDecided)
	assert.ErrorIs(t, f.endorsements.Return(ctx, applied.ID, "No procede el cambio", analyst, at), ports.ErrNotReturnable)
	_, err = f.endorsements.StartReview(ctx, "SOL-MOD-2026-9999", analyst, at)
	assert.ErrorIs(t, err, ports.ErrChangeRequestNotFound)
	assert.ErrorIs(t, f.endorsements.Reject(ctx, "SOL-MOD-2026-9999", "No procede el cambio", analyst, at), ports.ErrChangeRequestNotFound)

	reviewed := f.file(t, changeRequest("pol-2", domain.ChangeBeneficiaries, filedAt))
	started, err := f.endorsements.StartReview(ctx, reviewed.ID, analyst, at)
	require.NoError(t, err)
	assert.True(t, started)
	started, err = f.endorsements.StartReview(ctx, reviewed.ID, analyst, at)
	require.NoError(t, err)
	assert.False(t, started, "taking it again changes nothing")
	require.NoError(t, f.endorsements.Reject(ctx, reviewed.ID, "El parentesco no permite la designación", analyst, at))
	r := f.status(t, reviewed.ID)
	assert.Equal(t, domain.ChangeRequestRejected, r.Status)
	assert.Equal(t, "El parentesco no permite la designación", r.DecisionReason)
	assert.Equal(t, domain.ChangeRequestRejected, r.InsuredStatus())
}

func TestANotAppliedRequestIsRecordedAndThenReturned(t *testing.T) {
	f := newEndorsementFixture(t)
	ctx := context.Background()
	r := f.file(t, changeRequest("pol-1", domain.ChangeBeneficiaries, filedAt))
	failed := domain.Validations{{Code: domain.ValidationIdentityDocuments, Outcome: domain.ValidationFailed,
		Params: map[string]string{"checks": "2", "not_found": "1"}}}

	a, err := f.endorsements.RecordNotApplied(ctx, domain.EndorsementAttempt{RequestID: r.ID, PolicyID: r.PolicyID,
		Validations: failed, By: analyst, At: filedAt.Add(time.Hour)})

	require.NoError(t, err)
	assert.NotEmpty(t, a.ID)
	got := f.status(t, r.ID)
	assert.Equal(t, domain.ChangeRequestNotApplied, got.Status)
	assert.Equal(t, domain.ChangeRequestInReview, got.InsuredStatus(), "the insured sees it in review until it is returned")
	_, err = f.endorsements.RecordNotApplied(ctx, domain.EndorsementAttempt{RequestID: r.ID, PolicyID: r.PolicyID,
		Validations: failed, By: analyst, At: filedAt.Add(time.Hour)})
	assert.ErrorIs(t, err, ports.ErrRequestAlreadyDecided)
	latest, err := f.endorsements.LatestTerms(ctx, "pol-1")
	require.NoError(t, err)
	assert.False(t, latest.Found, "a request not applied leaves the policy as it was")

	require.NoError(t, f.endorsements.Return(ctx, r.ID, "Corrige el documento de Lucía y radica de nuevo", analyst, filedAt.Add(2*time.Hour)))
	assert.ErrorIs(t, f.endorsements.Return(ctx, r.ID, "Corrige el documento de Lucía y radica de nuevo", analyst,
		filedAt.Add(2*time.Hour)), ports.ErrNotReturnable)

	got = f.status(t, r.ID)
	assert.Equal(t, domain.ChangeRequestRejected, got.InsuredStatus())
	assert.Equal(t, "Corrige el documento de Lucía y radica de nuevo", got.DecisionReason)
	rec, err := f.endorsements.Record(ctx, r.ID)
	require.NoError(t, err)
	assert.Nil(t, rec.Endorsement)
	require.NotNil(t, rec.LastAttempt)
	assert.Equal(t, failed, rec.LastAttempt.Validations)
	require.NotNil(t, rec.Last(domain.ActionReturn))
	assert.Equal(t, "Corrige el documento de Lucía y radica de nuevo", rec.Last(domain.ActionReturn).Observations)
}

func TestTheEndorsementTablesOnlyReceiveRows(t *testing.T) {
	f := newEndorsementFixture(t)
	ctx := context.Background()
	r := f.file(t, changeRequest("pol-1", domain.ChangeInsuredAmount, filedAt))
	_, err := f.endorsements.StartReview(ctx, r.ID, analyst, filedAt)
	require.NoError(t, err)
	_, err = f.apply(t, r, 0, filedAt.Add(time.Hour))
	require.NoError(t, err)

	for _, stmt := range []string{
		`UPDATE endorsement SET premium_minor = 1`, `DELETE FROM endorsement`, `TRUNCATE endorsement CASCADE`,
		`UPDATE endorsement_attempt SET outcome = 'not_applied'`, `DELETE FROM endorsement_attempt`, `TRUNCATE endorsement_attempt CASCADE`,
		`UPDATE change_request_action SET actor_name = 'otro'`, `DELETE FROM change_request_action`, `TRUNCATE change_request_action`,
		`DELETE FROM endorsement_notice`, `UPDATE endorsement_notice SET outcome = 'rejected'`,
	} {
		_, err := f.db.ExecContext(ctx, stmt)
		assert.Error(t, err, stmt)
	}
}

func TestARenewalInheritsTheTermsOfThePolicyItRenews(t *testing.T) {
	f := newEndorsementFixture(t)
	ctx := context.Background()
	r := f.file(t, changeRequest("pol-1", domain.ChangeBeneficiaries, filedAt))
	_, err := f.apply(t, r, 0, filedAt.Add(time.Hour))
	require.NoError(t, err)
	renewed := samplePolicy("pol-1r", "sub-carolina", filedAt.AddDate(1, 10, 0))
	renewed.RenewalOf = "pol-1"
	require.NoError(t, f.policies.Create(ctx, renewed))

	latest, err := f.endorsements.LatestTerms(ctx, "pol-1r")

	require.NoError(t, err)
	assert.True(t, latest.Found)
	assert.False(t, latest.Own, "the beneficiaries come from the policy it renews")
	assert.Zero(t, latest.Version, "its own first endorsement is version 1")
	assert.Len(t, latest.Terms.Beneficiaries, 2)
	none, err := f.endorsements.LatestTerms(ctx, "pol-diego")
	require.NoError(t, err)
	assert.False(t, none.Found)
}

func TestTheInboxFiltersAndCountsInTheTimeOfColombia(t *testing.T) {
	f := newEndorsementFixture(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 17, 15, 0, 0, 0, time.UTC) // 10:00 in Colombia
	old := f.file(t, changeRequest("pol-1", domain.ChangeBeneficiaries, now.Add(-30*time.Hour)))
	recent := f.file(t, changeRequest("pol-1", domain.ChangeInsuredAmount, now.Add(-2*time.Hour)))
	notApplied := f.file(t, changeRequest("pol-2", domain.ChangeBeneficiaries, now.Add(-3*time.Hour)))
	returned := f.file(t, changeRequest("pol-diego", domain.ChangeBeneficiaries, now.Add(-4*time.Hour)))
	rejected := f.file(t, changeRequest("pol-2", domain.ChangeInsuredAmount, now.Add(-5*time.Hour)))
	applied := f.file(t, changeRequest("pol-2", domain.ChangePersonalData, now.Add(-6*time.Hour)))
	_, err := f.endorsements.StartReview(ctx, recent.ID, analyst, now.Add(-time.Hour))
	require.NoError(t, err)
	failed := domain.Validations{{Code: domain.ValidationIdentityDocuments, Outcome: domain.ValidationFailed}}
	for _, id := range []string{notApplied.ID, returned.ID} {
		policyID := map[string]string{notApplied.ID: "pol-2", returned.ID: "pol-diego"}[id]
		_, err := f.endorsements.RecordNotApplied(ctx, domain.EndorsementAttempt{RequestID: id, PolicyID: policyID,
			Validations: failed, By: analyst, At: now.Add(-time.Hour)})
		require.NoError(t, err)
	}
	require.NoError(t, f.endorsements.Return(ctx, returned.ID, "Corrige el documento y radica", analyst, now.Add(-time.Hour)))
	require.NoError(t, f.endorsements.Reject(ctx, rejected.ID, "La suma no procede para el crédito", analyst, now.Add(-time.Hour)))
	_, err = f.apply(t, applied, 0, now.Add(-time.Hour)) // 09:00 in Colombia: today
	require.NoError(t, err)

	page, err := f.endorsements.Inbox(ctx, domain.EndorsementInboxFilter{Now: now}.WithDefaults())

	require.NoError(t, err)
	ids := func(p domain.EndorsementInboxPage) []string {
		out := []string{}
		for _, it := range p.Items {
			out = append(out, it.Request.ID)
		}
		return out
	}
	assert.Equal(t, []string{recent.ID, notApplied.ID, old.ID}, ids(page), "pending: filed, in review and not returned, latest filed first")
	assert.Equal(t, 3, page.Total)
	assert.Equal(t, "Carolina Pérez", page.Items[0].HolderName)
	assert.Equal(t, "1032456789", page.Items[0].HolderDocument)
	ind := page.Indicators
	assert.Equal(t, 3, ind.Pending)
	assert.Equal(t, 1, ind.PendingOverdue, "only the one filed 30 h ago waits more than 24 h")
	assert.Equal(t, 1, ind.AppliedToday)
	require.NotNil(t, ind.MeanTimeToApply)
	assert.Equal(t, 5*time.Hour, *ind.MeanTimeToApply)
	assert.Equal(t, 3, ind.RejectedRecently, "rejected, not applied and returned")
	assert.Equal(t, string(domain.ValidationIdentityDocuments), ind.TopReason)

	for _, tc := range []struct {
		f    domain.EndorsementInboxFilter
		want []string
	}{
		{domain.EndorsementInboxFilter{Status: domain.InboxAll}, []string{recent.ID, notApplied.ID, returned.ID, rejected.ID, applied.ID, old.ID}},
		{domain.EndorsementInboxFilter{Status: domain.InboxStatus(domain.ChangeRequestNotApplied)}, []string{notApplied.ID, returned.ID}},
		{domain.EndorsementInboxFilter{Status: domain.InboxAll, ChangeType: domain.ChangeInsuredAmount}, []string{recent.ID, rejected.ID}},
		{domain.EndorsementInboxFilter{Status: domain.InboxAll, FiledFrom: time.Date(2026, 9, 17, 0, 0, 0, 0, domain.ColombiaTime)},
			[]string{recent.ID, notApplied.ID, returned.ID, rejected.ID, applied.ID}},
		{domain.EndorsementInboxFilter{Status: domain.InboxAll, Page: 2, PageSize: 4}, []string{applied.ID, old.ID}},
	} {
		tc.f.Now = now
		page, err := f.endorsements.Inbox(ctx, tc.f.WithDefaults())
		require.NoError(t, err)
		assert.Equal(t, tc.want, ids(page), "%+v", tc.f)
	}
}

func TestTheHistoryListsTheEndorsementsAndTheAttemptsNewestFirst(t *testing.T) {
	f := newEndorsementFixture(t)
	ctx := context.Background()
	amount := f.file(t, changeRequest("pol-1", domain.ChangeInsuredAmount, filedAt))
	beneficiaries := f.file(t, changeRequest("pol-1", domain.ChangeBeneficiaries, filedAt))
	_, err := f.apply(t, amount, 0, filedAt.Add(time.Hour))
	require.NoError(t, err)
	_, err = f.endorsements.RecordNotApplied(ctx, domain.EndorsementAttempt{RequestID: beneficiaries.ID, PolicyID: "pol-1",
		Validations: domain.Validations{{Code: domain.ValidationIdentityDocuments, Outcome: domain.ValidationFailed}},
		By:          analyst, At: filedAt.Add(2 * time.Hour)})
	require.NoError(t, err)

	got, err := f.endorsements.History(ctx, "pol-1")

	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, domain.OutcomeNotApplied, got[0].Outcome)
	assert.Equal(t, []domain.ValidationCode{domain.ValidationIdentityDocuments}, got[0].Failed)
	assert.Equal(t, beneficiaries.ID, got[0].Request.ID)
	assert.Equal(t, domain.OutcomeApplied, got[1].Outcome)
	assert.Equal(t, "END-2026-0001", got[1].EndorsementID)
	assert.InDelta(t, 142_300.5, got[1].PreviousPremium, 0)
	assert.InDelta(t, 160_000, got[1].NewPremium, 0)
	assert.Equal(t, analyst, got[1].By)
	empty, err := f.endorsements.History(ctx, "pol-diego")
	require.NoError(t, err)
	assert.Empty(t, empty)
}

func TestTheNoticeOfEachDecisionIsDueUntilItIsSent(t *testing.T) {
	f := newEndorsementFixture(t)
	ctx := context.Background()
	at := filedAt.Add(time.Hour)
	applied := f.file(t, changeRequest("pol-1", domain.ChangeInsuredAmount, filedAt))
	rejected := f.file(t, changeRequest("pol-2", domain.ChangeInsuredAmount, filedAt))
	e, err := f.apply(t, applied, 0, at)
	require.NoError(t, err)
	require.NoError(t, f.endorsements.Reject(ctx, rejected.ID, "La suma no procede para el crédito", analyst, at))

	due, err := f.endorsements.ListDueNotices(ctx, at, 10)

	require.NoError(t, err)
	require.Len(t, due, 2)
	byID := map[string]domain.EndorsementNotice{due[0].RequestID: due[0], due[1].RequestID: due[1]}
	assert.Equal(t, domain.NoticeApplied, byID[applied.ID].Outcome)
	assert.Equal(t, e.ID, byID[applied.ID].EndorsementID)
	assert.Equal(t, "carolina@example.com", byID[applied.ID].To)
	assert.Equal(t, "Carolina Pérez", byID[applied.ID].Name)
	assert.Equal(t, domain.NoticeRejected, byID[rejected.ID].Outcome)
	assert.Equal(t, "La suma no procede para el crédito", byID[rejected.ID].Observations)
	none, err := f.endorsements.ListDueNotices(ctx, at.Add(-time.Second), 10)
	require.NoError(t, err)
	assert.Empty(t, none, "not due before the decision")

	require.NoError(t, f.endorsements.UpdateNotice(ctx, applied.ID, domain.NoticeDelivery{Status: domain.NotificationSent,
		Attempts: 0, SentAt: at}))
	require.NoError(t, f.endorsements.UpdateNotice(ctx, rejected.ID, domain.NoticeDelivery{Status: domain.NotificationPending,
		Attempts: 1, NextAttemptAt: at.Add(time.Minute), LastFailureAt: at, LastFailure: "unavailable"}))
	due, err = f.endorsements.ListDueNotices(ctx, at.Add(30*time.Second), 10)
	require.NoError(t, err)
	assert.Empty(t, due)
	due, err = f.endorsements.ListDueNotices(ctx, at.Add(time.Minute), 10)
	require.NoError(t, err)
	require.Len(t, due, 1)
	assert.Equal(t, 1, due[0].Delivery.Attempts)
	assert.Equal(t, "unavailable", due[0].Delivery.LastFailure)
}

func TestEndorsementStoreReportsTheFailuresOfTheDatabase(t *testing.T) {
	f := newEndorsementFixture(t)
	ctx := context.Background()
	r := f.file(t, changeRequest("pol-1", domain.ChangeInsuredAmount, filedAt))
	require.NoError(t, f.db.Close())

	_, err := f.endorsements.Inbox(ctx, domain.EndorsementInboxFilter{Now: filedAt}.WithDefaults())
	assert.Error(t, err)
	_, err = f.endorsements.LatestTerms(ctx, "pol-1")
	assert.Error(t, err)
	_, err = f.endorsements.Record(ctx, r.ID)
	assert.Error(t, err)
	_, err = f.endorsements.Endorsements(ctx, "pol-1")
	assert.Error(t, err)
	_, err = f.endorsements.StartReview(ctx, r.ID, analyst, filedAt)
	assert.Error(t, err)
	_, err = f.apply(t, r, 0, filedAt)
	assert.Error(t, err)
	_, err = f.endorsements.RecordNotApplied(ctx, domain.EndorsementAttempt{RequestID: r.ID, PolicyID: "pol-1", By: analyst, At: filedAt})
	assert.Error(t, err)
	assert.Error(t, f.endorsements.Reject(ctx, r.ID, "No procede el cambio", analyst, filedAt))
	assert.Error(t, f.endorsements.Return(ctx, r.ID, "No procede el cambio", analyst, filedAt))
	_, err = f.endorsements.History(ctx, "pol-1")
	assert.Error(t, err)
	_, err = f.endorsements.ListDueNotices(ctx, filedAt, 10)
	assert.Error(t, err)
	assert.Error(t, f.endorsements.UpdateNotice(ctx, r.ID, domain.NoticeDelivery{Status: domain.NotificationSent}))
}
