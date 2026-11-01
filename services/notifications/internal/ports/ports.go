// Package ports declares what the core of the notifications service needs
// from the outside: its store, the push provider and a clock (HU-M3.1-01).
package ports

import (
	"context"
	"errors"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/domain"
)

// ErrNotificationNotFound is returned for a notification that does not exist
// or belongs to another insured: both look the same.
var ErrNotificationNotFound = errors.New("notification not found")

// Clock tells the time.
type Clock interface {
	Now() time.Time
}

// NotificationStore keeps the notification center and the devices of every
// insured.
type NotificationStore interface {
	// RecordPayout stores the notification of a payout and, in the same
	// transaction, one pending delivery per reachable device of the insured.
	// It is idempotent by notification id: when the notification already
	// exists it changes nothing and returns created=false with the deliveries
	// it had.
	RecordPayout(ctx context.Context, n domain.Notification, now time.Time) (created bool, deliveries []string, err error)
	// List returns the notifications of the subject, the newest first, at
	// most limit, and how many of all of them are unread.
	List(ctx context.Context, subject string, limit int) (items []domain.Notification, unread int, err error)
	// Get returns ErrNotificationNotFound when the notification is not the
	// subject's.
	Get(ctx context.Context, subject, id string) (domain.Notification, error)
	// MarkRead marks one notification read at now; reading it again keeps the
	// first time. ErrNotificationNotFound when it is not the subject's.
	MarkRead(ctx context.Context, subject, id string, now time.Time) error
	// MarkAllRead marks every unread notification of the subject read at now.
	MarkAllRead(ctx context.Context, subject string, now time.Time) error
	// SaveDevice creates or replaces the device of the subject.
	SaveDevice(ctx context.Context, d domain.Device) error
}

// DeliveryStore keeps the pushes to send, with the claim and retry protocol
// of pkg/idempotency.
type DeliveryStore interface {
	idempotency.Store[domain.Delivery]
	// Due returns the ids of the deliveries due at now, at most limit.
	Due(ctx context.Context, now time.Time, limit int) ([]string, error)
}

// PushSender delivers one push. It classifies its errors with
// pkg/idempotency: rejected, ambiguous or not sent.
type PushSender interface {
	Send(ctx context.Context, d domain.Delivery) (idempotency.Outcome, error)
}
