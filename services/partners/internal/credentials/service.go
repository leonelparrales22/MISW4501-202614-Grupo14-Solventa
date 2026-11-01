package credentials

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/events"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/partners/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/partners/internal/ports"
)

// DefaultGracePeriodHours is the transition period of a rotation when the
// request does not set one (CREDENTIAL_GRACE_PERIOD_HOURS).
const DefaultGracePeriodHours = 24

// Service rotates the credentials of the partners and revokes the previous
// one when its transition period ends (HU-W3.1-01 CA-4, CA-5).
type Service struct {
	store        ports.PartnerStore
	issuer       *Issuer
	clock        ports.Clock
	defaultGrace int
}

// NewService builds the service. defaultGrace is in hours (1–168).
func NewService(store ports.PartnerStore, issuer *Issuer, clock ports.Clock, defaultGrace int) (*Service, error) {
	if !domain.ValidGracePeriod(defaultGrace) {
		return nil, fmt.Errorf("the default grace period must be between %d and %d hours, got %d",
			domain.MinGracePeriodHours, domain.MaxGracePeriodHours, defaultGrace)
	}
	return &Service{store: store, issuer: issuer, clock: clock, defaultGrace: defaultGrace}, nil
}

// DefaultGracePeriodHours returns the configured transition period.
func (s *Service) DefaultGracePeriodHours() int { return s.defaultGrace }

// Rotate issues a new credential in the same usage plan and leaves the active
// one valid until the transition period ends. graceHours 0 is the default.
func (s *Service) Rotate(ctx context.Context, partnerID string, graceHours int, actor domain.Actor) (IssuedSecret, error) {
	if graceHours == 0 {
		graceHours = s.defaultGrace
	}
	if !domain.ValidGracePeriod(graceHours) {
		return IssuedSecret{}, &domain.ValidationError{Fields: []domain.FieldError{{Field: "grace_period_hours", Code: domain.CodeOutOfRange}}}
	}
	var (
		secret  IssuedSecret
		created domain.Credential
	)
	err := s.store.Mutate(ctx, partnerID, func(rec ports.PartnerRecord) (ports.Change, error) {
		p := rec.Partner
		if p.Status != domain.StatusActive || p.UsagePlanID == "" {
			return ports.Change{}, domain.ErrNotActive
		}
		var current *domain.Credential
		for i := range rec.Credentials {
			switch rec.Credentials[i].Status {
			case domain.CredentialRetiring:
				return ports.Change{}, domain.ErrRotationPending
			case domain.CredentialActive:
				current = &rec.Credentials[i]
			}
		}
		now := s.clock.Now().UTC()
		c, sec, err := s.issuer.Issue(ctx, p, p.UsagePlanID, actor, domain.ReasonRotation, now)
		if err != nil {
			return ports.Change{}, err
		}
		created, secret = c, sec
		change := ports.Change{NewCredentials: []domain.Credential{c}}
		details := map[string]any{"client_id": c.ClientID, "grace_period_hours": graceHours}
		var previous *domain.Credential
		if current != nil {
			prev := *current
			expires := now.Add(time.Duration(graceHours) * time.Hour)
			prev.Status, prev.ExpiresAt = domain.CredentialRetiring, &expires
			change.UpdatedCredentials = append(change.UpdatedCredentials, prev)
			details["previous_credential_id"] = prev.ID
			details["previous_client_id"] = prev.ClientID
			details["previous_expires_at"] = expires
			previous = &prev
		}
		entryID, err := s.issuer.Generator().ID("pev-")
		if err != nil {
			return ports.Change{}, err
		}
		change.History = []domain.HistoryEntry{{
			ID: entryID, PartnerID: p.ID, Type: domain.HistoryCredentialsRotated, OccurredAt: now, Actor: actor, Details: details,
		}}
		ev, err := domain.CredentialEvent(domain.EventCredentialsRotated, p, c, actor, now, previous, graceHours)
		if err != nil {
			return ports.Change{}, err
		}
		change.Events = []events.Envelope{ev}
		return change, nil
	})
	if err != nil {
		if created.GatewayKeyID != "" {
			// The key exists in the gateway but was not recorded.
			s.issuer.Discard(ctx, "", created.GatewayKeyID)
		}
		return IssuedSecret{}, err
	}
	slog.InfoContext(ctx, "partner credentials rotated", "partner_id", partnerID, "credential_id", created.ID,
		"grace_period_hours", graceHours)
	return secret, nil
}

// RetireDue revokes the retiring credentials whose transition period ended:
// it removes their API keys from the gateway and records the revocation. It
// returns how many partners it processed.
func (s *Service) RetireDue(ctx context.Context, limit int) (int, error) {
	ids, err := s.store.DueRetirements(ctx, s.clock.Now().UTC(), limit)
	if err != nil {
		return 0, fmt.Errorf("find the credentials to revoke: %w", err)
	}
	done := 0
	for _, id := range ids {
		if err := s.retire(ctx, id); err != nil {
			slog.WarnContext(ctx, "could not revoke a credential at the end of its transition period; it is retried",
				"partner_id", id, "error", err)
			continue
		}
		done++
	}
	return done, nil
}

func (s *Service) retire(ctx context.Context, partnerID string) error {
	return s.store.Mutate(ctx, partnerID, func(rec ports.PartnerRecord) (ports.Change, error) {
		now := s.clock.Now().UTC()
		var change ports.Change
		for _, c := range rec.Credentials {
			if c.Status != domain.CredentialRetiring || c.ExpiresAt == nil || c.ExpiresAt.After(now) {
				continue
			}
			if err := s.issuer.Retire(ctx, rec.Partner.UsagePlanID, c.GatewayKeyID); err != nil {
				return ports.Change{}, err
			}
			revoked := c
			revoked.Status, revoked.RevokedAt = domain.CredentialRevoked, &now
			change.UpdatedCredentials = append(change.UpdatedCredentials, revoked)
			entryID, err := s.issuer.Generator().ID("pev-")
			if err != nil {
				return ports.Change{}, err
			}
			change.History = append(change.History, domain.HistoryEntry{
				ID: entryID, PartnerID: rec.Partner.ID, Type: domain.HistoryCredentialsRevoked, OccurredAt: now,
				Actor: domain.SystemActor, Details: map[string]any{"client_id": c.ClientID, "reason": domain.ReasonGracePeriodEnded},
			})
			ev, err := domain.CredentialEvent(domain.EventCredentialsRevoked, rec.Partner, revoked, domain.SystemActor, now, nil, 0)
			if err != nil {
				return ports.Change{}, err
			}
			change.Events = append(change.Events, ev)
			slog.InfoContext(ctx, "partner credential revoked at the end of its transition period",
				"partner_id", rec.Partner.ID, "credential_id", c.ID)
		}
		return change, nil
	})
}

// Worker revokes the due credentials periodically.
type Worker struct {
	svc      *Service
	interval time.Duration
	batch    int
}

// NewWorker builds the worker.
func NewWorker(svc *Service, interval time.Duration) (*Worker, error) {
	if interval <= 0 {
		return nil, errors.New("the revocation poll interval must be positive")
	}
	return &Worker{svc: svc, interval: interval, batch: 20}, nil
}

// Run polls until ctx ends.
func (w *Worker) Run(ctx context.Context) {
	t := time.NewTicker(w.interval)
	defer t.Stop()
	for {
		if _, err := w.svc.RetireDue(ctx, w.batch); err != nil && ctx.Err() == nil {
			slog.WarnContext(ctx, "the credential revocation worker failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
