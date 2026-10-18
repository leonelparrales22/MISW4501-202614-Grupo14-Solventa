// Package s3 is a ports.DocumentStore over an Amazon S3 bucket: the documents
// bucket of the platform (infra/terraform/modules/platform) keeps the
// contractual documents of HU-W1.3-03 in AWS (HA-D02). It honours the same
// contract as the filestore adapter, which stays for local runs: one key per
// version, never overwritten with different content, and keys confined to
// the store. The bucket encrypts every object (SSE-S3) and keeps its
// versions; the adapter writes only with a conditional request, so a second
// version of the same key is never created.
package s3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// MaxDocumentSize bounds what Get reads (10 MiB): a contractual document is a
// few kilobytes, so anything bigger is not one of ours.
const MaxDocumentSize = 10 << 20

// ErrInvalidKey is returned for a key that is empty, absolute or tries to
// leave the store, with the same rules as the filestore adapter.
var ErrInvalidKey = errors.New("invalid document key")

// ErrImmutable is returned when a key already holds different content:
// documents are never overwritten, a new version gets a new key.
var ErrImmutable = errors.New("a stored document cannot be overwritten with different content")

// ErrTooLarge is returned by Get for an object bigger than MaxDocumentSize.
var ErrTooLarge = errors.New("the stored document is bigger than the maximum size")

// objectAPI is the part of the S3 client the store uses; tests replace it to
// check the mapping of the errors without a network.
type objectAPI interface {
	PutObject(ctx context.Context, in *awss3.PutObjectInput, opts ...func(*awss3.Options)) (*awss3.PutObjectOutput, error)
	GetObject(ctx context.Context, in *awss3.GetObjectInput, opts ...func(*awss3.Options)) (*awss3.GetObjectOutput, error)
	HeadBucket(ctx context.Context, in *awss3.HeadBucketInput, opts ...func(*awss3.Options)) (*awss3.HeadBucketOutput, error)
}

// Store keeps each document as an object of one bucket.
type Store struct {
	api    objectAPI
	bucket string
}

var _ ports.DocumentStore = (*Store)(nil)

// New builds a Store over the bucket with the given client. It does not call
// S3: Check tells whether the bucket is reachable.
func New(client *awss3.Client, bucket string) (*Store, error) {
	if client == nil {
		return nil, errors.New("s3: the client is required")
	}
	return newStore(client, bucket)
}

func newStore(api objectAPI, bucket string) (*Store, error) {
	if bucket == "" {
		return nil, errors.New("s3: the bucket is required")
	}
	return &Store{api: api, bucket: bucket}, nil
}

// Bucket returns the name of the bucket.
func (s *Store) Bucket() string { return s.bucket }

// Check fails when the bucket does not exist or the service cannot reach it,
// so origination does not start with a document store it cannot use.
func (s *Store) Check(ctx context.Context) error {
	if _, err := s.api.HeadBucket(ctx, &awss3.HeadBucketInput{Bucket: aws.String(s.bucket)}); err != nil {
		return fmt.Errorf("s3: the documents bucket %q is not reachable: %w", s.bucket, err)
	}
	return nil
}

// objectKey maps a key to the object key, refusing any key that would escape
// the store (the rules of filestore.resolve). The key is cleaned, so two
// spellings of the same key are the same document.
func objectKey(key string) (string, error) {
	if key == "" || strings.ContainsRune(key, 0) || strings.Contains(key, "\\") || path.IsAbs(key) {
		return "", ErrInvalidKey
	}
	cleaned := path.Clean(key)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", ErrInvalidKey
	}
	return cleaned, nil
}

// Put stores the content under the key with a conditional write
// (If-None-Match: *), so it never replaces an object. Writing the same content
// again is a no-op; different content under an existing key is ErrImmutable.
// A conflicting concurrent write (409) is retried once.
func (s *Store) Put(ctx context.Context, key string, content []byte, contentType string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	k, err := objectKey(key)
	if err != nil {
		return err
	}
	in := func() *awss3.PutObjectInput {
		put := &awss3.PutObjectInput{
			Bucket:        aws.String(s.bucket),
			Key:           aws.String(k),
			Body:          bytes.NewReader(content),
			ContentLength: aws.Int64(int64(len(content))),
			IfNoneMatch:   aws.String("*"),
		}
		if contentType != "" {
			put.ContentType = aws.String(contentType)
		}
		return put
	}
	for attempt := 1; ; attempt++ {
		_, err := s.api.PutObject(ctx, in())
		switch {
		case err == nil:
			return nil
		case isPreconditionFailed(err):
			return s.compare(ctx, k, content)
		case isConditionalConflict(err) && attempt == 1:
			continue
		default:
			return fmt.Errorf("s3: put %s: %w", k, err)
		}
	}
}

// compare resolves a refused conditional write: the key already exists, so the
// write is a no-op if it holds the same content and ErrImmutable otherwise.
func (s *Store) compare(ctx context.Context, key string, content []byte) error {
	existing, err := s.get(ctx, key)
	if err != nil {
		return fmt.Errorf("s3: read %s after a refused write: %w", key, err)
	}
	if bytes.Equal(existing, content) {
		return nil
	}
	return ErrImmutable
}

// Get returns the content stored under the key, or ports.ErrDocumentNotFound.
func (s *Store) Get(ctx context.Context, key string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	k, err := objectKey(key)
	if err != nil {
		return nil, err
	}
	return s.get(ctx, k)
}

func (s *Store) get(ctx context.Context, key string) ([]byte, error) {
	out, err := s.api.GetObject(ctx, &awss3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		if isNotFound(err) {
			return nil, ports.ErrDocumentNotFound
		}
		return nil, fmt.Errorf("s3: get %s: %w", key, err)
	}
	defer func() { _ = out.Body.Close() }()
	if out.ContentLength != nil && *out.ContentLength > MaxDocumentSize {
		return nil, ErrTooLarge
	}
	content, err := io.ReadAll(io.LimitReader(out.Body, MaxDocumentSize+1))
	if err != nil {
		return nil, fmt.Errorf("s3: read %s: %w", key, err)
	}
	if len(content) > MaxDocumentSize {
		return nil, ErrTooLarge
	}
	return content, nil
}

// isPreconditionFailed tells whether a conditional write was refused because
// the key exists (412 PreconditionFailed).
func isPreconditionFailed(err error) bool {
	return errorCode(err) == "PreconditionFailed" || statusCode(err) == http.StatusPreconditionFailed
}

// isConditionalConflict tells whether a conditional write lost against a
// concurrent write of the same key (409 ConditionalRequestConflict).
func isConditionalConflict(err error) bool {
	return errorCode(err) == "ConditionalRequestConflict" || statusCode(err) == http.StatusConflict
}

// isNotFound tells whether the object does not exist. With s3:ListBucket on
// the bucket S3 answers NoSuchKey (404); without it, 403, which stays an error.
func isNotFound(err error) bool {
	var noSuchKey *types.NoSuchKey
	if errors.As(err, &noSuchKey) {
		return true
	}
	code := errorCode(err)
	return code == "NoSuchKey" || code == "NotFound" || statusCode(err) == http.StatusNotFound
}

func errorCode(err error) string {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode()
	}
	return ""
}

func statusCode(err error) int {
	var respErr *smithyhttp.ResponseError
	if errors.As(err, &respErr) {
		return respErr.HTTPStatusCode()
	}
	return 0
}
