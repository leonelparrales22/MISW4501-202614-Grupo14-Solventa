package postgres_test

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// filedAt is when the requests are filed: 16 September 2026, 07:00 in Colombia.
var filedAt = time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)

type changeRequestFixture struct {
	db       *sql.DB
	store    *postgres.ChangeRequestStore
	policies *postgres.PolicyStore
}

func newChangeRequestFixture(t *testing.T) *changeRequestFixture {
	t.Helper()
	requireOfferDocker(t)
	db := newDB(t)
	f := &changeRequestFixture{db: db, store: postgres.NewChangeRequestStore(db), policies: postgres.NewPolicyStore(db)}
	for _, p := range []domain.Policy{
		samplePolicy("pol-1", "sub-carolina", filedAt.AddDate(0, 10, 0)),
		samplePolicy("pol-2", "sub-carolina", filedAt.AddDate(0, 11, 0)),
		samplePolicy("pol-diego", "sub-diego", filedAt.AddDate(0, 9, 0)),
	} {
		require.NoError(t, f.policies.Create(context.Background(), p))
	}
	return f
}

func strPtr(s string) *string { return &s }

// changeRequest is a request of carolina on the policy, of the type, filed at.
func changeRequest(policyID string, t domain.ChangeType, at time.Time) domain.ChangeRequest {
	r := domain.ChangeRequest{PolicyID: policyID, CustomerID: "sub-carolina", Type: t, Channel: domain.ChannelWeb,
		RequestedBy: domain.Requester{Subject: "sub-carolina", Email: "carolina@example.com", Name: "Carolina Pérez"},
		FiledAt:     at, Status: domain.ChangeRequestFiled, StatusChangedAt: at}
	switch t {
	case domain.ChangeBeneficiaries:
		r.Previous = domain.ChangeContent{Beneficiaries: []domain.Beneficiary{},
			Onerous: &domain.OnerousBeneficiary{PartnerID: "aliado-banco-1", Name: "Banco Aliado", Basis: domain.OnerousBasisOutstandingBalance}}
		r.Requested = domain.ChangeContent{Beneficiaries: []domain.Beneficiary{
			{FullName: "Mateo Pérez Gómez", Relationship: domain.RelationshipChild, DocumentType: domain.DocumentTI, DocumentNumber: "1140223001", ShareBasisPoints: 6000},
			{FullName: "Lucía Pérez Gómez", Relationship: domain.RelationshipChild, DocumentType: domain.DocumentTI, DocumentNumber: "1142998310", ShareBasisPoints: 4000},
		}}
	case domain.ChangeInsuredAmount:
		r.Previous = domain.ChangeContent{InsuredAmount: 68_000_000, Currency: "COP"}
		r.Requested = domain.ChangeContent{InsuredAmount: 80_000_000, Currency: "COP"}
	default:
		r.Previous = domain.ChangeContent{Contact: domain.ContactData{Email: strPtr("carolina@example.com")}}
		r.Requested = domain.ChangeContent{Contact: domain.ContactData{Address: strPtr("Calle 10 # 43-20"), Phone: strPtr("3001234567")}}
	}
	return r
}

func (f *changeRequestFixture) file(t *testing.T, r domain.ChangeRequest) domain.ChangeRequest {
	t.Helper()
	stored, err := f.store.Create(context.Background(), r)
	require.NoError(t, err)
	return stored
}

// decide does what HU-W1.4-02 will do: it changes the status of a request.
func (f *changeRequestFixture) decide(t *testing.T, id string, status domain.ChangeRequestStatus, reason string, at time.Time) {
	t.Helper()
	var r sql.NullString
	if reason != "" {
		r = sql.NullString{String: reason, Valid: true}
	}
	_, err := f.db.ExecContext(context.Background(),
		`UPDATE change_request SET status = $2, decision_reason = $3, status_changed_at = $4 WHERE request_id = $1`,
		id, string(status), r, at)
	require.NoError(t, err)
}

func TestChangeRequestStoreKeepsEveryTypeAsItWasFiled(t *testing.T) {
	f := newChangeRequestFixture(t)
	ctx := context.Background()
	for i, typ := range domain.ChangeTypes {
		r := changeRequest("pol-1", typ, filedAt.Add(time.Duration(i)*time.Minute+123*time.Nanosecond))

		stored := f.file(t, r)

		r.ID = domain.FormatChangeRequestID(2026, i+1)
		r.FiledAt, r.StatusChangedAt = r.FiledAt.Truncate(time.Microsecond), r.StatusChangedAt.Truncate(time.Microsecond)
		assert.Equal(t, r, stored)
		got, err := f.store.Get(ctx, stored.ID)
		require.NoError(t, err)
		assert.Equal(t, stored, got, "%s reads back as it was filed", typ)
	}

	_, err := f.store.Get(ctx, "SOL-MOD-2026-0404")
	assert.ErrorIs(t, err, ports.ErrChangeRequestNotFound)
}

