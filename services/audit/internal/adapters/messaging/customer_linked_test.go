package messaging_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/events"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/adapters/messaging"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
)

type linker struct {
	got []domain.HolderLink
	err error
}

func (l *linker) Link(_ context.Context, h domain.HolderLink) error {
	if l.err != nil {
		return l.err
	}
	l.got = append(l.got, h)
	return nil
}

var linkedAt = time.Date(2026, 10, 24, 20, 0, 0, 0, time.UTC)

func envelope(t *testing.T, payload any) events.Envelope {
	t.Helper()
	e, err := events.New("customer-linked-cus-1", messaging.EventTypeCustomerLinked, "origination", 1, linkedAt, payload)
	require.NoError(t, err)
	return e
}

func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func TestTheHandlerRecordsTheLinkOfTheEvent(t *testing.T) {
	logs := captureSlog(t)
	l := &linker{}
	h := messaging.NewCustomerLinkedHandler(l)

	err := h.Handle(context.Background(), envelope(t, map[string]any{
		"customer_id": "cus-1", "subject": "sub-1", "document_id": " ab12345 ", "linked_at": "2026-10-24T20:00:00Z",
		"a_future_field": true,
	}))

	require.NoError(t, err)
	require.Len(t, l.got, 1)
	assert.Equal(t, domain.HolderLink{Subject: "sub-1", DocumentID: "AB12345", CustomerID: "cus-1", LinkedAt: linkedAt,
		EventID: "customer-linked-cus-1"}, l.got[0])
	assert.Contains(t, logs.String(), "holder linked")
	assert.Contains(t, logs.String(), "cus-1")
	assert.NotContains(t, logs.String(), "AB12345", "the document is never logged")
	assert.NotContains(t, logs.String(), "ab12345")
}

func TestTheInstantOfTheEnvelopeStandsInForAMissingLinkedAt(t *testing.T) {
	l := &linker{}

	require.NoError(t, messaging.NewCustomerLinkedHandler(l).Handle(context.Background(),
		envelope(t, map[string]any{"customer_id": "cus-1", "subject": "sub-1", "document_id": "1020304050"})))

	assert.Equal(t, linkedAt, l.got[0].LinkedAt)
}

func TestAnInvalidPayloadIsAnErrorSoTheMessageEndsInTheDLQ(t *testing.T) {
	for name, payload := range map[string]map[string]any{
		"no subject":     {"customer_id": "cus-1", "document_id": "1020304050"},
		"no document":    {"customer_id": "cus-1", "subject": "sub-1"},
		"bad document":   {"customer_id": "cus-1", "subject": "sub-1", "document_id": "10-20"},
		"wrong type":     {"customer_id": 42, "subject": "sub-1", "document_id": "1020304050"},
		"no customer id": {"subject": "sub-1", "document_id": "1020304050"},
	} {
		t.Run(name, func(t *testing.T) {
			l := &linker{}
			err := messaging.NewCustomerLinkedHandler(l).Handle(context.Background(), envelope(t, payload))
			assert.Error(t, err)
			assert.Empty(t, l.got)
		})
	}
}

func TestAStoreErrorIsReturnedSoTheMessageIsRetried(t *testing.T) {
	boom := errors.New("db down")
	err := messaging.NewCustomerLinkedHandler(&linker{err: boom}).Handle(context.Background(),
		envelope(t, map[string]any{"customer_id": "cus-1", "subject": "sub-1", "document_id": "1020304050"}))
	assert.ErrorIs(t, err, boom)
}

func TestAPayloadThatIsNotAnObjectIsRefused(t *testing.T) {
	e := envelope(t, map[string]any{})
	e.Payload = json.RawMessage(`"text"`)

	err := messaging.NewCustomerLinkedHandler(&linker{}).Handle(context.Background(), e)

	assert.ErrorIs(t, err, events.ErrInvalidEnvelope)
}
