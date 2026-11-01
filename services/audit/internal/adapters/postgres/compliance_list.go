package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/ports"
)

// ComplianceListStore implements ports.ComplianceListStore (HU-W4.2-01): the
// list of consents of the compliance portal, filtered, counted and paged in
// PostgreSQL with the status rule of domain.DeriveStatus written in SQL
// (decision D-2 b′). It only reads; the indexes are those of migration 0006.
//
// The rule, as in domain.DeriveStatus and deriveNotices:
//
//  1. Revoked when an event of the whole consent (no entity) says revoked
//     before the expiry; the first one in the order (occurred_at,
//     recorded_at, event_id) decides. It is partially revoked when some
//     entity of the details of that event has not confirmed and has a
//     revocation_not_confirmed, or the end of its current window (the last
//     retry with a deadline, or the original one) is at or before now.
//  2. Otherwise expired when now is at or after the expiry.
//  3. Otherwise revocation_pending when it is the status of the last event of
//     the whole consent (a reserved status no code writes today).
//  4. Otherwise active.
//
// The expiry is granted_at plus domain.ConsentValidityMonths on the UTC
// calendar, as Consent.ExpiresAt: PostgreSQL turns 29 February plus a year
// into 28 February and Go into 1 March, so the 29 February adds a day. The
// equivalence test (compliance_equivalence_test.go) compares this query with
// the reference in Go on the same data; the service also logs any row whose
// status differs from the one it derives.
//
// The times compared with now have the microsecond precision of PostgreSQL:
// the end of a window written with nanoseconds in the details (the retry
// does it) can differ from Go during less than a microsecond.
type ComplianceListStore struct {
	db *sql.DB
}

// NewComplianceListStore builds the store over an opened and migrated
// database.
func NewComplianceListStore(db *sql.DB) *ComplianceListStore { return &ComplianceListStore{db: db} }

var _ ports.ComplianceListStore = (*ComplianceListStore)(nil)

// Bounds of the expiry that let the filters by status use the index on
// granted_at: an expired consent was granted at least 365 days before now and
// an active one less than 366 days before now (a year of the calendar lasts
// 365 or 366 days).
const (
	shortestValidity = 365 * 24 * time.Hour
	longestValidity  = 366 * 24 * time.Hour
)

// foldSQL is domain.FoldName in SQL over the column: the ASCII capitals and
// the Spanish accented letters, one by one, with translate(). The trigram
// index of migration 0006 is on the same expression for holder_full_name.
func foldSQL(column string) string {
	return fmt.Sprintf("translate(%s, '%s', '%s')", column,
		"ABCDEFGHIJKLMNOPQRSTUVWXYZ"+domain.NameFoldingFrom, "abcdefghijklmnopqrstuvwxyz"+domain.NameFoldingTo)
}

// expiresSQL is Consent.ExpiresAt in SQL over a timestamptz column.
func expiresSQL(column string) string {
	utc := column + " AT TIME ZONE 'UTC'"
	return fmt.Sprintf(`((%[1]s) + interval '%[2]d months'
		+ CASE WHEN extract(month FROM %[1]s) = 2 AND extract(day FROM %[1]s) = 29 THEN interval '1 day' ELSE interval '0 days' END)
		AT TIME ZONE 'UTC'`, utc, domain.ConsentValidityMonths)
}

// rfc3339Pattern is the shape of a time written by encoding/json; anything
// else in the details is no deadline, as for Go, and never breaks the query.
const rfc3339Pattern = `^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]+)?(Z|[+-][0-9]{2}:[0-9]{2})$`

// deadlineSQL reads a time of the details of an event; a missing, malformed
// or zero time (time.Time{} of Go) is NULL, a deadline that never ends.
func deadlineSQL(text string) string {
	return fmt.Sprintf(`CASE WHEN (%[1]s) ~ '%[2]s'
		THEN NULLIF((%[1]s)::timestamptz, '0001-01-01T00:00:00Z'::timestamptz) END`, text, rfc3339Pattern)
}

// listQuery builds the statements of one query with numbered arguments; $1 is
// always now.
type listQuery struct {
	args []any
}

