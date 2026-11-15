// This file checks each acceptance criterion of the change request of the
// insured (HU-W1.4-01) end to end, in process: the routes over HTTP, the use
// case of endorsements and the stores over a real PostgreSQL (Testcontainers),
// with policies sold from a stored offer and activated by their charge, as the
// premium charge of HA-D02 leaves them. The statuses that HU-W1.4-02 will
// write are set here with SQL on the test database.
package integration_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/endorsements"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/profiling"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/renewal"
)

const diego = "insured:diego"

// changeEnv is the environment of the online renewal (its database, policies,
// charges and clock) with the change requests mounted over it.
type changeEnv struct {
	*renewalEnv
	api    *acceptanceEnv
	offers *postgres.OfferStore
}

func newChangeEnv(t *testing.T) *changeEnv {
	t.Helper()
	e := &changeEnv{renewalEnv: newRenewalEnv(t)}
	e.offers = postgres.NewOfferStore(e.db)
	customers := postgres.NewCustomerStore(e.db)
	svc, err := endorsements.NewService(endorsements.Config{
		Policies: e.policies, Requests: postgres.NewChangeRequestStore(e.db),
		Origins: renewal.OriginFinder{Policies: e.policies, Charges: e.charges, Offers: e.offers, BySubject: customers,
			ByDocument: customers},
		PartnerNames: map[string]string{"aliado-banco-1": "Banco Aliado"}, Clock: e.clock,
	})
	require.NoError(t, err)
	srv := httptest.NewServer(httpadapter.NewRouter(profiling.NewResolver(nil, nil, e.clock),
		httpadapter.WithObservability("origination"),
		httpadapter.WithChangeRequests(svc, subjectVerifier{})))
	t.Cleanup(srv.Close)
	e.api = &acceptanceEnv{srv: srv}
	return e
}

func (e *changeEnv) call(t *testing.T, method, path, token, body string) (int, map[string]any) {
	t.Helper()
	return e.api.do(t, method, path, token, body)
}

// sold stores a policy of owner bought from an offer of the partner, whose
// coverage started started ago and ends in endsIn, with its confirmed charge.
func (e *changeEnv) sold(t *testing.T, id, owner, partner string, started, endsIn time.Duration) domain.Policy {
	t.Helper()
	ctx := context.Background()
	now := e.clock.Now().UTC().Truncate(time.Microsecond)
	holder := domain.Applicant{FullName: "Carolina Pérez", DocumentID: "1032456789", Email: owner + "@example.com"}
	terms := domain.CoverageTerms{InsuredAmount: 68_000_000, TermMonths: 12,
		Coverages: []domain.Coverage{{Code: domain.CoverageDeath, InsuredAmount: 68_000_000}}}
	offer := domain.PersonalizedOffer{ID: "off-" + id, CustomerID: "partner-" + id, Channel: domain.OfferChannelEmbedded,
		ConsentID: "cns-1", PartnerID: partner, ApplicationID: "app-" + id, Applicant: holder, Terms: terms,
		Premium: 142_300, Currency: "COP", Mode: domain.ModeNormal, BirthDate: time.Date(1985, 5, 1, 0, 0, 0, 0, time.UTC),
		CreatedAt: now.Add(-started - time.Hour), ExpiresAt: now.Add(-started)}
	_, _, err := e.offers.Create(ctx, offer)
	require.NoError(t, err)
	p := domain.Policy{ID: id, AcceptanceID: "acc-" + id, CustomerID: owner, Holder: holder, Premium: 142_300, Currency: "COP",
		InsuredAmount: 68_000_000, Coverages: terms.Coverages, CoverageStart: now.Add(-started), CoverageEnd: now.Add(endsIn)}
	require.NoError(t, e.policies.Create(ctx, p))
	c := domain.NewCharge(domain.Acceptance{ID: p.AcceptanceID, Channel: domain.ChannelWeb, AcceptedAt: now},
		domain.Offer{ID: offer.ID, CustomerID: owner, Applicant: holder, Premium: p.Premium, Currency: p.Currency}, terms, now, now.Add(time.Hour))
	_, _, err = e.charges.CreatePending(ctx, c)
	require.NoError(t, err)
	_, err = e.db.ExecContext(ctx, `UPDATE charge_attempt SET status = 'confirmed', next_attempt_at = NULL, confirmed_at = $2,
		policy_id = $3, coverage_start = $4, coverage_end = $5, activation_published_at = $2 WHERE acceptance_id = $1`,
		p.AcceptanceID, now, p.ID, p.CoverageStart, p.CoverageEnd)
	require.NoError(t, err)
	return p
}

