package snspublisher_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/events"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/events/snspublisher"
)

const topic = "arn:aws:sns:us-east-2:000000000000:solventa-staging-origination"

type fakeSNS struct {
	inputs []*sns.PublishInput
	err    error
}

func (f *fakeSNS) Publish(_ context.Context, in *sns.PublishInput, _ ...func(*sns.Options)) (*sns.PublishOutput, error) {
	f.inputs = append(f.inputs, in)
	if f.err != nil {
		return nil, f.err
	}
	return &sns.PublishOutput{MessageId: aws.String("m-1")}, nil
}

func envelope(t *testing.T) events.Envelope {
	t.Helper()
	e, err := events.New("customer-linked-c1", "CustomerLinked", "origination", 1,
		time.Date(2026, 10, 24, 20, 0, 0, 0, time.UTC), map[string]string{"customer_id": "c1"})
	require.NoError(t, err)
	return e
}

func TestPublishSendsTheEnvelopeWithItsTypeAsAttribute(t *testing.T) {
	api := &fakeSNS{}
	p, err := snspublisher.New(api, topic)
	require.NoError(t, err)
	e := envelope(t)

	require.NoError(t, p.Publish(context.Background(), e))

	require.Len(t, api.inputs, 1)
	in := api.inputs[0]
	assert.Equal(t, topic, aws.ToString(in.TopicArn))
	got, err := events.Decode([]byte(aws.ToString(in.Message)))
	require.NoError(t, err)
	assert.Equal(t, e.EventID, got.EventID)
	assert.JSONEq(t, string(e.Payload), string(got.Payload))
	attr := in.MessageAttributes[snspublisher.AttributeEventType]
	assert.Equal(t, "String", aws.ToString(attr.DataType))
	assert.Equal(t, "CustomerLinked", aws.ToString(attr.StringValue))
	assert.Equal(t, topic, p.Topic())
}

func TestPublishWrapsTheErrorWithTheEventID(t *testing.T) {
	boom := errors.New("connection refused")
	p, err := snspublisher.New(&fakeSNS{err: boom}, topic)
	require.NoError(t, err)

	err = p.Publish(context.Background(), envelope(t))

	assert.ErrorIs(t, err, boom)
	assert.ErrorContains(t, err, "customer-linked-c1")
}

func TestPublishNeverSendsAnInvalidEnvelope(t *testing.T) {
	api := &fakeSNS{}
	p, err := snspublisher.New(api, topic)
	require.NoError(t, err)

	err = p.Publish(context.Background(), events.Envelope{EventID: "e1"})

	assert.ErrorIs(t, err, events.ErrInvalidEnvelope)
	assert.Empty(t, api.inputs)
}

func TestNewRequiresTheClientAndTheTopic(t *testing.T) {
	_, err := snspublisher.New(nil, topic)
	assert.Error(t, err)
	_, err = snspublisher.New(&fakeSNS{}, "")
	assert.Error(t, err)
}