func newListQuery(now time.Time) *listQuery { return &listQuery{args: []any{now}} }

const nowParam = "$1::timestamptz"

func (b *listQuery) arg(v any) string {
	b.args = append(b.args, v)
	return fmt.Sprintf("$%d", len(b.args))
}

// derivedCTE is the common table expression "derived": every consent that
// meets the filter on consent_record (alias r) with its derived status, the
// time and channel of the revocation that decides it and whether some entity
// missed its first window (RevocationView.OutOfDeadline).
func derivedCTE(filter string) string {
	retryDeadline := deadlineSQL("e.details ->> 'deadline_at'")
	return `WITH candidate AS NOT MATERIALIZED (
	SELECT r.consent_id, r.granted_at, ` + expiresSQL("r.granted_at") + ` AS expires_at
	FROM consent_record r
	WHERE ` + filter + `
),
revoked AS (
	SELECT DISTINCT ON (e.consent_id) e.consent_id, e.occurred_at, e.channel, e.details
	FROM candidate c
	JOIN consent_event e ON e.consent_id = c.consent_id
	WHERE e.status = 'revoked' AND coalesce(e.entity_code, '') = '' AND e.occurred_at < c.expires_at
	ORDER BY e.consent_id, e.occurred_at, e.recorded_at, e.event_id COLLATE "C"
),
entity_window AS (
	SELECT v.consent_id, v.occurred_at AS revoked_at, w.value ->> 'code' AS code,
		` + deadlineSQL("w.value ->> 'deadline_at'") + ` AS deadline_at
	FROM revoked v
	CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(v.details -> 'entities') = 'array'
		THEN v.details -> 'entities' ELSE '[]'::jsonb END) AS w(value)
),
entity_notice AS (
	SELECT w.consent_id,
		a.confirmed_at IS NULL AND (coalesce(a.not_confirmed, false)
			OR coalesce(coalesce(a.retry_deadline_at, w.deadline_at) <= ` + nowParam + `, false)) AS partial,
		coalesce(a.not_confirmed, false)
			OR coalesce(coalesce(a.confirmed_at, ` + nowParam + `) >= w.deadline_at, false) AS out_of_deadline
	FROM entity_window w
	CROSS JOIN LATERAL (
		SELECT min(e.occurred_at) FILTER (WHERE e.status = 'revocation_confirmed') AS confirmed_at,
			bool_or(e.status = 'revocation_not_confirmed') AS not_confirmed,
			(array_agg(` + retryDeadline + ` ORDER BY e.occurred_at DESC, e.recorded_at DESC, e.event_id COLLATE "C" DESC)
				FILTER (WHERE e.status = 'revocation_retry_requested' AND ` + retryDeadline + ` IS NOT NULL))[1] AS retry_deadline_at
		FROM consent_event e
		WHERE e.consent_id = w.consent_id AND e.entity_code = w.code AND e.entity_code <> ''
			AND e.occurred_at >= w.revoked_at
	) a
),
revocation AS (
	SELECT v.consent_id, v.occurred_at AS effective_at, coalesce(v.channel, '') AS channel,
		coalesce(bool_or(n.partial), false) AS partial, coalesce(bool_or(n.out_of_deadline), false) AS out_of_deadline
	FROM revoked v
	LEFT JOIN entity_notice n ON n.consent_id = v.consent_id
	GROUP BY v.consent_id, v.occurred_at, v.channel
),
last_own AS (
	SELECT DISTINCT ON (e.consent_id) e.consent_id, e.status
	FROM candidate c
	JOIN consent_event e ON e.consent_id = c.consent_id
	WHERE coalesce(e.entity_code, '') = ''
	ORDER BY e.consent_id, e.occurred_at DESC, e.recorded_at DESC, e.event_id COLLATE "C" DESC
),
derived AS (
	SELECT c.consent_id, c.granted_at, v.effective_at, v.channel AS revocation_channel, v.out_of_deadline,
		CASE
			WHEN v.consent_id IS NOT NULL THEN CASE WHEN v.partial THEN 'partially_revoked' ELSE 'revoked' END
			WHEN ` + nowParam + ` >= c.expires_at THEN 'expired'
			WHEN l.status = 'revocation_pending' THEN 'revocation_pending'
			ELSE 'active'
		END AS status
	FROM candidate c
	LEFT JOIN revocation v ON v.consent_id = c.consent_id
	LEFT JOIN last_own l ON l.consent_id = c.consent_id
)
`
}

