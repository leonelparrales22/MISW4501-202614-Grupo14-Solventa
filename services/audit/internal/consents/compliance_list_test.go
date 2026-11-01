package consents_test

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/consents"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
)

// HU-W4.2-01: the list of consents of the compliance portal, over the doubles
// of the revocation process. The double of the list store answers with the
// reference in Go of the PostgreSQL query (domain.FilterForCompliance,
// Paginate, Indicators), which the adapter is tested against.

type referenceList struct {
	consents *memStore
	events   *syncEvents
	err      error
	calls    int
	gotNow   time.Time
	gotQuery domain.ComplianceQuery
	// status overrides the status the "database" gives a consent, to see the
	// service notice a difference.
	status map[string]domain.ConsentStatus
}

func (r *referenceList) ListForCompliance(ctx context.Context, q domain.ComplianceQuery, now time.Time) (domain.ComplianceListResult, error) {
	r.calls++
	r.gotNow, r.gotQuery = now, q
	if r.err != nil {
		return domain.ComplianceListResult{}, r.err
	}
	r.consents.mu.Lock()
	all := append([]domain.Consent(nil), r.consents.rows...)
	r.consents.mu.Unlock()
	ids := make([]string, 0, len(all))
	for _, c := range all {
		ids = append(ids, c.ID)
	}
	events, err := r.events.ListByConsents(ctx, ids)
	if err != nil {
		return domain.ComplianceListResult{}, err
	}
	views := make([]domain.ConsentView, 0, len(all))
	seen := map[string]bool{}
	var codes []string
	for _, c := range all {
		views = append(views, domain.View(c, events[c.ID], now, nil))
		if !seen[c.PartnerID] {
			seen[c.PartnerID] = true
			codes = append(codes, c.PartnerID)
		}
	}
	sort.Strings(codes)
	matching := domain.FilterForCompliance(views, q)
	out := domain.ComplianceListResult{Rows: []domain.ComplianceRow{}, TotalItems: len(matching), EntityCodes: codes}
	for _, v := range domain.Paginate(matching, q) {
		status := v.Status
		if s, ok := r.status[v.Consent.ID]; ok {
			status = s
		}
		out.Rows = append(out.Rows, domain.ComplianceRow{Consent: v.Consent, Status: status})
	}
	out.Indicators = domain.Indicators(views, now)
	out.Indicators.AsOf = time.Time{}
	return out, nil
}

type listFixture struct {
	*complianceFixture
	list   *referenceList
	lister *consents.ComplianceService
}

func newListFixture(t *testing.T) *listFixture {
	t.Helper()
	f := newComplianceFixture(t)
	list := &referenceList{consents: f.consents, events: f.events}
	return &listFixture{complianceFixture: f, list: list, lister: consents.NewComplianceService(consents.ComplianceDeps{
		Revocations: f.svc, Consents: f.consents, Events: f.events, Records: f.revocations, List: list, Clock: f.clock,
		Names: domain.EntityNames{"aliado-banco-1": "Banco Aliado", "aliado-coop": "Cooperativa Andina"},
	})}
}

func firstPage(size int) domain.ComplianceQuery {
	return domain.ComplianceQuery{Page: 1, PageSize: size}
}

func TestComplianceListReturnsThePageTheTotalsTheIndicatorsAndTheEntities(t *testing.T) {
	f := newListFixture(t)

	page, err := f.lister.List(context.Background(), firstPage(3))

	require.NoError(t, err)
	assert.Equal(t, 1, f.list.calls)
	now := f.clock.Now().UTC().Truncate(time.Microsecond)
	assert.Equal(t, now, f.list.gotNow, "one instant of the server, in microseconds, for the query and the views")
	assert.Equal(t, firstPage(3), f.list.gotQuery)
	require.Len(t, page.Items, 3)
	assert.Equal(t, 1, page.Page)
	assert.Equal(t, 3, page.PageSize)
	assert.Equal(t, 4, page.TotalItems)
	assert.Equal(t, 2, page.TotalPages)
	for _, item := range page.Items {
		assert.Equal(t, []domain.Entity{{Code: "aliado-banco-1", Name: "Banco Aliado"}}, item.Entities, "the name of the catalog")
	}
	assert.Equal(t, domain.ComplianceIndicators{Active: 3, Expired: 1, AsOf: now}, page.Indicators,
		"cns-old was granted a year before the others and expired")
	assert.Equal(t, []domain.Entity{
		{Code: "aliado-banco-1", Name: "Banco Aliado"}, {Code: "aliado-coop", Name: "Cooperativa Andina"},
	}, page.Entities, "the names of the catalog and the codes with a consent")
}

