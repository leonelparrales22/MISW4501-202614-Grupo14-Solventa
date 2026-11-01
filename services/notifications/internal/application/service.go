// Package application holds the use cases of the notification center
// (HU-M3.1-01): recording the notification of an automatic payment, listing
// and reading the notifications of the insured, and registering their
// devices.
package application

import (
	"context"
	"errors"
	"log/slog"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/ports"
)

// DefaultListLimit and MaxListLimit bound the notifications of one list.
const (
	DefaultListLimit = 50
	MaxListLimit     = 100
)

// Service is the use case behind the event consumer and the human routes.
type Service struct {
	store ports.NotificationStore
	clock ports.Clock
	wake  func()
}

// NewService builds the service. wake, when set, is called after a payout
// creates deliveries, so the push worker sends them without waiting for its
// next round.
func NewService(store ports.NotificationStore, clock ports.Clock, wake func()) (*Service, error) {
	if store == nil || clock == nil {
		return nil, errors.New("application: a store and a clock are required")
	}
	if wake == nil {
		wake = func() {}
	}
	return &Service{store: store, clock: clock, wake: wake}, nil
}

// RecordPayout records the notification of an automatic payment and the push
// to every device of the insured that accepts it. The notification is stored
// even when there is no such device (criterion 5). A redelivered event finds
// the notification it created and changes nothing.
func (s *Service) RecordPayout(ctx context.Context, p domain.ParametricPayout) error {
	if err := p.Validate(); err != nil {
		return err
	}
	now := s.clock.Now().UTC()
	n := domain.NewPayoutNotification(p, now)
	created, deliveries, err := s.store.RecordPayout(ctx, n, now)
	if err != nil {
		return err
	}
	if !created {
		slog.InfoContext(ctx, "parametric payout already notified", "notification_id", n.ID, "payout_id", p.PayoutID)
		return nil
	}
	slog.InfoContext(ctx, "parametric payout notification recorded", "notification_id", n.ID, "payout_id", p.PayoutID,
		"claim_id", p.ClaimID, "deliveries", len(deliveries), "received_after_ms", now.Sub(p.PaidAt).Milliseconds())
	if len(deliveries) > 0 {
		s.wake()
	}
	return nil
}

// List returns the notifications of the caller, the newest first, and how
// many are unread. A limit outside 1..MaxListLimit takes DefaultListLimit.
func (s *Service) List(ctx context.Context, limit int) ([]domain.Notification, int, error) {
	subject, err := caller(ctx)
	if err != nil {
		return nil, 0, err
	}
	if limit <= 0 || limit > MaxListLimit {
		limit = DefaultListLimit
	}
	return s.store.List(ctx, subject, limit)
}

// Get returns one notification of the caller.
func (s *Service) Get(ctx context.Context, id string) (domain.Notification, error) {
	subject, err := caller(ctx)
	if err != nil {
		return domain.Notification{}, err
	}
	return s.store.Get(ctx, subject, id)
}

// MarkRead marks one notification of the caller read.
func (s *Service) MarkRead(ctx context.Context, id string) error {
	subject, err := caller(ctx)
	if err != nil {
		return err
	}
	return s.store.MarkRead(ctx, subject, id, s.clock.Now().UTC())
}

// MarkAllRead marks every notification of the caller read («Marcar todas como
// leídas», M15).
func (s *Service) MarkAllRead(ctx context.Context) error {
	subject, err := caller(ctx)
	if err != nil {
		return err
	}
	return s.store.MarkAllRead(ctx, subject, s.clock.Now().UTC())
}

// RegisterDevice creates or replaces a device of the caller: its token, its
// language and whether the user granted the notification permission.
func (s *Service) RegisterDevice(ctx context.Context, d domain.Device) (domain.Device, error) {
	subject, err := caller(ctx)
	if err != nil {
		return domain.Device{}, err
	}
	d.Subject = subject
	d.UpdatedAt = s.clock.Now().UTC()
	if !d.PushEnabled {
		// Without the permission the token is of no use: it is not kept.
		d.Token = ""
	}
	if err := d.Validate(); err != nil {
		return domain.Device{}, err
	}
	if err := s.store.SaveDevice(ctx, d); err != nil {
		return domain.Device{}, err
	}
	return d, nil
}

func caller(ctx context.Context) (string, error) {
	claims, ok := security.ClaimsFromContext(ctx)
	if !ok || claims.Subject == "" {
		return "", security.ErrUnauthenticated
	}
	return claims.Subject, nil
}
