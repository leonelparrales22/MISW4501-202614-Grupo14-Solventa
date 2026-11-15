package emailadapter

import (
	"context"
	"fmt"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/email"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// CancellationNotifier implements ports.CancellationNotifier over e-mail
// (HU-W1.6-02): it tells the insured that their policy was cancelled for
// non-payment, why, and that there is no refund.
type CancellationNotifier struct {
	sender Sender
}

var _ ports.CancellationNotifier = (*CancellationNotifier)(nil)

// NewCancellationNotifier builds a CancellationNotifier.
func NewCancellationNotifier(sender Sender) *CancellationNotifier {
	return &CancellationNotifier{sender: sender}
}

// NotifyCancellation sends the e-mail with the id of the cancellation as its
// idempotency key: a retry never sends it twice.
func (n *CancellationNotifier) NotifyCancellation(ctx context.Context, in ports.CancellationNotice) error {
	return n.sender.Send(ctx, email.Message{
		To: in.Policy.Holder.Email, Subject: "Tu póliza fue cancelada por falta de pago", Body: cancellationBody(in),
		IdempotencyKey: in.Cancellation.EventID,
	})
}

func cancellationBody(in ports.CancellationNotice) string {
	i := in.Cancellation.Installment
	return fmt.Sprintf(`Hola %s,

Tu póliza %s fue cancelada el %s porque no recibimos el pago de la cuota que venció el %s (%s %s).

Intentamos cobrarla %d veces a tu medio de pago durante el periodo de gracia, que terminó el %s. Desde la cancelación la póliza no tiene cobertura.

Esta cancelación no genera reembolso.

Si quieres volver a asegurarte, solicita una nueva cotización desde el portal o habla con un asesor de Solventa.

Solventa`, in.Policy.Holder.FullName, in.Policy.ID, dateAtTime(in.Cancellation.CancelledAt), dateAtTime(i.DueAt),
		amount(domain.FromMinorUnits(i.AmountMinor)), i.Currency, i.Attempts, dateAtTime(i.GraceEndsAt))
}

// amount writes a premium with the thousands separator of Colombia.
func amount(v float64) string {
	whole := int64(v + 0.5)
	s := fmt.Sprintf("%d", whole)
	out := ""
	for len(s) > 3 {
		out = "." + s[len(s)-3:] + out
		s = s[:len(s)-3]
	}
	return "$ " + s + out
}
