package emailadapter

import (
	"context"
	"fmt"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/email"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// ConsentNotifier implements ports.ConsentCodeSender over e-mail
// (HU-W1.1-03 CA-5): it sends the customer the code that proves they agree to
// the purchase an advisor formalizes on their behalf. The message is addressed
// to the customer, so its text is in Spanish.
type ConsentNotifier struct {
	sender Sender
}

var _ ports.ConsentCodeSender = (*ConsentNotifier)(nil)

// NewConsentNotifier builds a ConsentNotifier.
func NewConsentNotifier(sender Sender) *ConsentNotifier { return &ConsentNotifier{sender: sender} }

// SendConsentCode sends the code with its idempotency key. A provider failure
// is returned as is, so the advisor can ask for the code again.
func (n *ConsentNotifier) SendConsentCode(ctx context.Context, in domain.ConsentCodeNotice) error {
	return n.sender.Send(ctx, email.Message{
		To:             in.To,
		Subject:        "Código para confirmar tu seguro de Solventa",
		Body:           consentBody(in),
		IdempotencyKey: in.IdempotencyKey,
	})
}

func consentBody(in domain.ConsentCodeNotice) string {
	return fmt.Sprintf(`Hola %s,

Un asesor de Solventa va a contratar un seguro en tu nombre. Para confirmar que estás de acuerdo, dale este código al asesor:

%s

El código vale hasta el %s. Si no pediste este seguro, no compartas el código con nadie.

Solventa`, in.CustomerName, in.Code, dateAtTime(in.ExpiresAt))
}
