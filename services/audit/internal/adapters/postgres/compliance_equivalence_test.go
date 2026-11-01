package postgres_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math/rand"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
)

// HU-W4.2-01, decision D-2 b′: the list of the compliance portal is filtered,
// counted and paged in PostgreSQL with the status rule written in SQL. This
// test is the guard of the two definitions: on the same data and at several
// instants, the query of the adapter and the reference in Go (domain.View,
// FilterForCompliance, Paginate, Indicators) must give the same status for
// every consent, the same rows in the same order on every page, the same total
// and the same indicators. The data mixes borders written by hand with
// combinations generated with a fixed seed. If someone changes one of the two
// rules alone, this test fails.

// eqNow is the instant the hand-written borders are around (microseconds, as
// PostgreSQL keeps them).
var eqNow = time.Date(2026, 10, 30, 15, 0, 0, 123456000, time.UTC)

type eqFixture struct {
	t        *testing.T
	db       *sql.DB
	store    *postgres.ComplianceListStore
	consents map[string]domain.Consent
	ids      []string
	events   int
}

func newEqFixture(t *testing.T) *eqFixture {
	db := newDB(t)
	return &eqFixture{t: t, db: db, store: postgres.NewComplianceListStore(db), consents: map[string]domain.Consent{}}
}

// grant stores a consent through the real store and keeps what it reads back.
func (f *eqFixture) grant(id string, at time.Time, partner, document, name string) domain.Consent {
	f.t.Helper()
	ctx := context.Background()
	terms := domain.TermsV1()
	store := postgres.NewConsentStore(f.db)
	_, created, err := store.Create(ctx, domain.Consent{
		ID: id, CustomerID: "cust-" + id, Holder: domain.Holder{DocumentID: document, FullName: name},
		TermsVersion: terms.Version, Scope: terms.Sources, Purpose: terms.Purpose, Channel: domain.ChannelEmbedded,
		PartnerID: partner, ApplicationID: "app-" + id, UserAgent: "UA", GrantedAt: at.UTC().Truncate(time.Microsecond),
	})
	require.NoError(f.t, err)
	require.True(f.t, created, id)
	c, err := store.Get(ctx, id)
	require.NoError(f.t, err)
	f.consents[id] = c
	f.ids = append(f.ids, id)
	return c
}

// rawEvent is one row of consent_event as the test writes it. entity is SQL:
// nil is NULL; a string (also "") is stored as it is.
type rawEvent struct {
	id, consent, status, channel string
	entity                       any
	at, recorded                 time.Time
	details                      any
}

