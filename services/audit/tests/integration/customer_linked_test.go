// This file checks the consumer of CustomerLinked (HU-M1.3-01) against
// LocalStack and a real PostgreSQL: an SNS topic, an SQS queue subscribed with
// raw message delivery and a dead-letter queue (like modules/messaging, with a
// maxReceiveCount of 2 and a visibility timeout of 1 s to keep the test short).
package integration_test

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/localstack"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/events"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/events/snspublisher"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/events/sqsconsumer"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/adapters/messaging"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/consents"
)

// emulatorImage is the last LocalStack image that starts without an auth token
// (decision D-10 a of HA-D02).
const emulatorImage = "localstack/localstack:4.14.0"

type messagingEnv struct {
	*env
	sns       *sns.Client
	sqs       *sqs.Client
	topicARN  string
	queueURL  string
	dlqURL    string
	publisher *snspublisher.Publisher
	consumer  *sqsconsumer.Consumer
}

func newMessagingEnv(t *testing.T) *messagingEnv {
	t.Helper()
	e := newEnv(t)
	ctx := context.Background()
	ctr, err := localstack.Run(ctx, emulatorImage, testcontainers.WithEnv(map[string]string{"SERVICES": "sns,sqs"}))
	testcontainers.CleanupContainer(t, ctr)
	require.NoError(t, err, "start LocalStack")
	host, err := ctr.Host(ctx)
	require.NoError(t, err)
	port, err := ctr.MappedPort(ctx, "4566/tcp")
	require.NoError(t, err)
	endpoint := fmt.Sprintf("http://%s:%s", host, port.Port())
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion("us-east-2"),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")))
	require.NoError(t, err)
	m := &messagingEnv{
		env: e,
		sns: sns.NewFromConfig(cfg, func(o *sns.Options) { o.BaseEndpoint = aws.String(endpoint) }),
		sqs: sqs.NewFromConfig(cfg, func(o *sqs.Options) { o.BaseEndpoint = aws.String(endpoint) }),
	}

	topic, err := m.sns.CreateTopic(ctx, &sns.CreateTopicInput{Name: aws.String("solventa-test-origination")})
	require.NoError(t, err)
	m.topicARN = aws.ToString(topic.TopicArn)
	dlq, err := m.sqs.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("solventa-test-audit-dlq")})
	require.NoError(t, err)
	m.dlqURL = aws.ToString(dlq.QueueUrl)
	dlqARN := m.queueARN(t, m.dlqURL)
	queue, err := m.sqs.CreateQueue(ctx, &sqs.CreateQueueInput{
		QueueName: aws.String("solventa-test-audit"),
		Attributes: map[string]string{
			"VisibilityTimeout": "1",
			"RedrivePolicy":     fmt.Sprintf(`{"deadLetterTargetArn":"%s","maxReceiveCount":"2"}`, dlqARN),
		},
	})
	require.NoError(t, err)
	m.queueURL = aws.ToString(queue.QueueUrl)
	_, err = m.sns.Subscribe(ctx, &sns.SubscribeInput{
		TopicArn: topic.TopicArn, Protocol: aws.String("sqs"), Endpoint: aws.String(m.queueARN(t, m.queueURL)),
		Attributes: map[string]string{"RawMessageDelivery": "true"},
	})
	require.NoError(t, err)

	m.publisher, err = snspublisher.New(m.sns, m.topicARN)
	require.NoError(t, err)
	holders := consents.NewHolderService(postgres.NewConsentStore(e.db), postgres.NewConsentEventStore(e.db),
		postgres.NewHolderLinkStore(e.db), e.clock, nil)
	router := events.NewRouter()
	require.NoError(t, router.Register(messaging.EventTypeCustomerLinked, messaging.NewCustomerLinkedHandler(holders)))
	m.consumer, err = sqsconsumer.New(m.sqs, m.queueURL, router, sqsconsumer.Options{WaitTime: time.Second, Logger: slog.Default()})
	require.NoError(t, err)
	return m
}

func (m *messagingEnv) queueARN(t *testing.T, url string) string {
	t.Helper()
	out, err := m.sqs.GetQueueAttributes(context.Background(), &sqs.GetQueueAttributesInput{
		QueueUrl: aws.String(url), AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameQueueArn},
	})
	require.NoError(t, err)
	return out.Attributes["QueueArn"]
}

