package credentials_test

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/events"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/partners/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/partners/internal/ports"
)

// memStore is an in-memory ports.PartnerStore with the semantics of the
// PostgreSQL one: Mutate serializes and writes nothing if fn fails.
type memStore struct {
	mu       sync.Mutex
	partners map[string]ports.PartnerRecord
	outbox   []events.Envelope
	failNext error
}

func newMemStore() *memStore { return &memStore{partners: map[string]ports.PartnerRecord{}} }

func (s *memStore) Create(_ context.Context, p domain.Partner, entry domain.HistoryEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.partners {
		if r.Partner.Code == p.Code {
			return domain.ErrCodeTaken
		}
	}
	s.partners[p.ID] = ports.PartnerRecord{Partner: p, History: []domain.HistoryEntry{entry}}
	return nil
}

func (s *memStore) List(_ context.Context) ([]ports.PartnerRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ports.PartnerRecord, 0, len(s.partners))
	for _, r := range s.partners {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Partner.ID < out[j].Partner.ID })
	return out, nil
}

func (s *memStore) Get(_ context.Context, id string) (ports.PartnerRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.partners[id]
	if !ok {
		return ports.PartnerRecord{}, domain.ErrPartnerNotFound
	}
	return r, nil
}

func (s *memStore) Mutate(_ context.Context, id string, fn func(ports.PartnerRecord) (ports.Change, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.partners[id]
	if !ok {
		return domain.ErrPartnerNotFound
	}
	ch, err := fn(r)
	if err != nil {
		return err
	}
	if s.failNext != nil {
		err, s.failNext = s.failNext, nil
		return err
	}
	if ch.Partner != nil {
		r.Partner = *ch.Partner
	}
	for _, u := range ch.UpdatedCredentials {
		for i := range r.Credentials {
			if r.Credentials[i].ID == u.ID {
				r.Credentials[i] = u
			}
		}
	}
	r.Credentials = append(append([]domain.Credential{}, ch.NewCredentials...), r.Credentials...)
	for _, h := range ch.History {
		r.History = append([]domain.HistoryEntry{h}, r.History...)
	}
	s.outbox = append(s.outbox, ch.Events...)
	s.partners[id] = r
	return nil
}

func (s *memStore) DueRetirements(_ context.Context, now time.Time, _ int) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []string
	for id, r := range s.partners {
		for _, c := range r.Credentials {
			if c.Status == domain.CredentialRetiring && c.ExpiresAt != nil && !c.ExpiresAt.After(now) {
				ids = append(ids, id)
				break
			}
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// fakeGateway records what exists in the "gateway".
type fakeGateway struct {
	mu        sync.Mutex
	n         int
	plans     map[string]domain.Quota
	keys      map[string]string // key id -> value
	attached  map[string]string // key id -> plan id
	usage     map[string]int64  // key id -> used today
	failOn    string
	usageErr  error
	deletedKs []string
}

func newFakeGateway() *fakeGateway {
	return &fakeGateway{plans: map[string]domain.Quota{}, keys: map[string]string{}, attached: map[string]string{},
		usage: map[string]int64{}}
}

var errBoom = fmt.Errorf("%w: boom", domain.ErrGatewayUnavailable)

func (g *fakeGateway) fail(op string) error {
	if g.failOn == op {
		return errBoom
	}
	return nil
}

func (g *fakeGateway) CreateUsagePlan(_ context.Context, spec ports.UsagePlanSpec) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.fail("plan"); err != nil {
		return "", err
	}
	g.n++
	id := fmt.Sprintf("plan-%d", g.n)
	g.plans[id] = spec.Quota
	return id, nil
}

func (g *fakeGateway) DeleteUsagePlan(_ context.Context, planID string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.plans, planID)
	return nil
}

func (g *fakeGateway) CreateAPIKey(_ context.Context, spec ports.APIKeySpec) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.fail("key"); err != nil {
		return "", err
	}
	g.n++
	id := fmt.Sprintf("key-%d", g.n)
	g.keys[id] = spec.Value
	return id, nil
}

func (g *fakeGateway) DeleteAPIKey(_ context.Context, keyID string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.fail("delete"); err != nil {
		return err
	}
	delete(g.keys, keyID)
	g.deletedKs = append(g.deletedKs, keyID)
	return nil
}

func (g *fakeGateway) AttachKey(_ context.Context, planID, keyID string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.fail("attach"); err != nil {
		return err
	}
	g.attached[keyID] = planID
	return nil
}

func (g *fakeGateway) DetachKey(_ context.Context, _, keyID string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.attached, keyID)
	return nil
}

func (g *fakeGateway) UsedOn(_ context.Context, planID, _ string) (map[string]int64, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.usageErr != nil {
		return nil, g.usageErr
	}
	out := map[string]int64{}
	for k, p := range g.attached {
		if p == planID {
			out[k] = g.usage[k]
		}
	}
	return out, nil
}

type fixedClock struct{ now time.Time }

func (c *fixedClock) Now() time.Time { return c.now }

var errDB = errors.New("database down")
