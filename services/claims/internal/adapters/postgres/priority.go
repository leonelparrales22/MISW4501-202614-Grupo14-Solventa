package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// PriorityQueue is the ports.PriorityQueue over the claims and their current
// triage (HU-W2.2-02): one query orders by the level of the triage, the claims
// without one after the three levels, and in each level by the time of the
// filing, so the page and its cursor follow the order of the queue.
type PriorityQueue struct {
	db *sql.DB
}

var _ ports.PriorityQueue = (*PriorityQueue)(nil)

// PriorityQueueOf builds the queue over the database of the claims.
func PriorityQueueOf(records *ClaimRecordStore) *PriorityQueue { return &PriorityQueue{db: records.db} }

// ranked is every claim with its rank: the level of its current triage, or
// RankUnassessed. The subquery keeps the columns of the claim unambiguous.
const ranked = `(SELECT c.*, COALESCE(t.level, 4) AS rank
		FROM claim c LEFT JOIN (SELECT claim_id, level FROM claim_current_triage) t ON t.claim_id = c.id) q`

// filters are the conditions of a query; $1 is the statuses, $2 the rank (0:
// any) and $3 the claim types (empty: any).
const filters = `status = ANY($1)
		AND ($2::int = 0 OR rank = $2)
		AND (cardinality($3::text[]) = 0 OR incident_type = ANY($3))`

// rankedRow scans a claim and its rank.
type rankedRow struct {
	rows *sql.Rows
	rank *int
}

func (r rankedRow) Scan(dest ...any) error { return r.rows.Scan(append(dest, r.rank)...) }

// ListByPriority returns one page of the queue.
func (s *PriorityQueue) ListByPriority(ctx context.Context, q ports.PriorityQuery) ([]ports.PrioritizedClaim, error) {
	if len(q.Statuses) == 0 || q.Limit <= 0 {
		return nil, nil
	}
	var (
		afterRank sql.NullInt64
		afterAt   sql.NullTime
		afterID   string
	)
	if a := q.After; a != nil {
		afterRank, afterAt, afterID = sql.NullInt64{Int64: int64(a.Rank), Valid: true}, sql.NullTime{Time: a.FiledAt, Valid: true}, a.ID
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+recordColumns+`, rank FROM `+ranked+`
		WHERE `+filters+`
		  AND ($4::int IS NULL OR (rank, filed_at, id) > ($4, $5::timestamptz, $6::text))
		ORDER BY rank, filed_at, id
		LIMIT $7`,
		statusNames(q.Statuses), q.Rank, incidentNames(q.IncidentTypes), afterRank, afterAt, afterID, q.Limit)
	if err != nil {
		return nil, fmt.Errorf("list the queue by priority: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []ports.PrioritizedClaim
	for rows.Next() {
		var rank int
		c, _, err := scanRecord(rankedRow{rows: rows, rank: &rank})
		if err != nil {
			return nil, fmt.Errorf("list the queue by priority: %w", err)
		}
		out = append(out, ports.PrioritizedClaim{Claim: c, Rank: rank})
	}
	return out, rows.Err()
}

// CountByRank counts the claims in the statuses by rank, and the ones that
// match every filter of the query.
func (s *PriorityQueue) CountByRank(ctx context.Context, q ports.PriorityQuery) (map[int]int, int, error) {
	byRank := map[int]int{}
	if len(q.Statuses) == 0 {
		return byRank, 0, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT rank, count(*),
			count(*) FILTER (WHERE ($2::int = 0 OR rank = $2) AND (cardinality($3::text[]) = 0 OR incident_type = ANY($3)))
		FROM `+ranked+` WHERE status = ANY($1) GROUP BY rank`,
		statusNames(q.Statuses), q.Rank, incidentNames(q.IncidentTypes))
	if err != nil {
		return nil, 0, fmt.Errorf("count the queue by priority: %w", err)
	}
	defer func() { _ = rows.Close() }()
	matching := 0
	for rows.Next() {
		var rank, n, m int
		if err := rows.Scan(&rank, &n, &m); err != nil {
			return nil, 0, fmt.Errorf("count the queue by priority: %w", err)
		}
		byRank[rank] = n
		matching += m
	}
	return byRank, matching, rows.Err()
}

// MeanResolution is the mean time from the filing to the decision of the
// claims decided since then.
func (s *PriorityQueue) MeanResolution(ctx context.Context, since time.Time) (time.Duration, int, error) {
	var (
		seconds sql.NullFloat64
		decided int
	)
	err := s.db.QueryRowContext(ctx, `SELECT avg(EXTRACT(EPOCH FROM d.decided_at - c.filed_at)), count(*)
		FROM claim_decision d JOIN claim c ON c.id = d.claim_id WHERE d.decided_at >= $1`, since.UTC()).Scan(&seconds, &decided)
	if err != nil {
		return 0, 0, fmt.Errorf("read the mean resolution time: %w", err)
	}
	return time.Duration(seconds.Float64 * float64(time.Second)), decided, nil
}

func incidentNames(types []domain.IncidentType) []string {
	out := make([]string, 0, len(types))
	for _, t := range types {
		out = append(out, string(t))
	}
	return out
}
