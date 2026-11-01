package domain_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
)

// HU-W4.2-01: the query, the filters, the order, the pages and the indicators
// of the list of the compliance portal (the reference definition of the
// PostgreSQL query, decision D-2 b′).

func TestComplianceListRequestValidateDefaults(t *testing.T) {
	q, err := domain.ComplianceListRequest{}.Validate()

	require.NoError(t, err)
	assert.Equal(t, domain.ComplianceQuery{Page: 1, PageSize: domain.DefaultCompliancePageSize}, q)
	assert.True(t, q.Search.Empty())
	assert.Equal(t, int64(0), q.Offset())
}

func TestComplianceListRequestValidateEveryField(t *testing.T) {
	q, err := domain.ComplianceListRequest{
		Status: " revoked ", GrantedFrom: "2026-10-01", GrantedTo: "2026-10-30", Entity: "aliado-banco-1",
		Search: "  Ana María  ", Page: "3", PageSize: "6",
	}.Validate()

	require.NoError(t, err)
	assert.Equal(t, domain.StatusRevoked, q.Status)
	assert.Equal(t, time.Date(2026, 10, 1, 5, 0, 0, 0, time.UTC), q.GrantedFrom, "the first instant of the day in Colombia")
	assert.Equal(t, time.UTC, q.GrantedFrom.Location())
	assert.Equal(t, time.Date(2026, 10, 31, 5, 0, 0, 0, time.UTC), q.GrantedBefore, "the day after the last one, exclusive")
	assert.Equal(t, "aliado-banco-1", q.Entity)
	assert.Equal(t, domain.ComplianceSearch{Text: "Ana María", Document: "ANAMARA", Name: "ana maria"}, q.Search)
	assert.Equal(t, 3, q.Page)
	assert.Equal(t, 6, q.PageSize)
	assert.Equal(t, int64(12), q.Offset())
}

func TestComplianceListRequestValidateNamesEveryInvalidField(t *testing.T) {
	_, err := domain.ComplianceListRequest{
		Status: "pending", GrantedFrom: "30/10/2026", GrantedTo: "2026-02-30", Entity: "aliado banco",
		Search: strings.Repeat("á", domain.MaxComplianceSearchLength+1), Page: "0", PageSize: "51",
	}.Validate()

	var verr *domain.ValidationError
	require.ErrorAs(t, err, &verr)
	assert.Equal(t, []domain.FieldError{
		{Field: "status", Code: domain.CodeInvalidFormat},
		{Field: "granted_from", Code: domain.CodeInvalidFormat},
		{Field: "granted_to", Code: domain.CodeInvalidFormat},
		{Field: "entity", Code: domain.CodeInvalidFormat},
		{Field: "q", Code: domain.CodeOutOfRange},
		{Field: "page", Code: domain.CodeOutOfRange},
		{Field: "page_size", Code: domain.CodeOutOfRange},
	}, verr.Fields)
}

