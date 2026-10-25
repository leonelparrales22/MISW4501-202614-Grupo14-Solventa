package events_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/events"
)

func TestRouterDispatchesByEventType(t *testing.T) {
	r := events.NewRouter()
	var got []string
	require.NoError(t, r.Register("CustomerLinked", events.HandlerFunc(func(_ context.Context, e events.Envelope) error {
		got = append(got, "linked:"+e.EventID)
		return nil
	})))
	require.NoError(t, r.Register("OfferGenerated", events.HandlerFunc(func(_ context.Context, e events.Envelope) error {
		got = append(got, "offer:"+e.EventID)
		return nil
	})))

	require.NoError(t, r.Handle(context.Background(), events.Envelope{EventID: "e1", EventType: "CustomerLinked"}))
	require.NoError(t, r.Handle(context.Background(), events.Envelope{EventID: "e2", EventType: "OfferGenerated"}))

	assert.Equal(t, []string{"linked:e1", "offer:e2"}, got)
}

func TestRouterAnswersErrUnhandledTypeForAnUnknownType(t *testing.T) {
	r := events.NewRouter()

	err := r.Handle(context.Background(), events.Envelope{EventID: "e1", EventType: "ClaimFiled"})

	assert.ErrorIs(t, err, events.ErrUnhandledType)
	assert.ErrorContains(t, err, "ClaimFiled")
}

func TestRouterReturnsTheErrorOfTheHandler(t *testing.T) {
	r := events.NewRouter()
	boom := errors.New("the database is down")
	require.NoError(t, r.Register("CustomerLinked", events.HandlerFunc(func(context.Context, events.Envelope) error { return boom })))

	err := r.Handle(context.Background(), events.Envelope{EventType: "CustomerLinked"})

	assert.ErrorIs(t, err, boom)
	assert.NotErrorIs(t, err, events.ErrUnhandledType)
}

func TestRouterRefusesAnInvalidRegistration(t *testing.T) {
	r := events.NewRouter()
	ok := events.HandlerFunc(func(context.Context, events.Envelope) error { return nil })

	assert.Error(t, r.Register("", ok), "a type is required")
	assert.Error(t, r.Register("CustomerLinked", nil), "a handler is required")
	require.NoError(t, r.Register("CustomerLinked", ok))
	assert.Error(t, r.Register("CustomerLinked", ok), "a type has a single handler")
}
