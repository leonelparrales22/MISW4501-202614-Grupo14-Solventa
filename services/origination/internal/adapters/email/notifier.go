// Package emailadapter is the bridge between the shared e-mail client
// (pkg/adapters/email: timeout, circuit breaker, idempotent sends) and the
// Notifier port of the contractual issuance (HU-W1.3-03). The message is
// addressed to the insured, so its text is in Spanish.
package emailadapter

import (
	"context"
	"fmt"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/email"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// Sender sends one e-mail; *email.Client implements it.
type Sender interface {
	Send(ctx context.Context, m email.Message) error
}

// Notifier implements ports.Notifier over e-mail, the contact channel
// configured for the insured.
type Notifier struct {
	sender Sender
}

var _ ports.Notifier = (*Notifier)(nil)

// NewNotifier builds a Notifier.
func NewNotifier(sender Sender) *Notifier { return &Notifier{sender: sender} }

// Notify sends the issuance message with the secure link to the document.
func (n *Notifier) Notify(ctx context.Context, in domain.IssuedNotification) error {
	return n.sender.Send(ctx, email.Message{
		To:             in.To,
		Subject:        "Tu póliza de Solventa fue emitida",
		Body:           body(in),
		IdempotencyKey: in.IdempotencyKey,
	})
}

func body(in domain.IssuedNotification) string {
	return fmt.Sprintf(`Hola %s,

Tu póliza %s fue emitida y tu cobertura ya está activa.

Descarga tu documento contractual en este enlace seguro (válido hasta el %s):
%s

Conserva este documento: es la evidencia formal de tu seguro. También lo encontrarás siempre en tu cuenta de Solventa.

Solventa`, in.HolderName, in.PolicyID, in.LinkExpiresAt.UTC().Format(time.DateOnly), in.DocumentURL)
}
