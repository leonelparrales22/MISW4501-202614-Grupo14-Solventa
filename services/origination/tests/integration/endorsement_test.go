// This file checks the controlled application of the endorsements
// (HU-W1.4-02) over a real PostgreSQL (Testcontainers): policies sold from a
// stored offer and activated by their charge, change requests filed by the
// insured through the routes of HU-W1.4-01, and the endorsements applied by
// the store of the analyst.
package integration_test

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
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
	// As in cmd: a policy cancelled for non-payment is not in force (A-9).
	e.terms.WithCancellations(postgres.NewInstallmentStore(e.db))
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
	return e.store.Apply(context.Background(), e.applicationOf(t, r, premium))
}

// applicationOf is what the analyst applies: the resulting terms over the
// current version of the policy, read now.
func (e *endorsementEnv) applicationOf(t *testing.T, r domain.ChangeRequest, premium float64) ports.AppliedEndorsement {
	t.Helper()
	ctx := context.Background()
	p, err := e.policies.Get(ctx, r.PolicyID)
	require.NoError(t, err)
	current, version, err := e.terms.Current(ctx, p)
	require.NoError(t, err)
	rated := domain.Rating{Premium: premium, Coverages: []domain.Coverage{{Code: domain.CoverageDeath, InsuredAmount: r.Requested.InsuredAmount}}}
	return ports.AppliedEndorsement{Request: r, BaseVersion: version, Terms: domain.ApplyChange(domain.TermsOf(p, current), r, rated),
		PreviousPremium: p.Premium, By: operationsAnalyst, At: e.clock.Now().UTC(),
		Validations: domain.Validations{{Code: domain.ValidationRequesterIsHolder, Outcome: domain.ValidationPassed}}}
}

// count runs a count query of the test.
func (e *endorsementEnv) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, e.db.QueryRowContext(context.Background(), query, args...).Scan(&n))
	return n
}

// atOnce releases n calls of do at the same instant and returns their errors.
func atOnce(n int, do func() error) []error {
	errs := make([]error, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[i] = do()
		}()
	}
	close(start)
	wg.Wait()
	return errs
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
	expiring, _, err := postgres.NewExpiringPolicyStore(e.db).ListExpiring(ctx, now, now.AddDate(0, 0, 30), now,
		now.AddDate(0, 0, -domain.CancelledListedDays), "", 0, 10)
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

