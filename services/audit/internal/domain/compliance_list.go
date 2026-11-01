package domain

import (
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
)

// HU-W4.2-01: the list of consents of the compliance portal, with its
// filters, its pages and its indicators.
//
// The database filters, counts and pages the list with the status rule
// written in SQL (decision D-2 b′, internal/adapters/postgres). The functions
// of this file are the reference definition in Go of that query: Matches,
// FilterForCompliance, Paginate and Indicators over the views derived with
// View. The equivalence test of the adapter compares both on the same data;
// the service only uses them to build what it shows.

// Limits of the list of the compliance portal.
const (
	// DefaultCompliancePageSize is the page size when the portal does not
	// ask for one.
	DefaultCompliancePageSize = 20
	// MaxCompliancePageSize bounds a page.
	MaxCompliancePageSize = 50
	// MaxComplianceSearchLength bounds the search, in characters.
	MaxComplianceSearchLength = 100
	// RevokedIndicatorWindow is the window of the indicator of recent
	// revocations: the last 30 days.
	RevokedIndicatorWindow = 30 * 24 * time.Hour
)

// civilDateLayout is the shape of a date of the filters: a civil day of
// Colombia (AAAA-MM-DD).
const civilDateLayout = "2006-01-02"

// ComplianceListRequest is the query of the list as the portal sends it: every
// field is the raw text of its parameter, empty when absent.
type ComplianceListRequest struct {
	Status      string
	GrantedFrom string
	GrantedTo   string
	Entity      string
	Search      string
	Page        string
	PageSize    string
}

// ComplianceSearch is the search of the list, in the three forms it is
// compared in.
type ComplianceSearch struct {
	// Text is the search as typed, trimmed: it matches a consent id exactly.
	Text string
	// Document is the search with only its ASCII letters and digits, in upper
	// case: it matches a holder document exactly, never a prefix, so the
	// documents cannot be enumerated.
	Document string
	// Name is the search folded with FoldName: it matches the holder names
	// that contain it.
	Name string
}

// Empty tells whether there is no search.
func (s ComplianceSearch) Empty() bool { return s.Text == "" }

// Matches tells whether the consent is found by the search: by its id, by the
// document of its holder or by a part of the name of its holder.
func (s ComplianceSearch) Matches(c Consent) bool {
	switch {
	case s.Empty():
		return true
	case c.ID == s.Text:
		return true
	case s.Document != "" && NormalizeDocument(c.Holder.DocumentID) == s.Document:
		return true
	}
	return s.Name != "" && strings.Contains(FoldName(c.Holder.FullName), s.Name)
}

// ComplianceQuery is a valid query of the list.
type ComplianceQuery struct {
	// Status is the derived status asked for; empty for every status.
	Status ConsentStatus
	// GrantedFrom is the first instant of the first civil day asked for
	// (inclusive); zero when there is no lower bound.
	GrantedFrom time.Time
	// GrantedBefore is the first instant after the last civil day asked for
	// (exclusive); zero when there is no upper bound.
	GrantedBefore time.Time
	// Entity is the code of the entity (the partner) asked for; empty for
	// every entity.
	Entity string
	Search ComplianceSearch
	// Page starts at 1.
	Page     int
	PageSize int
}

// Offset is the number of matching consents before the page.
func (q ComplianceQuery) Offset() int64 { return int64(q.Page-1) * int64(q.PageSize) }

// Matches tells whether the consent of the view, with the status derived at
// the instant of the query, meets every filter of the query.
func (q ComplianceQuery) Matches(v ConsentView) bool {
	c := v.Consent
	switch {
	case q.Status != "" && v.Status != q.Status:
		return false
	case !q.GrantedFrom.IsZero() && c.GrantedAt.Before(q.GrantedFrom):
		return false
	case !q.GrantedBefore.IsZero() && !c.GrantedAt.Before(q.GrantedBefore):
		return false
	case q.Entity != "" && c.PartnerID != q.Entity:
		return false
	}
	return q.Search.Matches(c)
}