// likeContains is the LIKE pattern of the text anywhere, with the wildcards
// of the text escaped (ESCAPE '\').
func likeContains(text string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(text) + "%"
}

// filters returns the condition on consent_record (alias r) of the filters of
// the query, and the condition on derived (alias d) of its status. Besides
// the status itself, a filter by status adds a bound that its rule implies,
// so the database can use the indexes of migration 0006.
func (b *listQuery) filters(q domain.ComplianceQuery, now time.Time) (onRecord, onDerived string) {
	conds := []string{"TRUE"}
	if !q.GrantedFrom.IsZero() {
		conds = append(conds, "r.granted_at >= "+b.arg(q.GrantedFrom.UTC()))
	}
	if !q.GrantedBefore.IsZero() {
		conds = append(conds, "r.granted_at < "+b.arg(q.GrantedBefore.UTC()))
	}
	if q.Entity != "" {
		conds = append(conds, "r.partner_id = "+b.arg(q.Entity))
	}
	if s := q.Search; !s.Empty() {
		alternatives := []string{"r.consent_id = " + b.arg(s.Text)}
		if s.Document != "" {
			alternatives = append(alternatives, "upper(r.holder_document_id) = "+b.arg(s.Document))
		}
		if s.Name != "" {
			alternatives = append(alternatives, foldSQL("r.holder_full_name")+" LIKE "+b.arg(likeContains(s.Name))+` ESCAPE '\'`)
		}
		conds = append(conds, "("+strings.Join(alternatives, " OR ")+")")
	}
	onDerived = "TRUE"
	switch q.Status {
	case domain.StatusExpired:
		conds = append(conds, "r.granted_at <= "+b.arg(now.Add(-shortestValidity)))
	case domain.StatusActive:
		conds = append(conds, "r.granted_at > "+b.arg(now.Add(-longestValidity)))
	case domain.StatusRevoked, domain.StatusPartiallyRevoked:
		conds = append(conds, `EXISTS (SELECT 1 FROM consent_event x
			WHERE x.consent_id = r.consent_id AND x.status = 'revoked' AND coalesce(x.entity_code, '') = '')`)
	}
	if q.Status != "" {
		onDerived = "d.status = " + b.arg(string(q.Status))
	}
	return strings.Join(conds, " AND "), onDerived
}

// pageSQL is the page of the query: the consent records with their derived
// status and the number of consents that meet the filters (zero rows after
// the last page).
func pageSQL(q domain.ComplianceQuery, now time.Time) (string, []any) {
	b := newListQuery(now)
	onRecord, onDerived := b.filters(q, now)
	limit, offset := b.arg(int64(q.PageSize)), b.arg(q.Offset())
	return derivedCTE(onRecord) + `,
page AS (
	SELECT d.consent_id, d.granted_at, d.status, count(*) OVER () AS total
	FROM derived d
	WHERE ` + onDerived + `
	ORDER BY d.granted_at DESC, d.consent_id COLLATE "C"
	LIMIT ` + limit + ` OFFSET ` + offset + `
)
SELECT ` + qualified("r", consentColumns) + `, p.status, p.total
FROM page p
JOIN consent_record r ON r.consent_id = p.consent_id
ORDER BY p.granted_at DESC, p.consent_id COLLATE "C"`, b.args
}

// countSQL counts the consents that meet the filters (for a page after the
// last one, which has no row to carry the count).
func countSQL(q domain.ComplianceQuery, now time.Time) (string, []any) {
	b := newListQuery(now)
	onRecord, onDerived := b.filters(q, now)
	return derivedCTE(onRecord) + `SELECT count(*) FROM derived d WHERE ` + onDerived, b.args
}

