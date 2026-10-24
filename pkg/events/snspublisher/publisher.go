// Package snspublisher publishes domain events to an Amazon SNS topic. It is
// the events.Publisher the outbox relay of a service uses (pkg/outbox).
package snspublisher

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sns/types"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/events"
)

// AttributeEventType is the message attribute that carries the event type,
// so a subscription can filter by type without reading the body.
const AttributeEventType = "event_type"

// API is the part of the SNS client the publisher needs; *sns.Client
// implements it and tests use a double.
type API interface {
	Publish(ctx context.Context, params *sns.PublishInput, optFns ...func(*sns.Options)) (*sns.PublishOutput, error)
}

// Publisher publishes envelopes to one topic.
type Publisher struct {
	api   API
	topic string
}

var _ events.Publisher = (*Publisher)(nil)

// New builds a publisher of the topic with the given ARN.
func New(api API, topicARN string) (*Publisher, error) {
	if api == nil {
		return nil, errors.New("snspublisher: the SNS client is required")
	}
	if topicARN == "" {
		return nil, errors.New("snspublisher: the topic ARN is required")
	}
	return &Publisher{api: api, topic: topicARN}, nil
}

// Topic returns the ARN of the topic.
func (p *Publisher) Topic() string { return p.topic }

// Publish sends the encoded envelope as the message body, with the event type
// as the event_type attribute. The subscriptions use raw message delivery, so
// the consumer reads the envelope itself.
func (p *Publisher) Publish(ctx context.Context, e events.Envelope) error {
	body, err := e.Encode()
	if err != nil {
		return err
	}
	_, err = p.api.Publish(ctx, &sns.PublishInput{
		TopicArn: aws.String(p.topic),
		Message:  aws.String(string(body)),
		MessageAttributes: map[string]types.MessageAttributeValue{
			AttributeEventType: {DataType: aws.String("String"), StringValue: aws.String(e.EventType)},
		},
	})
	if err != nil {
		return fmt.Errorf("publish event %s to SNS: %w", e.EventID, err)
	}
	return nil
}