func TestChangeRequestNumbersRestartEveryCivilYearOfColombia(t *testing.T) {
	f := newChangeRequestFixture(t)
	// 31 December 2026, 23:00 in Colombia is already 2027 in UTC.
	lastOf2026 := time.Date(2027, 1, 1, 4, 0, 0, 0, time.UTC)

	assert.Equal(t, "SOL-MOD-2026-0001", f.file(t, changeRequest("pol-1", domain.ChangeBeneficiaries, filedAt)).ID)
	assert.Equal(t, "SOL-MOD-2026-0002", f.file(t, changeRequest("pol-1", domain.ChangeInsuredAmount, lastOf2026)).ID)
	assert.Equal(t, "SOL-MOD-2027-0001", f.file(t, changeRequest("pol-1", domain.ChangePersonalData, lastOf2026.Add(time.Hour))).ID)
	assert.Equal(t, "SOL-MOD-2027-0002", f.file(t, changeRequest("pol-2", domain.ChangePersonalData, lastOf2026.Add(2*time.Hour))).ID)
}

func TestARequestThatLosesToAnOpenOneGivesItsNumberBack(t *testing.T) {
	f := newChangeRequestFixture(t)
	ctx := context.Background()
	first := f.file(t, changeRequest("pol-1", domain.ChangeBeneficiaries, filedAt))

	_, err := f.store.Create(ctx, changeRequest("pol-1", domain.ChangeBeneficiaries, filedAt.Add(time.Minute)))

	var open *ports.ChangeRequestOpenError
	require.ErrorAs(t, err, &open)
	assert.ErrorIs(t, err, ports.ErrChangeRequestOpen)
	assert.Equal(t, first.ID, open.RequestID, "it names the open request")
	assert.Equal(t, "SOL-MOD-2026-0002", f.file(t, changeRequest("pol-1", domain.ChangeInsuredAmount, filedAt.Add(2*time.Minute))).ID,
		"no gap: the number of the request that was not stored is reused")
	var count int
	require.NoError(t, f.db.QueryRowContext(ctx, `SELECT count(*) FROM change_request`).Scan(&count))
	assert.Equal(t, 2, count)
}

func TestOfTwentyRequestsOfTheSamePolicyAndTypeFiledAtOnceOneIsStored(t *testing.T) {
	f := newChangeRequestFixture(t)
	const n = 20
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		created []string
		named   []string
		other   []error
	)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stored, err := f.store.Create(context.Background(), changeRequest("pol-1", domain.ChangeBeneficiaries, filedAt))
			mu.Lock()
			defer mu.Unlock()
			var open *ports.ChangeRequestOpenError
			switch {
			case err == nil:
				created = append(created, stored.ID)
			case errors.As(err, &open):
				named = append(named, open.RequestID)
			default:
				other = append(other, err)
			}
		}()
	}
	wg.Wait()

	require.Empty(t, other)
	require.Len(t, created, 1)
	assert.Equal(t, "SOL-MOD-2026-0001", created[0], "the losers give their numbers back")
	require.Len(t, named, n-1)
	for _, id := range named {
		assert.Equal(t, created[0], id, "every loser is told which request is open")
	}
}

func TestRequestsOfDifferentTypesFiledAtOnceAreAllStored(t *testing.T) {
	f := newChangeRequestFixture(t)
	var wg sync.WaitGroup
	errs := make([]error, len(domain.ChangeTypes))
	for i, typ := range domain.ChangeTypes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = f.store.Create(context.Background(), changeRequest("pol-1", typ, filedAt))
		}()
	}
	wg.Wait()

	for i, err := range errs {
		assert.NoError(t, err, domain.ChangeTypes[i])
	}
	open, err := f.store.ListOpen(context.Background(), "pol-1")
	require.NoError(t, err)
	assert.Len(t, open, 3)
}