// Validate checks the request and returns the query it asks for. It returns a
// *ValidationError naming every invalid field at once: status (one of the
// four statuses of the filter), granted_from and granted_to (civil days of
// Colombia, AAAA-MM-DD; granted_to before granted_from is out of range),
// entity (a partner code), q (at most MaxComplianceSearchLength
// characters), page (1 or more) and page_size (1 to MaxCompliancePageSize).
func (r ComplianceListRequest) Validate() (ComplianceQuery, error) {
	var fields []FieldError
	bad := func(field, code string) { fields = append(fields, FieldError{Field: field, Code: code}) }
	q := ComplianceQuery{Page: 1, PageSize: DefaultCompliancePageSize}

	switch s := ConsentStatus(strings.TrimSpace(r.Status)); s {
	case "":
	case StatusActive, StatusRevoked, StatusPartiallyRevoked, StatusExpired:
		q.Status = s
	default:
		bad("status", CodeInvalidFormat)
	}

	from, fromOK := parseCivilDate(r.GrantedFrom)
	if !fromOK {
		bad("granted_from", CodeInvalidFormat)
	}
	to, toOK := parseCivilDate(r.GrantedTo)
	switch {
	case !toOK:
		bad("granted_to", CodeInvalidFormat)
	case fromOK && !from.IsZero() && !to.IsZero() && to.Before(from):
		bad("granted_to", CodeOutOfRange)
	}
	if fromOK && !from.IsZero() {
		q.GrantedFrom = from.UTC()
	}
	if toOK && !to.IsZero() {
		// The day ends where the next one starts: Colombia has no daylight
		// saving time, so it is always 24 hours later.
		q.GrantedBefore = to.AddDate(0, 0, 1).UTC()
	}

	switch entity := strings.TrimSpace(r.Entity); {
	case entity == "":
	case len(entity) > MaxIdentifierLength || !identifierPattern.MatchString(entity):
		bad("entity", CodeInvalidFormat)
	default:
		q.Entity = entity
	}

	switch text := strings.TrimSpace(r.Search); {
	case len([]rune(text)) > MaxComplianceSearchLength:
		bad("q", CodeOutOfRange)
	default:
		q.Search = NewComplianceSearch(text)
	}

	if page, code := parseBoundedInt(r.Page, 1, 1, maxCompliancePage); code != "" {
		bad("page", code)
	} else {
		q.Page = page
	}
	if size, code := parseBoundedInt(r.PageSize, DefaultCompliancePageSize, 1, MaxCompliancePageSize); code != "" {
		bad("page_size", code)
	} else {
		q.PageSize = size
	}

	if len(fields) > 0 {
		return ComplianceQuery{}, &ValidationError{Fields: fields}
	}
	return q, nil
}

// maxCompliancePage bounds the page number (a 32-bit integer), so the offset
// never overflows.
const maxCompliancePage = 1<<31 - 1

// NewComplianceSearch builds the search of the text, trimmed: empty for no
// search.
func NewComplianceSearch(text string) ComplianceSearch {
	text = strings.TrimSpace(text)
	if text == "" {
		return ComplianceSearch{}
	}
	var doc strings.Builder
	for _, r := range text {
		switch {
		case r >= '0' && r <= '9', r >= 'A' && r <= 'Z':
			doc.WriteRune(r)
		case r >= 'a' && r <= 'z':
			doc.WriteRune(r - 'a' + 'A')
		}
	}
	return ComplianceSearch{Text: text, Document: doc.String(), Name: FoldName(normalizeName(text))}
}

// parseCivilDate reads a civil day of Colombia (AAAA-MM-DD) as its first
// instant. Empty is the zero time and ok; anything else that is not a valid
// day is not ok.
func parseCivilDate(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, true
	}
	day, err := time.ParseInLocation(civilDateLayout, raw, receiptZone)
	if err != nil {
		return time.Time{}, false
	}
	return day, true
}

// parseBoundedInt reads a decimal integer between min and max; empty is def.
// The code says why it is invalid: not a number, or out of the bounds.
func parseBoundedInt(raw string, def, minimum, maximum int) (int, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return def, ""
	}
	n, err := strconv.ParseInt(raw, 10, 32)
	var numErr *strconv.NumError
	switch {
	case errors.As(err, &numErr) && errors.Is(numErr.Err, strconv.ErrRange):
		return 0, CodeOutOfRange
	case err != nil:
		return 0, CodeInvalidFormat
	case n < int64(minimum) || n > int64(maximum):
		return 0, CodeOutOfRange
	}
	return int(n), ""
}

// nameFolding maps the letters that FoldName changes: the ASCII capitals to
// small letters, and the Spanish accented vowels and the eñe to their plain
// small letter. Every other character stays as it is. The PostgreSQL adapter
// writes the same mapping with translate(), so the search and its trigram
// index do not depend on the locale of the database.
var nameFolding = func() map[rune]rune {
	m := map[rune]rune{}
	for r := 'A'; r <= 'Z'; r++ {
		m[r] = r - 'A' + 'a'
	}
	from, to := []rune(NameFoldingFrom), []rune(NameFoldingTo)
	for i := range from {
		m[from[i]] = to[i]
	}
	return m
}()

// NameFoldingFrom and NameFoldingTo are the accented letters FoldName
// changes, one by one, besides the ASCII capitals.
const (
	NameFoldingFrom = "ÁÉÍÓÚÜÑáéíóúüñ"
	NameFoldingTo   = "aeiouunaeiouun"
)

// FoldName returns the name to compare it without capitals or accents:
// "Ana María Peña" is "ana maria pena".
func FoldName(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if f, ok := nameFolding[r]; ok {
			r = f
		}
		b.WriteRune(r)
	}
	return b.String()
}