func TestComplianceListRequestValidateBounds(t *testing.T) {
	cases := []struct {
		name string
		req  domain.ComplianceListRequest
		want []domain.FieldError
	}{
		{name: "every status of the filter", req: domain.ComplianceListRequest{Status: "partially_revoked"}},
		{name: "active", req: domain.ComplianceListRequest{Status: "active"}},
		{name: "expired", req: domain.ComplianceListRequest{Status: "expired"}},
		{name: "a reserved status is not a filter", req: domain.ComplianceListRequest{Status: "revocation_pending"},
			want: []domain.FieldError{{Field: "status", Code: domain.CodeInvalidFormat}}},
		{name: "the same day in both bounds", req: domain.ComplianceListRequest{GrantedFrom: "2026-10-30", GrantedTo: "2026-10-30"}},
		{name: "granted_to before granted_from", req: domain.ComplianceListRequest{GrantedFrom: "2026-10-30", GrantedTo: "2026-10-29"},
			want: []domain.FieldError{{Field: "granted_to", Code: domain.CodeOutOfRange}}},
		{name: "only granted_to", req: domain.ComplianceListRequest{GrantedTo: "2026-10-29"}},
		{name: "a time is not a day", req: domain.ComplianceListRequest{GrantedFrom: "2026-10-30T00:00:00Z"},
			want: []domain.FieldError{{Field: "granted_from", Code: domain.CodeInvalidFormat}}},
		{name: "page 1", req: domain.ComplianceListRequest{Page: "1"}},
		{name: "page that is not a number", req: domain.ComplianceListRequest{Page: "two"},
			want: []domain.FieldError{{Field: "page", Code: domain.CodeInvalidFormat}}},
		{name: "negative page", req: domain.ComplianceListRequest{Page: "-1"},
			want: []domain.FieldError{{Field: "page", Code: domain.CodeOutOfRange}}},
		{name: "the largest page", req: domain.ComplianceListRequest{Page: "2147483647"}},
		{name: "a page over 32 bits", req: domain.ComplianceListRequest{Page: "2147483648"},
			want: []domain.FieldError{{Field: "page", Code: domain.CodeOutOfRange}}},
		{name: "page size 1", req: domain.ComplianceListRequest{PageSize: "1"}},
		{name: "page size 50", req: domain.ComplianceListRequest{PageSize: "50"}},
		{name: "page size 0", req: domain.ComplianceListRequest{PageSize: "0"},
			want: []domain.FieldError{{Field: "page_size", Code: domain.CodeOutOfRange}}},
		{name: "page size with decimals", req: domain.ComplianceListRequest{PageSize: "6.5"},
			want: []domain.FieldError{{Field: "page_size", Code: domain.CodeInvalidFormat}}},
		{name: "a search of 100 characters", req: domain.ComplianceListRequest{Search: strings.Repeat("ñ", domain.MaxComplianceSearchLength)}},
		{name: "a search of only spaces is no search", req: domain.ComplianceListRequest{Search: "   "}},
		{name: "an entity of 129 characters", req: domain.ComplianceListRequest{Entity: strings.Repeat("a", 129)},
			want: []domain.FieldError{{Field: "entity", Code: domain.CodeInvalidFormat}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.req.Validate()
			if tc.want == nil {
				assert.NoError(t, err)
				return
			}
			var verr *domain.ValidationError
			require.ErrorAs(t, err, &verr)
			assert.Equal(t, tc.want, verr.Fields)
		})
	}
}

func TestFoldName(t *testing.T) {
	assert.Equal(t, "ana maria pena", domain.FoldName("ANA MARÍA PEÑA"))
	assert.Equal(t, "aeiouunaeiouun", domain.FoldName(domain.NameFoldingFrom))
	assert.Equal(t, "çà o'brien", domain.FoldName("çà O'Brien"), "only ASCII capitals and the Spanish letters change")
	assert.Equal(t, len([]rune(domain.NameFoldingFrom)), len([]rune(domain.NameFoldingTo)))
}

func TestComplianceSearchMatches(t *testing.T) {
	c := holderConsent("cns-7f3a", viewGrantedAt)
	c.Holder = domain.Holder{DocumentID: "1020304050", FullName: "Ana María Peña"}
	cases := []struct {
		text string
		want bool
	}{
		{"", true},
		{"cns-7f3a", true},
		{"CNS-7F3A", false},
		{"cns-7f", false},
		{"1020304050", true},
		{"1.020.304.050", true},
		{" 10 2030 4050 ", true},
		{"102030", false},
		{"maria", true},
		{"MARÍA PEÑA", true},
		{"pena", true},
		{"ana  maría", true},
		{"pedro", false},
	}
	for _, tc := range cases {
		t.Run(tc.text, func(t *testing.T) {
			assert.Equal(t, tc.want, domain.NewComplianceSearch(tc.text).Matches(c))
		})
	}
}

