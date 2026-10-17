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

type recordingSender struct {
	got email.Message
	err error
}

func (r *recordingSender) Send(_ context.Context, m email.Message) error {
	r.got = m
	return r.err
}

func notification() domain.IssuedNotification {
	return domain.IssuedNotification{
		To: "ana@example.com", HolderName: "Ana Pérez", PolicyID: "pol-1",
		DocumentURL:   "https://api.solventa.example/v1/documents/abc.def",
		LinkExpiresAt: time.Date(2026, 10, 23, 15, 0, 0, 0, time.UTC), IdempotencyKey: "policy-issued:pol-1:v1",
	}
}

func TestNotifySendsTheLinkToTheInsured(t *testing.T) {
	s := &recordingSender{}

	require.NoError(t, emailadapter.NewNotifier(s).Notify(context.Background(), notification()))

	assert.Equal(t, "ana@example.com", s.got.To)
	assert.Equal(t, "policy-issued:pol-1:v1", s.got.IdempotencyKey)
	assert.Contains(t, s.got.Subject, "emitida")
	for _, want := range []string{"Ana Pérez", "pol-1", "https://api.solventa.example/v1/documents/abc.def", "2026-10-23"} {
		assert.Contains(t, s.got.Body, want)
	}
}

func TestNotifyReportsASenderFailure(t *testing.T) {
	boom := errors.New("provider down")
	err := emailadapter.NewNotifier(&recordingSender{err: boom}).Notify(context.Background(), notification())
	assert.ErrorIs(t, err, boom)
}
