package consents

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/ports"
)

// validate checks the command with the rules of its channel. The compliance
// portal (HU-W4.2-02) gives a reason, a reference and the server time, and has
// no device to confirm on; any other channel, the holder's app, is checked
// exactly as HU-M1.3-02 does.
func (cmd RevokeCommand) validate(c domain.Consent, now time.Time) error {
	if cmd.Channel == domain.RevocationChannelCompliancePortal {
		return domain.ValidatePortalCommand(cmd.Reason, cmd.Reference, cmd.Request.RequestedAt)
	}
	return cmd.Request.Validate(c, now)
}

// ComplianceService is what the compliance portal does with a consent
// (HU-W4.2-02): read its revocation state, revoke it and retry the entities
// that did not confirm. The process is the one of the RevocationService.
type ComplianceService struct {
	revocations *RevocationService
	consents    ports.ConsentStore
	events      ports.ConsentEventReader
	records     ports.RevocationStore
	clock       ports.Clock
	names       domain.EntityNames
}

// ComplianceDeps are the ports of the ComplianceService.
type ComplianceDeps struct {
	Revocations *RevocationService
	Consents    ports.ConsentStore
	Events      ports.ConsentEventReader
	Records     ports.RevocationStore
	Clock       ports.Clock
	// Names is the catalog of display names of the entities.
	Names domain.EntityNames
}

// NewComplianceService builds the service.
func NewComplianceService(d ComplianceDeps) *ComplianceService {
	return &ComplianceService{
		revocations: d.Revocations, consents: d.Consents, events: d.Events, records: d.Records, clock: d.Clock, names: d.Names,
	}
}

// State returns the consent as the portal sees it now: the derived view, the
// immutable record of its revocation (when it has one) and the history with
// its actors, reasons and references. An unknown consent is
// ports.ErrConsentNotFound.
func (s *ComplianceService) State(ctx context.Context, consentID string) (domain.ComplianceState, error) {
	c, err := s.consents.Get(ctx, consentID)
	if errors.Is(err, ports.ErrConsentNotFound) {
		return domain.ComplianceState{}, err
	}
	if err != nil {
		return domain.ComplianceState{}, fmt.Errorf("read the consent: %w", err)
	}
	byConsent, err := s.events.ListByConsents(ctx, []string{c.ID})
	if err != nil {
		return domain.ComplianceState{}, fmt.Errorf("read the history of the consent: %w", err)
	}
	now := s.clock.Now().UTC()
	events := byConsent[c.ID]
	state := domain.ComplianceState{View: domain.View(c, events, now, s.names), History: complianceHistory(c, events, now)}
	// A consent that was not revoked has no record: the poll of the portal
	// does not need to ask for it.
	if state.View.Revocation == nil {
		return state, nil
	}
	rev, err := s.records.GetByConsent(ctx, c.ID)
	switch {
	case errors.Is(err, ports.ErrRevocationNotFound):
		// A revoked event without record (a seed from before HU-M1.3-02).
	case err != nil:
		return domain.ComplianceState{}, fmt.Errorf("read the revocation: %w", err)
	default:
		state.Record = &rev
	}
	return state, nil
}

// complianceHistory is the history of the consent in chronological order: the
// grant, the events (with their actor, reason and reference) and the expiry
// when it happened. The grant and the expiry are synthetic, with no actor.
func complianceHistory(c domain.Consent, events []domain.ConsentEvent, now time.Time) []domain.ConsentEvent {
	own := make([]domain.ConsentEvent, 0, len(events))
	for _, e := range events {
		if e.ConsentID == c.ID {
			own = append(own, e)
		}
	}
	sort.SliceStable(own, func(i, j int) bool {
		a, b := own[i], own[j]
		if !a.OccurredAt.Equal(b.OccurredAt) {
			return a.OccurredAt.Before(b.OccurredAt)
		}
		if !a.RecordedAt.Equal(b.RecordedAt) {
			return a.RecordedAt.Before(b.RecordedAt)
		}
		// The request and the revocation are written in one transaction, so
		// they can share both times: the lifecycle decides, never the random id.
		if ra, rb := lifecycleRank(a.Status), lifecycleRank(b.Status); ra != rb {
			return ra < rb
		}
		return a.EventID < b.EventID
	})
	history := make([]domain.ConsentEvent, 0, len(own)+2)
	history = append(history, domain.ConsentEvent{
		ConsentID: c.ID, Status: domain.HistoryGranted, OccurredAt: c.GrantedAt, Channel: string(c.Channel),
	})
	history = append(history, own...)
	if expires := c.ExpiresAt(); !now.Before(expires) {
		history = append(history, domain.ConsentEvent{ConsentID: c.ID, Status: domain.HistoryExpired, OccurredAt: expires})
		// An event at the same instant goes first.
		sort.SliceStable(history, func(i, j int) bool { return history[i].OccurredAt.Before(history[j].OccurredAt) })
	}
	return history
}

