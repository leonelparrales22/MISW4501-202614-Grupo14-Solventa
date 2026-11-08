// This file checks each acceptance criterion of the list of the policies
// about to expire of the advisor (HU-W1.5-01) end to end, in process: the
// route over HTTP, the use case and the store over a real PostgreSQL
// (Testcontainers), next to the online renewal of HU-W1.5-02 and its real
// charge worker, which produce the states the list shows.
package integration_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/profiling"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/renewal"
)

const laura = "advisor:laura"

// expiringEnv is the renewal environment of HU-W1.5-02 plus the list of the
// advisor over the same database and clock.
type expiringEnv struct {
	*renewalEnv
	list *acceptanceEnv
}

func newExpiringEnv(t *testing.T) *expiringEnv {
	t.Helper()
	e := &expiringEnv{renewalEnv: newRenewalEnv(t)}
	svc, err := renewal.NewExpiringService(postgres.NewExpiringPolicyStore(e.db), domain.DefaultRenewalPolicy, e.clock)
	require.NoError(t, err)
	srv := httptest.NewServer(httpadapter.NewRouter(profiling.NewResolver(nil, nil, e.clock),
		httpadapter.WithObservability("origination"),
		httpadapter.WithExpiringPolicies(svc, subjectVerifier{})))
	t.Cleanup(srv.Close)
	e.list = &acceptanceEnv{srv: srv}
	return e
}

// expiring calls the list as the advisor with the given query.
func (e *expiringEnv) expiring(t *testing.T, query string) (int, map[string]any) {
	t.Helper()
	return e.list.do(t, http.MethodGet, "/v1/policies/expiring"+query, laura, "")
}

// policyAt stores a policy of carolina that ends exactly at end.
func (e *expiringEnv) policyAt(t *testing.T, id string, end time.Time) domain.Policy {
	t.Helper()
	p := domain.Policy{
		ID: id, AcceptanceID: "acc-" + id, CustomerID: "carolina",
		Holder:  domain.Applicant{FullName: "Carolina Pérez", DocumentID: "1032456789", Email: "carolina@example.com"},
		Premium: 142_300, Currency: "COP", InsuredAmount: 68_000_000,
		Coverages:     []domain.Coverage{{Code: domain.CoverageDeath, InsuredAmount: 68_000_000}},
		CoverageStart: end.AddDate(-1, 0, 0), CoverageEnd: end.UTC(),
	}
	require.NoError(t, e.policies.Create(context.Background(), p))
	return p
}

func itemsOf(body map[string]any) []map[string]any {
	raw, _ := body["items"].([]any)
	out := make([]map[string]any, len(raw))
	for i, it := range raw {
		out[i] = it.(map[string]any)
	}
	return out
}

func policyIDs(body map[string]any) []string {
	items := itemsOf(body)
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it["policy_id"].(string)
	}
	return out
}

func (e *expiringEnv) today() time.Time { return domain.CivilDayOf(e.clock.Now()) }

func day(t time.Time) string { return t.Format(domain.CivilDayLayout) }

// CA 1: the advisor sees the policies about to expire with their end and status.
func TestExpiringCA1TheAdvisorSeesTheExpiringPoliciesWithTheirEndAndStatus(t *testing.T) {
	e := newExpiringEnv(t)
	first := e.seedPolicy(t, "pol-x1", 10*24*time.Hour)
	second := e.seedPolicy(t, "pol-x2", 20*24*time.Hour)

	code, body := e.expiring(t, "")

	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, day(e.today()), body["expires_from"], "by default, from today")
	assert.Equal(t, day(e.today().AddDate(0, 0, 30)), body["expires_to"], "to the end of the renewal window")
	assert.EqualValues(t, 30, body["window_days"])
	assert.EqualValues(t, 2, body["due_within_window"])
	assert.EqualValues(t, 2, body["total"])
	require.Equal(t, []string{"pol-x1", "pol-x2"}, policyIDs(body), "the ones ending first")
	for i, p := range []domain.Policy{first, second} {
		it := itemsOf(body)[i]
		assert.Equal(t, p.CoverageEnd.Format(time.RFC3339Nano), it["coverage_end"])
		assert.Equal(t, "expiring", it["status"])
		assert.Equal(t, "life", it["line"])
		assert.InDelta(t, 142_300, it["premium"], 0.001)
		assert.Equal(t, map[string]any{"full_name": "Carolina Pérez", "document_type": nil, "document_id": "1032456789"}, it["holder"])
		assert.Nil(t, it["renewal"])
	}
}

// CA 2: the range of expiration is inclusive by civil day of Colombia.
func TestExpiringCA2TheRangeFilterIsInclusiveByColombianDay(t *testing.T) {
	e := newExpiringEnv(t)
	first := e.today().AddDate(0, 0, 5)
	last := first.AddDate(0, 0, 3)
	e.policyAt(t, "pol-day-before", first.Add(-time.Millisecond)) // 23:59:59.999 of the day before
	e.policyAt(t, "pol-first", first)                             // 00:00 of the first day
	e.policyAt(t, "pol-last", last.AddDate(0, 0, 1).Add(-time.Millisecond))
	e.policyAt(t, "pol-day-after", last.AddDate(0, 0, 1)) // 00:00 of the day after

	code, body := e.expiring(t, "?expires_from="+day(first)+"&expires_to="+day(last))

	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, []string{"pol-first", "pol-last"}, policyIDs(body))
	assert.EqualValues(t, 2, body["total"])
	assert.Equal(t, day(first), body["expires_from"])
	assert.Equal(t, day(last), body["expires_to"])

	code, body = e.expiring(t, "?expires_from="+day(last)+"&expires_to="+day(first))
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Equal(t, "out_of_range", body["error"])
}

