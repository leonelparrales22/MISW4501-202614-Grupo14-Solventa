package memory

import (
	"context"
	"sort"
	"sync"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// CaseStore keeps the claims of both channels and the uploaded supports in
// memory (HU-W2.1-01), so that without a database the list of the insured
// shows the claims filed from the mobile app too. It is the mobile
// ClaimRepository as well: one numbering and one set of filing keys for both
// channels, as in PostgreSQL. Everything is lost when the service restarts.
//
// Provisional (HU-W2.1-01 §9): it repeats what ClaimStore does for the mobile
// filing, which is left untouched.
type CaseStore struct {
	mu       sync.Mutex
	byID     map[string]domain.ClaimRecord
	byKey    map[string]string
	sequence int
	supports map[string]domain.SupportFile
	contents map[string][]byte
	// attachedTo tells the claim each support is attached to.
	attachedTo map[string]string
}

// NewCaseStore builds an empty store.
func NewCaseStore() *CaseStore {
	return &CaseStore{
		byID: map[string]domain.ClaimRecord{}, byKey: map[string]string{},
		supports: map[string]domain.SupportFile{}, contents: map[string][]byte{}, attachedTo: map[string]string{},
	}
}

var (
	_ ports.ClaimRepository = (*CaseStore)(nil)
	_ ports.ClaimRecords    = (*CaseStore)(nil)
	_ ports.SupportStore    = (*CaseStore)(nil)
)

// Create stores a claim of the mobile filing (HU-M2.2-01) and assigns its
// filing number.
func (s *CaseStore) Create(_ context.Context, c domain.Claim) (domain.Claim, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if existing, ok := s.byFilingKey(c.Subject, c.FilingKey); ok {
		if existing.Channel != domain.ChannelMobile || existing.Fingerprint() != c.Fingerprint() {
			return domain.Claim{}, false, ports.ErrFilingKeyReused
		}
		return existing.Claim, false, nil
	}
	stored := s.insert(domain.ClaimRecord{Claim: c, Channel: domain.ChannelMobile})
	return stored.Claim, true, nil
}

// CreateWeb stores a web claim and attaches its supports, all or nothing.
func (s *CaseStore) CreateWeb(_ context.Context, c domain.ClaimRecord) (domain.ClaimRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if existing, ok := s.byFilingKey(c.Subject, c.FilingKey); ok {
		if existing.Channel != domain.ChannelWeb || existing.WebFingerprint() != c.WebFingerprint() {
			return domain.ClaimRecord{}, false, ports.ErrFilingKeyReused
		}
		return existing, false, nil
	}
	for _, a := range c.Supports {
		if _, taken := s.attachedTo[a.File.ID]; taken {
			return domain.ClaimRecord{}, false, ports.ErrSupportAlreadyAttached
		}
	}
	c.Channel = domain.ChannelWeb
	stored := s.insert(c)
	for _, a := range stored.Supports {
		s.attachedTo[a.File.ID] = stored.ID
	}
	return stored, true, nil
}

// FindByKey returns the claim the subject filed with the key.
func (s *CaseStore) FindByKey(_ context.Context, subject, key string) (domain.ClaimRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if c, ok := s.byFilingKey(subject, key); ok {
		return c, nil
	}
	return domain.ClaimRecord{}, ports.ErrClaimNotFound
}

// Get returns the claim with its supports.
func (s *CaseStore) Get(_ context.Context, id string) (domain.ClaimRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	c, ok := s.byID[id]
	if !ok {
		return domain.ClaimRecord{}, ports.ErrClaimNotFound
	}
	return copyRecord(c), nil
}

// ListByOwner returns the subject's claims, newest first, without supports.
func (s *CaseStore) ListByOwner(_ context.Context, subject string, f ports.ClaimFilter,
	after *ports.ClaimCursor, limit int) ([]domain.ClaimRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var out []domain.ClaimRecord
	for _, c := range s.byID {
		switch {
		case c.Subject != subject,
			f.Status != "" && c.Status != f.Status,
			f.PolicyID != "" && c.PolicyID != f.PolicyID,
			!f.FiledFrom.IsZero() && c.FiledAt.Before(f.FiledFrom),
			after != nil && !olderThan(c, *after):
			continue
		}
		c = copyRecord(c)
		c.Supports = nil
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		return !olderThan(out[i], ports.ClaimCursor{FiledAt: out[j].FiledAt, ID: out[j].ID})
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Save stores a new support with its content.
func (s *CaseStore) Save(_ context.Context, f domain.SupportFile, content []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.supports[f.ID] = f
	s.contents[f.ID] = content
	return nil
}

// Find returns the supports with the ids that exist.
func (s *CaseStore) Find(_ context.Context, ids []string) (map[string]domain.SupportFile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make(map[string]domain.SupportFile, len(ids))
	for _, id := range ids {
		if f, ok := s.supports[id]; ok {
			out[id] = f
		}
	}
	return out, nil
}

// CountPending counts the supports of the owner not attached to any claim.
func (s *CaseStore) CountPending(_ context.Context, owner string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	n := 0
	for id, f := range s.supports {
		if _, attached := s.attachedTo[id]; f.OwnerSubject == owner && !attached {
			n++
		}
	}
	return n, nil
}

func (s *CaseStore) byFilingKey(subject, key string) (domain.ClaimRecord, bool) {
	id, ok := s.byKey[subject+"\x00"+key]
	if !ok {
		return domain.ClaimRecord{}, false
	}
	return copyRecord(s.byID[id]), true
}

// insert assigns the filing number and keeps the claim. The caller holds the
// lock and has checked the filing key.
func (s *CaseStore) insert(c domain.ClaimRecord) domain.ClaimRecord {
	s.sequence++
	c.FilingNumber = domain.FilingNumber(c.FiledAt, int64(s.sequence))
	c = copyRecord(c)
	s.byID[c.ID] = c
	s.byKey[c.Subject+"\x00"+c.FilingKey] = c.ID
	return copyRecord(c)
}

// olderThan tells whether the claim comes after the cursor in the list: the
// claims are ordered by (filed_at, id) descending.
func olderThan(c domain.ClaimRecord, cursor ports.ClaimCursor) bool {
	if !c.FiledAt.Equal(cursor.FiledAt) {
		return c.FiledAt.Before(cursor.FiledAt)
	}
	return c.ID < cursor.ID
}

// copyRecord returns a claim that shares no slice, map or pointer with c, so
// no caller can change what the store keeps.
func copyRecord(c domain.ClaimRecord) domain.ClaimRecord {
	details := make(map[string]string, len(c.Details))
	for k, v := range c.Details {
		details[k] = v
	}
	c.Details = details
	if c.Policy != nil {
		p := c.Policy.Copy()
		c.Policy = &p
	}
	c.Supports = append([]domain.AttachedSupport(nil), c.Supports...)
	return c
}
