package sqsconsumer_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/events"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/events/sqsconsumer"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
)

const queue = "http://localhost:4566/000000000000/solventa-staging-audit"

// fakeSQS answers the queued batches (or errors) in order and records the
// deleted receipt handles.
type fakeSQS struct {
	mu        sync.Mutex
	batches   [][]types.Message
	errs      []error
	receives  []*sqs.ReceiveMessageInput
	deleted   []string
	deleteErr error
	onEmpty   func()
}

func (f *fakeSQS) ReceiveMessage(_ context.Context, in *sqs.ReceiveMessageInput, _ ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.receives = append(f.receives, in)
	if len(f.errs) > 0 {
		err := f.errs[0]
		f.errs = f.errs[1:]
		if err != nil {
			return nil, err
		}
	}
	if len(f.batches) == 0 {
		if f.onEmpty != nil {
			f.onEmpty()
		}
		return &sqs.ReceiveMessageOutput{}, nil
	}
	b := f.batches[0]
	f.batches = f.batches[1:]
	return &sqs.ReceiveMessageOutput{Messages: b}, nil
}

func (f *fakeSQS) DeleteMessage(_ context.Context, in *sqs.DeleteMessageInput, _ ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deleteErr != nil {
		return nil, f.deleteErr
	}
	f.deleted = append(f.deleted, aws.ToString(in.ReceiptHandle))
	return &sqs.DeleteMessageOutput{}, nil
}

func message(t *testing.T, receipt, eventType string, payload any) types.Message {
	t.Helper()
	e, err := events.New("evt-"+receipt, eventType, "origination", 1, time.Date(2026, 10, 24, 20, 0, 0, 0, time.UTC), payload)
	require.NoError(t, err)
	body, err := e.Encode()
	require.NoError(t, err)
	return types.Message{
		MessageId: aws.String("m-" + receipt), ReceiptHandle: aws.String(receipt), Body: aws.String(string(body)),
		Attributes: map[string]string{"ApproximateReceiveCount": "1"},
	}
}

type recorder struct {
	mu   sync.Mutex
	seen []string
	err  error
}

func (r *recorder) handler() events.Handler {
	router := events.NewRouter()
	_ = router.Register("CustomerLinked", events.HandlerFunc(func(_ context.Context, e events.Envelope) error {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.seen = append(r.seen, e.EventID)
		return r.err
	}))
	return router
}

