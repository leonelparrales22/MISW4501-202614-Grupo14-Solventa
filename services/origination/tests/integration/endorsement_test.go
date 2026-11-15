// This file checks the controlled application of the endorsements
// (HU-W1.4-02) over a real PostgreSQL (Testcontainers): policies sold from a
// stored offer and activated by their charge, change requests filed by the
// insured through the routes of HU-W1.4-01, and the endorsements applied by
// the store of the analyst.
package integration_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/endorsements"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/renewal"
)

var operationsAnalyst = domain.Actor{Subject: "andres", Name: "Andrés Ruiz", Email: "operaciones@example.com"}

// endorsementEnv is the environment of the change requests with the store of
// the endorsements over the same database.
type endorsementEnv struct {
	*changeEnv
	store    *postgres.EndorsementStore
	requests *postgres.ChangeRequestStore
	terms    *endorsements.Terms
}

func newEndorsementEnv(t *testing.T) *endorsementEnv {
	t.Helper()
	e := &endorsementEnv{changeEnv: newChangeEnv(t)}
	e.store, e.requests = postgres.NewEndorsementStore(e.db), postgres.NewChangeRequestStore(e.db)
	customers := postgres.NewCustomerStore(e.db)
	e.terms = endorsements.NewTerms(renewal.OriginFinder{Policies: e.policies, Charges: e.charges, Offers: e.offers,
		BySubject: customers, ByDocument: customers}, map[string]string{"aliado-banco-1": "Banco Aliado"}).WithEndorsements(e.store)
	return e
}

// file files a request of carolina on the policy and returns it.
func (e *endorsementEnv) file(t *testing.T, policyID, body string) domain.ChangeRequest {
	t.Helper()
	code, r := e.call(t, http.MethodPost, "/v1/policies/"+policyID+"/change-requests", carolina, body)
	require.Equal(t, http.StatusCreated, code, "%v", r)
	stored, err := e.requests.Get(context.Background(), r["request_id"].(string))
	require.NoError(t, err)
	return stored
}

// applyWithStore applies the request as the analyst would, through the store,
// with the premium priced by the caller.
func (e *endorsementEnv) applyWithStore(t *testing.T, r domain.ChangeRequest, premium float64) (domain.Endorsement, error) {
	t.Helper()
	ctx := context.Background()
	p, err := e.policies.Get(ctx, r.PolicyID)
	require.NoError(t, err)
	current, version, err := e.terms.Current(ctx, p)
	require.NoError(t, err)
	rated := domain.Rating{Premium: premium, Coverages: []domain.Coverage{{Code: domain.CoverageDeath, InsuredAmount: r.Requested.InsuredAmount}}}
	return e.store.Apply(ctx, ports.AppliedEndorsement{Request: r, BaseVersion: version, Terms: domain.ApplyChange(domain.TermsOf(p, current), r, rated),
		PreviousPremium: p.Premium, By: operationsAnalyst, At: e.clock.Now().UTC(),
		Validations: domain.Validations{{Code: domain.ValidationRequesterIsHolder, Outcome: domain.ValidationPassed}}})
}

