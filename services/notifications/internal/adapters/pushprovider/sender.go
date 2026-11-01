// Package pushprovider bridges the push client of pkg/adapters/push to the
// PushSender port (HU-M3.1-01): it writes the push of each delivery in the
// language of its device and sends it with the delivery id as the key.
package pushprovider

import (
	"context"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/push"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/templates"
)

// Data keys the app reads from a push to open what it reports (criterion 4).
const (
	DataNotificationID = "notification_id"
	DataKind           = "kind"
	DataClaimID        = "claim_id"
)

// Client is what the sender needs from the push client.
type Client interface {
	Send(ctx context.Context, m push.Message) (push.Receipt, error)
}

// Sender implements ports.PushSender.
type Sender struct {
	client Client
}

// NewSender builds the sender over a push client.
func NewSender(client Client) *Sender { return &Sender{client: client} }

var _ ports.PushSender = (*Sender)(nil)

// Send delivers the push of d. The errors of the client already classify the
// outcome for pkg/idempotency.
func (s *Sender) Send(ctx context.Context, d domain.Delivery) (idempotency.Outcome, error) {
	m := push.Message{Token: d.Token, Group: domain.GroupParametricPayouts, IdempotencyKey: d.Key,
		Data: map[string]string{DataNotificationID: d.NotificationID, DataKind: d.Notification.Kind}}
	if p := d.Notification.Payout; p != nil {
		text := templates.ParametricPayout(*p, d.Locale)
		m.Title, m.Body = text.Title, text.Body
		m.Data[DataClaimID] = p.ClaimID
	}
	r, err := s.client.Send(ctx, m)
	if err != nil {
		return idempotency.Outcome{}, err
	}
	return idempotency.Outcome{Reference: r.ID}, nil
}