// inForce is a policy of carolina sold by the partner of screen E1, two months
// into its year.
func (e *changeEnv) inForce(t *testing.T, id string) domain.Policy {
	t.Helper()
	return e.sold(t, id, "carolina", "aliado-banco-1", 60*24*time.Hour, 300*24*time.Hour)
}

// decide does what HU-W1.4-02 will do: it changes the status of a request.
func (e *changeEnv) decide(t *testing.T, id string, status domain.ChangeRequestStatus, reason string) {
	t.Helper()
	var r any
	if reason != "" {
		r = reason
	}
	_, err := e.db.ExecContext(context.Background(),
		`UPDATE change_request SET status = $2, decision_reason = $3, status_changed_at = now() WHERE request_id = $1`, id, string(status), r)
	require.NoError(t, err)
}

func (e *changeEnv) requests(t *testing.T) int {
	t.Helper()
	var n int
	require.NoError(t, e.db.QueryRowContext(context.Background(), `SELECT count(*) FROM change_request`).Scan(&n))
	return n
}

func (e *changeEnv) idOf(n int) string {
	return domain.FormatChangeRequestID(domain.ChangeRequestYear(e.clock.Now()), n)
}

const (
	twoBeneficiaries = `{"change_type":"beneficiaries","channel":"web","beneficiaries":[
		{"full_name":"Mateo Pérez Gómez","relationship":"child","document_type":"TI","document_number":"1140223001","share_percent":60},
		{"full_name":"Lucía Pérez Gómez","relationship":"child","document_type":"TI","document_number":"1142998310","share_percent":40}]}`
	newInsuredAmount = `{"change_type":"insured_amount","channel":"web","insured_amount":80000000}`
	newContact       = `{"change_type":"personal_data","channel":"web","personal_data":{"address":"Calle 10 # 43-20, Medellín","phone":"+57 300 123 4567"}}`
)

// CA 1: the insured opens a policy in force, picks the type of change and
// captures the new data.
func TestChangeRequest_CA1_TheInsuredPicksTheTypeAndCapturesTheNewData(t *testing.T) {
	e := newChangeEnv(t)
	p := e.inForce(t, "pol-cr1")

	code, terms := e.call(t, http.MethodGet, "/v1/policies/"+p.ID+"/current-terms", carolina, "")

	require.Equal(t, http.StatusOK, code, "%v", terms)
	assert.Equal(t, true, terms["policy"].(map[string]any)["in_force"])
	current := terms["terms"].(map[string]any)
	assert.InDelta(t, 68_000_000, current["insured_amount"], 0.001)
	assert.Equal(t, []any{}, current["beneficiaries"], "no beneficiaries designated for the remainder yet")
	assert.Equal(t, map[string]any{"partner_id": "aliado-banco-1", "name": "Banco Aliado", "basis": "outstanding_balance"},
		current["onerous_beneficiary"], "the lender of the original offer, by its display name")
	assert.Equal(t, map[string]any{"address": nil, "email": "carolina@example.com", "phone": nil}, current["personal_data"])
	assert.Equal(t, []any{}, terms["open_requests"])

	for i, body := range []string{twoBeneficiaries, newInsuredAmount, newContact} {
		code, r := e.call(t, http.MethodPost, "/v1/policies/"+p.ID+"/change-requests", carolina, body)
		require.Equal(t, http.StatusCreated, code, "%v", r)
		assert.Equal(t, e.idOf(i+1), r["request_id"])
		assert.Equal(t, "filed", r["status"])
	}

	code, terms = e.call(t, http.MethodGet, "/v1/policies/"+p.ID+"/current-terms", carolina, "")
	require.Equal(t, http.StatusOK, code)
	assert.Len(t, terms["open_requests"], 3, "one open request of each type")
}

