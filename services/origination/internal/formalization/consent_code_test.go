package formalization_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/formalization"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// The verification and the generation of the confirmation code are shared
// with the assisted renewal (HU-W1.5-03); here they are called directly, with
// any id as the key of the challenge.

var codeAt = time.Date(2026, 9, 16, 21, 5, 0, 0, time.UTC)

func issuedChallenge(t *testing.T, id, code string) ports.ConsentChallengeStore {
	t.Helper()
	store := memory.NewConsentChallengeStore()
	_, err := store.Issue(context.Background(), domain.ConsentChallenge{
		QuoteID: id, AdvisorID: "adv-1", CodeHash: domain.HashConsentCode(id, code), CreatedAt: codeAt,
		ExpiresAt: codeAt.Add(domain.ConsentCodeTTL),
	})
	require.NoError(t, err)
	return store
}

func TestVerifyConsentCodeAcceptsTheCodeSentForTheId(t *testing.T) {
	store := issuedChallenge(t, "rq-1", "482913")

	assert.NoError(t, formalization.VerifyConsentCode(context.Background(), store, "rq-1", "482913", codeAt.Add(time.Minute)))
}

func TestVerifyConsentCodeRefusesAWrongOrMalformedCode(t *testing.T) {
	store := issuedChallenge(t, "rq-1", "482913")
	ctx := context.Background()

	assert.ErrorIs(t, formalization.VerifyConsentCode(ctx, store, "rq-1", "000000", codeAt), formalization.ErrConsentCodeInvalid)
	assert.ErrorIs(t, formalization.VerifyConsentCode(ctx, store, "rq-1", "48291", codeAt), formalization.ErrConsentCodeInvalid)
	other := issuedChallenge(t, "rq-2", "482913")
	assert.NoError(t, formalization.VerifyConsentCode(ctx, other, "rq-2", "482913", codeAt), "the hash binds the code to its id")
}

func TestVerifyConsentCodeNeedsACodeSentAndUnexpired(t *testing.T) {
	ctx := context.Background()

	err := formalization.VerifyConsentCode(ctx, memory.NewConsentChallengeStore(), "rq-1", "482913", codeAt)
	assert.ErrorIs(t, err, formalization.ErrConsentCodeRequired)

	store := issuedChallenge(t, "rq-1", "482913")
	err = formalization.VerifyConsentCode(ctx, store, "rq-1", "482913", codeAt.Add(domain.ConsentCodeTTL))
	assert.ErrorIs(t, err, formalization.ErrConsentCodeExpired, "it expires at the exact deadline")
}

func TestVerifyConsentCodeLocksAfterFiveAttemptsEvenWithTheRightCode(t *testing.T) {
	store := issuedChallenge(t, "rq-1", "482913")
	ctx := context.Background()
	for i := 0; i < domain.MaxConsentCodeAttempts; i++ {
		assert.ErrorIs(t, formalization.VerifyConsentCode(ctx, store, "rq-1", "111111", codeAt), formalization.ErrConsentCodeInvalid)
	}

	err := formalization.VerifyConsentCode(ctx, store, "rq-1", "482913", codeAt)

	assert.ErrorIs(t, err, formalization.ErrConsentCodeLocked)
}

func TestNewConsentCodeHasSixDigits(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		code, err := formalization.NewConsentCode()
		require.NoError(t, err)
		assert.True(t, domain.ValidConsentCode(code), code)
		seen[code] = true
	}
	assert.Greater(t, len(seen), 1, "the codes are random")
}