func TestANotAppliedRequestHoldsThePlaceUntilItIsReturnedToTheInsured(t *testing.T) {
	f := newChangeRequestFixture(t)
	ctx := context.Background()
	first := f.file(t, changeRequest("pol-1", domain.ChangeInsuredAmount, filedAt))

	f.decide(t, first.ID, domain.ChangeRequestNotApplied, "", filedAt.Add(time.Hour))
	_, err := f.store.Create(ctx, changeRequest("pol-1", domain.ChangeInsuredAmount, filedAt.Add(2*time.Hour)))
	assert.ErrorIs(t, err, ports.ErrChangeRequestOpen, "not applied and not returned: still open (adjustment A-2)")

	f.decide(t, first.ID, domain.ChangeRequestNotApplied, "El avalúo no corresponde al inmueble", filedAt.Add(3*time.Hour))
	second := f.file(t, changeRequest("pol-1", domain.ChangeInsuredAmount, filedAt.Add(4*time.Hour)))
	assert.Equal(t, "SOL-MOD-2026-0002", second.ID, "returned to the insured: the policy is free again")

	f.decide(t, second.ID, domain.ChangeRequestApplied, "", filedAt.Add(5*time.Hour))
	f.file(t, changeRequest("pol-1", domain.ChangeInsuredAmount, filedAt.Add(6*time.Hour)))

	open, err := f.store.ListOpen(ctx, "pol-1")
	require.NoError(t, err)
	require.Len(t, open, 1)
	assert.Equal(t, "SOL-MOD-2026-0003", open[0].ID)
}

func TestTheContentOfARequestNeverChangesAndARequestIsNeverDeleted(t *testing.T) {
	f := newChangeRequestFixture(t)
	ctx := context.Background()
	r := f.file(t, changeRequest("pol-1", domain.ChangeBeneficiaries, filedAt))

	for _, stmt := range []string{
		`UPDATE change_request SET requested = '{"beneficiaries":[]}' WHERE request_id = $1`,
		`UPDATE change_request SET previous = '{}' WHERE request_id = $1`,
		`UPDATE change_request SET customer_id = 'sub-diego' WHERE request_id = $1`,
		`UPDATE change_request SET filed_at = now() WHERE request_id = $1`,
		`DELETE FROM change_request WHERE request_id = $1`,
	} {
		_, err := f.db.ExecContext(ctx, stmt, r.ID)
		assert.ErrorContains(t, err, "change_request", stmt)
	}
	_, err := f.db.ExecContext(ctx, `TRUNCATE change_request`)
	assert.Error(t, err)
	_, err = f.db.ExecContext(ctx, `UPDATE change_request SET decision_reason = '  ' WHERE request_id = $1`, r.ID)
	assert.Error(t, err, "no reason is NULL, never an empty text")

	f.decide(t, r.ID, domain.ChangeRequestRejected, "Falta el documento del beneficiario", filedAt.Add(time.Hour))
	got, err := f.store.Get(ctx, r.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.ChangeRequestRejected, got.Status)
	assert.Equal(t, "Falta el documento del beneficiario", got.DecisionReason)
	assert.Equal(t, filedAt.Add(time.Hour), got.StatusChangedAt)
	assert.Equal(t, r.Requested, got.Requested, "only the status and the reason changed")
}

func idsOfRequests(rs []domain.ChangeRequest) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.ID
	}
	return out
}

