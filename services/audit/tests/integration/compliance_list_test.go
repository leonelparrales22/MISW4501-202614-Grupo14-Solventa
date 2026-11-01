// This file exercises each acceptance criterion of HU-W4.2-01 (the compliance
// portal lists the consents of Open Finance) over HTTP against the real router
// and a real PostgreSQL (Testcontainers), with the routes mounted as cmd/audit
// does. The only doubles are the tokens and the clock.
package integration_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/adapters/pdf"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/consents"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
)

type listEnv struct {
	*env
	router http.Handler
}

// newListEnv mounts every route of audit over the real stores: the partner
// channel, the evidence, the holder's routes (sub-a is linked to 1020304050)
// and the compliance portal, with the list.
func newListEnv(t *testing.T) *listEnv {
	t.Helper()
	e := newEnv(t)
	ctx := context.Background()
	consentStore, events := postgres.NewConsentStore(e.db), postgres.NewConsentEventStore(e.db)
	links := postgres.NewHolderLinkStore(e.db)
	_, _, err := links.Link(ctx, domain.HolderLink{
		Subject: "sub-a", DocumentID: "1020304050", CustomerID: "cus-a", LinkedAt: e.clock.Now(), EventID: "customer-linked-cus-a",
	})
	require.NoError(t, err)
	names := domain.EntityNames{"aliado-banco-1": "Banco Aliado"}
	svc := consents.NewService(consentStore, e.clock, consents.WithEvents(events))
	holders := consents.NewHolderService(consentStore, events, links, e.clock, names)
	revocationStore := postgres.NewRevocationStore(e.db)
	revocations := consents.NewRevocationService(consents.RevocationDeps{
		Consents: consentStore, Events: events, Revocations: revocationStore, Notices: postgres.NewNoticeStore(e.db),
		Receipts: pdf.NewUncompressed(), Clock: e.clock, Names: names,
	})
	compliance := consents.NewComplianceService(consents.ComplianceDeps{
		Revocations: revocations, Consents: consentStore, Events: events, Records: revocationStore,
		List: postgres.NewComplianceListStore(e.db), Clock: e.clock, Names: names,
	})
	verifier := tokenVerifier{
		"operations": {Subject: "u-ops", Roles: []security.Role{security.RoleOperations}},
		"advisor":    {Subject: "u-adv", Roles: []security.Role{security.RoleAdvisor}},
		"insured-a":  {Subject: "sub-a", Roles: []security.Role{security.RoleInsured}},
		"partner":    {Subject: "u-par", Roles: []security.Role{security.RolePartner}},
	}
	router := httpadapter.NewRouter(httpadapter.WithConsents(svc), httpadapter.WithConsentReads(svc, verifier),
		httpadapter.WithHolderConsents(holders, verifier), httpadapter.WithHolderRevocations(holders, revocations, verifier),
		httpadapter.WithComplianceRevocations(compliance, verifier), httpadapter.WithComplianceConsents(compliance, verifier))
	return &listEnv{env: e, router: router}
}

func (l *listEnv) call(method, target, token, key, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	rec := httptest.NewRecorder()
	l.router.ServeHTTP(rec, req)
	return rec
}