// CA 3: with no policies in the range the list is empty, with no error.
func TestExpiringCA3AnEmptyRangeAnswersAnEmptyList(t *testing.T) {
	e := newExpiringEnv(t)
	e.seedPolicy(t, "pol-x3", 10*24*time.Hour)
	from := e.today().AddDate(0, 3, 0)

	code, body := e.expiring(t, "?expires_from="+day(from)+"&expires_to="+day(from.AddDate(0, 0, 7)))

	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, []any{}, body["items"])
	assert.EqualValues(t, 0, body["total"])
	assert.EqualValues(t, 1, body["due_within_window"], "the indicator does not depend on the filter")
}

// CA 4: a policy inside its renewal window comes with can_renew and its id,
// the entry to the assisted renewal (HU-W1.5-03); outside the window, or with
// a renewal in progress, it does not.
func TestExpiringCA4ARenewablePolicyComesWithCanRenew(t *testing.T) {
	e := newExpiringEnv(t)
	inWindow := e.seedPolicy(t, "pol-in", 10*24*time.Hour)
	later := e.seedPolicy(t, "pol-later", 60*24*time.Hour)
	inProgress := e.seedPolicy(t, "pol-busy", 15*24*time.Hour)
	code, _ := e.do(t, http.MethodPost, "/v1/policies/"+inProgress.ID+"/renewal", carolina, `{}`)
	require.Equal(t, http.StatusCreated, code)

	code, body := e.expiring(t, "?expires_to="+day(e.today().AddDate(0, 0, 90)))

	require.Equal(t, http.StatusOK, code)
	require.Equal(t, []string{"pol-in", "pol-busy", "pol-later"}, policyIDs(body))
	items := itemsOf(body)
	assert.Equal(t, inWindow.ID, items[0]["policy_id"])
	assert.Equal(t, true, items[0]["can_renew"])
	assert.Equal(t, false, items[1]["can_renew"], "a renewal in progress is seen, not started again")
	assert.Equal(t, false, items[2]["can_renew"], "outside the window of HU-W1.5-02")
	assert.Equal(t, "expiring", items[2]["status"])
	window := items[2]["renewal_window"].(map[string]any)
	assert.Equal(t, later.CoverageEnd.AddDate(0, 0, -30).Format(time.RFC3339Nano), window["opens_at"])
}

// Renewal in progress: once the insured accepts the renewal online, the
// advisor sees it in progress; once the sweep settles its confirmed charge,
// the period is closed and the policy leaves the list (decision D-4 a).
func TestExpiringARenewalAcceptedOnlineIsInProgressUntilTheSweepClosesIt(t *testing.T) {
	e := newExpiringEnv(t)
	p := e.seedPolicy(t, "pol-online", 10*24*time.Hour)

	code, accepted := e.do(t, http.MethodPost, "/v1/policies/"+p.ID+"/renewal", carolina, `{}`)
	require.Equal(t, http.StatusCreated, code)
	chargeID := accepted["renewal"].(map[string]any)["charge_id"].(string)

	_, body := e.expiring(t, "?status=renewal_in_progress")
	require.Equal(t, []string{p.ID}, policyIDs(body))
	row := itemsOf(body)[0]
	assert.Equal(t, "renewal_in_progress", row["status"])
	assert.Equal(t, false, row["can_renew"])
	summary := row["renewal"].(map[string]any)
	assert.Equal(t, "accepted", summary["status"])
	assert.Equal(t, chargeID, summary["charge_id"])
	_, body = e.expiring(t, "?status=expiring")
	assert.Empty(t, itemsOf(body))

	require.Eventually(t, func() bool { return mustCharge(t, e.renewalEnv, chargeID).Status == domain.ChargeConfirmed },
		15*time.Second, 20*time.Millisecond)
	_, body = e.expiring(t, "")
	assert.Equal(t, []string{p.ID}, policyIDs(body), "until the sweep settles it, the stored renewal is still accepted")

	_, err := e.svc.Sweep(context.Background())
	require.NoError(t, err)
	_, body = e.expiring(t, "")
	assert.Empty(t, itemsOf(body), "the renewed period leaves the list and the new policy ends in a year")
	assert.EqualValues(t, 0, body["due_within_window"])
	_, body = e.expiring(t, "?expires_from="+day(e.today().AddDate(1, 0, 0))+"&expires_to="+day(e.today().AddDate(1, 1, 0)))
	require.Len(t, itemsOf(body), 1, "the renewed policy appears by its own end")
	assert.Equal(t, "expiring", itemsOf(body)[0]["status"])
}

func TestExpiringOnlyTheAdvisorListsThePolicies(t *testing.T) {
	e := newExpiringEnv(t)
	e.seedPolicy(t, "pol-x5", 10*24*time.Hour)

	code, _ := e.list.do(t, http.MethodGet, "/v1/policies/expiring", "", "")
	assert.Equal(t, http.StatusUnauthorized, code)
	for _, token := range []string{carolina, "operations:ops"} {
		code, _ := e.list.do(t, http.MethodGet, "/v1/policies/expiring", token, "")
		assert.Equal(t, http.StatusForbidden, code, token)
	}
	code, _ = e.expiring(t, "")
	assert.Equal(t, http.StatusOK, code)
}