func TestComplianceQueryMatchesTheDaysOfColombia(t *testing.T) {
	q, err := domain.ComplianceListRequest{GrantedFrom: "2026-10-30", GrantedTo: "2026-10-30"}.Validate()
	require.NoError(t, err)
	at := func(ts time.Time) domain.ConsentView {
		return domain.View(holderConsent("cns-1", ts), nil, ts, nil)
	}

	assert.False(t, q.Matches(at(time.Date(2026, 10, 30, 4, 59, 59, 999999000, time.UTC))), "still the 29th in Colombia")
	assert.True(t, q.Matches(at(time.Date(2026, 10, 30, 5, 0, 0, 0, time.UTC))), "midnight in Colombia")
	assert.True(t, q.Matches(at(time.Date(2026, 10, 31, 4, 59, 59, 999999000, time.UTC))), "the last microsecond of the 30th")
	assert.False(t, q.Matches(at(time.Date(2026, 10, 31, 5, 0, 0, 0, time.UTC))), "the 31st in Colombia")
}

func TestComplianceQueryMatchesTheStatusEntityAndSearch(t *testing.T) {
	c := holderConsent("cns-1", viewGrantedAt)
	now := viewGrantedAt.AddDate(0, 2, 0)
	active := domain.View(c, nil, now, nil)
	revoked := domain.View(c, []domain.ConsentEvent{event("e1", "cns-1", domain.HistoryRevoked, now.Add(-time.Hour))}, now, nil)

	assert.True(t, domain.ComplianceQuery{Status: domain.StatusActive}.Matches(active))
	assert.False(t, domain.ComplianceQuery{Status: domain.StatusActive}.Matches(revoked))
	assert.True(t, domain.ComplianceQuery{Status: domain.StatusRevoked}.Matches(revoked))
	assert.True(t, domain.ComplianceQuery{Entity: "aliado-banco-1"}.Matches(active))
	assert.False(t, domain.ComplianceQuery{Entity: "aliado-banco-2"}.Matches(active))
	assert.True(t, domain.ComplianceQuery{Search: domain.NewComplianceSearch("perez")}.Matches(active))
	assert.False(t, domain.ComplianceQuery{Search: domain.NewComplianceSearch("gomez")}.Matches(active))
}

// partialAt builds a revocation of the consent at revokedAt with one entity
// whose window ends five minutes later, plus the given entity events.
func partialAt(c domain.Consent, revokedAt time.Time, channel string, entityEvents ...domain.ConsentEvent) []domain.ConsentEvent {
	details, _ := json.Marshal(domain.RevokedDetails{Entities: []domain.EntityWindow{
		{Code: "aliado-banco-1", DeadlineAt: revokedAt.Add(domain.RevocationNoticeDeadline)},
	}})
	revoked := event("rev-"+c.ID, c.ID, domain.HistoryRevoked, revokedAt)
	revoked.Details, revoked.Channel = details, channel
	return append([]domain.ConsentEvent{revoked}, entityEvents...)
}

func noticeEvent(id, consent, status string, at time.Time, details string) domain.ConsentEvent {
	e := event(id, consent, status, at)
	e.EntityCode = "aliado-banco-1"
	if details != "" {
		e.Details = []byte(details)
	}
	return e
}