func (m *messagingEnv) publishLinked(t *testing.T, eventID, customer, subject, document string) {
	t.Helper()
	e, err := events.New(eventID, messaging.EventTypeCustomerLinked, "origination", 1, time.Now().UTC(),
		map[string]string{"customer_id": customer, "subject": subject, "document_id": document})
	require.NoError(t, err)
	require.NoError(t, m.publisher.Publish(context.Background(), e))
}

// drain processes the queue until it stays empty for two polls.
func (m *messagingEnv) drain(t *testing.T) {
	t.Helper()
	empty := 0
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline) && empty < 2; {
		n, err := m.consumer.ProcessOnce(context.Background())
		require.NoError(t, err)
		if n == 0 {
			empty++
		} else {
			empty = 0
		}
	}
}

func (m *messagingEnv) links(t *testing.T, subject string) (count int, document string) {
	t.Helper()
	require.NoError(t, m.db.QueryRow(`SELECT count(*), coalesce(max(document_id), '') FROM holder_link WHERE subject = $1`,
		subject).Scan(&count, &document))
	return count, document
}

func TestTheAuditQueueConsumesCustomerLinked(t *testing.T) {
	m := newMessagingEnv(t)

	t.Run("a published link is stored", func(t *testing.T) {
		m.publishLinked(t, "customer-linked-cus-a", "cus-a", "sub-a", "ab12345")
		m.drain(t)

		n, doc := m.links(t, "sub-a")
		assert.Equal(t, 1, n)
		assert.Equal(t, "AB12345", doc, "normalized")
	})

	t.Run("the same event twice leaves one row", func(t *testing.T) {
		m.publishLinked(t, "customer-linked-cus-b", "cus-b", "sub-b", "9988776655")
		m.publishLinked(t, "customer-linked-cus-b", "cus-b", "sub-b", "9988776655")
		m.drain(t)

		n, _ := m.links(t, "sub-b")
		assert.Equal(t, 1, n)
	})

	t.Run("another document for the same subject keeps the first", func(t *testing.T) {
		m.publishLinked(t, "customer-linked-cus-a-bis", "cus-a", "sub-a", "1111111111")
		m.drain(t)

		n, doc := m.links(t, "sub-a")
		assert.Equal(t, 1, n)
		assert.Equal(t, "AB12345", doc)
	})

	t.Run("an unknown type is deleted without effect", func(t *testing.T) {
		e, err := events.New("claim-filed-1", "ClaimFiled", "claims", 1, time.Now().UTC(), map[string]string{"claim_id": "c1"})
		require.NoError(t, err)
		require.NoError(t, m.publisher.Publish(context.Background(), e))
		m.drain(t)

		assert.Zero(t, m.queueDepth(t, m.queueURL), "deleted, not left in the queue")
		assert.Zero(t, m.queueDepth(t, m.dlqURL))
		var n int
		require.NoError(t, m.db.QueryRow(`SELECT count(*) FROM holder_link`).Scan(&n))
		assert.Equal(t, 2, n)
	})

	t.Run("an invalid envelope ends in the dead-letter queue", func(t *testing.T) {
		_, err := m.sns.Publish(context.Background(), &sns.PublishInput{
			TopicArn: aws.String(m.topicARN), Message: aws.String(`{"not":"an envelope"}`),
		})
		require.NoError(t, err)

		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) && m.queueDepth(t, m.dlqURL) == 0 {
			_, err := m.consumer.ProcessOnce(context.Background())
			require.NoError(t, err)
		}
		assert.Equal(t, 1, m.queueDepth(t, m.dlqURL), "after maxReceiveCount the queue moves it to its DLQ")
	})
}

// queueDepth counts the visible and in-flight messages of a queue.
func (m *messagingEnv) queueDepth(t *testing.T, url string) int {
	t.Helper()
	out, err := m.sqs.GetQueueAttributes(context.Background(), &sqs.GetQueueAttributesInput{
		QueueUrl: aws.String(url),
		AttributeNames: []sqstypes.QueueAttributeName{
			sqstypes.QueueAttributeNameApproximateNumberOfMessages,
			sqstypes.QueueAttributeNameApproximateNumberOfMessagesNotVisible,
		},
	})
	require.NoError(t, err)
	var visible, inFlight int
	_, _ = fmt.Sscan(out.Attributes["ApproximateNumberOfMessages"], &visible)
	_, _ = fmt.Sscan(out.Attributes["ApproximateNumberOfMessagesNotVisible"], &inFlight)
	return visible + inFlight
}