// CA-3 (plan §7.1): the claim of the request (EC-D02) and the version of the
// policy (D-3 a) hold under real concurrency, so the analysts never leave two
// endorsements of one request nor two endorsements built on the same terms.
// Each round uses a policy of its own; the counter of END-<year>-<number>
// keeps no number of a loser.
func TestConcurrentApplications(t *testing.T) {
	const rounds = 10
	e := newEndorsementEnv(t)
	ctx := context.Background()

	t.Run("twenty applications of the same request leave one endorsement", func(t *testing.T) {
		for round := range rounds {
			p := e.inForce(t, fmt.Sprintf("pol-race-%d", round))
			r := e.file(t, p.ID, newContact)
			application := e.applicationOf(t, r, 0)

			errs := atOnce(20, func() error {
				_, err := e.store.Apply(ctx, application)
				return err
			})

			won := 0
			for _, err := range errs {
				if err == nil {
					won++
					continue
				}
				assert.ErrorIs(t, err, ports.ErrRequestAlreadyDecided, "round %d: a loser finds the request decided", round)
			}
			assert.Equal(t, 1, won, "round %d: exactly one application wins", round)
			assert.Equal(t, 1, e.count(t, `SELECT count(*) FROM endorsement WHERE request_id = $1`, r.ID), "round %d", round)
			assert.Equal(t, 1, e.count(t, `SELECT count(*) FROM endorsement_attempt WHERE request_id = $1`, r.ID), "round %d", round)
			assert.Equal(t, 1, e.count(t, `SELECT count(*) FROM endorsement_notice WHERE request_id = $1`, r.ID),
				"round %d: one notice to the insured", round)
			stored, err := e.requests.Get(ctx, r.ID)
			require.NoError(t, err)
			assert.Equal(t, domain.ChangeRequestApplied, stored.Status, "round %d", round)
			assert.Equal(t, e.count(t, `SELECT count(*) FROM endorsement`), e.count(t, `SELECT max(last_number) FROM endorsement_counter`),
				"round %d: the losers give their number back", round)
		}
	})

	t.Run("two types of one policy from the same version: one wins, the other policy_changed", func(t *testing.T) {
		for round := range rounds {
			p := e.inForce(t, fmt.Sprintf("pol-version-%d", round))
			amount := e.file(t, p.ID, `{"change_type":"insured_amount","channel":"web","insured_amount":120000000}`)
			contact := e.file(t, p.ID, newContact)
			applications := []ports.AppliedEndorsement{e.applicationOf(t, amount, 250_900), e.applicationOf(t, contact, 0)}
			require.Equal(t, applications[0].BaseVersion, applications[1].BaseVersion, "both start from the same terms")

			var next atomic.Int32
			errs := atOnce(2, func() error {
				_, err := e.store.Apply(ctx, applications[next.Add(1)-1])
				return err
			})

			won := 0
			for _, err := range errs {
				if err == nil {
					won++
					continue
				}
				assert.ErrorIs(t, err, ports.ErrPolicyChanged, "round %d: the other answers policy_changed (409)", round)
			}
			assert.Equal(t, 1, won, "round %d: exactly one wins the version", round)
			assert.Equal(t, 1, e.count(t, `SELECT count(*) FROM endorsement WHERE policy_id = $1`, p.ID), "round %d", round)
			var statuses []domain.ChangeRequestStatus
			for _, id := range []string{amount.ID, contact.ID} {
				stored, err := e.requests.Get(ctx, id)
				require.NoError(t, err)
				statuses = append(statuses, stored.Status)
			}
			assert.ElementsMatch(t, []domain.ChangeRequestStatus{domain.ChangeRequestApplied, domain.ChangeRequestFiled}, statuses,
				"round %d: the loser keeps its status, nothing of it was written", round)
		}
	})
}

// A-9: a policy cancelled for non-payment (HU-W1.6-02) within its dates is
// not in force for the analyst either: the detail says so, and applying the
// request records the attempt with policy_in_force failed and leaves the
// policy as it was, with the review over the stores of PostgreSQL.
func TestACancelledPolicyGetsNoEndorsement(t *testing.T) {
	e := newEndorsementEnv(t)
	ctx := context.Background()
	// Three months into its year, so its second month is past its grace, as
	// in the case of HU-W1.4-01.
	p := e.sold(t, "pol-end-cancelled", "carolina", "aliado-banco-1", 90*24*time.Hour, 275*24*time.Hour)
	r := e.file(t, p.ID, newContact)
	e.cancel(t, p)
	review, err := endorsements.NewReview(endorsements.ReviewConfig{Policies: e.policies, Requests: e.requests,
		Store: e.store, Terms: e.terms, Renewals: postgres.NewRenewalStore(e.db), Clock: e.clock})
	require.NoError(t, err)
	analyst := security.WithClaims(ctx, security.Claims{Subject: operationsAnalyst.Subject, Name: operationsAnalyst.Name,
		Email: operationsAnalyst.Email, Roles: []security.Role{security.RoleOperations}})

	d, err := review.Detail(analyst, r.ID)
	require.NoError(t, err)
	assert.False(t, d.InForce, "the detail shows it not in force")

	got, err := review.Apply(analyst, r.ID)

	require.NoError(t, err)
	assert.Equal(t, domain.OutcomeNotApplied, got.Outcome)
	require.NotNil(t, got.Attempt)
	assert.Equal(t, domain.ValidationPolicyInForce, got.Attempt.Validations.FirstFailed(), "the reason is recorded")
	assert.Equal(t, domain.ChangeRequestNotApplied, got.Request.Status)
	assert.Zero(t, e.count(t, `SELECT count(*) FROM endorsement WHERE policy_id = $1`, p.ID), "the policy keeps its terms")
	assert.Equal(t, 1, e.count(t, `SELECT count(*) FROM endorsement_attempt WHERE request_id = $1`, r.ID))
}
