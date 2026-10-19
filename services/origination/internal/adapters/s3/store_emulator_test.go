package s3_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/localstack"

	s3adapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/s3"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// emulatorImage is the last LocalStack image that starts without an auth
// token (decision D-10 a of HA-D02): from 2026.03 on the image requires one.
const emulatorImage = "localstack/localstack:4.14.0"

// newEmulatedClient starts LocalStack with only S3 and returns a client of it
// and a fresh, versioned bucket like the documents bucket of the platform.
func newEmulatedClient(t *testing.T) (*awss3.Client, string) {
	t.Helper()
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx := context.Background()

	ctr, err := localstack.Run(ctx, emulatorImage, testcontainers.WithEnv(map[string]string{"SERVICES": "s3"}))
	testcontainers.CleanupContainer(t, ctr)
	require.NoError(t, err, "start LocalStack")
	host, err := ctr.Host(ctx)
	require.NoError(t, err)
	port, err := ctr.MappedPort(ctx, "4566/tcp")
	require.NoError(t, err)

	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion("us-east-2"),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")))
	require.NoError(t, err)
	client := awss3.NewFromConfig(cfg, func(o *awss3.Options) {
		o.BaseEndpoint = aws.String(fmt.Sprintf("http://%s:%s", host, port.Port()))
		o.UsePathStyle = true
	})

	bucket := "solventa-test-documents"
	_, err = client.CreateBucket(ctx, &awss3.CreateBucketInput{
		Bucket:                    aws.String(bucket),
		CreateBucketConfiguration: &types.CreateBucketConfiguration{LocationConstraint: types.BucketLocationConstraintUsEast2},
	})
	require.NoError(t, err)
	_, err = client.PutBucketVersioning(ctx, &awss3.PutBucketVersioningInput{
		Bucket:                  aws.String(bucket),
		VersioningConfiguration: &types.VersioningConfiguration{Status: types.BucketVersioningStatusEnabled},
	})
	require.NoError(t, err)
	return client, bucket
}

// TestTheStoreAgainstAnEmulatedS3 checks the contract of ports.DocumentStore
// over the S3 API (LocalStack): the conditional write, the no-op, the
// immutability, the unknown key, a missing bucket and two simultaneous writes
// of the same key.
func TestTheStoreAgainstAnEmulatedS3(t *testing.T) {
	client, bucket := newEmulatedClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	store, err := s3adapter.New(client, bucket)
	require.NoError(t, err)

	t.Run("the bucket is reachable", func(t *testing.T) {
		require.NoError(t, store.Check(ctx))
	})

	t.Run("put then get", func(t *testing.T) {
		pdf := []byte("%PDF-1.4 contract of pol-1")
		require.NoError(t, store.Put(ctx, "policies/pol-1/contract-v1.pdf", pdf, "application/pdf"))

		got, err := store.Get(ctx, "policies/pol-1/contract-v1.pdf")
		require.NoError(t, err)
		assert.Equal(t, pdf, got)

		head, err := client.HeadObject(ctx, &awss3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String("policies/pol-1/contract-v1.pdf")})
		require.NoError(t, err)
		assert.Equal(t, "application/pdf", aws.ToString(head.ContentType))
	})

	t.Run("the same content again is a no-op and keeps one version", func(t *testing.T) {
		require.NoError(t, store.Put(ctx, "policies/pol-2/contract-v1.pdf", []byte("one"), "application/pdf"))
		require.NoError(t, store.Put(ctx, "policies/pol-2/contract-v1.pdf", []byte("one"), "application/pdf"))
		assert.Equal(t, 1, versions(ctx, t, client, bucket, "policies/pol-2/contract-v1.pdf"))
	})

	t.Run("different content under an existing key is refused", func(t *testing.T) {
		require.NoError(t, store.Put(ctx, "policies/pol-3/contract-v1.pdf", []byte("one"), "application/pdf"))

		err := store.Put(ctx, "policies/pol-3/contract-v1.pdf", []byte("two"), "application/pdf")

		assert.ErrorIs(t, err, s3adapter.ErrImmutable)
		got, err := store.Get(ctx, "policies/pol-3/contract-v1.pdf")
		require.NoError(t, err)
		assert.Equal(t, []byte("one"), got, "the stored document is untouched")
		assert.Equal(t, 1, versions(ctx, t, client, bucket, "policies/pol-3/contract-v1.pdf"))
	})

	t.Run("an unknown key is not found", func(t *testing.T) {
		_, err := store.Get(ctx, "policies/none/contract-v1.pdf")
		assert.ErrorIs(t, err, ports.ErrDocumentNotFound)
	})

	t.Run("simultaneous writes of the same document all succeed once", func(t *testing.T) {
		const writers = 8
		key := "policies/pol-4/contract-v1.pdf"
		errs := make([]error, writers)
		var wg sync.WaitGroup
		for i := range errs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs[i] = store.Put(ctx, key, []byte("same"), "application/pdf")
			}()
		}
		wg.Wait()
		for _, err := range errs {
			assert.NoError(t, err)
		}
		assert.Equal(t, 1, versions(ctx, t, client, bucket, key))
	})

	t.Run("simultaneous writes of different content: one wins, the other is refused", func(t *testing.T) {
		key := "policies/pol-5/contract-v1.pdf"
		errs := make([]error, 2)
		var wg sync.WaitGroup
		for i := range errs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs[i] = store.Put(ctx, key, []byte(fmt.Sprintf("content %d", i)), "application/pdf")
			}()
		}
		wg.Wait()
		ok, refused := 0, 0
		for _, err := range errs {
			switch {
			case err == nil:
				ok++
			case errors.Is(err, s3adapter.ErrImmutable):
				refused++
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}
		assert.Equal(t, 1, ok)
		assert.Equal(t, 1, refused)
		assert.Equal(t, 1, versions(ctx, t, client, bucket, key))
	})

	t.Run("a missing bucket fails the check", func(t *testing.T) {
		missing, err := s3adapter.New(client, "solventa-test-missing")
		require.NoError(t, err)
		assert.Error(t, missing.Check(ctx))
	})
}

// versions counts the stored versions of the key.
func versions(ctx context.Context, t *testing.T, client *awss3.Client, bucket, key string) int {
	t.Helper()
	out, err := client.ListObjectVersions(ctx, &awss3.ListObjectVersionsInput{Bucket: aws.String(bucket), Prefix: aws.String(key)})
	require.NoError(t, err)
	return len(out.Versions)
}
