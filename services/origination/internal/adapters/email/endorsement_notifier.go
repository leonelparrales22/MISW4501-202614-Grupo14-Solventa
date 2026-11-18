package emailadapter

import (
	"context"
	"errors"
	"fmt"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/email"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// ErrUnknownNoticeOutcome is returned for a notice of an outcome the e-mail
// does not know.
var ErrUnknownNoticeOutcome = errors.New("only applied, rejected and returned requests are notified")

// EndorsementNotifier implements ports.EndorsementOutcomeNotifier over e-mail
// (HU-W1.4-02, decision D-12 a): it tells the insured what the analyst decided
// on their change request, the promise of "Te avisaremos por correo" of the
// screens of HU-W1.4-01. The observations of a rejection or a return are
// written for the insured, so they travel; the messages carry no link and
// none of the data the request changes.
type EndorsementNotifier struct {
	sender Sender
}

var _ ports.EndorsementOutcomeNotifier = (*EndorsementNotifier)(nil)

// NewEndorsementNotifier builds an EndorsementNotifier.
func NewEndorsementNotifier(sender Sender) *EndorsementNotifier {
	return &EndorsementNotifier{sender: sender}
}

// NotifyEndorsementOutcome sends the message of the decision with the key
// endorsement-outcome:<request_id>. A provider failure is returned as is, so
// the caller retries with the same key.
func (n *EndorsementNotifier) NotifyEndorsementOutcome(ctx context.Context, in domain.EndorsementNotice) error {
	var subject, body string
	what := changeTypeText(in.ChangeType)
	switch in.Outcome {
	case domain.NoticeApplied:
		subject = "Aplicamos el cambio de tu póliza"
		body = fmt.Sprintf(`Hola %s,

Tu solicitud %s de cambio de %s en la póliza %s fue aplicada el %s con el endoso %s.

Ya puedes ver la póliza actualizada en el portal y en la app de Solventa.

Solventa`, in.Name, in.RequestID, what, in.PolicyID, dateAtTime(in.DecidedAt), in.EndorsementID)
	case domain.NoticeRejected:
		subject = "Tu solicitud de cambio no fue aprobada"
		body = fmt.Sprintf(`Hola %s,

Revisamos tu solicitud %s de cambio de %s en la póliza %s y el %s no fue aprobada.

Motivo: %s

Tu póliza conserva sus condiciones actuales. Puedes ver el estado de la solicitud en el portal de Solventa.

Solventa`, in.Name, in.RequestID, what, in.PolicyID, dateAtTime(in.DecidedAt), in.Observations)
	case domain.NoticeReturned:
		subject = "Tu solicitud de cambio necesita ajustes"
		body = fmt.Sprintf(`Hola %s,

Revisamos tu solicitud %s de cambio de %s en la póliza %s y el %s no se pudo aplicar.

Qué ajustar: %s

Tu póliza conserva sus condiciones actuales. Puedes radicar una nueva solicitud con los datos corregidos desde el portal de Solventa.

Solventa`, in.Name, in.RequestID, what, in.PolicyID, dateAtTime(in.DecidedAt), in.Observations)
	default:
		return fmt.Errorf("%w: %s", ErrUnknownNoticeOutcome, in.Outcome)
	}
	return n.sender.Send(ctx, email.Message{To: in.To, Subject: subject, Body: body,
		IdempotencyKey: domain.EndorsementOutcomeIdempotencyKey(in.RequestID)})
}

func changeTypeText(t domain.ChangeType) string {
	switch t {
	case domain.ChangeBeneficiaries:
		return "beneficiarios"
	case domain.ChangeInsuredAmount:
		return "suma asegurada"
	default:
		return "datos personales"
	}
}
