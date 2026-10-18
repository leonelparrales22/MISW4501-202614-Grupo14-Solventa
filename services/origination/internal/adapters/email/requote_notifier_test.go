package emailadapter_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/email"
	emailadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/email"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

// failingSender fails the first failures sends and records every attempt.
type failingSender struct {
	failures int
	err      error
	got      []email.Message
}

func (s *failingSender) Send(_ context.Context, m email.Message) error {
	s.got = append(s.got, m)
	if len(s.got) <= s.failures {
		return s.err
	}
	return nil
}

func requoteNotice() domain.RequoteNotice {
	return domain.RequoteNotice{
		To: "ana@example.com", CustomerName: "Ana Pérez", OldOfferID: "off-1", NewOfferID: "off-2",
		OldPremium: 147_400, NewPremium: 137_600, Currency: "COP",
		ExpiresAt:      time.Date(2026, 10, 19, 13, 0, 0, 0, time.UTC),
		IdempotencyKey: "offer-requoted:off-1",
	}
}

var fastRetries = emailadapter.WithRequoteRetries(time.Millisecond, 4*time.Millisecond, 4)

func TestRequoteNotifierTellsBothPremiumsTheValidityAndTheLink(t *testing.T) {
	s := &failingSender{}

	require.NoError(t, emailadapter.NewRequoteNotifier(s, "https://app.solventa.test/", fastRetries).Notify(context.Background(), requoteNotice()))

	require.Len(t, s.got, 1)
	m := s.got[0]
	assert.Equal(t, "ana@example.com", m.To)
	assert.Equal(t, "offer-requoted:off-1", m.IdempotencyKey)
	assert.Equal(t, "Tu oferta de seguro se actualizó", m.Subject)
	for _, want := range []string{"Ana Pérez", "$ 147.400 COP", "$ 137.600 COP", "19/10/2026 a las 13:00 (UTC)",
		"https://app.solventa.test/embebido/oferta/off-2", "ya no se puede aceptar"} {
		assert.Contains(t, m.Body, want)
	}
}

func TestRequoteNotifierRetriesWithTheSameKeyUntilItSucceeds(t *testing.T) {
	s := &failingSender{failures: 2, err: email.ErrUnavailable}

	err := emailadapter.NewRequoteNotifier(s, "https://app", fastRetries).Notify(context.Background(), requoteNotice())

	require.NoError(t, err)
	require.Len(t, s.got, 3)
	for _, m := range s.got {
		assert.Equal(t, "offer-requoted:off-1", m.IdempotencyKey)
	}
}

func TestRequoteNotifierGivesUpAfterTheLastAttempt(t *testing.T) {
	s := &failingSender{failures: 99, err: email.ErrUnavailable}

	err := emailadapter.NewRequoteNotifier(s, "https://app", fastRetries).Notify(context.Background(), requoteNotice())

	assert.ErrorIs(t, err, email.ErrUnavailable)
	assert.Len(t, s.got, 4)
}

func TestRequoteNotifierDoesNotRetryAnInvalidMessage(t *testing.T) {
	s := &failingSender{failures: 99, err: email.ErrInvalidMessage}

	err := emailadapter.NewRequoteNotifier(s, "https://app", fastRetries).Notify(context.Background(), requoteNotice())

	assert.ErrorIs(t, err, email.ErrInvalidMessage)
	assert.Len(t, s.got, 1)
}

func TestRequoteNotifierStopsRetryingWhenTheContextEnds(t *testing.T) {
	s := &failingSender{failures: 99, err: errors.New("down")}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	n := emailadapter.NewRequoteNotifier(s, "https://app", emailadapter.WithRequoteRetries(time.Hour, time.Hour, 5))

	err := n.Notify(ctx, requoteNotice())

	assert.ErrorIs(t, err, context.Canceled)
	assert.Len(t, s.got, 1)
}