// indicatorsSQL computes the four cards over every consent (decision D-5 a).
func indicatorsSQL(now time.Time) (string, []any) {
	b := newListQuery(now)
	since := b.arg(now.Add(-domain.RevokedIndicatorWindow))
	recent := "d.effective_at IS NOT NULL AND d.effective_at > " + since + " AND d.effective_at <= " + nowParam
	return derivedCTE("TRUE") + `SELECT
	count(*) FILTER (WHERE d.status = 'active'),
	count(*) FILTER (WHERE d.status = 'expired'),
	count(*) FILTER (WHERE ` + recent + `),
	count(*) FILTER (WHERE ` + recent + ` AND d.revocation_channel = 'mobile_app'),
	count(*) FILTER (WHERE ` + recent + ` AND d.revocation_channel = 'compliance_portal'),
	count(*) FILTER (WHERE d.out_of_deadline)
FROM derived d`, b.args
}

// qualified prefixes every column of the list with the alias.
func qualified(alias, columns string) string {
	parts := strings.Split(columns, ",")
	for i, p := range parts {
		parts[i] = alias + "." + strings.TrimSpace(p)
	}
	return strings.Join(parts, ", ")
}

// ListForCompliance implements ports.ComplianceListStore. Every statement runs
// in one read-only transaction with a repeatable snapshot, so the page, the
// count, the indicators and the entities agree with each other.
func (s *ComplianceListStore) ListForCompliance(ctx context.Context, q domain.ComplianceQuery, now time.Time) (domain.ComplianceListResult, error) {
	now = now.UTC()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return domain.ComplianceListResult{}, fmt.Errorf("begin the list of consents: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	out := domain.ComplianceListResult{Rows: []domain.ComplianceRow{}}
	query, args := pageSQL(q, now)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return domain.ComplianceListResult{}, fmt.Errorf("list the consents: %w", err)
	}
	for rows.Next() {
		var (
			status string
			total  int64
		)
		c, err := scanConsent(withExtra{row: rows, extra: []any{&status, &total}})
		if err != nil {
			_ = rows.Close()
			return domain.ComplianceListResult{}, fmt.Errorf("read a consent of the list: %w", err)
		}
		out.Rows = append(out.Rows, domain.ComplianceRow{Consent: c, Status: domain.ConsentStatus(status)})
		out.TotalItems = int(total)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return domain.ComplianceListResult{}, fmt.Errorf("list the consents: %w", err)
	}
	if len(out.Rows) == 0 && q.Page > 1 {
		query, args := countSQL(q, now)
		if err := tx.QueryRowContext(ctx, query, args...).Scan(&out.TotalItems); err != nil {
			return domain.ComplianceListResult{}, fmt.Errorf("count the consents: %w", err)
		}
	}

	query, args = indicatorsSQL(now)
	ind := &out.Indicators
	if err := tx.QueryRowContext(ctx, query, args...).Scan(&ind.Active, &ind.Expired, &ind.RevokedLast30Days.Total,
		&ind.RevokedLast30Days.MobileApp, &ind.RevokedLast30Days.CompliancePortal, &ind.OutOfDeadline); err != nil {
		return domain.ComplianceListResult{}, fmt.Errorf("compute the indicators of the consents: %w", err)
	}

	codes, err := tx.QueryContext(ctx, `SELECT partner_id FROM consent_record GROUP BY partner_id ORDER BY partner_id COLLATE "C"`)
	if err != nil {
		return domain.ComplianceListResult{}, fmt.Errorf("list the entities of the consents: %w", err)
	}
	defer func() { _ = codes.Close() }()
	for codes.Next() {
		var code string
		if err := codes.Scan(&code); err != nil {
			return domain.ComplianceListResult{}, fmt.Errorf("read an entity of the consents: %w", err)
		}
		out.EntityCodes = append(out.EntityCodes, code)
	}
	if err := codes.Err(); err != nil {
		return domain.ComplianceListResult{}, fmt.Errorf("list the entities of the consents: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return domain.ComplianceListResult{}, fmt.Errorf("end the list of consents: %w", err)
	}
	return out, nil
}

// withExtra scans the columns of a consent followed by extra ones.
type withExtra struct {
	row   scanner
	extra []any
}

func (w withExtra) Scan(dest ...any) error { return w.row.Scan(append(dest, w.extra...)...) }
