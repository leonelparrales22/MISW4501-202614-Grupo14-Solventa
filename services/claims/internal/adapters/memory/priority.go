package memory

import (
	"context"
	"sort"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// LevelReader returns the level of the current triage of each claim that has
// one (HU-W2.2-01); a claim without a level is not in the map.
type LevelReader func(ctx context.Context, ids []string) (map[string]int, error)

// PriorityQueue is the queue by priority over the claims of a CaseStore
// (HU-W2.2-02), with the same order as PostgreSQL: rank, then the time of the
// filing, then the id. Everything is lost when the service restarts.
type PriorityQueue struct {
	cases     *CaseStore
	decisions *DecisionStore
	levels    LevelReader
}

// NewPriorityQueue builds the queue over the claims of cases. decisions may be
// nil: then no claim is decided.
func NewPriorityQueue(cases *CaseStore, decisions *DecisionStore, levels LevelReader) *PriorityQueue {
	return &PriorityQueue{cases: cases, decisions: decisions, levels: levels}
}

var _ ports.PriorityQueue = (*PriorityQueue)(nil)

// ranked returns every claim in the statuses with its rank, in the order of
// the queue.
func (q *PriorityQueue) ranked(ctx context.Context, statuses []domain.Status) ([]ports.PrioritizedClaim, error) {
	claims, err := q.cases.ListByStatus(ctx, statuses, nil, -1)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(claims))
	for _, c := range claims {
		ids = append(ids, c.ID)
	}
	levels, err := q.levels(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]ports.PrioritizedClaim, 0, len(claims))
	for _, c := range claims {
		rank, ok := levels[c.ID]
		if !ok {
			rank = ports.RankUnassessed
		}
		out = append(out, ports.PrioritizedClaim{Claim: c, Rank: rank})
	}
	sort.SliceStable(out, func(i, j int) bool { return before(out[i], out[j]) })
	return out, nil
}

func before(a, b ports.PrioritizedClaim) bool {
	switch {
	case a.Rank != b.Rank:
		return a.Rank < b.Rank
	case !a.Claim.FiledAt.Equal(b.Claim.FiledAt):
		return a.Claim.FiledAt.Before(b.Claim.FiledAt)
	}
	return a.Claim.ID < b.Claim.ID
}

func matches(c ports.PrioritizedClaim, q ports.PriorityQuery) bool {
	if q.Rank != 0 && c.Rank != q.Rank {
		return false
	}
	if len(q.IncidentTypes) == 0 {
		return true
	}
	for _, t := range q.IncidentTypes {
		if c.Claim.IncidentType == t {
			return true
		}
	}
	return false
}

// ListByPriority returns one page of the queue.
func (q *PriorityQueue) ListByPriority(ctx context.Context, query ports.PriorityQuery) ([]ports.PrioritizedClaim, error) {
	if len(query.Statuses) == 0 || query.Limit <= 0 {
		return nil, nil
	}
	all, err := q.ranked(ctx, query.Statuses)
	if err != nil {
		return nil, err
	}
	var out []ports.PrioritizedClaim
	for _, c := range all {
		if !matches(c, query) {
			continue
		}
		if a := query.After; a != nil && !before(ports.PrioritizedClaim{Rank: a.Rank,
			Claim: domain.ClaimRecord{Claim: domain.Claim{ID: a.ID, FiledAt: a.FiledAt}}}, c) {
			continue
		}
		out = append(out, c)
		if len(out) == query.Limit {
			break
		}
	}
	return out, nil
}

// CountByRank counts the claims in the statuses by rank, and the ones that
// match every filter.
func (q *PriorityQueue) CountByRank(ctx context.Context, query ports.PriorityQuery) (map[int]int, int, error) {
	byRank := map[int]int{}
	all, err := q.ranked(ctx, query.Statuses)
	if err != nil {
		return nil, 0, err
	}
	matching := 0
	for _, c := range all {
		byRank[c.Rank]++
		if matches(c, query) {
			matching++
		}
	}
	return byRank, matching, nil
}

// MeanResolution is the mean time from the filing to the decision of the
// claims decided since then.
func (q *PriorityQueue) MeanResolution(_ context.Context, since time.Time) (time.Duration, int, error) {
	if q.decisions == nil {
		return 0, 0, nil
	}
	q.decisions.mu.Lock()
	defer q.decisions.mu.Unlock()
	q.cases.mu.Lock()
	defer q.cases.mu.Unlock()
	var total time.Duration
	decided := 0
	for id, d := range q.decisions.decisions {
		if d.DecidedAt.Before(since) {
			continue
		}
		total += d.DecidedAt.Sub(q.cases.byID[id].FiledAt)
		decided++
	}
	if decided == 0 {
		return 0, 0, nil
	}
	return total / time.Duration(decided), decided, nil
}