func TestTheStatusOfThePartialRevocationWithAndWithoutRetry(t *testing.T) {
	c := holderConsent("cns-1", viewGrantedAt)
	revokedAt := viewGrantedAt.AddDate(0, 1, 0)
	deadline := revokedAt.Add(domain.RevocationNoticeDeadline)
	retryDeadline := deadline.Add(10 * time.Minute)
	retry := `{"deadline_at":"` + retryDeadline.Format(time.RFC3339Nano) + `"}`

	cases := []struct {
		name   string
		events []domain.ConsentEvent
		now    time.Time
		want   domain.ConsentStatus
	}{
		{name: "the window is open", events: partialAt(c, revokedAt, "mobile_app"), now: deadline.Add(-time.Microsecond),
			want: domain.StatusRevoked},
		{name: "the window ended without a confirmation", events: partialAt(c, revokedAt, "mobile_app"), now: deadline,
			want: domain.StatusPartiallyRevoked},
		{name: "not confirmed", now: revokedAt.Add(time.Minute), want: domain.StatusPartiallyRevoked,
			events: partialAt(c, revokedAt, "mobile_app", noticeEvent("n1", "cns-1", domain.HistoryRevocationNotConfirmed, revokedAt.Add(time.Minute), ""))},
		{name: "not confirmed and retried: still partial", now: deadline.Add(time.Minute), want: domain.StatusPartiallyRevoked,
			events: partialAt(c, revokedAt, "mobile_app",
				noticeEvent("n1", "cns-1", domain.HistoryRevocationNotConfirmed, deadline, ""),
				noticeEvent("n2", "cns-1", domain.HistoryRevocationRetryRequested, deadline.Add(time.Second), retry))},
		{name: "not confirmed, retried and confirmed", now: deadline.Add(2 * time.Minute), want: domain.StatusRevoked,
			events: partialAt(c, revokedAt, "mobile_app",
				noticeEvent("n1", "cns-1", domain.HistoryRevocationNotConfirmed, deadline, ""),
				noticeEvent("n2", "cns-1", domain.HistoryRevocationRetryRequested, deadline.Add(time.Second), retry),
				noticeEvent("n3", "cns-1", domain.HistoryRevocationConfirmed, deadline.Add(time.Minute), ""))},
		{name: "a retry without a valid deadline keeps the first one", now: deadline, want: domain.StatusPartiallyRevoked,
			events: partialAt(c, revokedAt, "mobile_app",
				noticeEvent("n2", "cns-1", domain.HistoryRevocationRetryRequested, revokedAt.Add(time.Second), `{"deadline_at":null}`))},
		{name: "a revocation after the expiry does not count", now: c.ExpiresAt().Add(time.Hour), want: domain.StatusExpired,
			events: partialAt(c, c.ExpiresAt().Add(time.Minute), "mobile_app")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, _, _ := domain.DeriveStatus(c, tc.events, tc.now)
			assert.Equal(t, tc.want, status)
			assert.True(t, domain.ComplianceQuery{Status: tc.want}.Matches(domain.View(c, tc.events, tc.now, nil)))
		})
	}
}

func TestSortAndPaginateForCompliance(t *testing.T) {
	now := viewGrantedAt.AddDate(0, 2, 0)
	var views []domain.ConsentView
	for i, id := range []string{"cns-b", "cns-a", "cns-c", "cns-d", "cns-e", "cns-f", "cns-g"} {
		at := viewGrantedAt.Add(time.Duration(i) * time.Hour)
		if id == "cns-a" {
			at = viewGrantedAt // ties with cns-b
		}
		views = append(views, domain.View(holderConsent(id, at), nil, now, nil))
	}
	q := domain.ComplianceQuery{Page: 1, PageSize: 3}
	matching := domain.FilterForCompliance(views, q)

	ids := func(vs []domain.ConsentView) []string {
		out := make([]string, 0, len(vs))
		for _, v := range vs {
			out = append(out, v.Consent.ID)
		}
		return out
	}
	assert.Equal(t, []string{"cns-g", "cns-f", "cns-e", "cns-d", "cns-c", "cns-a", "cns-b"}, ids(matching),
		"most recently granted first; the id breaks the tie")
	assert.Equal(t, []string{"cns-g", "cns-f", "cns-e"}, ids(domain.Paginate(matching, q)))
	q.Page = 3
	assert.Equal(t, []string{"cns-b"}, ids(domain.Paginate(matching, q)), "the last page is short")
	q.Page = 4
	assert.Empty(t, domain.Paginate(matching, q), "a page after the last one is empty")
	assert.NotNil(t, domain.Paginate(matching, q))

	assert.Equal(t, 1, domain.TotalPages(0, 6), "an empty list is page 1 of 1")
	assert.Equal(t, 2, domain.TotalPages(12, 6))
	assert.Equal(t, 3, domain.TotalPages(13, 6))
	assert.Equal(t, 1, domain.TotalPages(1, 50))
}

