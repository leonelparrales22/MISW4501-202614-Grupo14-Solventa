package emailadapter

import (
	"context"
	"fmt"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/email"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// RenewalNotifier implements ports.RenewalNotifier over e-mail (HU-W1.5-02):
// it tells the insured that their policy was not renewed and their coverage
// ended, and whether a grace period applies.
type RenewalNotifier struct {
	sender Sender
}

var _ ports.RenewalNotifier = (*RenewalNotifier)(nil)

// NewRenewalNotifier builds a RenewalNotifier.
func NewRenewalNotifier(sender Sender) *RenewalNotifier { return &RenewalNotifier{sender: sender} }

// Notify sends the message with its idempotency key. A provider failure is
// returned as is, so the caller retries with the same key.
func (n *RenewalNotifier) Notify(ctx context.Context, in domain.RenewalNotice) error {
	return n.sender.Send(ctx, email.Message{
		To: in.To, Subject: "Tu póliza no fue renovada y tu cobertura finalizó", Body: renewalBody(in),
		IdempotencyKey: in.IdempotencyKey,
	})
}

func renewalBody(in domain.RenewalNotice) string {
	why := "No recibimos tu respuesta antes de que se cerrara la ventana de renovación."
	if in.Reason == domain.NotRenewedPaymentFailed {
		why = "El pago de la renovación no se completó dentro del plazo."
	}
	grace := "No aplica ningún período de gracia: tu cobertura ya no está activa."
	if in.GraceDays > 0 {
		grace = fmt.Sprintf("Tienes un período de gracia de %d días para solicitar una nueva cotización; durante ese tiempo no hay cobertura vigente.", in.GraceDays)
	}
	return fmt.Sprintf(`Hola %s,

Tu póliza %s no fue renovada y su cobertura finalizó el %s.

%s
%s

Si aún quieres estar asegurado, genera una nueva cotización desde el portal o habla con un asesor de Solventa.

Solventa`, in.HolderName, in.PolicyID, dateAtTime(in.CoverageEnded), why, grace)
}
