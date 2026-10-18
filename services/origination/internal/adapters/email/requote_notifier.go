package emailadapter

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/email"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// Defaults of the retries of the requote notice (HU-W1.2-03).
const (
	DefaultRequoteRetryBase   = time.Second
	DefaultRequoteRetryMax    = 30 * time.Second
	DefaultRequoteMaxAttempts = 6
)

// OfferPath is the path, under OFFER_LINK_BASE_URL, of the screen of an offer
// in the embedded flow.
const OfferPath = "/embebido/oferta/"

// RequoteNotifier implements ports.RequoteNotifier over e-mail (HU-W1.2-03
// CA-4): it tells the customer that the provisional offer was replaced by a
// normal one, with both premiums, the new validity and a link to the new
// offer. A failed send is retried with a growing wait and the same idempotency
// key, so a retry never duplicates the message. The retries live in the
// process: if it stops meanwhile the notice is lost, and the customer finds
// the replacement when they return to the flow.
type RequoteNotifier struct {
	sender      Sender
	linkBaseURL string
	retryBase   time.Duration
	retryMax    time.Duration
	maxAttempts int
}

var _ ports.RequoteNotifier = (*RequoteNotifier)(nil)

// RequoteOption tunes a RequoteNotifier.
type RequoteOption func(*RequoteNotifier)

// WithRequoteRetries sets the wait after the first failed send (it doubles up
// to max) and the number of sends before giving up.
func WithRequoteRetries(base, max time.Duration, attempts int) RequoteOption {
	return func(n *RequoteNotifier) {
		if base > 0 {
			n.retryBase = base
		}
		if max >= n.retryBase {
			n.retryMax = max
		}
		if attempts > 0 {
			n.maxAttempts = attempts
		}
	}
}

// NewRequoteNotifier builds a RequoteNotifier. linkBaseURL is
// OFFER_LINK_BASE_URL, the address of the front end.
func NewRequoteNotifier(sender Sender, linkBaseURL string, opts ...RequoteOption) *RequoteNotifier {
	n := &RequoteNotifier{
		sender: sender, linkBaseURL: strings.TrimRight(linkBaseURL, "/"),
		retryBase: DefaultRequoteRetryBase, retryMax: DefaultRequoteRetryMax, maxAttempts: DefaultRequoteMaxAttempts,
	}
	for _, opt := range opts {
		opt(n)
	}
	return n
}

// Notify sends the notice, retrying a provider failure. A message the provider
// can never accept (email.ErrInvalidMessage) is not retried.
func (n *RequoteNotifier) Notify(ctx context.Context, in domain.RequoteNotice) error {
	msg := email.Message{
		To:             in.To,
		Subject:        "Tu oferta de seguro se actualizó",
		Body:           n.body(in),
		IdempotencyKey: in.IdempotencyKey,
	}
	wait := n.retryBase
	var err error
	for attempt := 1; attempt <= n.maxAttempts; attempt++ {
		if err = n.sender.Send(ctx, msg); err == nil || errors.Is(err, email.ErrInvalidMessage) {
			return err
		}
		if attempt == n.maxAttempts {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
		wait = min(wait*2, n.retryMax)
	}
	return fmt.Errorf("the requote notice was not sent after %d attempts: %w", n.maxAttempts, err)
}

func (n *RequoteNotifier) body(in domain.RequoteNotice) string {
	return fmt.Sprintf(`Hola %s,

La fuente de datos financieros ya está disponible y recalculamos tu oferta con tu perfil real. Tu oferta anterior era provisional.

Prima mensual anterior: %s
Prima mensual nueva: %s
Vigencia de la nueva oferta: hasta el %s (24 horas)

Revisa y acepta la nueva oferta en este enlace: %s%s%s

La oferta anterior ya no se puede aceptar.

Solventa`, in.CustomerName, money(in.Currency, in.OldPremium), money(in.Currency, in.NewPremium),
		dateAtTime(in.ExpiresAt), n.linkBaseURL, OfferPath, in.NewOfferID)
}