func (f *eqFixture) insert(e rawEvent) {
	f.t.Helper()
	f.events++
	if e.id == "" {
		e.id = fmt.Sprintf("ev-%05d", f.events)
	}
	if e.recorded.IsZero() {
		e.recorded = e.at
	}
	var channel any
	if e.channel != "" {
		channel = e.channel
	}
	_, err := f.db.ExecContext(context.Background(), `INSERT INTO consent_event
		(event_id, consent_id, entity_code, status, occurred_at, recorded_at, channel, details)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		e.id, e.consent, e.entity, e.status, e.at.UTC().Truncate(time.Microsecond),
		e.recorded.UTC().Truncate(time.Microsecond), channel, e.details)
	require.NoError(f.t, err)
}

// windows is the details of a revoked event: the entities and the end of
// their window.
func windows(deadline time.Time, entities ...string) []byte {
	ws := make([]domain.EntityWindow, 0, len(entities))
	for _, e := range entities {
		ws = append(ws, domain.EntityWindow{Code: e, DeadlineAt: deadline.UTC().Truncate(time.Microsecond)})
	}
	raw, _ := json.Marshal(domain.RevokedDetails{Entities: ws})
	return raw
}

func retryDetails(deadline time.Time) []byte {
	raw, _ := json.Marshal(domain.RetryDetails{DeadlineAt: deadline.UTC().Truncate(time.Microsecond)})
	return raw
}

// revoke writes the revocation_requested and revoked events of the consent at
// the instant, notifying the entities with a window of five minutes.
func (f *eqFixture) revoke(consent string, at time.Time, channel string, entities ...string) time.Time {
	f.t.Helper()
	deadline := at.Add(domain.RevocationNoticeDeadline)
	f.insert(rawEvent{consent: consent, status: domain.HistoryRevocationRequested, at: at, channel: channel})
	f.insert(rawEvent{consent: consent, status: domain.HistoryRevoked, at: at, channel: channel, details: windows(deadline, entities...)})
	return deadline
}

func (f *eqFixture) entity(consent, entity, status string, at time.Time, details []byte) {
	f.t.Helper()
	var d any
	if details != nil {
		d = details
	}
	f.insert(rawEvent{consent: consent, entity: entity, status: status, at: at, details: d})
}

// reference computes the list in Go at now.
func (f *eqFixture) reference(views []domain.ConsentView, q domain.ComplianceQuery) ([]domain.ConsentView, int) {
	matching := domain.FilterForCompliance(views, q)
	return domain.Paginate(matching, q), len(matching)
}

func (f *eqFixture) views(now time.Time) []domain.ConsentView {
	f.t.Helper()
	events, err := postgres.NewConsentEventStore(f.db).ListByConsents(context.Background(), f.ids)
	require.NoError(f.t, err)
	out := make([]domain.ConsentView, 0, len(f.ids))
	for _, id := range f.ids {
		out = append(out, domain.View(f.consents[id], events[id], now, nil))
	}
	return out
}

func (f *eqFixture) entityCodes() []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range f.consents {
		if !seen[c.PartnerID] {
			seen[c.PartnerID] = true
			out = append(out, c.PartnerID)
		}
	}
	sort.Strings(out)
	return out
}

type eqRow struct {
	ID     string
	Status domain.ConsentStatus
}

// compare runs the query at now through the adapter and the reference and
// asserts they agree.
func (f *eqFixture) compare(views []domain.ConsentView, q domain.ComplianceQuery, now time.Time, label string) domain.ComplianceListResult {
	f.t.Helper()
	got, err := f.store.ListForCompliance(context.Background(), q, now)
	require.NoError(f.t, err, label)

	page, total := f.reference(views, q)
	want := make([]eqRow, 0, len(page))
	for _, v := range page {
		want = append(want, eqRow{ID: v.Consent.ID, Status: v.Status})
	}
	have := make([]eqRow, 0, len(got.Rows))
	for _, r := range got.Rows {
		have = append(have, eqRow{ID: r.Consent.ID, Status: r.Status})
		assert.Equal(f.t, f.consents[r.Consent.ID], r.Consent, "%s: the record of %s survives the query", label, r.Consent.ID)
	}
	assert.Equal(f.t, want, have, "%s: the rows of the page and their status", label)
	assert.Equal(f.t, total, got.TotalItems, "%s: the total", label)
	indicators := domain.Indicators(views, now)
	indicators.AsOf = time.Time{}
	assert.Equal(f.t, indicators, got.Indicators, "%s: the indicators", label)
	assert.Equal(f.t, f.entityCodes(), got.EntityCodes, "%s: the entities", label)
	return got
}

// seedBorders writes the borders of the status rule by hand.
func (f *eqFixture) seedBorders() {
	f.t.Helper()
	// 29 February: Go expires it on 1 March, PostgreSQL alone on 28 February.
	f.grant("cns-h-leap", time.Date(2024, 2, 29, 10, 0, 0, 0, time.UTC), "aliado-banco-1", "1000000001", "Lucía Leap")
	// It expires exactly at eqNow.
	f.grant("cns-h-expires-now", eqNow.AddDate(-1, 0, 0), "aliado-banco-1", "1000000002", "Andrés Vence Hoy")
	// The window of its only entity ends exactly at eqNow.
	f.grant("cns-h-deadline-now", eqNow.AddDate(0, -2, 0), "aliado-banco-1", "1000000003", "Pedro Plazo")
	f.revoke("cns-h-deadline-now", eqNow.Add(-domain.RevocationNoticeDeadline), "mobile_app", "aliado-banco-1")
	// Confirmed in the last microsecond of the window, and at its end.
	f.grant("cns-h-confirm-last", eqNow.AddDate(0, -2, 0), "aliado-banco-2", "1000000004", "Ana María Pérez")
	d := f.revoke("cns-h-confirm-last", eqNow.AddDate(0, 0, -1), "compliance_portal", "aliado-banco-2")
	f.entity("cns-h-confirm-last", "aliado-banco-2", domain.HistoryRevocationConfirmed, d.Add(-time.Microsecond), nil)
	f.grant("cns-h-confirm-late", eqNow.AddDate(0, -2, 0), "aliado-banco-2", "1000000005", "José Ñuñez")
	d = f.revoke("cns-h-confirm-late", eqNow.AddDate(0, 0, -2), "mobile_app", "aliado-banco-2")
	f.entity("cns-h-confirm-late", "aliado-banco-2", domain.HistoryRevocationConfirmed, d, nil)
	// Not confirmed, then a retry that the entity confirms.
	f.grant("cns-h-retry-ok", eqNow.AddDate(0, -3, 0), "aliado-coop", "1000000006", "MARÍA PEÑA")
	d = f.revoke("cns-h-retry-ok", eqNow.AddDate(0, 0, -3), "compliance_portal", "aliado-coop")
	f.entity("cns-h-retry-ok", "aliado-coop", domain.HistoryRevocationNotConfirmed, d, nil)
	f.entity("cns-h-retry-ok", "aliado-coop", domain.HistoryRevocationRetryRequested, d.Add(time.Second), retryDetails(d.Add(6*time.Minute)))
	f.entity("cns-h-retry-ok", "aliado-coop", domain.HistoryRevocationConfirmed, d.Add(2*time.Minute), nil)
	// Not confirmed, then a retry whose window is still open: still partial.
	f.grant("cns-h-retry-open", eqNow.AddDate(0, -3, 0), "aliado-coop", "1000000007", "Carlos Reintento")
	d = f.revoke("cns-h-retry-open", eqNow.Add(-10*time.Minute), "compliance_portal", "aliado-coop")
	f.entity("cns-h-retry-open", "aliado-coop", domain.HistoryRevocationNotConfirmed, d, nil)
	f.entity("cns-h-retry-open", "aliado-coop", domain.HistoryRevocationRetryRequested, d.Add(time.Second), retryDetails(eqNow.Add(time.Minute)))
	// A retry without a not_confirmed (the worker did not run): revoked now,
	// but the first window was missed.
	f.grant("cns-h-retry-only", eqNow.AddDate(0, -3, 0), "aliado-coop", "1000000008", "Diana Sola")
	d = f.revoke("cns-h-retry-only", eqNow.Add(-8*time.Minute), "mobile_app", "aliado-coop")
	f.entity("cns-h-retry-only", "aliado-coop", domain.HistoryRevocationRetryRequested, d.Add(time.Second), retryDetails(eqNow.Add(2*time.Minute)))
	// A retry with an unreadable deadline keeps the first window.
	f.grant("cns-h-retry-bad", eqNow.AddDate(0, -3, 0), "aliado-coop", "1000000009", "Elena Mala")
	d = f.revoke("cns-h-retry-bad", eqNow.Add(-6*time.Minute), "mobile_app", "aliado-coop")
	f.entity("cns-h-retry-bad", "aliado-coop", domain.HistoryRevocationRetryRequested, d.Add(-time.Minute), []byte(`{"deadline_at":"tomorrow"}`))
	// Revoked after it expired: still expired.
	f.grant("cns-h-late-revoke", eqNow.AddDate(0, -13, 0), "aliado-banco-1", "1000000010", "Fabio Tarde")
	f.revoke("cns-h-late-revoke", eqNow.AddDate(0, -1, 0), "mobile_app", "aliado-banco-1")
	// Two entities: one confirmed in time, the other still in its window.
	f.grant("cns-h-two", eqNow.AddDate(0, -1, 0), "aliado-banco-1", "1000000011", "Gloria Dos")
	d = f.revoke("cns-h-two", eqNow.Add(-2*time.Minute), "compliance_portal", "aliado-banco-1", "aliado-banco-2")
	f.entity("cns-h-two", "aliado-banco-1", domain.HistoryRevocationConfirmed, eqNow.Add(-time.Minute), nil)
	_ = d
	// Two revoked events at the same instant: the first by recorded_at, then
	// by the id compared byte by byte ("ev-B" before "ev-a"), decides.
	f.grant("cns-h-two-revoked", eqNow.AddDate(0, -1, 0), "aliado-banco-1", "1000000012", "Hugo Doble")
	at := eqNow.AddDate(0, 0, -4)
	f.insert(rawEvent{id: "ev-a-first", consent: "cns-h-two-revoked", status: domain.HistoryRevoked, at: at, recorded: at,
		channel: "mobile_app", details: windows(at.Add(domain.RevocationNoticeDeadline), "aliado-banco-1")})
	f.insert(rawEvent{id: "ev-B-first", consent: "cns-h-two-revoked", status: domain.HistoryRevoked, at: at, recorded: at,
		channel: "compliance_portal", details: windows(at.Add(time.Hour), "aliado-banco-1")})
	f.insert(rawEvent{id: "ev-0-later", consent: "cns-h-two-revoked", status: domain.HistoryRevoked, at: at, recorded: at.Add(time.Second),
		channel: "assisted", details: windows(at.Add(time.Hour), "aliado-banco-1")})
	f.entity("cns-h-two-revoked", "aliado-banco-1", domain.HistoryRevocationConfirmed, at.Add(10*time.Minute), nil)
	// The reserved status as the last event of the whole consent.
	f.grant("cns-h-pending", eqNow.AddDate(0, -1, 0), "aliado-banco-2", "1000000013", "Iván Reservado")
	f.insert(rawEvent{consent: "cns-h-pending", status: string(domain.StatusRevocationPending), at: eqNow.AddDate(0, 0, -1)})
	f.insert(rawEvent{consent: "cns-h-pending", status: domain.HistoryRevocationRequested, at: eqNow.AddDate(0, 0, -2)})
	// A revoked event with an empty entity code is an event of the whole consent.
	f.grant("cns-h-empty-entity", eqNow.AddDate(0, -1, 0), "aliado-banco-2", "1000000014", "Julia Vacía")
	f.insert(rawEvent{consent: "cns-h-empty-entity", entity: "", status: domain.HistoryRevoked, at: eqNow.AddDate(0, 0, -5), channel: "mobile_app"})
	// Details that are not what audit writes: no entity to follow.
	f.grant("cns-h-odd-details", eqNow.AddDate(0, -1, 0), "aliado-banco-2", "1000000015", "Karla Rara")
	f.insert(rawEvent{consent: "cns-h-odd-details", status: domain.HistoryRevoked, at: eqNow.AddDate(0, 0, -6), details: []byte(`"text"`)})
	f.grant("cns-h-odd-entities", eqNow.AddDate(0, -1, 0), "aliado-banco-2", "1000000016", "Luis Raro")
	f.insert(rawEvent{consent: "cns-h-odd-entities", status: domain.HistoryRevoked, at: eqNow.AddDate(0, 0, -7),
		details: []byte(`{"entities":[{"code":"aliado-banco-2"},{"code":"aliado-banco-1","deadline_at":null},{"deadline_at":"` +
			eqNow.AddDate(0, 0, -7).Format(time.RFC3339Nano) + `"}]}`)})
	// The borders of the day in Colombia (UTC-5).
	f.grant("cns-h-day-before", time.Date(2026, 10, 1, 4, 59, 59, 999999000, time.UTC), "aliado-banco-1", "1000000017", "Mario Antes")
	f.grant("cns-h-day-start", time.Date(2026, 10, 1, 5, 0, 0, 0, time.UTC), "aliado-banco-1", "1000000018", "Nora Inicio")
	// Wildcards of LIKE in a name, and a document with letters.
	f.grant("cns-h-wildcards", eqNow.AddDate(0, -1, 0), "aliado-banco-1", "AB12345", "Ana_Maria 100% Real")
	// The same grant instant: the id compared byte by byte breaks the tie.
	f.grant("cns-h-tie-a", eqNow.AddDate(0, -1, -1), "aliado-banco-1", "1000000019", "Olga Empate")
	f.grant("cns-h-tie-B", eqNow.AddDate(0, -1, -1), "aliado-banco-1", "1000000020", "Pablo Empate")
}

var (
	eqPartners = []string{"aliado-banco-1", "aliado-banco-2", "aliado-coop"}
	eqNames    = []string{"Ana María Pérez", "José Ñuñez", "MARÍA PEÑA", "Luis Gómez", "Sofía Ríos", "Ana Lucía Ortiz", "Martín Ibáñez"}
	eqChannels = []string{"mobile_app", "compliance_portal", ""}
)

// seedGenerated writes n consents with random histories, from a fixed seed.
func (f *eqFixture) seedGenerated(seed int64, n int) {
	f.t.Helper()
	rnd := rand.New(rand.NewSource(seed)) //nolint:gosec // G404: a fixed seed makes the combinations reproducible; nothing secret
	micro := func(d time.Duration) time.Duration {
		return time.Duration(rnd.Int63n(int64(d)/1000)) * time.Microsecond
	}
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("cns-g-%03d", i)
		granted := eqNow.Add(-time.Hour - micro(420*24*time.Hour))
		f.grant(id, granted, eqPartners[rnd.Intn(len(eqPartners))], fmt.Sprintf("%010d", rnd.Int63n(1e10)), eqNames[rnd.Intn(len(eqNames))])
		switch r := rnd.Float64(); {
		case r < 0.05:
			f.insert(rawEvent{consent: id, status: string(domain.StatusRevocationPending), at: granted.Add(micro(30 * 24 * time.Hour))})
			continue
		case r > 0.55:
			continue
		}
		revokedAt := granted.Add(micro(14 * 30 * 24 * time.Hour))
		var entities []string
		for _, p := range eqPartners {
			if rnd.Float64() < 0.6 {
				entities = append(entities, p)
			}
		}
		deadline := f.revoke(id, revokedAt, eqChannels[rnd.Intn(len(eqChannels))], entities...)
		for _, e := range entities {
			switch rnd.Intn(6) {
			case 0: // no answer yet
			case 1:
				f.entity(id, e, domain.HistoryRevocationConfirmed, revokedAt.Add(micro(10*time.Minute)), nil)
			case 2:
				f.entity(id, e, domain.HistoryRevocationNotConfirmed, deadline, nil)
			case 3:
				f.entity(id, e, domain.HistoryRevocationNotConfirmed, deadline, nil)
				f.entity(id, e, domain.HistoryRevocationRetryRequested, deadline.Add(time.Minute), retryDetails(deadline.Add(6*time.Minute)))
				if rnd.Intn(2) == 0 {
					f.entity(id, e, domain.HistoryRevocationConfirmed, deadline.Add(2*time.Minute+micro(8*time.Minute)), nil)
				}
			case 4:
				f.entity(id, e, domain.HistoryRevocationRetryRequested, deadline.Add(micro(time.Hour)), retryDetails(deadline.Add(time.Hour)))
			case 5:
				// An event of the entity before the revocation is ignored.
				f.entity(id, e, domain.HistoryRevocationConfirmed, revokedAt.Add(-time.Second), nil)
			}
		}
		if rnd.Float64() < 0.1 {
			// A second revocation never decides over the first one.
			f.revoke(id, revokedAt.Add(time.Hour+micro(24*time.Hour)), "compliance_portal", eqPartners[0])
		}
	}
}

