package evaluation

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/triage"
)

// ResolutionWindow is how far back the mean time of resolution of the queue
// looks (screen O5: «últimos 30 días»).
const ResolutionWindow = 30 * 24 * time.Hour

// holderReads bounds the reads of the insured that run at once for a page.
const holderReads = 8

// QueueFilter narrows the queue by priority (screen O5). Its zero value is the
// whole queue.
type QueueFilter struct {
	// Level is 1 to 3, ports.RankUnassessed for the claims without a level,
	// or 0 for any.
	Level int
	// Line is the line of the claim type; empty for any.
	Line domain.PolicyLine
	// Status is one of the statuses of the queue; empty for any.
	Status domain.Status
}

// QueueSummary is what the indicators of O5 show: the claims of the queue by
// level, without the filters, how many match them and the mean time of
// resolution of the last 30 days.
type QueueSummary struct {
	ByLevel    map[int]int
	Unassessed int
	Total      int
	Matching   int
	// MeanResolution is nil when no claim was decided in the window.
	MeanResolution *time.Duration
	Decided        int
}

// PriorityPage is one page of the queue by priority and its summary.
type PriorityPage struct {
	Items         []QueueItem
	NextPageToken string
	Summary       QueueSummary
}

// WithPriorityQueue makes the claims in evaluation come ordered by priority
// (HU-W2.2-02). clock gives the window of the mean time of resolution.
func (s *Service) WithPriorityQueue(queue ports.PriorityQueue, clock ports.Clock) *Service {
	s.priority, s.clock = queue, clock
	return s
}

// PriorityQueue returns one page of the claims waiting for the decision of
// operations, ordered by priority: Triage 1, 2 and 3, then the ones the triage
// has not assessed yet, and in each the one that waits the longest first. Each
// claim carries its insured when the policy core answers.
func (s *Service) PriorityQueue(ctx context.Context, pageSize int, pageToken string, f QueueFilter) (PriorityPage, error) {
	if s.priority == nil {
		return PriorityPage{}, fmt.Errorf("claims: the queue by priority is not configured")
	}
	switch {
	case pageSize == 0:
		pageSize = DefaultPageSize
	case pageSize < 1 || pageSize > MaxPageSize:
		return PriorityPage{}, ErrInvalidQuery
	}
	after, err := decodePriorityCursor(pageToken)
	if err != nil {
		return PriorityPage{}, err
	}
	q, err := queryOf(f)
	if err != nil {
		return PriorityPage{}, err
	}

	q.After, q.Limit = after, pageSize+1
	claims, err := s.priority.ListByPriority(ctx, q)
	if err != nil {
		return PriorityPage{}, fmt.Errorf("list the queue by priority: %w", err)
	}
	var page PriorityPage
	if len(claims) > pageSize {
		claims = claims[:pageSize]
		last := claims[len(claims)-1]
		page.NextPageToken = encodePriorityCursor(ports.PriorityCursor{Rank: last.Rank, FiledAt: last.Claim.FiledAt, ID: last.Claim.ID})
	}
	if page.Summary, err = s.summary(ctx, q, f.Status != ""); err != nil {
		return PriorityPage{}, err
	}
	if len(claims) == 0 {
		return page, nil
	}

	ids := make([]string, 0, len(claims))
	records := make([]domain.ClaimRecord, 0, len(claims))
	for _, c := range claims {
		ids = append(ids, c.Claim.ID)
		records = append(records, c.Claim)
	}
	summaries, err := s.triage.Summaries(ctx, ids)
	if err != nil {
		return PriorityPage{}, fmt.Errorf("read the priority of the claims: %w", err)
	}
	byID := make(map[string]triage.Summary, len(summaries))
	for _, sum := range summaries {
		byID[sum.ClaimID] = sum
	}
	holders := s.holders(ctx, records)
	for _, c := range records {
		page.Items = append(page.Items, QueueItem{Claim: c, Triage: byID[c.ID], Holder: holders[c.Subject]})
	}
	return page, nil
}

