package renewal

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// maxRenewalChain bounds how many renewals back the origin of a policy is
// looked for, so a malformed chain cannot loop.
const maxRenewalChain = 50

// Origin is where a policy came from (HU-W1.5-03, decision D-6): the customer
// its risk profile and its consent are keyed by, the birth date its renewal
// is priced with and the channel its original offer was sold in.
type Origin struct {
	// CustomerID keys the cached profile and the consent in audit: the id the
	// partner or the advisor gave the customer when the offer was quoted.
	// Empty when no customer could be found for the policy.
	CustomerID string
	// BirthDate is zero when unknown; then the renewal is not re-priced.
	BirthDate time.Time
	// Channel is the channel of the original offer, empty when it is unknown
	// (the policy was found through the fallbacks).
	Channel domain.OfferChannel
	// OfferID is the original offer, empty when it is unknown.
	OfferID string
}

// OriginFinder follows a policy back to the offer it was bought from: policy
// → its charge → the offer of the charge, through the policies it renews when
// it is a renewal itself. When the offer has no stored row (a demonstration
// offer) the customer is looked for by the subject of the insured and then
// by the document of the holder, without a channel.
type OriginFinder struct {
	Policies   ports.PolicyRepository
	Charges    PaymentMethodHistory
	Offers     ports.OfferReader
	BySubject  ports.CustomerBySubject
	ByDocument ports.CustomerByDocument
}

// Find returns the origin of the policy. Only infrastructure failures are
// errors: a policy whose customer cannot be found has an empty Origin.
func (f OriginFinder) Find(ctx context.Context, p domain.Policy) (Origin, error) {
	cur := p
	for i := 0; i < maxRenewalChain; i++ {
		if cur.RenewalOf != "" {
			// A renewal was charged with no offer behind it: the origin is the
			// one of the policy it renews.
			prev, err := f.Policies.Get(ctx, cur.RenewalOf)
			if errors.Is(err, ports.ErrPolicyNotFound) {
				break
			}
			if err != nil {
				return Origin{}, fmt.Errorf("read the renewed policy %s: %w", cur.RenewalOf, err)
			}
			cur = prev
			continue
		}
		o, found, err := f.offerOf(ctx, cur)
		if err != nil || found {
			return o, err
		}
		break
	}
	return f.fallback(ctx, p)
}

func (f OriginFinder) offerOf(ctx context.Context, p domain.Policy) (Origin, bool, error) {
	if f.Charges == nil || f.Offers == nil {
		return Origin{}, false, nil
	}
	c, err := f.Charges.Get(ctx, p.AcceptanceID)
	if errors.Is(err, ports.ErrChargeNotFound) {
		return Origin{}, false, nil
	}
	if err != nil {
		return Origin{}, false, fmt.Errorf("read the charge of policy %s: %w", p.ID, err)
	}
	o, err := f.Offers.Get(ctx, c.OfferID)
	if errors.Is(err, ports.ErrOfferNotFound) {
		return Origin{}, false, nil
	}
	if err != nil {
		return Origin{}, false, fmt.Errorf("read the offer of policy %s: %w", p.ID, err)
	}
	return Origin{CustomerID: o.CustomerID, BirthDate: o.BirthDate, Channel: o.Channel, OfferID: o.ID}, true, nil
}

func (f OriginFinder) fallback(ctx context.Context, p domain.Policy) (Origin, error) {
	if f.BySubject != nil && p.CustomerID != "" {
		c, err := f.BySubject.GetBySubject(ctx, p.CustomerID)
		switch {
		case err == nil:
			return Origin{CustomerID: c.ID, BirthDate: c.BirthDate}, nil
		case !errors.Is(err, ports.ErrCustomerNotFound):
			return Origin{}, fmt.Errorf("read the customer of policy %s: %w", p.ID, err)
		}
	}
	if f.ByDocument != nil && p.Holder.DocumentID != "" {
		c, err := f.ByDocument.GetByDocument(ctx, p.Holder.DocumentID)
		switch {
		case err == nil:
			return Origin{CustomerID: c.ID, BirthDate: c.BirthDate}, nil
		case !errors.Is(err, ports.ErrCustomerNotFound):
			return Origin{}, fmt.Errorf("read the holder of policy %s: %w", p.ID, err)
		}
	}
	return Origin{}, nil
}