func TestComplianceIndicators(t *testing.T) {
	now := time.Date(2026, 10, 30, 15, 0, 0, 0, time.UTC)
	granted := now.AddDate(0, -3, 0)
	view := func(id string, at time.Time, events func(domain.Consent) []domain.ConsentEvent) domain.ConsentView {
		c := holderConsent(id, at)
		var es []domain.ConsentEvent
		if events != nil {
			es = events(c)
		}
		return domain.View(c, es, now, nil)
	}
	revokedBy := func(at time.Time, channel string, entity ...func(domain.Consent, time.Time) domain.ConsentEvent) func(domain.Consent) []domain.ConsentEvent {
		return func(c domain.Consent) []domain.ConsentEvent {
			var extra []domain.ConsentEvent
			for _, e := range entity {
				extra = append(extra, e(c, at))
			}
			return partialAt(c, at, channel, extra...)
		}
	}
	confirmedAfter := func(d time.Duration) func(domain.Consent, time.Time) domain.ConsentEvent {
		return func(c domain.Consent, at time.Time) domain.ConsentEvent {
			return noticeEvent("conf-"+c.ID, c.ID, domain.HistoryRevocationConfirmed, at.Add(d), "")
		}
	}
	notConfirmedAfter := func(d time.Duration) func(domain.Consent, time.Time) domain.ConsentEvent {
		return func(c domain.Consent, at time.Time) domain.ConsentEvent {
			return noticeEvent("nc-"+c.ID, c.ID, domain.HistoryRevocationNotConfirmed, at.Add(d), "")
		}
	}
	deadline := domain.RevocationNoticeDeadline

	views := []domain.ConsentView{
		view("cns-active-1", granted, nil),
		view("cns-active-2", granted.Add(time.Hour), nil),
		view("cns-expired", now.AddDate(-1, 0, -1), nil),
		// Revoked within the window, by channel; confirmed in time.
		view("cns-app", granted, revokedBy(now.Add(-time.Hour), "mobile_app", confirmedAfter(time.Minute))),
		view("cns-portal", granted, revokedBy(now.Add(-2*time.Hour), "compliance_portal", confirmedAfter(deadline-time.Microsecond))),
		// The border of the 30 days: exactly 30 days ago is out, a microsecond later is in.
		view("cns-30-out", granted, revokedBy(now.Add(-domain.RevokedIndicatorWindow), "mobile_app", confirmedAfter(time.Minute))),
		view("cns-30-in", granted, revokedBy(now.Add(-domain.RevokedIndicatorWindow+time.Microsecond), "mobile_app", confirmedAfter(time.Minute))),
		// Out of the deadline: confirmed exactly at the end of the window.
		view("cns-late", granted, revokedBy(now.AddDate(0, -2, 0), "mobile_app", confirmedAfter(deadline))),
		// Out of the deadline: not confirmed, then retried and confirmed.
		view("cns-retried", granted, func(c domain.Consent) []domain.ConsentEvent {
			at := now.AddDate(0, -1, -5)
			retry := `{"deadline_at":"` + at.Add(2*deadline).Format(time.RFC3339Nano) + `"}`
			return partialAt(c, at, "compliance_portal",
				noticeEvent("nc", c.ID, domain.HistoryRevocationNotConfirmed, at.Add(deadline), ""),
				noticeEvent("rt", c.ID, domain.HistoryRevocationRetryRequested, at.Add(deadline+time.Second), retry),
				noticeEvent("cf", c.ID, domain.HistoryRevocationConfirmed, at.Add(deadline+time.Minute), ""))
		}),
		// In progress: the window is open; not out of the deadline yet.
		view("cns-pending", granted, revokedBy(now.Add(-time.Minute), "mobile_app")),
		// Partial now: the window ended without a confirmation; a channel without its own counter.
		view("cns-partial", granted, revokedBy(now.Add(-time.Hour), "assisted", notConfirmedAfter(deadline))),
	}

	got := domain.Indicators(views, now)

	assert.Equal(t, domain.ComplianceIndicators{
		Active:            2,
		Expired:           1,
		RevokedLast30Days: domain.RevokedCount{Total: 5, MobileApp: 3, CompliancePortal: 1},
		OutOfDeadline:     3,
		AsOf:              now,
	}, got)
	byID := map[string]domain.ConsentView{}
	for _, v := range views {
		byID[v.Consent.ID] = v
	}
	assert.False(t, byID["cns-portal"].Revocation.OutOfDeadline, "confirmed in the last microsecond of the window")
	assert.True(t, byID["cns-late"].Revocation.OutOfDeadline, "confirmed at the end of the window")
	assert.True(t, byID["cns-retried"].Revocation.OutOfDeadline, "a successful retry does not undo it")
	assert.Equal(t, domain.StatusRevoked, byID["cns-retried"].Status)
	assert.False(t, byID["cns-pending"].Revocation.OutOfDeadline)
	assert.True(t, byID["cns-partial"].Revocation.OutOfDeadline)
	assert.Equal(t, domain.StatusPartiallyRevoked, byID["cns-partial"].Status)
}