// queryOf turns the filter into the query of the store. The statuses are the
// ones of the queue, the ones the triage evaluates.
func queryOf(f QueueFilter) (ports.PriorityQuery, error) {
	q := ports.PriorityQuery{Statuses: domain.TriageableStatuses()}
	if f.Status != "" {
		if !domain.Triageable(f.Status) {
			return ports.PriorityQuery{}, ErrInvalidQuery
		}
		q.Statuses = []domain.Status{f.Status}
	}
	if f.Level < 0 || f.Level > ports.RankUnassessed {
		return ports.PriorityQuery{}, ErrInvalidQuery
	}
	q.Rank = f.Level
	if f.Line != "" {
		for _, o := range domain.CatalogV1().Incidents {
			if o.Line == f.Line {
				q.IncidentTypes = append(q.IncidentTypes, o.Type)
			}
		}
		if len(q.IncidentTypes) == 0 {
			return ports.PriorityQuery{}, ErrInvalidQuery
		}
	}
	return q, nil
}

// summary counts the whole queue by level. What matches counts the filters,
// and only when the status is filtered does it need a second count.
func (s *Service) summary(ctx context.Context, q ports.PriorityQuery, byStatus bool) (QueueSummary, error) {
	byRank, matching, err := s.priority.CountByRank(ctx, ports.PriorityQuery{Statuses: domain.TriageableStatuses(),
		Rank: q.Rank, IncidentTypes: q.IncidentTypes})
	if err != nil {
		return QueueSummary{}, fmt.Errorf("count the queue by priority: %w", err)
	}
	out := QueueSummary{ByLevel: map[int]int{1: byRank[1], 2: byRank[2], 3: byRank[3]},
		Unassessed: byRank[ports.RankUnassessed], Matching: matching}
	for _, n := range byRank {
		out.Total += n
	}
	if byStatus {
		_, out.Matching, err = s.priority.CountByRank(ctx, q)
		if err != nil {
			return QueueSummary{}, fmt.Errorf("count the queue by priority: %w", err)
		}
	}
	mean, decided, err := s.priority.MeanResolution(ctx, s.clock.Now().Add(-ResolutionWindow))
	if err != nil {
		return QueueSummary{}, fmt.Errorf("read the mean time of resolution: %w", err)
	}
	if decided > 0 {
		out.MeanResolution, out.Decided = &mean, decided
	}
	return out, nil
}

// holders reads the insured of each subject of the page from the policy core,
// a few at a time. A read that fails leaves that insured out: the queue is
// still worth reading without the name.
func (s *Service) holders(ctx context.Context, claims []domain.ClaimRecord) map[string]*domain.Holder {
	out := map[string]*domain.Holder{}
	if s.terms == nil {
		return out
	}
	var subjects []string
	seen := map[string]bool{}
	for _, c := range claims {
		if !seen[c.Subject] {
			seen[c.Subject] = true
			subjects = append(subjects, c.Subject)
		}
	}
	var (
		mu   sync.Mutex
		wg   sync.WaitGroup
		slot = make(chan struct{}, holderReads)
	)
	for _, subject := range subjects {
		wg.Add(1)
		go func(subject string) {
			defer wg.Done()
			slot <- struct{}{}
			defer func() { <-slot }()
			h, err := s.terms.Holder(ctx, subject)
			if err != nil {
				slog.WarnContext(ctx, "could not read the insured of a claim of the queue", "error", err)
				return
			}
			mu.Lock()
			out[subject] = &h
			mu.Unlock()
		}(subject)
	}
	wg.Wait()
	return out
}

// The page token of the queue by priority is the cursor of its last claim:
// rank, when it was filed and its id. It is opaque to the caller.
func encodePriorityCursor(c ports.PriorityCursor) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(c.Rank) + "|" +
		c.FiledAt.UTC().Format(time.RFC3339Nano) + "|" + c.ID))
}

func decodePriorityCursor(token string) (*ports.PriorityCursor, error) {
	if token == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, ErrInvalidQuery
	}
	parts := strings.SplitN(string(raw), "|", 3)
	if len(parts) != 3 || parts[2] == "" {
		return nil, ErrInvalidQuery
	}
	rank, err := strconv.Atoi(parts[0])
	if err != nil || rank < 1 || rank > ports.RankUnassessed {
		return nil, ErrInvalidQuery
	}
	filedAt, err := time.Parse(time.RFC3339Nano, parts[1])
	if err != nil {
		return nil, ErrInvalidQuery
	}
	return &ports.PriorityCursor{Rank: rank, FiledAt: filedAt, ID: parts[2]}, nil
}