// CA-3: the photo of the last endorsement is the current state of the policy:
// the view lays it over the policy and the terms of HU-W1.4-01 read it.
func TestPolicyCurrentOverlaysLatestEndorsement(t *testing.T) {
	e := newEndorsementEnv(t)
	ctx := context.Background()
	p := e.inForce(t, "pol-end1")
	amount := e.file(t, p.ID, `{"change_type":"insured_amount","channel":"web","insured_amount":120000000}`)
	contact := e.file(t, p.ID, newContact)

	first, err := e.applyWithStore(t, contact, 0)
	require.NoError(t, err)
	second, err := e.applyWithStore(t, amount, 250_900)
	require.NoError(t, err)

	assert.Equal(t, 1, first.Version)
	assert.Equal(t, 2, second.Version, "the second starts from the terms of the first")
	var insured, premium int64
	require.NoError(t, e.db.QueryRowContext(ctx, `SELECT insured_amount_minor, premium_minor FROM policy_current WHERE policy_id = $1`,
		p.ID).Scan(&insured, &premium))
	assert.Equal(t, int64(12_000_000_000), insured)
	assert.Equal(t, int64(25_090_000), premium)
	var stored int64
	require.NoError(t, e.db.QueryRowContext(ctx, `SELECT insured_amount_minor FROM policy WHERE policy_id = $1`, p.ID).Scan(&stored))
	assert.Equal(t, int64(6_800_000_000), stored, "policy itself never changes")

	code, terms := e.call(t, http.MethodGet, "/v1/policies/"+p.ID+"/current-terms", carolina, "")
	require.Equal(t, http.StatusOK, code)
	got := terms["terms"].(map[string]any)
	assert.Equal(t, map[string]any{"address": "Calle 10 # 43-20, Medellín", "email": "carolina@example.com", "phone": "3001234567"},
		got["personal_data"], "the second keeps the contact data of the first, and the insured reads them")
	assert.InDelta(t, 120_000_000, got["insured_amount"], 0.001)
	assert.Equal(t, []any{}, terms["open_requests"], "both requests are applied")
}

// CA-3: every reader of the policy sees the endorsed terms in the same COMMIT:
// the wallet of the web and the phone, the renewal (priced with the new
// insured value), the list of the advisor, the alerts of expiry and the terms
// of the insured.
func TestWalletRenewalExpiringAndCurrentTermsReadEndorsedTerms(t *testing.T) {
	e := newEndorsementEnv(t)
	ctx := context.Background()
	p := e.sold(t, "pol-end2", "carolina", "aliado-banco-1", 355*24*time.Hour, 10*24*time.Hour)
	amount := e.file(t, p.ID, `{"change_type":"insured_amount","channel":"web","insured_amount":120000000}`)

	_, err := e.applyWithStore(t, amount, 250_900)
	require.NoError(t, err)

	code, list := e.do(t, http.MethodGet, "/v1/policies", carolina, "")
	require.Equal(t, http.StatusOK, code, "%v", list)
	item := list["items"].([]any)[0].(map[string]any)
	assert.InDelta(t, 120_000_000, item["insured_amount"], 0.001, "the wallet of the web and the phone")
	assert.InDelta(t, 250_900, item["premium"], 0.001)

	code, renewalOffer := e.do(t, http.MethodGet, "/v1/policies/"+p.ID+"/renewal", carolina, "")
	require.Equal(t, http.StatusOK, code, "%v", renewalOffer)
	current := renewalOffer["current"].(map[string]any)
	assert.InDelta(t, 250_900, current["premium"], 0.001, "the renewal starts from the endorsed premium")
	assert.InDelta(t, 120_000_000, renewalOffer["renewal"].(map[string]any)["insured_amount"], 0.001,
		"and renews the endorsed insured value")

	now := e.clock.Now()
	expiring, _, err := postgres.NewExpiringPolicyStore(e.db).ListExpiring(ctx, now, now.AddDate(0, 0, 30), now, "", 0, 10)
	require.NoError(t, err)
	require.Len(t, expiring, 1)
	assert.InDelta(t, 120_000_000, expiring[0].Policy.InsuredAmount, 0.001, "the list of the advisor")

	alerts, err := e.policies.ListExpiringUnrenewed(ctx, now, now.AddDate(0, 0, 30))
	require.NoError(t, err)
	require.Len(t, alerts, 1)
	assert.InDelta(t, 250_900, alerts[0].Premium, 0.001, "the alerts read the same view, though only dates matter to them")

	code, terms := e.call(t, http.MethodGet, "/v1/policies/"+p.ID+"/current-terms", carolina, "")
	require.Equal(t, http.StatusOK, code)
	assert.InDelta(t, 120_000_000, terms["terms"].(map[string]any)["insured_amount"], 0.001, "the terms of the insured")
}