// eqQueries are the queries compared at each instant.
func (f *eqFixture) eqQueries() map[string]domain.ComplianceQuery {
	statuses := []domain.ConsentStatus{"", domain.StatusActive, domain.StatusRevoked, domain.StatusPartiallyRevoked, domain.StatusExpired}
	out := map[string]domain.ComplianceQuery{}
	for _, s := range statuses {
		for _, page := range []struct{ page, size int }{{1, 6}, {2, 6}, {99, 6}, {1, 50}, {2, 50}} {
			out[fmt.Sprintf("status=%q page=%d size=%d", s, page.page, page.size)] = domain.ComplianceQuery{Status: s, Page: page.page, PageSize: page.size}
		}
	}
	day := func(s string) (time.Time, time.Time) {
		q, err := domain.ComplianceListRequest{GrantedFrom: s, GrantedTo: s}.Validate()
		require.NoError(f.t, err)
		return q.GrantedFrom, q.GrantedBefore
	}
	rangeQ, err := domain.ComplianceListRequest{GrantedFrom: "2026-01-01", GrantedTo: "2026-09-30", PageSize: "6"}.Validate()
	require.NoError(f.t, err)
	out["a range of days"] = rangeQ
	from, before := day("2026-10-01")
	out["one day of Colombia"] = domain.ComplianceQuery{GrantedFrom: from, GrantedBefore: before, Page: 1, PageSize: 50}
	revokedInRange := rangeQ
	revokedInRange.Status = domain.StatusRevoked
	out["revoked in a range, page 2"] = func() domain.ComplianceQuery { q := revokedInRange; q.Page = 2; return q }()
	out["an entity"] = domain.ComplianceQuery{Entity: "aliado-banco-2", Page: 1, PageSize: 6}
	out["an entity and a status"] = domain.ComplianceQuery{Entity: "aliado-coop", Status: domain.StatusPartiallyRevoked, Page: 1, PageSize: 50}
	for _, text := range []string{"maria", "PEÑA", "nunez", "1000000004", "1.000.000.004", "ab12345", "100%", "_", "cns-h-two", "cns-g-007", "nadie"} {
		out["search "+text] = domain.ComplianceQuery{Search: domain.NewComplianceSearch(text), Page: 1, PageSize: 50}
	}
	out["everything at once"] = domain.ComplianceQuery{
		Status: domain.StatusRevoked, GrantedFrom: rangeQ.GrantedFrom, GrantedBefore: rangeQ.GrantedBefore,
		Entity: "aliado-banco-1", Search: domain.NewComplianceSearch("ana"), Page: 1, PageSize: 6,
	}
	return out
}