// SortForCompliance orders the views as the list shows them: most recently
// granted first; the id, compared byte by byte, breaks a tie.
func SortForCompliance(views []ConsentView) {
	sort.SliceStable(views, func(i, j int) bool {
		a, b := views[i].Consent, views[j].Consent
		if !a.GrantedAt.Equal(b.GrantedAt) {
			return a.GrantedAt.After(b.GrantedAt)
		}
		return a.ID < b.ID
	})
}

// FilterForCompliance returns the views that meet the query, in the order of
// the list.
func FilterForCompliance(views []ConsentView, q ComplianceQuery) []ConsentView {
	out := make([]ConsentView, 0, len(views))
	for _, v := range views {
		if q.Matches(v) {
			out = append(out, v)
		}
	}
	SortForCompliance(out)
	return out
}

// Paginate returns the page of the query out of the matching views, already
// in the order of the list. A page after the last one is empty.
func Paginate(matching []ConsentView, q ComplianceQuery) []ConsentView {
	start := q.Offset()
	if start >= int64(len(matching)) {
		return []ConsentView{}
	}
	end := start + int64(q.PageSize)
	if end > int64(len(matching)) {
		end = int64(len(matching))
	}
	return matching[start:end]
}

// TotalPages is the number of pages of total consents: at least 1, so an
// empty list is "page 1 of 1".
func TotalPages(total, pageSize int) int {
	if total <= 0 || pageSize <= 0 {
		return 1
	}
	return (total + pageSize - 1) / pageSize
}

// RevokedCount counts the recent revocations by channel.
type RevokedCount struct {
	// Total counts every channel.
	Total            int
	MobileApp        int
	CompliancePortal int
}

// ComplianceIndicators are the four cards of the list (decision D-5 a). They
// count every consent, whatever the filter of the list.
type ComplianceIndicators struct {
	// Active counts the consents whose derived status is active.
	Active int
	// Expired counts the consents whose derived status is expired.
	Expired int
	// RevokedLast30Days counts the revoked and partially revoked consents
	// whose revocation took effect in the last 30 days.
	RevokedLast30Days RevokedCount
	// OutOfDeadline counts the revocations in which some entity did not
	// confirm within its first window (RN-02), even if a retry later
	// succeeded (RevocationView.OutOfDeadline).
	OutOfDeadline int
	// AsOf is the instant the indicators were computed at.
	AsOf time.Time
}

// Indicators computes the indicators over the views of every consent,
// derived at now.
func Indicators(views []ConsentView, now time.Time) ComplianceIndicators {
	out := ComplianceIndicators{AsOf: now}
	since := now.Add(-RevokedIndicatorWindow)
	for _, v := range views {
		switch v.Status {
		case StatusActive:
			out.Active++
		case StatusExpired:
			out.Expired++
		}
		r := v.Revocation
		if r == nil {
			continue
		}
		if r.OutOfDeadline {
			out.OutOfDeadline++
		}
		if r.EffectiveAt.After(since) && !r.EffectiveAt.After(now) {
			out.RevokedLast30Days.Total++
			switch RevocationChannel(r.Channel) {
			case RevocationChannelMobileApp:
				out.RevokedLast30Days.MobileApp++
			case RevocationChannelCompliancePortal:
				out.RevokedLast30Days.CompliancePortal++
			}
		}
	}
	return out
}

// ComplianceRow is one consent of a page of the list as the database computed
// it: the record and the status derived by the query.
type ComplianceRow struct {
	Consent Consent
	Status  ConsentStatus
}

// ComplianceListResult is what the database answers for a query of the list:
// the page, the number of consents that meet the filters, the indicators
// (without AsOf) and the codes of the entities that have a consent.
type ComplianceListResult struct {
	Rows        []ComplianceRow
	TotalItems  int
	Indicators  ComplianceIndicators
	EntityCodes []string
}

// CompliancePage is a page of the list as the portal shows it.
type CompliancePage struct {
	Items      []ConsentView
	Page       int
	PageSize   int
	TotalItems int
	TotalPages int
	Indicators ComplianceIndicators
	// Entities are the options of the filter by entity: every entity with a
	// display name and every one that has a consent, by name.
	Entities []Entity
}

// EntityCatalog returns the entities of the filter: those of the catalog of
// names and the codes given, without repeats, ordered by name and then code.
func EntityCatalog(names EntityNames, codes []string) []Entity {
	seen := map[string]bool{}
	out := make([]Entity, 0, len(names)+len(codes))
	add := func(code string) {
		if code == "" || seen[code] {
			return
		}
		seen[code] = true
		out = append(out, names.Entity(code))
	}
	for code := range names {
		add(code)
	}
	for _, code := range codes {
		add(code)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Code < out[j].Code
	})
	return out
}
