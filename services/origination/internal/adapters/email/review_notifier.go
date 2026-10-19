package emailadapter

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/email"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// ErrUnsupportedOutcome is returned for a notice that is not an adjust or a
// reject: an accepted request is told by the issuance e-mail (HU-W1.3-03).
var ErrUnsupportedOutcome = errors.New("only adjust and reject decisions are notified")

// ReviewNotifier implements ports.ReviewOutcomeNotifier over e-mail
// (HU-W1.1-02, decision D-3 c): it tells the customer that an advisor adjusted
// their request, with the new offer, or did not accept the risk. The messages
// carry no link and no advisor observations, which are an internal record.
type ReviewNotifier struct {
	sender Sender
}

var _ ports.ReviewOutcomeNotifier = (*ReviewNotifier)(nil)

// NewReviewNotifier builds a ReviewNotifier.
func NewReviewNotifier(sender Sender) *ReviewNotifier { return &ReviewNotifier{sender: sender} }

// NotifyReviewOutcome sends the message of the decision with its idempotency
// key. A provider failure is returned as is (email.ErrUnavailable), so the
// caller retries with the same key.
func (n *ReviewNotifier) NotifyReviewOutcome(ctx context.Context, in domain.ReviewOutcomeNotice) error {
	var subject, body string
	switch {
	case in.Outcome == domain.DecisionAdjust && in.AdjustedOffer != nil:
		subject, body = "Revisamos tu solicitud de seguro: tienes una nueva oferta", adjustedBody(in)
	case in.Outcome == domain.DecisionReject:
		subject, body = "Tu solicitud de seguro no fue aprobada", rejectedBody(in)
	default:
		return fmt.Errorf("%w: %s", ErrUnsupportedOutcome, in.Outcome)
	}
	return n.sender.Send(ctx, email.Message{To: in.To, Subject: subject, Body: body, IdempotencyKey: in.IdempotencyKey})
}

func adjustedBody(in domain.ReviewOutcomeNotice) string {
	o := in.AdjustedOffer
	return fmt.Sprintf(`Hola %s,

Un asesor de Solventa revisó tu solicitud %s el %s y ajustó la prima.

Nueva prima mensual: %s
Vigencia de la nueva oferta: hasta el %s (24 horas)

Tu aceptación anterior no se cobra. Si estás de acuerdo con las nuevas condiciones, revisa y acepta la nueva oferta desde el mismo flujo de aceptación, en el sitio de tu banco. Solo se cobrará la prima cuando aceptes la nueva oferta.

Solventa`, in.CustomerName, in.AcceptanceID, dateAtTime(in.DecidedAt), money(o.Currency, o.Premium), dateAtTime(o.ExpiresAt))
}

func rejectedBody(in domain.ReviewOutcomeNotice) string {
	return fmt.Sprintf(`Hola %s,

Un asesor de Solventa revisó tu solicitud %s el %s y el riesgo no fue aceptado.

No se realizó ningún cobro. Puedes continuar con tu crédito y el banco te indicará los siguientes pasos. Si tienes preguntas, comunícate con tu banco o con Solventa.

Solventa`, in.CustomerName, in.AcceptanceID, dateAtTime(in.DecidedAt))
}

// dateAtTime formats a moment as "16/10/2026 a las 15:20 (UTC)".
func dateAtTime(t time.Time) string {
	return t.UTC().Format("02/01/2006") + " a las " + t.UTC().Format("15:04") + " (UTC)"
}

// money formats 98450 as "$ 98.450 COP" and 96212.5 as "$ 96.212,50 COP",
// the format of the embedded screens.
func money(currency string, amount float64) string {
	cents := int64(math.Round(amount * 100))
	digits := strconv.FormatInt(cents/100, 10)
	var b strings.Builder
	for i, r := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(r)
	}
	if rest := cents % 100; rest != 0 {
		fmt.Fprintf(&b, ",%02d", rest)
	}
	return "$ " + b.String() + " " + currency
}