func TestTheListQueryIsEquivalentToTheStatusRuleOfTheDomain(t *testing.T) {
	f := newEqFixture(t)
	f.seedBorders()
	f.seedGenerated(20261030, 160)

	instants := map[string]time.Time{
		"now":                               eqNow,
		"a microsecond before now":          eqNow.Add(-time.Microsecond),
		"in three minutes":                  eqNow.Add(3 * time.Minute),
		"in a year":                         eqNow.AddDate(1, 0, 0),
		"28 February 2025, 10:00":           time.Date(2025, 2, 28, 10, 0, 0, 0, time.UTC),
		"1 March 2025, a microsecond early": time.Date(2025, 3, 1, 9, 59, 59, 999999000, time.UTC),
		"1 March 2025, 10:00":               time.Date(2025, 3, 1, 10, 0, 0, 0, time.UTC),
		"a year ago":                        eqNow.AddDate(-1, 0, 0),
	}
	queries := f.eqQueries()
	compared := 0
	for name, now := range instants {
		views := f.views(now)
		for label, q := range queries {
			f.compare(views, q, now, name+" / "+label)
			compared++
		}
	}
	t.Logf("compared %d queries at %d instants over %d consents", compared, len(instants), len(f.ids))
}

func TestTheListQueryAtTheHandWrittenBorders(t *testing.T) {
	f := newEqFixture(t)
	f.seedBorders()
	statusAt := func(now time.Time) map[string]domain.ConsentStatus {
		got, err := f.store.ListForCompliance(context.Background(), domain.ComplianceQuery{Page: 1, PageSize: 50}, now)
		require.NoError(t, err)
		out := map[string]domain.ConsentStatus{}
		for _, r := range got.Rows {
			out[r.Consent.ID] = r.Status
		}
		return out
	}

	now := statusAt(eqNow)
	before := statusAt(eqNow.Add(-time.Microsecond))
	assert.Equal(t, domain.StatusExpired, now["cns-h-expires-now"], "expired exactly at the expiry")
	assert.Equal(t, domain.StatusActive, before["cns-h-expires-now"])
	assert.Equal(t, domain.StatusPartiallyRevoked, now["cns-h-deadline-now"], "the window ends exactly at now")
	assert.Equal(t, domain.StatusRevoked, before["cns-h-deadline-now"], "the window is still open")
	assert.Equal(t, domain.StatusRevoked, now["cns-h-confirm-last"])
	assert.Equal(t, domain.StatusRevoked, now["cns-h-confirm-late"])
	assert.Equal(t, domain.StatusRevoked, now["cns-h-retry-ok"])
	assert.Equal(t, domain.StatusPartiallyRevoked, now["cns-h-retry-open"], "a retry keeps it partial until the entity confirms")
	assert.Equal(t, domain.StatusRevoked, now["cns-h-retry-only"], "a retry without not_confirmed opens a new window")
	assert.Equal(t, domain.StatusPartiallyRevoked, now["cns-h-retry-bad"], "an unreadable retry keeps the first window")
	assert.Equal(t, domain.StatusExpired, now["cns-h-late-revoke"], "a revocation after the expiry does not count")
	assert.Equal(t, domain.StatusRevoked, now["cns-h-two"])
	assert.Equal(t, domain.StatusRevoked, now["cns-h-two-revoked"], "the first revoked event decides: ev-B-first, confirmed in time")
	assert.Equal(t, domain.StatusRevocationPending, now["cns-h-pending"])
	assert.Equal(t, domain.StatusRevoked, now["cns-h-empty-entity"])
	assert.Equal(t, domain.StatusRevoked, now["cns-h-odd-details"])
	assert.Equal(t, domain.StatusPartiallyRevoked, now["cns-h-odd-entities"],
		"an entity without a code but with an ended window was never confirmed; the ones without a deadline never end")

	leap := map[string]domain.ConsentStatus{
		"28 February": statusAt(time.Date(2025, 2, 28, 10, 0, 0, 0, time.UTC))["cns-h-leap"],
		"1 March":     statusAt(time.Date(2025, 3, 1, 10, 0, 0, 0, time.UTC))["cns-h-leap"],
	}
	assert.Equal(t, map[string]domain.ConsentStatus{"28 February": domain.StatusActive, "1 March": domain.StatusExpired}, leap,
		"a 29 February expires on 1 March, as in Go")

	got, err := f.store.ListForCompliance(context.Background(), domain.ComplianceQuery{Page: 1, PageSize: 50}, eqNow)
	require.NoError(t, err)
	order := make([]string, 0, 2)
	for _, r := range got.Rows {
		if r.Consent.ID == "cns-h-tie-a" || r.Consent.ID == "cns-h-tie-B" {
			order = append(order, r.Consent.ID)
		}
	}
	assert.Equal(t, []string{"cns-h-tie-B", "cns-h-tie-a"}, order, "the tie is broken by the id compared byte by byte")
	assert.Equal(t, 7, got.Indicators.OutOfDeadline,
		"deadline-now, confirm-late, retry-ok, retry-open, retry-only, retry-bad and odd-entities; not confirm-last nor two-revoked")
}