func TestTheListOfTheInsuredFiltersPagesAndCountsWhatTheInsuredSees(t *testing.T) {
	f := newChangeRequestFixture(t)
	ctx := context.Background()
	day := 24 * time.Hour
	filed := f.file(t, changeRequest("pol-1", domain.ChangeBeneficiaries, filedAt.Add(-10*day)))   // 0001
	inReview := f.file(t, changeRequest("pol-1", domain.ChangeInsuredAmount, filedAt.Add(-9*day))) // 0002
	applied := f.file(t, changeRequest("pol-1", domain.ChangePersonalData, filedAt.Add(-8*day)))   // 0003
	rejected := f.file(t, changeRequest("pol-2", domain.ChangeBeneficiaries, filedAt.Add(-7*day))) // 0004
	working := f.file(t, changeRequest("pol-2", domain.ChangeInsuredAmount, filedAt.Add(-6*day)))  // 0005
	returned := f.file(t, changeRequest("pol-2", domain.ChangePersonalData, filedAt.Add(-5*day)))  // 0006
	diego := changeRequest("pol-diego", domain.ChangeInsuredAmount, filedAt.Add(-4*day))
	diego.CustomerID, diego.RequestedBy.Subject = "sub-diego", "sub-diego"
	f.file(t, diego) // 0007
	f.decide(t, applied.ID, domain.ChangeRequestApplied, "", filedAt.Add(-8*day+time.Hour))
	appliedLastYear := f.file(t, changeRequest("pol-1", domain.ChangePersonalData, time.Date(2025, 12, 1, 15, 0, 0, 0, time.UTC)))

	f.decide(t, inReview.ID, domain.ChangeRequestInReview, "", filedAt.Add(-9*day+time.Hour))
	f.decide(t, rejected.ID, domain.ChangeRequestRejected, "Falta el documento", filedAt.Add(-7*day+time.Hour))
	f.decide(t, working.ID, domain.ChangeRequestNotApplied, "", filedAt.Add(-6*day+time.Hour))
	f.decide(t, returned.ID, domain.ChangeRequestNotApplied, "El teléfono no es del titular", filedAt.Add(-5*day+time.Hour))
	f.decide(t, appliedLastYear.ID, domain.ChangeRequestApplied, "", time.Date(2025, 12, 2, 15, 0, 0, 0, time.UTC))

	list := func(filter domain.ChangeRequestFilter) domain.ChangeRequestPage {
		t.Helper()
		filter.Now = filedAt
		page, err := f.store.ListByCustomer(ctx, "sub-carolina", filter.WithDefaults())
		require.NoError(t, err)
		return page
	}

	all := list(domain.ChangeRequestFilter{})
	assert.Equal(t, []string{returned.ID, working.ID, rejected.ID, applied.ID, inReview.ID, filed.ID, appliedLastYear.ID}, idsOfRequests(all.Items),
		"the latest filed first, never another insured's")
	assert.Equal(t, 7, all.Total)
	assert.Equal(t, []string{"pol-1", "pol-2"}, all.Policies)

	for status, want := range map[domain.ChangeRequestStatus][]string{
		domain.ChangeRequestFiled:    {filed.ID},
		domain.ChangeRequestInReview: {working.ID, inReview.ID},
		domain.ChangeRequestApplied:  {applied.ID, appliedLastYear.ID},
		domain.ChangeRequestRejected: {returned.ID, rejected.ID},
	} {
		page := list(domain.ChangeRequestFilter{Status: status})
		assert.Equal(t, want, idsOfRequests(page.Items), status)
		assert.Equal(t, len(want), page.Total, status)
	}

	assert.Equal(t, []string{returned.ID, working.ID, rejected.ID}, idsOfRequests(list(domain.ChangeRequestFilter{PolicyID: "pol-2"}).Items))
	assert.Empty(t, list(domain.ChangeRequestFilter{PolicyID: "pol-diego"}).Items, "the policy of another insured lists nothing")

	since, err := domain.ParseCivilDay("2026-09-09") // filedAt - 7 days is 9 September, 07:00 in Colombia
	require.NoError(t, err)
	assert.Equal(t, []string{returned.ID, working.ID, rejected.ID}, idsOfRequests(list(domain.ChangeRequestFilter{FiledFrom: since}).Items),
		"the first day is included")

	second := list(domain.ChangeRequestFilter{Page: 2, PageSize: 3})
	assert.Equal(t, []string{applied.ID, inReview.ID, filed.ID}, idsOfRequests(second.Items))
	assert.Equal(t, 7, second.Total)
	assert.Equal(t, 3, second.TotalPages())

	ind := list(domain.ChangeRequestFilter{Status: domain.ChangeRequestFiled}).Indicators
	assert.Equal(t, 3, ind.Open, "filed, in review and not applied without reason, whatever the filter")
	require.NotNil(t, ind.LatestOpen)
	assert.Equal(t, working.ID, ind.LatestOpen.ID)
	assert.Equal(t, domain.ChangeInsuredAmount, ind.LatestOpen.Type)
	assert.Equal(t, 1, ind.AppliedThisYear, "only the civil year of the query")
	require.NotNil(t, ind.LastApplied)
	assert.Equal(t, applied.ID, ind.LastApplied.ID)
	assert.Equal(t, filedAt.Add(-8*day+time.Hour), ind.LastApplied.StatusChangedAt)
	assert.Equal(t, 2, ind.Rejected, "rejected and returned")
	require.NotNil(t, ind.LastRejected)
	assert.Equal(t, returned.ID, ind.LastRejected.ID)
}

func TestTheListOfAnInsuredWithoutRequestsIsEmpty(t *testing.T) {
	f := newChangeRequestFixture(t)

	page, err := f.store.ListByCustomer(context.Background(), "sub-nobody", domain.ChangeRequestFilter{Now: filedAt}.WithDefaults())

	require.NoError(t, err)
	assert.Empty(t, page.Items)
	assert.NotNil(t, page.Items)
	assert.Zero(t, page.Total)
	assert.Equal(t, domain.ChangeRequestIndicators{}, page.Indicators)
	assert.Equal(t, []string{}, page.Policies)
}