func TestTheOutOfDeadlineOfARevocationWithoutEntitiesOrDeadline(t *testing.T) {
	c := holderConsent("cns-1", viewGrantedAt)
	at := viewGrantedAt.AddDate(0, 1, 0)
	noEntities := event("e1", "cns-1", domain.HistoryRevoked, at)
	noDeadline := event("e1", "cns-1", domain.HistoryRevoked, at)
	noDeadline.Details = []byte(`{"entities":[{"code":"aliado-banco-1"}]}`)
	unreadable := event("e1", "cns-1", domain.HistoryRevoked, at)
	unreadable.Details = []byte(`not json`)

	for name, e := range map[string]domain.ConsentEvent{"no entities": noEntities, "no deadline": noDeadline, "unreadable": unreadable} {
		t.Run(name, func(t *testing.T) {
			v := domain.View(c, []domain.ConsentEvent{e}, at.AddDate(0, 1, 0), nil)
			require.NotNil(t, v.Revocation)
			assert.False(t, v.Revocation.OutOfDeadline)
		})
	}
}

func TestTheExpiryIsCountedOnTheUTCCalendar(t *testing.T) {
	bogota := time.FixedZone("America/Bogota", -5*60*60)
	// 1 March 2024 at 02:00 UTC is still 29 February in Colombia.
	c := holderConsent("cns-1", time.Date(2024, 2, 29, 21, 0, 0, 0, bogota))
	assert.Equal(t, time.Date(2025, 3, 1, 2, 0, 0, 0, time.UTC), c.ExpiresAt())

	leap := holderConsent("cns-2", time.Date(2024, 2, 29, 10, 0, 0, 0, time.UTC))
	assert.Equal(t, time.Date(2025, 3, 1, 10, 0, 0, 0, time.UTC), leap.ExpiresAt(), "a 29 February expires on 1 March")
}

func TestEntityCatalog(t *testing.T) {
	names := domain.EntityNames{"aliado-banco-1": "Banco Aliado", "aliado-coop": "Cooperativa Andina"}

	got := domain.EntityCatalog(names, []string{"aliado-banco-1", "aliado-zeta", "", "aliado-zeta"})

	assert.Equal(t, []domain.Entity{
		{Code: "aliado-banco-1", Name: "Banco Aliado"},
		{Code: "aliado-coop", Name: "Cooperativa Andina"},
		{Code: "aliado-zeta", Name: "aliado-zeta"},
	}, got, "the names and the codes with a consent, once, by name")
	assert.Empty(t, domain.EntityCatalog(nil, nil))
}
