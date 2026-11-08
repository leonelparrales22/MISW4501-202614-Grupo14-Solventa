package emailadapter

import (
	"context"
	"fmt"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/email"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// RenewalConsentNotifier implements ports.ConsentCodeSender over e-mail for
// the assisted renewal (HU-W1.5-03, decision D-9 a): it sends the insured the
// code that proves they agree to the renewal an advisor formalizes on their
// behalf. It is the ConsentNotifier of HU-W1.1-03 with the text of a renewal.
// The message is addressed to the customer, so its text is in Spanish.
type RenewalConsentNotifier struct {
	sender Sender
}

var _ ports.ConsentCodeSender = (*RenewalConsentNotifier)(nil)

// NewRenewalConsentNotifier builds a RenewalConsentNotifier.
func NewRenewalConsentNotifier(sender Sender) *RenewalConsentNotifier {
	return &RenewalConsentNotifier{sender: sender}
}

// SendConsentCode sends the code with its idempotency key. A provider failure
// is returned as is, so the advisor can ask for the code again.
func (n *RenewalConsentNotifier) SendConsentCode(ctx context.Context, in domain.ConsentCodeNotice) error {
	return n.sender.Send(ctx, email.Message{
		To:             in.To,
		Subject:        "Código para confirmar la renovación de tu póliza de Solventa",
		Body:           renewalConsentBody(in),
		IdempotencyKey: in.IdempotencyKey,
	})
}

func renewalConsentBody(in domain.ConsentCodeNotice) string {
	return fmt.Sprintf(`Hola %s,

Un asesor de Solventa va a renovar tu póliza en tu nombre. Para confirmar que estás de acuerdo, dale este código al asesor:

%s

El código vale hasta el %s. Si no pediste renovar tu póliza, no compartas el código con nadie.

Solventa`, in.CustomerName, in.Code, dateAtTime(in.ExpiresAt))
}
