package memory

import (
	"context"
	"slices"
	"sort"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// AssistanceInboxStore keeps the decisions of the inbox of operations
// (HU-W2.3-03) over the requests of an AssistanceStore, without a database,
// with the same rules as PostgreSQL: every decision appends an action
// numbered after the ones the analyst read, the request only changes its
// status, and nothing decided is changed. It shares the lock of the requests,
// so a decision and a new request never interleave. Everything is lost when
// the service restarts.
type AssistanceInboxStore struct {
	requests   *AssistanceStore
	needs      map[string]domain.RecordedNeed
	voices     map[string]domain.VoiceLocation
	dispatches map[string]domain.AssistanceDispatch
	closures   map[string]domain.AssistanceClosure
	history    map[string][]domain.AssistanceAction
	notices    map[string]ports.AssistanceNotice
}

// NewAssistanceInboxStore builds the inbox over the requests the phone sends
// to requests (the store of HU-M2.3-01).
func NewAssistanceInboxStore(requests *AssistanceStore) *AssistanceInboxStore {
	return &AssistanceInboxStore{requests: requests, needs: map[string]domain.RecordedNeed{},
		voices: map[string]domain.VoiceLocation{}, dispatches: map[string]domain.AssistanceDispatch{},
		closures: map[string]domain.AssistanceClosure{}, history: map[string][]domain.AssistanceAction{},
		notices: map[string]ports.AssistanceNotice{}}
}

var _ ports.AssistanceInboxStore = (*AssistanceInboxStore)(nil)

// record builds the record of a request; the caller holds the lock.
func (s *AssistanceInboxStore) record(r domain.AssistanceRequest) ports.AssistanceRecord {
	c := domain.AssistanceCase{Request: copyAssistance(r),
		History: append([]domain.AssistanceAction(nil), s.history[r.ID]...)}
	if n, ok := s.needs[r.ID]; ok {
		c.Need = &n
	}
	if v, ok := s.voices[r.ID]; ok {
		c.Voice = &v
	}
	if d, ok := s.dispatches[r.ID]; ok {
		d.Candidates = append([]domain.Candidate(nil), d.Candidates...)
		d.Attempts = append([]domain.SearchAttempt(nil), d.Attempts...)
		c.Dispatch = &d
	}
	if cl, ok := s.closures[r.ID]; ok {
		c.Closure = &cl
	}
	out := ports.AssistanceRecord{Case: c}
	if n, ok := s.notices[r.ID]; ok {
		out.Notice = &n
	}
	return out
}

// List returns the requests of the filter, the latest first.
func (s *AssistanceInboxStore) List(_ context.Context, f ports.InboxFilter) ([]ports.AssistanceRecord, error) {
	s.requests.mu.Lock()
	defer s.requests.mu.Unlock()
	var list []domain.AssistanceRequest
	for _, r := range s.requests.byID {
		if len(f.Statuses) == 0 || slices.Contains(f.Statuses, r.Status) {
			list = append(list, r)
		}
	}
	sort.Slice(list, func(i, j int) bool {
		if !list[i].RequestedAt.Equal(list[j].RequestedAt) {
			return list[i].RequestedAt.After(list[j].RequestedAt)
		}
		return list[i].ID > list[j].ID
	})
	if f.Limit > 0 && len(list) > f.Limit {
		list = list[:f.Limit]
	}
	out := make([]ports.AssistanceRecord, 0, len(list))
	for _, r := range list {
		out = append(out, s.record(r))
	}
	return out, nil
}

// Counts returns the new and in call requests and how long each dispatch
// made since dayStart took.
func (s *AssistanceInboxStore) Counts(_ context.Context, dayStart time.Time) (int, int, []time.Duration, error) {
	s.requests.mu.Lock()
	defer s.requests.mu.Unlock()
	newCount, inCall := 0, 0
	for _, r := range s.requests.byID {
		switch r.Status {
		case domain.AssistanceNew:
			newCount++
		case domain.AssistanceInCall:
			inCall++
		}
	}
	var took []time.Duration
	for id, d := range s.dispatches {
		if !d.At.Before(dayStart) {
			took = append(took, d.At.Sub(s.requests.byID[id].RequestedAt))
		}
	}
	return newCount, inCall, took, nil
}

// Case returns the request with its decisions.
func (s *AssistanceInboxStore) Case(_ context.Context, id string) (ports.AssistanceRecord, error) {
	s.requests.mu.Lock()
	defer s.requests.mu.Unlock()
	r, ok := s.requests.byID[id]
	if !ok {
		return ports.AssistanceRecord{}, ports.ErrAssistanceRequestNotFound
	}
	return s.record(r), nil
}

// decide appends the action numbered version+1 and applies the decision,
// unless the request changed since it was read.
func (s *AssistanceInboxStore) decide(id string, version int, a domain.AssistanceAction,
	apply func(r *domain.AssistanceRequest)) error {
	s.requests.mu.Lock()
	defer s.requests.mu.Unlock()
	r, ok := s.requests.byID[id]
	if !ok {
		return ports.ErrAssistanceRequestNotFound
	}
	if len(s.history[id]) != version {
		return ports.ErrAssistanceChanged
	}
	a.Seq = version + 1
	s.history[id] = append(s.history[id], a)
	apply(&r)
	s.requests.byID[id] = r
	return nil
}

// Attend makes the request in_call.
func (s *AssistanceInboxStore) Attend(_ context.Context, id string, version int, a domain.AssistanceAction) error {
	return s.decide(id, version, a, func(r *domain.AssistanceRequest) { r.Status = domain.AssistanceInCall })
}

// RecordNeed records the need in force.
func (s *AssistanceInboxStore) RecordNeed(_ context.Context, id string, version int, n domain.RecordedNeed,
	a domain.AssistanceAction) error {
	return s.decide(id, version, a, func(*domain.AssistanceRequest) { s.needs[id] = n })
}

// RecordVoiceLocation records the location given by voice in force.
func (s *AssistanceInboxStore) RecordVoiceLocation(_ context.Context, id string, version int, v domain.VoiceLocation,
	a domain.AssistanceAction) error {
	return s.decide(id, version, a, func(*domain.AssistanceRequest) { s.voices[id] = v })
}

// Dispatch makes the request dispatched, records the dispatch and leaves its
// notice due.
func (s *AssistanceInboxStore) Dispatch(_ context.Context, id string, version int, d domain.AssistanceDispatch,
	a domain.AssistanceAction, n ports.AssistanceNotice) error {
	return s.decide(id, version, a, func(r *domain.AssistanceRequest) {
		r.Status = domain.AssistanceDispatched
		d.Candidates = append([]domain.Candidate(nil), d.Candidates...)
		d.Attempts = append([]domain.SearchAttempt(nil), d.Attempts...)
		s.dispatches[id] = d
		n.RequestID, n.Status, n.Attempts, n.SentAt, n.LastError = id, ports.NoticePending, 0, time.Time{}, ""
		s.notices[id] = n
	})
}

// Close makes the request closed and records why.
func (s *AssistanceInboxStore) Close(_ context.Context, id string, version int, c domain.AssistanceClosure,
	a domain.AssistanceAction) error {
	return s.decide(id, version, a, func(r *domain.AssistanceRequest) {
		r.Status = domain.AssistanceClosed
		s.closures[id] = c
	})
}

// DispatchesSince counts the dispatches of each provider since dayStart.
func (s *AssistanceInboxStore) DispatchesSince(_ context.Context, providerIDs []string,
	dayStart time.Time) (map[string]int, error) {
	s.requests.mu.Lock()
	defer s.requests.mu.Unlock()
	out := map[string]int{}
	for _, d := range s.dispatches {
		if slices.Contains(providerIDs, d.Provider.ID) && !d.At.Before(dayStart) {
			out[d.Provider.ID]++
		}
	}
	return out, nil
}

// ClaimNoticesDue holds the due notices for lease by moving their next
// attempt.
func (s *AssistanceInboxStore) ClaimNoticesDue(_ context.Context, now time.Time, lease time.Duration,
	limit int) ([]ports.AssistanceNotice, error) {
	s.requests.mu.Lock()
	defer s.requests.mu.Unlock()
	var due []ports.AssistanceNotice
	for _, n := range s.notices {
		if n.Status == ports.NoticePending && !n.NextAttemptAt.After(now) {
			due = append(due, n)
		}
	}
	sort.Slice(due, func(i, j int) bool {
		if !due[i].NextAttemptAt.Equal(due[j].NextAttemptAt) {
			return due[i].NextAttemptAt.Before(due[j].NextAttemptAt)
		}
		return due[i].RequestID < due[j].RequestID
	})
	if limit > 0 && len(due) > limit {
		due = due[:limit]
	}
	for i := range due {
		due[i].NextAttemptAt = now.Add(lease)
		s.notices[due[i].RequestID] = due[i]
	}
	return due, nil
}

// NoticeSent records the delivery of the notice.
func (s *AssistanceInboxStore) NoticeSent(_ context.Context, requestID string, at time.Time) error {
	s.requests.mu.Lock()
	defer s.requests.mu.Unlock()
	if n, ok := s.notices[requestID]; ok && n.Status == ports.NoticePending {
		n.Status, n.SentAt, n.Attempts, n.NextAttemptAt, n.LastError = ports.NoticeSent, at, n.Attempts+1, time.Time{}, ""
		s.notices[requestID] = n
	}
	return nil
}

// NoticeFailed counts a failed delivery and makes the notice due at next.
func (s *AssistanceInboxStore) NoticeFailed(_ context.Context, requestID string, next time.Time, cause string) error {
	s.requests.mu.Lock()
	defer s.requests.mu.Unlock()
	if n, ok := s.notices[requestID]; ok && n.Status == ports.NoticePending {
		n.Attempts, n.NextAttemptAt, n.LastError = n.Attempts+1, next, cause
		s.notices[requestID] = n
	}
	return nil
}
