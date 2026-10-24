// This file checks the CustomerLinked event of HU-M1.3-01 from the sign-up to
// the queue: a registration over PostgreSQL writes the event to the outbox,
// the relay publishes it to an SNS topic of LocalStack, and the SQS queue
// subscribed with raw message delivery (like modules/messaging) receives the
// exact envelope. With the broker unreachable the event stays in the outbox,
// counting attempts, and it is published once the broker answers.
package integration_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sync"
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
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/outbox"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/orchestrator"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// brokerImage is the last LocalStack image that starts without an auth token
// (decision D-10 a of HA-D02).
const brokerImage = "localstack/localstack:4.14.0"

// broker is LocalStack with the topic of origination and the audit queue
// subscribed to it.
type broker struct {
	endpoint string
	sns      *sns.Client
	sqs      *sqs.Client
	topicARN string
	queueURL string
}

func awsTestConfig(t *testing.T) aws.Config {
	t.Helper()
	cfg, err := config.LoadDefaultConfig(context.Background(), config.WithRegion("us-east-2"),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")))
	require.NoError(t, err)
	return cfg
}

func newBroker(t *testing.T) *broker {
	t.Helper()
	requireOfferDocker(t)
	ctx := context.Background()
	ctr, err := localstack.Run(ctx, brokerImage, testcontainers.WithEnv(map[string]string{"SERVICES": "sns,sqs"}))
	testcontainers.CleanupContainer(t, ctr)
	require.NoError(t, err, "start LocalStack")
	host, err := ctr.Host(ctx)
	require.NoError(t, err)
	port, err := ctr.MappedPort(ctx, "4566/tcp")
	require.NoError(t, err)
	b := &broker{endpoint: fmt.Sprintf("http://%s:%s", host, port.Port())}
	cfg := awsTestConfig(t)
	b.sns = sns.NewFromConfig(cfg, func(o *sns.Options) { o.BaseEndpoint = aws.String(b.endpoint) })
	b.sqs = sqs.NewFromConfig(cfg, func(o *sqs.Options) { o.BaseEndpoint = aws.String(b.endpoint) })

	topic, err := b.sns.CreateTopic(ctx, &sns.CreateTopicInput{Name: aws.String("solventa-test-origination")})
	require.NoError(t, err)
	b.topicARN = aws.ToString(topic.TopicArn)
	queue, err := b.sqs.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("solventa-test-audit")})
	require.NoError(t, err)
	b.queueURL = aws.ToString(queue.QueueUrl)
	attrs, err := b.sqs.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl: queue.QueueUrl, AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameQueueArn},
	})
	require.NoError(t, err)
	_, err = b.sns.Subscribe(ctx, &sns.SubscribeInput{
		TopicArn: topic.TopicArn, Protocol: aws.String("sqs"), Endpoint: aws.String(attrs.Attributes["QueueArn"]),
		Attributes: map[string]string{"RawMessageDelivery": "true"},
	})
	require.NoError(t, err)
	return b
}

// publisher returns an SNS publisher of the topic at endpoint, with a single
// attempt so an unreachable broker fails at once.
func (b *broker) publisher(t *testing.T, endpoint string) *snspublisher.Publisher {
	t.Helper()
	client := sns.NewFromConfig(awsTestConfig(t), func(o *sns.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.RetryMaxAttempts = 1
	})
	p, err := snspublisher.New(client, b.topicARN)
	require.NoError(t, err)
	return p
}

// receive waits up to 15 s for one message of the queue and deletes it.
func (b *broker) receive(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		out, err := b.sqs.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl: aws.String(b.queueURL), MaxNumberOfMessages: 1, WaitTimeSeconds: 2,
		})
		require.NoError(t, err)
		if len(out.Messages) == 1 {
			m := out.Messages[0]
			_, err := b.sqs.DeleteMessage(ctx, &sqs.DeleteMessageInput{QueueUrl: aws.String(b.queueURL), ReceiptHandle: m.ReceiptHandle})
			require.NoError(t, err)
			return aws.ToString(m.Body)
		}
	}
	t.Fatal("no message reached the audit queue")
	return ""
}

// identityPool is the user pool of the sign-up: it hands out subjects.
type identityPool struct {
	mu   sync.Mutex
	next int
}

func (p *identityPool) CreateAccount(context.Context, string, string, string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.next++
	return fmt.Sprintf("sub-%d", p.next), nil
}

// documentBucket keeps the faces of the identity document in memory.
type documentBucket struct {
	mu   sync.Mutex
	objs map[string][]byte
}

func (d *documentBucket) Put(_ context.Context, key string, content []byte, _ string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.objs[key] = content
	return nil
}

func (d *documentBucket) Get(_ context.Context, key string) ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	c, ok := d.objs[key]
	if !ok {
		return nil, ports.ErrDocumentNotFound
	}
	return c, nil
}