// CA 2: filing answers the id, and the insured follows the request through
// every status, with the reason of a rejection.
func TestChangeRequest_CA2_TheInsuredGetsTheIDAndFollowsTheStatus(t *testing.T) {
	e := newChangeEnv(t)
	first := e.inForce(t, "pol-cr2a")
	second := e.inForce(t, "pol-cr2b")

	code, filed := e.call(t, http.MethodPost, "/v1/policies/"+first.ID+"/change-requests", carolina, twoBeneficiaries)
	require.Equal(t, http.StatusCreated, code)
	assert.Equal(t, e.idOf(1), filed["request_id"])
	assert.Equal(t, map[string]any{"email": "carolina@example.com", "name": nil}, filed["requested_by"])
	previous := filed["previous"].(map[string]any)
	assert.Equal(t, "Banco Aliado", previous["onerous_beneficiary"].(map[string]any)["name"], "the photo keeps the onerous beneficiary")
	assert.Len(t, filed["changes"], 2)

	code, got := e.call(t, http.MethodGet, "/v1/policies/change-requests/"+e.idOf(1), carolina, "")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, filed, got, "the detail is what was filed")

	ids := []string{e.idOf(1)}
	for _, call := range []struct{ policy, body string }{
		{first.ID, newInsuredAmount}, {first.ID, newContact}, {second.ID, twoBeneficiaries}, {second.ID, newInsuredAmount},
	} {
		code, r := e.call(t, http.MethodPost, "/v1/policies/"+call.policy+"/change-requests", carolina, call.body)
		require.Equal(t, http.StatusCreated, code, "%v", r)
		ids = append(ids, r["request_id"].(string))
	}
	assert.Equal(t, []string{e.idOf(1), e.idOf(2), e.idOf(3), e.idOf(4), e.idOf(5)}, ids, "consecutive, with no gaps")

	// What HU-W1.4-02 will do.
	e.decide(t, ids[0], domain.ChangeRequestInReview, "")
	e.decide(t, ids[1], domain.ChangeRequestApplied, "")
	e.decide(t, ids[2], domain.ChangeRequestRejected, "La dirección no corresponde al titular")
	e.decide(t, ids[3], domain.ChangeRequestNotApplied, "")
	e.decide(t, ids[4], domain.ChangeRequestNotApplied, "El avalúo no soporta el nuevo valor")

	code, list := e.call(t, http.MethodGet, "/v1/policies/change-requests", carolina, "")
	require.Equal(t, http.StatusOK, code)
	assert.EqualValues(t, 5, list["total_items"])
	statuses := map[string]string{}
	for _, it := range list["items"].([]any) {
		row := it.(map[string]any)
		statuses[row["request_id"].(string)] = row["status"].(string)
	}
	assert.Equal(t, map[string]string{ids[0]: "in_review", ids[1]: "applied", ids[2]: "rejected", ids[3]: "not_applied",
		ids[4]: "not_applied"}, statuses)
	ind := list["indicators"].(map[string]any)
	assert.EqualValues(t, 2, ind["open"].(map[string]any)["count"], "in review and not applied without a reason")
	assert.EqualValues(t, 1, ind["applied_this_year"].(map[string]any)["count"])
	assert.EqualValues(t, 2, ind["rejected"].(map[string]any)["count"], "rejected and returned with a reason")
	assert.Equal(t, []any{first.ID, second.ID}, list["policies"])

	for status, want := range map[string][]string{
		"in_review": {ids[3], ids[0]},
		"rejected":  {ids[4], ids[2]},
		"applied":   {ids[1]},
		"filed":     {},
	} {
		code, page := e.call(t, http.MethodGet, "/v1/policies/change-requests?status="+status, carolina, "")
		require.Equal(t, http.StatusOK, code)
		got := []string{}
		for _, it := range page["items"].([]any) {
			got = append(got, it.(map[string]any)["request_id"].(string))
		}
		assert.Equal(t, want, got, status)
	}

	code, rejected := e.call(t, http.MethodGet, "/v1/policies/change-requests/"+ids[4], carolina, "")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "El avalúo no soporta el nuevo valor", rejected["decision_reason"], "the insured sees the reason")

	code, page := e.call(t, http.MethodGet, "/v1/policies/change-requests?policy_id="+second.ID+"&page_size=1&page=2", carolina, "")
	require.Equal(t, http.StatusOK, code)
	assert.EqualValues(t, 2, page["total_items"])
	assert.EqualValues(t, 2, page["total_pages"])
	require.Len(t, page["items"], 1)
	assert.Equal(t, ids[3], page["items"].([]any)[0].(map[string]any)["request_id"])
}

