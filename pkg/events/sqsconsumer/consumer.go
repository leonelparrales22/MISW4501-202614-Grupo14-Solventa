// Package sqsconsumer consumes domain events from an Amazon SQS queue and hands
// each one to an events.Handler.
//
// Delivery is at-least-once (pkg/events): a message is deleted only when the
// handler succeeded or when nobody consumes its type. Otherwise it stays in the
// queue, reappears when its visibility timeout ends and, after the queue's
// maxReceiveCount, the redrive policy moves it to the dead-letter queue
// (infra/terraform/modules/messaging). The consumer never logs the payload.
package sqsconsumer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/events"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
)

// API is the part of the SQS client the consumer needs; *sqs.Client
// implements it and tests use a double.
type API interface {
	ReceiveMessage(ctx context.Context, params *sqs.ReceiveMessageInput, optFns ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	DeleteMessage(ctx context.Context, params *sqs.DeleteMessageInput, optFns ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error)
}

// Options tunes the consumer. The zero value of a field takes its default.
type Options struct {
	// WaitTime is the long polling wait of each receive (default and maximum
	// 20 s).
	WaitTime time.Duration
	// MaxMessages is how many messages a receive asks for (default and
	// maximum 10).
	MaxMessages int32
	// ErrorBackoff is the growing wait after consecutive receive failures
	// (default 1 s doubling up to 30 s, without a window).
	ErrorBackoff resilience.RetryPolicy
	// Logger receives the consumer logs (default slog.Default()).
	Logger *slog.Logger
}

// Defaults of Options.
const (
	DefaultWaitTime    = 20 * time.Second
	DefaultMaxMessages = 10
)

// DefaultErrorBackoff is the wait between receive attempts while SQS fails.
func DefaultErrorBackoff() resilience.RetryPolicy {
	return resilience.RetryPolicy{Base: time.Second, Max: 30 * time.Second}
}

// Consumer polls one queue.
type Consumer struct {
	api     API
	queue   string
	handler events.Handler
	opts    Options
	sleep   func(ctx context.Context, d time.Duration) error
}

// New builds a consumer of the queue with the given URL.
func New(api API, queueURL string, h events.Handler, opts Options) (*Consumer, error) {
	if api == nil {
		return nil, errors.New("sqsconsumer: the SQS client is required")
	}
	if queueURL == "" {
		return nil, errors.New("sqsconsumer: the queue URL is required")
	}
	if h == nil {
		return nil, errors.New("sqsconsumer: the handler is required")
	}
	if opts.WaitTime <= 0 || opts.WaitTime > DefaultWaitTime {
		opts.WaitTime = DefaultWaitTime
	}
	if opts.MaxMessages <= 0 || opts.MaxMessages > DefaultMaxMessages {
		opts.MaxMessages = DefaultMaxMessages
	}
	if opts.ErrorBackoff == (resilience.RetryPolicy{}) {
		opts.ErrorBackoff = DefaultErrorBackoff()
	}
	if err := opts.ErrorBackoff.Validate(); err != nil {
		return nil, fmt.Errorf("sqsconsumer: %w", err)
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Consumer{api: api, queue: queueURL, handler: h, opts: opts, sleep: sleep}, nil
}

// Run polls the queue until ctx ends. While receiving fails it waits with the
// growing ErrorBackoff, so a broken queue is not hammered; one successful
// receive resets the wait.
func (c *Consumer) Run(ctx context.Context) {
	failures := 0
	for ctx.Err() == nil {
		_, err := c.ProcessOnce(ctx)
		if err == nil {
			failures = 0
			continue
		}
		if ctx.Err() != nil {
			return
		}
		failures++
		wait := c.opts.ErrorBackoff.Backoff(failures)
		c.opts.Logger.WarnContext(ctx, "could not receive events from the queue; retrying",
			"queue", c.queue, "consecutive_failures", failures, "retry_in", wait.String(), "error", err)
		if c.sleep(ctx, wait) != nil {
			return
		}
	}
}

// ProcessOnce receives one batch with long polling and processes every
// message in it. It returns how many messages it received; the error is only
// the receive's (a message that fails stays in the queue and is logged).
func (c *Consumer) ProcessOnce(ctx context.Context) (int, error) {
	out, err := c.api.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:                    aws.String(c.queue),
		MaxNumberOfMessages:         c.opts.MaxMessages,
		WaitTimeSeconds:             int32(c.opts.WaitTime / time.Second), //nolint:gosec // G115: New bounds WaitTime to 0..20 s
		MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameApproximateReceiveCount},
	})
	if err != nil {
		return 0, fmt.Errorf("receive from %s: %w", c.queue, err)
	}
	for _, m := range out.Messages {
		c.process(ctx, m)
	}
	return len(out.Messages), nil
}

// process handles one message and deletes it when it must not come back.
func (c *Consumer) process(ctx context.Context, m types.Message) {
	log := c.opts.Logger.With("queue", c.queue, "message_id", aws.ToString(m.MessageId),
		"receive_count", receiveCount(m))
	e, err := events.Decode([]byte(aws.ToString(m.Body)))
	if err != nil {
		// Not deleted: after maxReceiveCount the queue moves it to its DLQ.
		log.WarnContext(ctx, "invalid event envelope; it stays in the queue until the dead-letter queue takes it", "error", err)
		return
	}
	log = log.With("event_id", e.EventID, "event_type", e.EventType)
	err = c.handler.Handle(ctx, e)
	switch {
	case errors.Is(err, events.ErrUnhandledType):
		log.DebugContext(ctx, "event type not consumed here; discarded")
	case err != nil:
		log.WarnContext(ctx, "could not handle the event; it will be retried when its visibility timeout ends", "error", err)
		return
	}
	if _, err := c.api.DeleteMessage(ctx, &sqs.DeleteMessageInput{
		QueueUrl: aws.String(c.queue), ReceiptHandle: m.ReceiptHandle,
	}); err != nil {
		// Handled but not deleted: it comes back and the idempotent handler
		// absorbs the repetition.
		log.WarnContext(ctx, "the event was handled but could not be deleted; it will be delivered again", "error", err)
	}
}

func receiveCount(m types.Message) int {
	n, err := strconv.Atoi(m.Attributes[string(types.MessageSystemAttributeNameApproximateReceiveCount)])
	if err != nil {
		return 0
	}
	return n
}

// sleep waits d or until ctx ends, whichever comes first.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
