package formalization

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// The confirmation code is shared by the purchase an advisor formalizes
// (HU-W1.1-03) and the renewal an advisor formalizes (HU-W1.5-03): the same
// generation, the same TTL and the same bounds on codes sent and typed. Each
// keeps its codes in its own store, keyed by the quote or by the requote.

// VerifyConsentCode checks code against the one sent for id. Every typed code
// counts, whether it is right or not, so the code cannot be guessed: past
// domain.MaxConsentCodeAttempts it is ErrConsentCodeLocked even if right.
func VerifyConsentCode(ctx context.Context, store ports.ConsentChallengeStore, id, code string, now time.Time) error {
	challenge, err := store.Get(ctx, id)
	if errors.Is(err, ports.ErrConsentChallengeNotFound) {
		return ErrConsentCodeRequired
	}
	if err != nil {
		return err
	}
	if challenge.IsExpired(now) {
		return ErrConsentCodeExpired
	}
	attempts, err := store.RegisterAttempt(ctx, id)
	if err != nil {
		return err
	}
	if attempts > domain.MaxConsentCodeAttempts {
		return ErrConsentCodeLocked
	}
	if !domain.ValidConsentCode(code) || !challenge.Matches(code) {
		return ErrConsentCodeInvalid
	}
	return nil
}

// NewConsentCode returns a uniformly random code of domain.ConsentCodeLength
// digits.
func NewConsentCode() (string, error) {
	max := new(big.Int).Exp(big.NewInt(10), big.NewInt(domain.ConsentCodeLength), nil)
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		return "", fmt.Errorf("could not generate the confirmation code: %w", err)
	}
	return fmt.Sprintf("%0*d", domain.ConsentCodeLength, n), nil
}