// CA 3: invalid or incomplete data is flagged before filing; nothing is filed
// and no number is used.
func TestChangeRequest_CA3_InvalidDataIsFlaggedAndNothingIsFiled(t *testing.T) {
	e := newChangeEnv(t)
	p := e.inForce(t, "pol-cr3")
	path := "/v1/policies/" + p.ID + "/change-requests"

	code, body := e.call(t, http.MethodPost, path, carolina, `{"change_type":"beneficiaries","channel":"web","beneficiaries":[
		{"full_name":"Mateo Pérez Gómez","relationship":"child","document_type":"CC","document_number":"11402A3001","share_percent":80},
		{"full_name":"","relationship":"child","document_type":"TI","document_number":"","share_percent":40}]}`)
	require.Equal(t, http.StatusUnprocessableEntity, code)
	assert.Equal(t, "validation_failed", body["error"])
	assert.ElementsMatch(t, []any{
		map[string]any{"field": "beneficiaries[0].document_number", "code": "invalid_format"},
		map[string]any{"field": "beneficiaries[1].full_name", "code": "required"},
		map[string]any{"field": "beneficiaries[1].document_number", "code": "required"},
		map[string]any{"field": "beneficiaries", "code": "share_total_not_100"},
	}, body["fields"], "every field at once")

	for name, invalid := range map[string]string{
		"amount out of range": `{"change_type":"insured_amount","channel":"web","insured_amount":5000000}`,
		"same amount":         `{"change_type":"insured_amount","channel":"web","insured_amount":68000000}`,
		"bad contact":         `{"change_type":"personal_data","channel":"web","personal_data":{"email":"carolina@","phone":"300"}}`,
		"no contact":          `{"change_type":"personal_data","channel":"web","personal_data":{}}`,
	} {
		code, _ := e.call(t, http.MethodPost, path, carolina, invalid)
		assert.Equal(t, http.StatusUnprocessableEntity, code, name)
	}
	code, body = e.call(t, http.MethodPost, path, carolina,
		`{"change_type":"beneficiaries","channel":"web","beneficiaries":[],"onerous_beneficiary":{"partner_id":"otro"}}`)
	assert.Equal(t, http.StatusBadRequest, code, "the onerous beneficiary never travels")
	assert.Equal(t, "invalid_request", body["error"])

	assert.Zero(t, e.requests(t), "nothing was filed")
	code, r := e.call(t, http.MethodPost, path, carolina, newInsuredAmount)
	require.Equal(t, http.StatusCreated, code)
	assert.Equal(t, e.idOf(1), r["request_id"], "the invalid attempts used no number")
}

// CA 4: only the insured who owns the policy files and reads its requests.
func TestChangeRequest_CA4_OnlyTheOwnerFilesAndReads(t *testing.T) {
	e := newChangeEnv(t)
	carolinas := e.inForce(t, "pol-cr4")
	diegos := e.sold(t, "pol-cr4-diego", "diego", "aliado-banco-1", 30*24*time.Hour, 300*24*time.Hour)
	code, filed := e.call(t, http.MethodPost, "/v1/policies/"+carolinas.ID+"/change-requests", carolina, newInsuredAmount)
	require.Equal(t, http.StatusCreated, code)
	id := filed["request_id"].(string)

	code, _ = e.call(t, http.MethodPost, "/v1/policies/"+carolinas.ID+"/change-requests", diego, newContact)
	assert.Equal(t, http.StatusForbidden, code, "Diego cannot ask for a change of the policy of Carolina")
	code, _ = e.call(t, http.MethodGet, "/v1/policies/"+carolinas.ID+"/current-terms", diego, "")
	assert.Equal(t, http.StatusForbidden, code)
	code, _ = e.call(t, http.MethodGet, "/v1/policies/change-requests/"+id, diego, "")
	assert.Equal(t, http.StatusForbidden, code, "nor read her request")
	code, list := e.call(t, http.MethodGet, "/v1/policies/change-requests", diego, "")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, []any{}, list["items"], "his list never shows hers")
	code, list = e.call(t, http.MethodGet, "/v1/policies/change-requests?policy_id="+carolinas.ID, diego, "")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, []any{}, list["items"])
	assert.Equal(t, 1, e.requests(t), "nothing was filed on her policy")

	code, _ = e.call(t, http.MethodPost, "/v1/policies/"+diegos.ID+"/change-requests", diego, newContact)
	assert.Equal(t, http.StatusCreated, code, "his own policy")

	for _, staff := range []string{"advisor:laura", "operations:ops"} {
		for _, r := range []struct{ method, path, body string }{
			{http.MethodGet, "/v1/policies/" + carolinas.ID + "/current-terms", ""},
			{http.MethodPost, "/v1/policies/" + carolinas.ID + "/change-requests", newContact},
			{http.MethodGet, "/v1/policies/change-requests", ""},
			{http.MethodGet, "/v1/policies/change-requests/" + id, ""},
		} {
			code, _ := e.call(t, r.method, r.path, staff, r.body)
			assert.Equal(t, http.StatusForbidden, code, "%s %s", staff, r.path)
		}
	}
	assert.Equal(t, 2, e.requests(t))
}