type fixedNow struct{ t time.Time }

func (c fixedNow) Now() time.Time { return c.t }

func signUp(t *testing.T, svc *orchestrator.RegistrationService, document, email string) domain.Customer {
	t.Helper()
	face := func(fill byte) string {
		return base64.StdEncoding.EncodeToString(append([]byte{0xFF, 0xD8, 0xFF, 0xE0},
			bytes.Repeat([]byte{fill}, domain.MinIdentityDocumentBytes)...))
	}
	c, err := svc.Register(context.Background(), domain.RegistrationRequest{
		DocumentID: document, FullName: "Carolina Pérez Ruiz", Email: email, BirthDate: "1992-04-18",
		Password: "Solventa2026!", DocumentFront: face(0x11), DocumentBack: face(0x22),
	})
	require.NoError(t, err)
	return c
}

func TestTheRegistrationLinkReachesTheAuditQueueThroughTheOutbox(t *testing.T) {
	b := newBroker(t)
	db := newChargeDB(t)
	ctx := context.Background()
	registeredAt := time.Now().UTC().Truncate(time.Second)
	customers := postgres.NewCustomerStore(db)
	svc := orchestrator.NewRegistrationService(customers, &documentBucket{objs: map[string][]byte{}}, &identityPool{},
		fixedNow{t: registeredAt})
	store := postgres.NewOutboxStore(db)

	t.Run("the event is written with the subject and published to the topic", func(t *testing.T) {
		c := signUp(t, svc, "1020345678", "carolina@example.com")

		var stored []byte
		require.NoError(t, db.QueryRowContext(ctx, `SELECT payload FROM outbox_event WHERE event_id = $1`,
			"customer-linked-"+c.ID).Scan(&stored))
		relay, err := outbox.NewRelay(store, b.publisher(t, b.endpoint), func() time.Time { return registeredAt }, outbox.DefaultConfig())
		require.NoError(t, err)
		n, err := relay.ProcessOnce(ctx)
		require.NoError(t, err)
		require.Equal(t, 1, n)

		body := b.receive(t)
		got, err := events.Decode([]byte(body))
		require.NoError(t, err, "raw delivery: the body is the envelope itself")
		want, err := events.Decode(stored)
		require.NoError(t, err)
		assert.Equal(t, want.EventID, got.EventID)
		assert.Equal(t, "CustomerLinked", got.EventType)
		assert.JSONEq(t, string(want.Payload), string(got.Payload), "the exact envelope of the outbox")
		var p map[string]string
		require.NoError(t, json.Unmarshal(got.Payload, &p))
		assert.Equal(t, c.CognitoSub, p["subject"])
		assert.Equal(t, "1020345678", p["document_id"])
		var published bool
		require.NoError(t, db.QueryRowContext(ctx, `SELECT published_at IS NOT NULL FROM outbox_event WHERE event_id = $1`,
			"customer-linked-"+c.ID).Scan(&published))
		assert.True(t, published)
	})

	t.Run("with the broker unreachable the event waits in the outbox and is published later", func(t *testing.T) {
		c := signUp(t, svc, "9988776655", "otra@example.com")
		now := registeredAt
		clock := func() time.Time { return now }

		down, err := outbox.NewRelay(store, b.publisher(t, "http://127.0.0.1:1"), clock, outbox.DefaultConfig())
		require.NoError(t, err)
		_, err = down.ProcessOnce(ctx)
		require.NoError(t, err)

		var (
			attempts  int
			lastError string
			published bool
		)
		require.NoError(t, db.QueryRowContext(ctx, `SELECT attempts, last_error, published_at IS NOT NULL FROM outbox_event
			WHERE event_id = $1`, "customer-linked-"+c.ID).Scan(&attempts, &lastError, &published))
		assert.Equal(t, 1, attempts)
		assert.NotEmpty(t, lastError)
		assert.False(t, published, "never lost: it stays pending")

		now = now.Add(time.Second) // past the first retry wait
		up, err := outbox.NewRelay(store, b.publisher(t, b.endpoint), clock, outbox.DefaultConfig())
		require.NoError(t, err)
		n, err := up.ProcessOnce(ctx)
		require.NoError(t, err)
		require.Equal(t, 1, n)

		got, err := events.Decode([]byte(b.receive(t)))
		require.NoError(t, err)
		assert.Equal(t, "customer-linked-"+c.ID, got.EventID)
		require.NoError(t, db.QueryRowContext(ctx, `SELECT attempts, published_at IS NOT NULL FROM outbox_event
			WHERE event_id = $1`, "customer-linked-"+c.ID).Scan(&attempts, &published))
		assert.Equal(t, 2, attempts)
		assert.True(t, published)
	})
}
