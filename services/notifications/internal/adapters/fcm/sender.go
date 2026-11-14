// Package fcm bridges the Firebase Cloud Messaging client of pkg/adapters/fcm
// to the PushSender port (HU-M3.3-01): every push is a data message with high
// priority, so it wakes the app even when it is closed, and the app writes and
// shows the notification itself, in its language and its channels.
package fcm

import (
	"context"

	pkgfcm "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/fcm"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/ports"
)

// Data keys the app reads from the message (the same of the simulator).
const (
	DataNotificationID = "notification_id"
	DataKind           = "kind"
	DataClaimID        = "claim_id"
	// DataPolicyID is the policy an expiry alert opens (HU-M3.2-01).
	DataPolicyID = "policy_id"
)

// Client is what the sender needs from the FCM client.
type Client interface {
	Send(ctx context.Context, m pkgfcm.Message) (pkgfcm.Receipt, error)
}

// Sender implements ports.PushSender over FCM.
type Sender struct {
	client Client
}

// NewSender builds the sender over an FCM client.
func NewSender(client Client) *Sender { return &Sender{client: client} }

var _ ports.PushSender = (*Sender)(nil)

// Send delivers the push of d. The text is not sent: the app writes it from
// the center. The claim goes with a change of stage, so the app can open its
// timeline, and with a payout, as the simulator did; the policy goes with an
// expiry alert (HU-M3.2-01), so the app can open it. The errors of the client
// already classify the outcome for pkg/idempotency.
func (s *Sender) Send(ctx context.Context, d domain.Delivery) (idempotency.Outcome, error) {
	data := map[string]string{DataNotificationID: d.NotificationID, DataKind: d.Notification.Kind}
	if p := d.Notification.Payout; p != nil {
		data[DataClaimID] = p.ClaimID
	}
	if c := d.Notification.Stage; c != nil {
		data[DataClaimID] = c.ClaimID
	}
	if e := d.Notification.Expiry; e != nil {
		data[DataPolicyID] = e.PolicyID
	}
	r, err := s.client.Send(ctx, pkgfcm.Message{Token: d.Token, Data: data, CollapseKey: d.NotificationID})
	if err != nil {
		return idempotency.Outcome{}, err
	}
	return idempotency.Outcome{Reference: r.ID}, nil
}
