// This file checks each acceptance criterion of the summary of the dashboard
// of a commercial partner (HU-W3.2-01) end to end, in process: the partner
// requests its quotes with POST /v1/offers over a real PostgreSQL
// (Testcontainers), some of them become policies, and the partner portal reads
// the summary over HTTP.
package integration_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/partnermetrics"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/profiling"
)

// partnerPortalVerifier reads "<role>:<subject>@<partner_id>": the user of
// the partner portal carries its partner in custom:partner_id (HA-S01).
type partnerPortalVerifier struct{}

func (partnerPortalVerifier) Verify(_ context.Context, raw string) (security.Claims, error) {
	role, rest, ok := strings.Cut(raw, ":")
	if !ok {
		return security.Claims{}, errors.New("invalid token")
	}
	subject, partner, _ := strings.Cut(rest, "@")
	return security.Claims{Subject: subject, Roles: []security.Role{security.Role(role)}, PartnerID: partner}, nil
}

const (
	bancoAliado = "partner:ana@partner-1"
	otroSocio   = "partner:luis@partner-2"
)

type partnerMetricsEnv struct {
	*offerIT
	portal *acceptanceEnv
}

func newPartnerMetricsEnv(t *testing.T) *partnerMetricsEnv {
	t.Helper()
	e := &partnerMetricsEnv{offerIT: newOfferITWithDB(t)}
	srv := httptest.NewServer(httpadapter.NewRouter(profiling.NewResolver(nil, nil, e.clock),
		httpadapter.WithObservability("origination"),
		httpadapter.WithPartnerMetrics(partnermetrics.NewService(postgres.NewPartnerActivityStore(e.db)), partnerPortalVerifier{})))
	t.Cleanup(srv.Close)
	e.portal = &acceptanceEnv{srv: srv}
	return e
}

// quote requests a quote as the partner through the embedded channel and
// returns the offer id.
func (e *partnerMetricsEnv) quote(t *testing.T, partner string) string {
	t.Helper()
	body := strings.Replace(offerBody, `"partner_id":"partner-1"`, `"partner_id":"`+partner+`"`, 1)
	status, offer := e.do(t, http.MethodPost, "/v1/offers", "", body)
	require.Equal(t, http.StatusCreated, status, "%v", offer)
	return offer["offer_id"].(string)
}

// issue records the policy of the offer, as the activation of its coverage
// does once its premium is confirmed (HA-D02, HU-W1.5-02).
func (e *partnerMetricsEnv) issue(t *testing.T, offerID string) {
	t.Helper()
	ctx, now := context.Background(), e.clock.Now().UTC()
	acc := "acc-" + offerID
	_, _, err := postgres.NewAcceptanceStore(e.db).Create(ctx, domain.Acceptance{ID: acc, OfferID: offerID, CustomerID: "cust-1",
		Channel: domain.ChannelWeb, AcceptedAt: now, Status: domain.AcceptanceRegistered, NextAttemptAt: now, UpdatedAt: now})
	require.NoError(t, err)
	require.NoError(t, postgres.NewPolicyStore(e.db).Create(ctx, domain.Policy{
		ID: domain.PolicyIDFor(acc), AcceptanceID: acc, CustomerID: "cust-1",
		Holder:  domain.Applicant{FullName: "Ana María Pérez", DocumentID: "1020304050", Email: "ana@example.com"},
		Premium: 137_600, Currency: "COP", InsuredAmount: 250_000_000,
		Coverages:     []domain.Coverage{{Code: domain.CoverageDeath, InsuredAmount: 250_000_000}},
		CoverageStart: now, CoverageEnd: now.AddDate(10, 0, 0),
	}))
}

func (e *partnerMetricsEnv) day(offset int) string {
	return domain.CivilDayOf(e.clock.Now()).AddDate(0, 0, offset).Format(domain.CivilDayLayout)
}

func (e *partnerMetricsEnv) summary(t *testing.T, token, from, to string) (int, map[string]any) {
	t.Helper()
	return e.portal.do(t, http.MethodGet, "/v1/partner-metrics/summary?from="+from+"&to="+to, token, "")
}