// D-6: of two requests of the same policy and type filed at once, one is filed
// and the other is told which one is open.
func TestChangeRequestOneOpenPerPolicyAndTypeEvenAtOnce(t *testing.T) {
	e := newChangeEnv(t)
	p := e.inForce(t, "pol-cr6")
	path := "/v1/policies/" + p.ID + "/change-requests"

	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		codes []int
		named []any
		ids   []any
	)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, body := e.call(t, http.MethodPost, path, carolina, twoBeneficiaries)
			mu.Lock()
			defer mu.Unlock()
			codes = append(codes, code)
			if code == http.StatusConflict {
				named = append(named, body["request_id"])
			} else {
				ids = append(ids, body["request_id"])
			}
		}()
	}
	wg.Wait()

	assert.ElementsMatch(t, []int{http.StatusCreated, http.StatusConflict}, codes)
	require.Len(t, ids, 1)
	assert.Equal(t, ids, named, "the 409 names the open request")
	assert.Equal(t, e.idOf(1), ids[0], "the loser gave its number back")

	code, body := e.call(t, http.MethodPost, path, carolina, twoBeneficiaries)
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "change_request_open", body["error"])
	e.decide(t, fmt.Sprint(ids[0]), domain.ChangeRequestNotApplied, "El documento del beneficiario no es válido")
	code, _ = e.call(t, http.MethodPost, path, carolina, twoBeneficiaries)
	assert.Equal(t, http.StatusCreated, code, "returned to the insured: a new one can be filed (adjustment A-2)")
}

// D-2: only the policies of origination in force can change.
func TestChangeRequestOnlyAPolicyOfOriginationInForce(t *testing.T) {
	e := newChangeEnv(t)
	expired := e.sold(t, "pol-cr-old", "carolina", "aliado-banco-1", 400*24*time.Hour, -35*24*time.Hour)

	code, body := e.call(t, http.MethodPost, "/v1/policies/"+expired.ID+"/change-requests", carolina, newInsuredAmount)
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "policy_not_in_force", body["error"])
	code, terms := e.call(t, http.MethodGet, "/v1/policies/"+expired.ID+"/current-terms", carolina, "")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, false, terms["policy"].(map[string]any)["in_force"])

	code, body = e.call(t, http.MethodPost, "/v1/policies/AU-2025-118204/change-requests", carolina, newInsuredAmount)
	assert.Equal(t, http.StatusNotFound, code, "a policy of the policy core is not origination's")
	assert.Equal(t, "policy_not_found", body["error"])
	assert.Zero(t, e.requests(t))

	advisorSale := e.sold(t, "pol-cr-advisor", "carolina", domain.AdvisorPartnerID, 10*24*time.Hour, 300*24*time.Hour)
	code, terms = e.call(t, http.MethodGet, "/v1/policies/"+advisorSale.ID+"/current-terms", carolina, "")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, map[string]any{"partner_id": nil, "name": nil, "basis": "outstanding_balance"},
		terms["terms"].(map[string]any)["onerous_beneficiary"], "an advisor sale: generic label")
}
