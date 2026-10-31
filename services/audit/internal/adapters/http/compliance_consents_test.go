package httpadapter_test

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
)

// The list of consents of the compliance portal (HU-W4.2-01).

// listFake is the use case of the list: it records the query and answers with
// three consents (active, partially revoked from the portal, expired) or fails.
type listFake struct {
	mu    sync.Mutex
	err   error
	calls int
	got   domain.ComplianceQuery
}

var listAsOf = time.Date(2026, 10, 30, 15, 0, 0, 123456000, time.UTC)

func (f *listFake) List(_ context.Context, q domain.ComplianceQuery) (domain.CompliancePage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.got = q
	if f.err != nil {
		return domain.CompliancePage{}, f.err
	}
	partial := portalState("cns-c-partial", true, true).View
	expired := holderView("cns-c-expired", "9988776655", nil, holderNow.AddDate(-1, -1, 0))
	return domain.CompliancePage{
		Items: []domain.ConsentView{portalState("cns-c-active", false, false).View, partial, expired},
		Page:  q.Page, PageSize: q.PageSize, TotalItems: 13, TotalPages: 3,
		Indicators: domain.ComplianceIndicators{
			Active: 9, Expired: 1, RevokedLast30Days: domain.RevokedCount{Total: 3, MobileApp: 2, CompliancePortal: 1},
			OutOfDeadline: 1, AsOf: listAsOf,
		},
		Entities: []domain.Entity{{Code: "aliado-banco-1", Name: "Banco Aliado"}, {Code: "aliado-coop", Name: "aliado-coop"}},
	}, nil
}

func (f *listFake) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func listRouter(svc httpadapter.ComplianceConsentLister) http.Handler {
	return httpadapter.NewRouter(httpadapter.WithComplianceConsents(svc, complianceVerifier()))
}

func TestTheAnalystListsTheConsents(t *testing.T) {
	fake := &listFake{}

	rec := complianceDo(listRouter(fake), http.MethodGet, "/v1/audit/consents?page_size=6", "operations", "", nil)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, domain.ComplianceQuery{Page: 1, PageSize: 6}, fake.got, "the defaults and the size asked for")
	out := decodeMap(t, rec)
	assert.Equal(t, float64(1), out["page"])
	assert.Equal(t, float64(6), out["page_size"])
	assert.Equal(t, float64(13), out["total_items"])
	assert.Equal(t, float64(3), out["total_pages"])
	assert.Equal(t, map[string]any{
		"active": float64(9), "expired": float64(1),
		"revoked_last_30_days":        map[string]any{"total": float64(3), "mobile_app": float64(2), "compliance_portal": float64(1)},
		"revocations_out_of_deadline": float64(1), "as_of": "2026-10-30T15:00:00.123456Z",
	}, out["indicators"])
	assert.Equal(t, []any{
		map[string]any{"code": "aliado-banco-1", "name": "Banco Aliado"},
		map[string]any{"code": "aliado-coop", "name": "aliado-coop"},
	}, out["entities"])

	items := out["items"].([]any)
	require.Len(t, items, 3)
	active := items[0].(map[string]any)
	assert.Equal(t, "cns-c-active", active["consent_id"])
	assert.Equal(t, map[string]any{"full_name": "Ana María Pérez", "document_masked": "******4050"}, active["holder"], "decision D-4 a")
	assert.Equal(t, "2026-09-24T20:00:00Z", active["granted_at"])
	assert.Equal(t, "2027-09-24T20:00:00Z", active["expires_at"])
	assert.Nil(t, active["ended_at"])
	assert.Equal(t, "active", active["status"])
	assert.Nil(t, active["revocation"])
	assert.Equal(t, "embedded", active["grant_channel"])
	assert.Len(t, active["scope"], 3, "criterion 1: the authorized scope")
	assert.Equal(t, []any{map[string]any{"code": "aliado-banco-1", "name": "Banco Aliado"}}, active["entities"])

	partial := items[1].(map[string]any)
	assert.Equal(t, "partially_revoked", partial["status"])
	assert.Equal(t, "2026-10-24T20:00:01.123456Z", partial["ended_at"])
	assert.Equal(t, map[string]any{
		"effective_at": "2026-10-24T20:00:01.123456Z", "channel": "compliance_portal", "notice_status": "partial",
	}, partial["revocation"])
	expired := items[2].(map[string]any)
	assert.Equal(t, "expired", expired["status"])
	assert.Equal(t, "******6655", expired["holder"].(map[string]any)["document_masked"])

	assert.NotContains(t, rec.Body.String(), "1020304050", "the full document is only in the evidence")
	assert.NotContains(t, rec.Body.String(), "9988776655")
}

func TestTheListReadsEveryFilterOfTheQuery(t *testing.T) {
	fake := &listFake{}

	rec := complianceDo(listRouter(fake), http.MethodGet,
		"/v1/audit/consents?status=revoked&granted_from=2026-10-01&granted_to=2026-10-30&entity=aliado-banco-1&q=Ana%20Mar%C3%ADa&page=2&page_size=6",
		"operations", "", nil)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, domain.ComplianceQuery{
		Status:      domain.StatusRevoked,
		GrantedFrom: time.Date(2026, 10, 1, 5, 0, 0, 0, time.UTC), GrantedBefore: time.Date(2026, 10, 31, 5, 0, 0, 0, time.UTC),
		Entity: "aliado-banco-1", Search: domain.NewComplianceSearch("Ana María"), Page: 2, PageSize: 6,
	}, fake.got)
}

func TestTheListNamesEveryInvalidParameterAtOnce(t *testing.T) {
	fake := &listFake{}

	rec := complianceDo(listRouter(fake), http.MethodGet,
		"/v1/audit/consents?status=pending&granted_from=2026-10-30&granted_to=2026-10-01&entity=a%20b&page=0&page_size=51",
		"operations", "", nil)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	out := decodeMap(t, rec)
	assert.Equal(t, "validation_failed", out["error"])
	assert.Equal(t, []any{
		map[string]any{"field": "status", "code": "invalid_format"},
		map[string]any{"field": "granted_to", "code": "out_of_range"},
		map[string]any{"field": "entity", "code": "invalid_format"},
		map[string]any{"field": "page", "code": "out_of_range"},
		map[string]any{"field": "page_size", "code": "out_of_range"},
	}, out["fields"])
	assert.Zero(t, fake.count(), "nothing is queried")
}

func TestTheListAnswersAFailureOfTheStoreWith503(t *testing.T) {
	fake := &listFake{err: errors.New("list the consents: SQLSTATE 57014 for 1020304050")}

	rec := complianceDo(listRouter(fake), http.MethodGet, "/v1/audit/consents", "operations", "", nil)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.JSONEq(t, `{"error":"service_unavailable","message":"the consents could not be listed; try again"}`, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "SQLSTATE", "the cause stays in the log")
}

func TestTheListOnlyAnswersGet(t *testing.T) {
	h := listRouter(&listFake{})

	assert.Equal(t, http.StatusMethodNotAllowed, complianceDo(h, http.MethodPut, "/v1/audit/consents", "operations", "", nil).Code)
	assert.Equal(t, http.StatusMethodNotAllowed, complianceDo(h, http.MethodDelete, "/v1/audit/consents", "operations", "", nil).Code)
}