func TestPartnerMetricsCA1TheCardsShowQuotesPoliciesAndConversion(t *testing.T) {
	e := newPartnerMetricsEnv(t)
	for i := 0; i < 4; i++ {
		id := e.quote(t, "partner-1")
		if i == 0 {
			e.issue(t, id)
		}
	}

	status, body := e.summary(t, bancoAliado, e.day(-29), e.day(0))

	require.Equal(t, http.StatusOK, status, "%v", body)
	assert.Equal(t, float64(4), body["quotes_requested"])
	assert.Equal(t, float64(1), body["policies_issued"])
	assert.Equal(t, 25.0, body["conversion_rate"], "1 / 4 × 100")
}

func TestPartnerMetricsCA2TheRangeChangesTheMetricsAndIncludesBothDays(t *testing.T) {
	e := newPartnerMetricsEnv(t)
	e.issue(t, e.quote(t, "partner-1"))

	for name, r := range map[string][2]string{
		"today":                  {e.day(0), e.day(0)},
		"last 7 days":            {e.day(-6), e.day(0)},
		"custom ending today":    {e.day(-3), e.day(0)},
		"custom starting today":  {e.day(0), e.day(2)},
		"custom of a single day": {e.day(0), e.day(0)},
	} {
		status, body := e.summary(t, bancoAliado, r[0], r[1])
		require.Equal(t, http.StatusOK, status, name)
		assert.Equal(t, float64(1), body["quotes_requested"], name)
		assert.Equal(t, float64(1), body["policies_issued"], name)
	}

	status, body := e.summary(t, bancoAliado, e.day(-7), e.day(-1))
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, float64(0), body["quotes_requested"], "a range that ends yesterday leaves today out")

	status, body = e.summary(t, bancoAliado, e.day(0), e.day(-1))
	assert.Equal(t, http.StatusBadRequest, status, "the first day cannot be after the last")
	assert.Equal(t, "out_of_range", body["error"])
}

func TestPartnerMetricsCA3ARangeWithoutDataShowsZerosWithoutErrors(t *testing.T) {
	e := newPartnerMetricsEnv(t)
	e.quote(t, "partner-1")

	status, body := e.summary(t, bancoAliado, e.day(-40), e.day(-31))

	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, map[string]any{"partner_id": "partner-1", "from": e.day(-40), "to": e.day(-31),
		"quotes_requested": float64(0), "policies_issued": float64(0), "conversion_rate": float64(0)}, body)
}

func TestPartnerMetricsCA4OnlyTheDataOfThePartnerOfTheToken(t *testing.T) {
	e := newPartnerMetricsEnv(t)
	e.issue(t, e.quote(t, "partner-1"))
	e.issue(t, e.quote(t, "partner-2"))
	e.quote(t, "partner-2")
	e.quote(t, "partner-2")

	_, mine := e.summary(t, bancoAliado, e.day(0), e.day(0))
	_, theirs := e.summary(t, otroSocio, e.day(0), e.day(0))
	assert.Equal(t, []any{float64(1), float64(1)}, []any{mine["quotes_requested"], mine["policies_issued"]})
	assert.Equal(t, []any{float64(3), float64(1)}, []any{theirs["quotes_requested"], theirs["policies_issued"]})

	status, body := e.portal.do(t, http.MethodGet,
		"/v1/partner-metrics/summary?from="+e.day(0)+"&to="+e.day(0)+"&partner_id=partner-2", bancoAliado, "")
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, "partner-1", body["partner_id"], "the query cannot name another partner")
	assert.Equal(t, float64(1), body["quotes_requested"])

	status, _ = e.summary(t, "partner:sin-socio", e.day(0), e.day(0))
	assert.Equal(t, http.StatusForbidden, status, "a portal user without custom:partner_id sees nothing")
	status, _ = e.summary(t, "operations:ops@partner-1", e.day(0), e.day(0))
	assert.Equal(t, http.StatusForbidden, status, "only the partner portal")
	status, _ = e.summary(t, "", e.day(0), e.day(0))
	assert.Equal(t, http.StatusUnauthorized, status)
}

// The quotes of the partner keep counting while time passes: a quote of
// yesterday is in the last 7 days but not in today.
func TestPartnerMetricsAQuoteOfYesterdayLeavesToday(t *testing.T) {
	e := newPartnerMetricsEnv(t)
	e.quote(t, "partner-1")
	e.clock.advance(24 * time.Hour)

	_, today := e.summary(t, bancoAliado, e.day(0), e.day(0))
	_, week := e.summary(t, bancoAliado, e.day(-6), e.day(0))

	assert.Equal(t, float64(0), today["quotes_requested"])
	assert.Equal(t, float64(1), week["quotes_requested"])
}