func newConsumer(t *testing.T, api sqsconsumer.API, h events.Handler, logs *bytes.Buffer) *sqsconsumer.Consumer {
	t.Helper()
	c, err := sqsconsumer.New(api, queue, h, sqsconsumer.Options{
		Logger: slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	require.NoError(t, err)
	return c
}

func TestProcessOnceDeletesWhatItHandled(t *testing.T) {
	api := &fakeSQS{batches: [][]types.Message{{
		message(t, "r1", "CustomerLinked", map[string]string{"document_id": "1020304050"}),
		message(t, "r2", "CustomerLinked", map[string]string{"document_id": "99999"}),
	}}}
	rec := &recorder{}
	var logs bytes.Buffer
	c := newConsumer(t, api, rec.handler(), &logs)

	n, err := c.ProcessOnce(context.Background())

	require.NoError(t, err)
	assert.Equal(t, 2, n)
	assert.Equal(t, []string{"evt-r1", "evt-r2"}, rec.seen)
	assert.Equal(t, []string{"r1", "r2"}, api.deleted)
	in := api.receives[0]
	assert.Equal(t, queue, aws.ToString(in.QueueUrl))
	assert.Equal(t, int32(10), in.MaxNumberOfMessages)
	assert.Equal(t, int32(20), in.WaitTimeSeconds, "long polling")
	assert.Contains(t, in.MessageSystemAttributeNames, types.MessageSystemAttributeNameApproximateReceiveCount)
	assert.NotContains(t, logs.String(), "1020304050", "the payload is never logged")
}

func TestAHandlerErrorLeavesTheMessageInTheQueue(t *testing.T) {
	api := &fakeSQS{batches: [][]types.Message{{message(t, "r1", "CustomerLinked", map[string]string{})}}}
	rec := &recorder{err: errors.New("the database is down")}
	var logs bytes.Buffer

	_, err := newConsumer(t, api, rec.handler(), &logs).ProcessOnce(context.Background())

	require.NoError(t, err)
	assert.Equal(t, []string{"evt-r1"}, rec.seen)
	assert.Empty(t, api.deleted, "it reappears when the visibility timeout ends")
	assert.Contains(t, logs.String(), "will be retried")
}

func TestAnInvalidEnvelopeIsNotDeletedSoItReachesTheDLQ(t *testing.T) {
	api := &fakeSQS{batches: [][]types.Message{{{
		MessageId: aws.String("m-x"), ReceiptHandle: aws.String("rx"), Body: aws.String(`{"not":"an envelope"}`),
		Attributes: map[string]string{"ApproximateReceiveCount": "3"},
	}}}}
	rec := &recorder{}
	var logs bytes.Buffer

	n, err := newConsumer(t, api, rec.handler(), &logs).ProcessOnce(context.Background())

	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Empty(t, rec.seen)
	assert.Empty(t, api.deleted)
	assert.Contains(t, logs.String(), "dead-letter queue")
	assert.Contains(t, logs.String(), `"receive_count":3`)
}

func TestAnUnhandledTypeIsDiscarded(t *testing.T) {
	api := &fakeSQS{batches: [][]types.Message{{message(t, "r1", "ClaimFiled", map[string]string{})}}}
	rec := &recorder{}
	var logs bytes.Buffer

	_, err := newConsumer(t, api, rec.handler(), &logs).ProcessOnce(context.Background())

	require.NoError(t, err)
	assert.Empty(t, rec.seen)
	assert.Equal(t, []string{"r1"}, api.deleted, "the audit queue gets every event of its topics")
}

func TestAFailedDeleteIsOnlyLogged(t *testing.T) {
	api := &fakeSQS{batches: [][]types.Message{{message(t, "r1", "CustomerLinked", map[string]string{})}},
		deleteErr: errors.New("throttled")}
	rec := &recorder{}
	var logs bytes.Buffer

	n, err := newConsumer(t, api, rec.handler(), &logs).ProcessOnce(context.Background())

	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Contains(t, logs.String(), "delivered again")
}

func TestProcessOnceReturnsTheReceiveError(t *testing.T) {
	boom := errors.New("connection refused")
	api := &fakeSQS{errs: []error{boom}}
	var logs bytes.Buffer

	_, err := newConsumer(t, api, (&recorder{}).handler(), &logs).ProcessOnce(context.Background())

	assert.ErrorIs(t, err, boom)
}

func TestRunWaitsLongerAfterEachFailedReceiveAndStopsWithTheContext(t *testing.T) {
	boom := errors.New("connection refused")
	api := &fakeSQS{
		errs:    []error{boom, boom, boom, nil, boom},
		batches: [][]types.Message{{message(t, "r1", "CustomerLinked", map[string]string{})}},
	}
	rec := &recorder{}
	var logs bytes.Buffer
	c, err := sqsconsumer.New(api, queue, rec.handler(), sqsconsumer.Options{
		ErrorBackoff: resilience.RetryPolicy{Base: time.Second, Max: 3 * time.Second},
		Logger:       slog.New(slog.NewJSONHandler(&logs, nil)),
	})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	var waits []time.Duration
	c.SetSleep(func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		if len(waits) == 4 {
			cancel()
			return context.Canceled
		}
		return nil
	})

	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop when the context ended")
	}

	assert.Equal(t, []time.Duration{time.Second, 2 * time.Second, 3 * time.Second, time.Second}, waits,
		"1 s doubling up to the cap; a successful receive resets the wait")
	assert.Equal(t, []string{"evt-r1"}, rec.seen)
}

func TestRunReturnsWhenTheContextEndsDuringAReceive(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	api := &fakeSQS{}
	api.onEmpty = cancel
	c, err := sqsconsumer.New(api, queue, (&recorder{}).handler(), sqsconsumer.Options{})
	require.NoError(t, err)

	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
}

func TestNewValidatesItsArguments(t *testing.T) {
	h := (&recorder{}).handler()
	_, err := sqsconsumer.New(nil, queue, h, sqsconsumer.Options{})
	assert.Error(t, err)
	_, err = sqsconsumer.New(&fakeSQS{}, "", h, sqsconsumer.Options{})
	assert.Error(t, err)
	_, err = sqsconsumer.New(&fakeSQS{}, queue, nil, sqsconsumer.Options{})
	assert.Error(t, err)
	_, err = sqsconsumer.New(&fakeSQS{}, queue, h, sqsconsumer.Options{ErrorBackoff: resilience.RetryPolicy{Base: time.Second}})
	assert.Error(t, err, "a maximum below the base")
}

func TestOptionsAboveTheSQSLimitsAreCapped(t *testing.T) {
	api := &fakeSQS{}
	c, err := sqsconsumer.New(api, queue, (&recorder{}).handler(), sqsconsumer.Options{WaitTime: time.Minute, MaxMessages: 50})
	require.NoError(t, err)

	_, err = c.ProcessOnce(context.Background())

	require.NoError(t, err)
	assert.Equal(t, int32(20), api.receives[0].WaitTimeSeconds)
	assert.Equal(t, int32(10), api.receives[0].MaxNumberOfMessages)
}
