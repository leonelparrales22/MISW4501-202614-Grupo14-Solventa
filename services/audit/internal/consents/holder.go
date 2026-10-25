package consents

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/ports"
)

// HolderService is the holder's side of the consents (HU-M1.3-01): it keeps
// the links between accounts and documents that origination sends with
// CustomerLinked, resolves the account of a request to its holder and lists
// what that holder may see. It never returns a consent of another holder
// (criterion 6).
type HolderService struct {
	consents ports.ConsentStore
	events   ports.ConsentEventReader
	links    ports.HolderLinkStore
	clock    ports.Clock
	names    domain.EntityNames
}

// NewHolderService builds the service. names is the catalog of display names
// of the entities (PARTNER_DISPLAY_NAMES); it may be empty.
func NewHolderService(consents ports.ConsentStore, events ports.ConsentEventReader, links ports.HolderLinkStore,
	clock ports.Clock, names domain.EntityNames) *HolderService {
	return &HolderService{consents: consents, events: events, links: links, clock: clock, names: names}
}

// Link records the link of an account to its document. It is idempotent: the
// same link again changes nothing. A different document for a subject that
// already has one keeps the first and only logs the conflict, so the message
// is not retried forever. An invalid link is an error.
func (s *HolderService) Link(ctx context.Context, l domain.HolderLink) error {
	l.DocumentID = domain.NormalizeDocument(l.DocumentID)
	l.LinkedAt = l.LinkedAt.UTC()
	if err := l.Validate(); err != nil {
		return err
	}
	stored, created, err := s.links.Link(ctx, l)
	if err != nil {
		return fmt.Errorf("record the holder link: %w", err)
	}
	if !created && stored.DocumentID != l.DocumentID {
		slog.WarnContext(ctx, "holder link conflict: the account already has another document; the first link is kept",
			"event_id", l.EventID, "customer_id", l.CustomerID, "kept_event_id", stored.EventID)
	}
	return nil
}

// Resolve returns the document of the holder the account belongs to, or
// ports.ErrHolderNotLinked.
func (s *HolderService) Resolve(ctx context.Context, subject string) (string, error) {
	doc, err := s.links.DocumentOf(ctx, subject)
	if errors.Is(err, ports.ErrHolderNotLinked) {
		return "", err
	}
	if err != nil {
		return "", fmt.Errorf("resolve the holder: %w", err)
	}
	return doc, nil
}

// List returns every consent of the holder with its status and history, the
// current ones first (criteria 1 to 3).
func (s *HolderService) List(ctx context.Context, document string) ([]domain.ConsentView, error) {
	own, err := s.consents.ListByHolder(ctx, document)
	if err != nil {
		return nil, fmt.Errorf("list the consents of the holder: %w", err)
	}
	ids := make([]string, len(own))
	for i, c := range own {
		ids[i] = c.ID
	}
	byConsent, err := s.events.ListByConsents(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("read the history of the consents: %w", err)
	}
	now := s.clock.Now()
	views := make([]domain.ConsentView, 0, len(own))
	for _, c := range own {
		views = append(views, domain.View(c, byConsent[c.ID], now, s.names))
	}
	domain.SortForHolder(views)
	return views, nil
}

// Get returns one consent of the holder. A consent that does not exist and a
// consent of another holder are the same answer, ports.ErrConsentNotFound, so
// the ids of others are never revealed (criterion 6).
func (s *HolderService) Get(ctx context.Context, document, id string) (domain.ConsentView, error) {
	c, err := s.consents.Get(ctx, id)
	if errors.Is(err, ports.ErrConsentNotFound) {
		return domain.ConsentView{}, err
	}
	if err != nil {
		return domain.ConsentView{}, fmt.Errorf("read the consent: %w", err)
	}
	if domain.NormalizeDocument(c.Holder.DocumentID) != domain.NormalizeDocument(document) {
		return domain.ConsentView{}, ports.ErrConsentNotFound
	}
	byConsent, err := s.events.ListByConsents(ctx, []string{c.ID})
	if err != nil {
		return domain.ConsentView{}, fmt.Errorf("read the history of the consent: %w", err)
	}
	return domain.View(c, byConsent[c.ID], s.clock.Now(), s.names), nil
}
