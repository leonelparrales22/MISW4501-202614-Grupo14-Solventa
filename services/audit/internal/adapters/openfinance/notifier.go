// Package openfinance bridges pkg/adapters/openfinance to the ports of audit:
// it tells the Open Finance ecosystem that a consent was revoked
// (HU-M1.3-02). audit reaches the provider over HTTP, never by importing a
// stub.
package openfinance

import (
	"context"

	ofclient "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/openfinance"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/ports"
)

// Revoker is what the notifier needs from the Open Finance client.
type Revoker interface {
	RevokeConsent(ctx context.Context, n ofclient.RevocationNotice, key string) (ofclient.RevocationConfirmation, error)
}

// Notifier implements ports.EntityNotifier with the Open Finance client.
type Notifier struct {
	client Revoker
}

// NewNotifier builds the notifier over a client.
func NewNotifier(client Revoker) *Notifier { return &Notifier{client: client} }

var _ ports.EntityNotifier = (*Notifier)(nil)

// Notify sends the notice with its idempotency key. It sends only the
// consent, the entity, the customer of the partner and the effective time
// (data minimization, services/audit/README.md): never the document nor the
// name of the holder. The client's errors keep the classification of
// pkg/idempotency.
func (n *Notifier) Notify(ctx context.Context, rec ports.NoticeRecord) (idempotency.Outcome, error) {
	conf, err := n.client.RevokeConsent(ctx, ofclient.RevocationNotice{
		ConsentID: rec.ConsentID, EntityCode: rec.EntityCode, CustomerID: rec.CustomerID, EffectiveAt: rec.EffectiveAt,
	}, rec.Key)
	if err != nil {
		return idempotency.Outcome{}, err
	}
	return idempotency.Outcome{Reference: conf.Reference}, nil
}
