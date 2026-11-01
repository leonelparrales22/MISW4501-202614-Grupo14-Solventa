package domain

import (
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
)

// DeliveryWindow is how long the service keeps retrying a push that the
// provider does not accept (criterion 7). The notification stays in the
// center whatever happens to its push.
const DeliveryWindow = 24 * time.Hour

// Delivery is the push of one notification to one device, retried with the
// same idempotency key until the provider accepts it or its window ends.
type Delivery struct {
	idempotency.Attempt
	NotificationID string
	InstallationID string
	// Token and Locale are the device's when the delivery was created.
	Token  string
	Locale string
	// Reference is the provider's id once it accepted the message.
	Reference string
	SentAt    *time.Time
	LastError string
	// Notification is what the push tells; the store loads it with the claim.
	Notification Notification
}

// AttemptState implements idempotency.Record.
func (d Delivery) AttemptState() idempotency.Attempt { return d.Attempt }

// DeliveryIDFor is the id of the delivery of a notification to a device. It
// is also the idempotency key sent to the provider.
func DeliveryIDFor(notificationID, installationID string) string {
	return notificationID + ":" + installationID
}

// NewDelivery builds the pending delivery of n to d, due now and retried
// until now+DeliveryWindow.
func NewDelivery(n Notification, d Device, now time.Time) Delivery {
	id := DeliveryIDFor(n.ID, d.InstallationID)
	return Delivery{
		Attempt: idempotency.Attempt{
			ID: id, Key: id, Status: idempotency.StatusPending, NextAttemptAt: now.UTC(),
			WindowEndsAt: now.UTC().Add(DeliveryWindow),
		},
		NotificationID: n.ID, InstallationID: d.InstallationID, Token: d.Token, Locale: d.Locale, Notification: n,
	}
}