// list asks for the list as the analyst of operations.
func (l *listEnv) list(t *testing.T, query string) map[string]any {
	t.Helper()
	rec := l.call(http.MethodGet, "/v1/audit/consents?"+query, "operations", "", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	return jsonObject(t, rec)
}

// grantAs records a consent through the partner route at the time of the
// clock, as screen E1 does.
func (l *listEnv) grantAs(t *testing.T, n int, document, name, partner string) string {
	t.Helper()
	body := fmt.Sprintf(`{"customer_id":"cust-%d","document_id":"%s","full_name":"%s","terms_version":"v1",
"accepted_sources":["open_finance","open_data","kyc"],"accepted_data_policy":true,"channel":"embedded",
"partner_id":"%s","application_id":"app-%d"}`, n, document, name, partner, n)
	rec := l.call(http.MethodPost, "/v1/audit/consents", "", "", body)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	return jsonObject(t, rec)["consent_id"].(string)
}

func ids(page map[string]any) []string {
	var out []string
	for _, raw := range page["items"].([]any) {
		out = append(out, raw.(map[string]any)["consent_id"].(string))
	}
	return out
}

func statuses(page map[string]any) map[string]string {
	out := map[string]string{}
	for _, raw := range page["items"].([]any) {
		item := raw.(map[string]any)
		out[item["consent_id"].(string)] = item["status"].(string)
	}
	return out
}

var listNames = []string{"Ana María Pérez", "José Ñuñez", "Luis Gómez", "Sofía Ríos"}

func TestComplianceListAcceptanceCriteria(t *testing.T) {
	l := newListEnv(t)
	now := time.Date(2026, 10, 30, 15, 0, 0, 0, time.UTC)

	// One expired consent, granted 13 months ago.
	l.clock.set(now.AddDate(0, -13, 0))
	expired := l.grantAs(t, 100, "5566778899", "Elena Vencida", "aliado-banco-1")
	// Thirteen consents of the last 13 days; the holder A (1020304050) has the
	// first two, one entity has every third one.
	var granted []string
	for i := 0; i < 13; i++ {
		l.clock.set(now.AddDate(0, 0, -13+i))
		document := fmt.Sprintf("70000000%02d", i)
		if i < 2 {
			document = "1020304050"
		}
		partner := "aliado-banco-1"
		if i%3 == 0 {
			partner = "aliado-coop"
		}
		granted = append(granted, l.grantAs(t, i, document, listNames[i%len(listNames)], partner))
	}
	l.clock.set(now)
	newestFirst := make([]string, 0, 14)
	for i := len(granted) - 1; i >= 0; i-- {
		newestFirst = append(newestFirst, granted[i])
	}
	newestFirst = append(newestFirst, expired)

	t.Run("CA1 a paged table with the holder, the id, the grant, the expiry, the status and the scope", func(t *testing.T) {
		page := l.list(t, "page_size=6")
		assert.Equal(t, float64(14), page["total_items"])
		assert.Equal(t, float64(3), page["total_pages"])
		assert.Equal(t, newestFirst[:6], ids(page), "most recently granted first")
		first := page["items"].([]any)[0].(map[string]any)
		assert.Equal(t, granted[12], first["consent_id"])
		assert.Equal(t, map[string]any{"full_name": "Ana María Pérez", "document_masked": "******0012"}, first["holder"])
		assert.Equal(t, "2026-10-29T15:00:00Z", first["granted_at"])
		assert.Equal(t, "2027-10-29T15:00:00Z", first["expires_at"])
		assert.Equal(t, "active", first["status"])
		assert.Equal(t, "embedded", first["grant_channel"])
		assert.Len(t, first["scope"], 3)
		assert.Equal(t, []any{map[string]any{"code": "aliado-coop", "name": "aliado-coop"}}, first["entities"],
			"a code without a display name is shown as it is")

		last := l.list(t, "page_size=6&page=3")
		assert.Equal(t, newestFirst[12:], ids(last))
		assert.Equal(t, "expired", statuses(last)[expired], "the one granted 13 months ago")
	})

	t.Run("CA2 the filters by status and by grant days, and the pages keep them", func(t *testing.T) {
		onlyExpired := l.list(t, "status=expired")
		assert.Equal(t, []string{expired}, ids(onlyExpired))

		// Granted between 2026-10-20 and 2026-10-25 (days of Colombia): i = 3..8.
		days := l.list(t, "status=active&granted_from=2026-10-20&granted_to=2026-10-25&page_size=4")
		assert.Equal(t, float64(6), days["total_items"])
		assert.Equal(t, float64(2), days["total_pages"])
		assert.Equal(t, []string{granted[8], granted[7], granted[6], granted[5]}, ids(days))
		second := l.list(t, "status=active&granted_from=2026-10-20&granted_to=2026-10-25&page_size=4&page=2")
		assert.Equal(t, []string{granted[4], granted[3]}, ids(second), "page 2 keeps the filter")
		assert.Equal(t, float64(6), second["total_items"])

		entity := l.list(t, "entity=aliado-coop&page_size=50")
		assert.Equal(t, []string{granted[12], granted[9], granted[6], granted[3], granted[0]}, ids(entity))

		byName := l.list(t, "q=nunez&page_size=50")
		assert.Len(t, ids(byName), 3, "José Ñuñez without capitals or accents")
		byDocument := l.list(t, "q="+url.QueryEscape("1.020.304.050"))
		assert.ElementsMatch(t, []string{granted[0], granted[1]}, ids(byDocument), "the full document, written with dots")
		prefix := l.list(t, "q=102030")
		assert.Empty(t, ids(prefix), "never a prefix of a document")
		byID := l.list(t, "q="+granted[5])
		assert.Equal(t, []string{granted[5]}, ids(byID))
	})

	t.Run("CA3 an empty table with no error when nothing meets the filter", func(t *testing.T) {
		page := l.list(t, "status=revoked&entity=aliado-banco-1")
		assert.Equal(t, []any{}, page["items"])
		assert.Equal(t, float64(0), page["total_items"])
		assert.Equal(t, float64(1), page["total_pages"], "page 1 of 1")
		indicators := page["indicators"].(map[string]any)
		assert.Equal(t, float64(13), indicators["active"], "the indicators do not depend on the filter")
		assert.Equal(t, float64(1), indicators["expired"])
	})

	t.Run("CA4 the detail has the holder, the channel, the grant, the purpose, the validity, the scope, the entities and the history", func(t *testing.T) {
		rec := l.call(http.MethodGet, "/v1/audit/consents/"+granted[10]+"/revocation", "operations", "", "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		state := jsonObject(t, rec)
		assert.Equal(t, "Luis Gómez", state["holder"].(map[string]any)["full_name"])
		assert.Equal(t, "embedded", state["grant_channel"])
		assert.Equal(t, "2026-10-27T15:00:00Z", state["granted_at"])
		assert.Equal(t, "2027-10-27T15:00:00Z", state["expires_at"])
		assert.Nil(t, state["ended_at"])
		assert.Equal(t, "risk_profiling_mortgage_life_offer", state["purpose"])
		assert.Equal(t, "v1", state["terms_version"])
		assert.Len(t, state["scope"], 3)
		assert.Equal(t, []any{map[string]any{"code": "aliado-banco-1", "name": "Banco Aliado"}}, state["entities"])
		history := state["history"].([]any)
		require.Len(t, history, 1)
		assert.Equal(t, map[string]any{
			"status": "granted", "occurred_at": "2026-10-27T15:00:00Z", "entity_code": nil, "actor_type": nil,
			"channel": "embedded", "reason": nil, "reference": nil,
		}, history[0])
	})

	t.Run("CA5 a revocation from the app and one from the portal show in the next query", func(t *testing.T) {
		before := l.list(t, "page_size=50")["indicators"].(map[string]any)
		assert.Equal(t, map[string]any{"total": float64(0), "mobile_app": float64(0), "compliance_portal": float64(0)}, before["revoked_last_30_days"])

		holderBody := `{"requested_at":"` + l.clock.Now().Format(time.RFC3339) + `","device":"Google Pixel 8","confirmation_method":"biometric"}`
		app := l.call(http.MethodPost, "/v1/consents/"+granted[0]+"/revocations", "insured-a", "6b7e3f5a-2c1d-4e8f-9a6b-1c2d3e4f5a6b", holderBody)
		require.Equal(t, http.StatusCreated, app.Code, app.Body.String())
		portal := l.call(http.MethodPost, "/v1/audit/consents/"+granted[4]+"/revocations", "operations", portalKey, portalJSON)
		require.Equal(t, http.StatusCreated, portal.Code, portal.Body.String())

		after := l.list(t, "page_size=50")
		assert.Equal(t, "revoked", statuses(after)[granted[0]])
		assert.Equal(t, "revoked", statuses(after)[granted[4]])
		var channels []string
		for _, raw := range after["items"].([]any) {
			if r, ok := raw.(map[string]any)["revocation"].(map[string]any); ok {
				channels = append(channels, r["channel"].(string)+"/"+r["notice_status"].(string))
			}
		}
		assert.ElementsMatch(t, []string{"mobile_app/in_progress", "compliance_portal/in_progress"}, channels)
		indicators := after["indicators"].(map[string]any)
		assert.Equal(t, map[string]any{"total": float64(2), "mobile_app": float64(1), "compliance_portal": float64(1)},
			indicators["revoked_last_30_days"])
		assert.Equal(t, float64(11), indicators["active"])
		assert.Equal(t, float64(0), indicators["revocations_out_of_deadline"])

		revoked := l.list(t, "status=revoked")
		assert.ElementsMatch(t, []string{granted[0], granted[4]}, ids(revoked), "the filter by status sees them at once")
		state := jsonObject(t, l.call(http.MethodGet, "/v1/audit/consents/"+granted[0]+"/revocation", "operations", "", ""))
		assert.Equal(t, "revoked", state["status"])
		assert.Equal(t, "mobile_app", state["history"].([]any)[2].(map[string]any)["channel"], "the detail shows the channel")

		// No entity confirms within its 5 minutes: both are partially revoked and out of the deadline.
		l.clock.set(now.Add(domain.RevocationNoticeDeadline))
		late := l.list(t, "status=partially_revoked")
		assert.ElementsMatch(t, []string{granted[0], granted[4]}, ids(late))
		assert.Equal(t, float64(2), late["indicators"].(map[string]any)["revocations_out_of_deadline"])
		assert.Empty(t, ids(l.list(t, "status=revoked")))
	})

	t.Run("CA6 only the operations role reads the list and reading writes nothing", func(t *testing.T) {
		rows := func() int {
			return l.count(t, `SELECT (SELECT count(*) FROM consent_record) + (SELECT count(*) FROM consent_event)
				+ (SELECT count(*) FROM consent_revocation) + (SELECT count(*) FROM revocation_notice)
				+ (SELECT count(*) FROM revocation_notice_step) + (SELECT count(*) FROM holder_link)
				+ (SELECT count(*) FROM underwriting_decision)`)
		}
		before := rows()
		for _, caller := range []string{"", "forged", "insured-a", "advisor", "partner"} {
			rec := l.call(http.MethodGet, "/v1/audit/consents?status=active", caller, "", "")
			want := http.StatusForbidden
			if caller == "" || caller == "forged" {
				want = http.StatusUnauthorized
			}
			assert.Equal(t, want, rec.Code, "caller %q", caller)
			assert.NotContains(t, rec.Body.String(), "consent_id")
		}
		for _, q := range []string{"", "status=expired", "q=maria", "page=9"} {
			l.list(t, q)
		}
		assert.Equal(t, before, rows(), "the list only reads")
	})
}

// A smoke of volume, for information only: with 5 000 consents written in SQL
// the list still answers every filter with the totals the data has. The times
// are not a result (contexto/MAPA-PRUEBAS.md §2); the plans of the queries are
// in the adapter (TestExplainTheListQueries).
func TestComplianceListOverFiveThousandConsents(t *testing.T) {
	l := newListEnv(t)
	now := time.Date(2026, 10, 30, 15, 0, 0, 0, time.UTC)
	l.clock.set(now)
	_, err := l.db.Exec(`INSERT INTO consent_record (consent_id, customer_id, holder_document_id, holder_full_name,
			terms_version, scope, purpose, channel, partner_id, application_id, user_agent, granted_at)
		SELECT 'cns-v-' || lpad(i::text, 5, '0'), 'cust-v-' || i, (1000000000 + i)::text,
			(ARRAY['Ana María Pérez', 'José Ñuñez', 'Luis Gómez', 'Sofía Ríos', 'Martín Ibáñez'])[1 + i % 5] || ' ' || i,
			'v1', '[{"code":"open_finance","data_categories":["balances"],"validity":"12_months"}]'::jsonb,
			'risk_profiling_mortgage_life_offer', 'embedded', (ARRAY['aliado-banco-1', 'aliado-coop'])[1 + i % 2], 'app-v-' || i, '',
			$1::timestamptz - i * interval '2 hours'
		FROM generate_series(1, 5000) AS i`, now)
	require.NoError(t, err)
	_, err = l.db.Exec(`INSERT INTO consent_event (event_id, consent_id, status, occurred_at, channel)
		SELECT 'ev-v-' || r.consent_id, r.consent_id, 'revoked', r.granted_at + interval '1 hour', 'mobile_app'
		FROM consent_record r WHERE right(r.consent_id, 2) = '00'`)
	require.NoError(t, err)

	all := l.list(t, "page_size=50")
	assert.Equal(t, float64(5000), all["total_items"])
	assert.Equal(t, float64(100), all["total_pages"])
	// Granted every 2 hours: from i = 4 380 (8 760 hours ago) on, they are
	// expired; one in a hundred is revoked (50), 7 of them among the old ones.
	indicators := all["indicators"].(map[string]any)
	assert.Equal(t, float64(621-7), indicators["expired"])
	assert.Equal(t, float64(4379-43), indicators["active"], "the revoked ones are not active")
	revoked := l.list(t, "status=revoked&page_size=6")
	assert.Equal(t, float64(50), revoked["total_items"], "one in a hundred")
	assert.Len(t, ids(l.list(t, "q=ibanez%204994")), 1)
	lastExpired := l.list(t, "status=expired&page=103&page_size=6")
	assert.Len(t, ids(lastExpired), 2, "the last page of the expired ones")
	assert.Equal(t, float64(103), lastExpired["total_pages"])
}