// hasNotConfirmed tells whether some entity of the revocation did not confirm
// and has no retry window open.
func hasNotConfirmed(rev *domain.RevocationView) bool {
	for _, e := range rev.Entities {
		if e.Status == domain.NoticeNotConfirmed {
			return true
		}
	}
	return false
}

// lifecycleRank orders the events of the whole consent that can share their
// times: the request comes before the revocation it asks for. The events of an
// entity never tie with them (they are written later, by the worker).
func lifecycleRank(status string) int {
	switch status {
	case domain.HistoryRevocationRequested:
		return 0
	case domain.HistoryRevoked:
		return 1
	}
	return 2
}

// Revoke revokes the consent on behalf of the analyst whose token has the
// subject given. An invalid request records nothing (*domain.ValidationError).
// The rest of the outcomes are the ones of RevocationService.Revoke: created is
// false when the same requestID already revoked the consent, and another
// request on a revoked consent is domain.ErrAlreadyRevoked.
func (s *ComplianceService) Revoke(ctx context.Context, consentID, actorSub, requestID string,
	req domain.ComplianceRevokeRequest) (domain.ComplianceState, bool, error) {
	if err := req.Validate(); err != nil {
		return domain.ComplianceState{}, false, err
	}
	_, created, err := s.revocations.Revoke(ctx, RevokeCommand{
		ConsentID: consentID,
		Actor:     domain.Actor{Type: domain.ActorOperations, ID: actorSub},
		Channel:   domain.RevocationChannelCompliancePortal,
		// The portal has no offline queue: the request is the server time.
		Request: domain.RevokeRequest{
			RequestedAt: s.clock.Now(), Device: req.Device(), ConfirmationMethod: string(domain.ConfirmationRoleAttestation),
		},
		Reason: req.Reason, Reference: req.NormalizedReference(), RequestID: requestID,
	})
	if err != nil {
		return domain.ComplianceState{}, false, err
	}
	state, err := s.State(ctx, consentID)
	if err != nil {
		return domain.ComplianceState{}, false, err
	}
	return state, created, nil
}

// Retry notifies again, in a new window, the entities of the revoked consent
// that did not confirm, and only them. It returns the entities retried and the
// state afterwards. Only a partial revocation with an entity that did not
// confirm is retried: a consent that is not revoked is domain.ErrConsentNotRevoked
// and one whose notice is in progress or completed, or whose entities are all
// waiting in a retry window already open (a retry keeps the revocation
// partial until the entity confirms), or that another retry just took, is
// domain.ErrNothingToRetry; both come with the state read, for the message.
func (s *ComplianceService) Retry(ctx context.Context, consentID, actorSub string) ([]string, domain.ComplianceState, error) {
	state, err := s.State(ctx, consentID)
	if err != nil {
		return nil, domain.ComplianceState{}, err
	}
	switch rev := state.View.Revocation; {
	case rev == nil:
		return nil, state, domain.ErrConsentNotRevoked
	case rev.Summary != domain.NoticePartial, !hasNotConfirmed(rev):
		return nil, state, domain.ErrNothingToRetry
	}
	retried, err := s.revocations.RetryPending(ctx, consentID, domain.Actor{Type: domain.ActorOperations, ID: actorSub})
	if err != nil {
		return nil, domain.ComplianceState{}, err
	}
	if len(retried) == 0 {
		// Another retry took the pending entities between the read and now.
		return nil, state, domain.ErrNothingToRetry
	}
	after, err := s.State(ctx, consentID)
	if err != nil {
		return nil, domain.ComplianceState{}, err
	}
	return retried, after, nil
}