func TestComplianceListPassesTheQueryAndAnswersAPageAfterTheLast(t *testing.T) {
	f := newListFixture(t)
	q := domain.ComplianceQuery{Status: domain.StatusActive, Page: 9, PageSize: 6}

	page, err := f.lister.List(context.Background(), q)

	require.NoError(t, err)
	assert.Equal(t, q, f.list.gotQuery)
	assert.Empty(t, page.Items)
	assert.NotNil(t, page.Items)
	assert.Equal(t, 3, page.TotalItems)
	assert.Equal(t, 1, page.TotalPages)
	assert.Equal(t, 9, page.Page)
}

// Criterion 5: a revocation from the app (HU-M1.3-02) and one from the portal
// (HU-W4.2-02) are in the next query of the list, with their channel in the
// indicators.
func TestComplianceListShowsTheRevocationsOfTheAppAndOfThePortal(t *testing.T) {
	f := newListFixture(t)
	ctx := context.Background()
	before, err := f.lister.List(ctx, firstPage(50))
	require.NoError(t, err)
	require.Equal(t, domain.RevokedCount{}, before.Indicators.RevokedLast30Days)

	_, _, err = f.svc.RevokeAsHolder(ctx, "1020304050", "sub-a", "cns-a1", "req-app", holderRequest(f.clock.Now()))
	require.NoError(t, err)
	_, _, err = f.lister.Revoke(ctx, "cns-b1", "sub-ops", "req-portal", portalRequest())
	require.NoError(t, err)

	after, err := f.lister.List(ctx, firstPage(50))
	require.NoError(t, err)
	status := map[string]domain.ConsentStatus{}
	channel := map[string]string{}
	for _, item := range after.Items {
		status[item.Consent.ID] = item.Status
		if item.Revocation != nil {
			channel[item.Consent.ID] = item.Revocation.Channel
		}
	}
	assert.Equal(t, domain.StatusRevoked, status["cns-a1"])
	assert.Equal(t, domain.StatusRevoked, status["cns-b1"])
	assert.Equal(t, domain.StatusActive, status["cns-a2"])
	assert.Equal(t, map[string]string{"cns-a1": "mobile_app", "cns-b1": "compliance_portal"}, channel)
	assert.Equal(t, domain.RevokedCount{Total: 2, MobileApp: 1, CompliancePortal: 1}, after.Indicators.RevokedLast30Days)
	assert.Equal(t, 1, after.Indicators.Active)

	revoked, err := f.lister.List(ctx, domain.ComplianceQuery{Status: domain.StatusRevoked, Page: 1, PageSize: 6})
	require.NoError(t, err)
	assert.Equal(t, 2, revoked.TotalItems, "the filter by status sees them in the next query")
}

func TestComplianceListShowsTheDerivedStatusAndLogsADifference(t *testing.T) {
	f := newListFixture(t)
	buf := captureSlog(t)
	f.list.status = map[string]domain.ConsentStatus{"cns-a1": domain.StatusExpired}

	page, err := f.lister.List(context.Background(), firstPage(50))

	require.NoError(t, err)
	for _, item := range page.Items {
		if item.Consent.ID == "cns-a1" {
			assert.Equal(t, domain.StatusActive, item.Status, "the view wins")
		}
	}
	logged := buf.String()
	assert.Contains(t, logged, "the status of the compliance list differs from the derived one")
	assert.Contains(t, logged, `"consent_id":"cns-a1"`)
	assert.Contains(t, logged, `"list_status":"expired"`)
	assert.Contains(t, logged, `"derived_status":"active"`)
	assert.NotContains(t, logged, "1020304050", "no personal data in the log")
	assert.NotContains(t, logged, "Ana Pérez")
}

func TestComplianceListWithoutDifferencesLogsNothing(t *testing.T) {
	f := newListFixture(t)
	buf := captureSlog(t)

	_, err := f.lister.List(context.Background(), firstPage(50))

	require.NoError(t, err)
	assert.NotContains(t, buf.String(), "differs")
}

func TestComplianceListWrapsTheErrorsOfTheStores(t *testing.T) {
	f := newListFixture(t)
	f.list.err = errors.New("SQLSTATE 57014")

	_, err := f.lister.List(context.Background(), firstPage(6))

	require.Error(t, err)
	assert.ErrorIs(t, err, f.list.err)
	assert.Contains(t, err.Error(), "list the consents")

	f.list.err = nil
	down := errors.New("events down")
	svc := consents.NewComplianceService(consents.ComplianceDeps{
		Consents: f.consents, Events: failingEvents{err: down}, List: f.list, Clock: f.clock,
	})
	_, err = svc.List(context.Background(), firstPage(6))
	require.Error(t, err)
	assert.ErrorIs(t, err, down)
	assert.Contains(t, err.Error(), "read the history of the listed consents")
}

// failingEvents is an event reader that always fails.
type failingEvents struct{ err error }

func (f failingEvents) ListByConsents(context.Context, []string) (map[string][]domain.ConsentEvent, error) {
	return nil, f.err
}
