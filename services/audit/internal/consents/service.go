// Package consents is the module of the audit service that owns the explicit
// consent of the customer to be profiled (HU-W1.2-01, decision D1). Other
// services never decide it themselves: they ask audit.
package consents

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/ports"
)

// IDPrefix starts every consent id; 128 random bits follow, so an id cannot be
// guessed.
const IDPrefix = "cns-"

// Service grants, reads and verifies consents.
type Service struct {
	store  ports.ConsentStore
	events ports.ConsentEventReader
	clock  ports.Clock
}

// ServiceOption configures a Service.
type ServiceOption func(*Service)

// WithEvents makes Verify read the append-only events of the consent and
// answer with its derived status (HU-M1.3-02): a revoked or partially
// revoked consent is not active from the instant its revocation is recorded,
// so origination stops querying the sources at once (criterion 3). Without
// it, Verify only looks at the validity of the record (HU-W1.2-01).
func WithEvents(events ports.ConsentEventReader) ServiceOption {
	return func(s *Service) { s.events = events }
}

// NewService builds the service over a consent store and the clock.
func NewService(store ports.ConsentStore, clock ports.Clock, opts ...ServiceOption) *Service {
	s := &Service{store: store, clock: clock}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// CurrentTerms returns the terms the customer must see before consenting
// (criterion 1).
func (s *Service) CurrentTerms() domain.Terms { return domain.TermsV1() }

// Grant validates and records the consent. Sending the same request again (same
// customer, application and terms version) returns the stored consent with
// created=false instead of recording a second one. The granted_at is the
// server time, never the caller's.
func (s *Service) Grant(ctx context.Context, req domain.GrantRequest) (consent domain.Consent, created bool, err error) {
	terms := s.CurrentTerms()
	if err := req.Validate(terms); err != nil {
		return domain.Consent{}, false, err
	}
	id, err := newID()
	if err != nil {
		return domain.Consent{}, false, err
	}
	candidate := domain.Consent{
		ID:            id,
		CustomerID:    req.CustomerID,
		Holder:        req.Holder(),
		TermsVersion:  terms.Version,
		Scope:         terms.Sources,
		Purpose:       terms.Purpose,
		Channel:       domain.Channel(req.Channel),
		PartnerID:     req.PartnerID,
		ApplicationID: req.ApplicationID,
		UserAgent:     req.UserAgent,
		// PostgreSQL keeps microseconds: truncating here makes the value the
		// caller gets equal to the stored one.
		GrantedAt: s.clock.Now().UTC().Truncate(time.Microsecond),
	}
	stored, created, err := s.store.Create(ctx, candidate)
	if err != nil {
		return domain.Consent{}, false, fmt.Errorf("record the consent: %w", err)
	}
	return stored, created, nil
}

// Get returns the consent evidence or ports.ErrConsentNotFound (criterion 2).
func (s *Service) Get(ctx context.Context, id string) (domain.Consent, error) {
	c, err := s.store.Get(ctx, id)
	if err != nil {
		return domain.Consent{}, wrapStore(err)
	}
	return c, nil
}

// Verify tells whether the customer has an active consent now. A customer
// without any consent is not an error: the answer is Active=false. With
// WithEvents, a revoked consent is not active and lists no source; the
// contract of the answer does not change, so origination is not touched.
func (s *Service) Verify(ctx context.Context, customerID string) (domain.Verification, error) {
	c, err := s.store.LatestByCustomer(ctx, customerID)
	if errors.Is(err, ports.ErrConsentNotFound) {
		return domain.Verification{}, nil
	}
	if err != nil {
		return domain.Verification{}, fmt.Errorf("look up the consent: %w", err)
	}
	now := s.clock.Now()
	v := domain.Verification{
		ConsentID: c.ID,
		GrantedAt: c.GrantedAt,
		ExpiresAt: c.ExpiresAt(),
		Sources:   c.ActiveSources(now),
	}
	v.Active = c.Active(now)
	if s.events == nil || !v.Active {
		return v, nil
	}
	byConsent, err := s.events.ListByConsents(ctx, []string{c.ID})
	if err != nil {
		return domain.Verification{}, fmt.Errorf("read the events of the consent: %w", err)
	}
	if status, _, _ := domain.DeriveStatus(c, byConsent[c.ID], now); !status.Current() {
		v.Active, v.Sources = false, []string{}
	}
	return v, nil
}

func wrapStore(err error) error {
	if errors.Is(err, ports.ErrConsentNotFound) {
		return err
	}
	return fmt.Errorf("read the consent: %w", err)
}

func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate the consent id: %w", err)
	}
	return IDPrefix + hex.EncodeToString(b[:]), nil
}
