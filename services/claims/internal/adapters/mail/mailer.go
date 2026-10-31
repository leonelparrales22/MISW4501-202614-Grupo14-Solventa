// Package mail delivers the e-mails of the claims service through the shared
// client of the e-mail provider (pkg/adapters/email).
package mail

import (
	"context"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/email"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// Sender is the part of the e-mail client this adapter uses.
type Sender interface {
	Send(ctx context.Context, m email.Message) error
}

// Mailer is the ports.Mailer over the e-mail provider.
type Mailer struct {
	sender Sender
}

var _ ports.Mailer = (*Mailer)(nil)

// NewMailer builds the adapter.
func NewMailer(sender Sender) *Mailer { return &Mailer{sender: sender} }

// Send delivers the message once per key.
func (m *Mailer) Send(ctx context.Context, to, subject, body, key string) error {
	return m.sender.Send(ctx, email.Message{To: to, Subject: subject, Body: body, IdempotencyKey: key})
}
