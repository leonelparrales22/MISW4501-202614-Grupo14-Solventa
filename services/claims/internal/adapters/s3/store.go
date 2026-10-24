// Package s3 is a ports.EvidenceStore over an Amazon S3 bucket: the evidence
// bucket of the platform (infra/terraform/modules/platform) keeps the
// photographs and the videos of HU-M2.1-01. The file never crosses the
// service: this adapter signs the URL the phone uploads to, and reads back
// what the storage received.
//
// The signature pins the size, the media type and the digest of the object,
// and S3 itself refuses a body whose digest does not match, so the integrity
// of the evidence does not depend on the client telling the truth.
package s3

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// headAPI and presignAPI are the parts of the S3 client the store uses; the
// tests replace them to check the mapping of the errors without a network.
type headAPI interface {
	HeadObject(ctx context.Context, in *awss3.HeadObjectInput, opts ...func(*awss3.Options)) (*awss3.HeadObjectOutput, error)
	HeadBucket(ctx context.Context, in *awss3.HeadBucketInput, opts ...func(*awss3.Options)) (*awss3.HeadBucketOutput, error)
}

type presignAPI interface {
	PresignPutObject(ctx context.Context, in *awss3.PutObjectInput, opts ...func(*awss3.PresignOptions)) (*v4.PresignedHTTPRequest, error)
}

// Store keeps each evidence as an object of one bucket.
type Store struct {
	api     headAPI
	presign presignAPI
	bucket  string
}

var _ ports.EvidenceStore = (*Store)(nil)

// New builds a Store over the bucket with the given client. It does not call
// S3: Check tells whether the bucket is reachable.
func New(client *awss3.Client, bucket string) (*Store, error) {
	if client == nil {
		return nil, errors.New("s3: the client is required")
	}
	return newStore(client, awss3.NewPresignClient(client), bucket)
}

func newStore(api headAPI, presign presignAPI, bucket string) (*Store, error) {
	if bucket == "" {
		return nil, errors.New("s3: the bucket is required")
	}
	return &Store{api: api, presign: presign, bucket: bucket}, nil
}

// Bucket returns the name of the bucket.
func (s *Store) Bucket() string { return s.bucket }

// Check fails when the bucket does not exist or the service cannot reach it,
// so claims does not start with an evidence store it cannot use.
func (s *Store) Check(ctx context.Context) error {
	if _, err := s.api.HeadBucket(ctx, &awss3.HeadBucketInput{Bucket: aws.String(s.bucket)}); err != nil {
		return fmt.Errorf("s3: the evidence bucket %q is not reachable: %w", s.bucket, err)
	}
	return nil
}

// SignUpload signs a PUT of exactly that object: the media type, the length
// and the digest travel in the signature, so a body that is not the declared
// one is refused by the storage.
func (s *Store) SignUpload(ctx context.Context, key string, u domain.Upload,
	ttl time.Duration) (ports.SignedUpload, error) {
	checksum, err := base64Digest(u.SHA256)
	if err != nil {
		return ports.SignedUpload{}, err
	}
	signed, err := s.presign.PresignPutObject(ctx, &awss3.PutObjectInput{
		Bucket:         aws.String(s.bucket),
		Key:            aws.String(key),
		ContentType:    aws.String(u.ContentType),
		ContentLength:  aws.Int64(u.ByteSize),
		ChecksumSHA256: aws.String(checksum),
	}, awss3.WithPresignExpires(ttl))
	if err != nil {
		return ports.SignedUpload{}, fmt.Errorf("s3: could not sign the upload of %q: %w", key, err)
	}

	// Only the headers that were signed: any other one makes S3 refuse the
	// upload, and a missing one does too.
	headers := map[string]string{}
	for name, values := range signed.SignedHeader {
		if len(values) > 0 && !strings.EqualFold(name, "host") {
			headers[http.CanonicalHeaderKey(name)] = values[0]
		}
	}
	return ports.SignedUpload{
		URL:       signed.URL,
		Headers:   headers,
		ExpiresAt: time.Now().UTC().Add(ttl),
	}, nil
}

// Stat reads back the size and the digest the storage computed.
func (s *Store) Stat(ctx context.Context, key string) (ports.StoredEvidence, error) {
	out, err := s.api.HeadObject(ctx, &awss3.HeadObjectInput{
		Bucket:       aws.String(s.bucket),
		Key:          aws.String(key),
		ChecksumMode: types.ChecksumModeEnabled,
	})
	if err != nil {
		if notFound(err) {
			return ports.StoredEvidence{}, ports.ErrEvidenceNotFound
		}
		return ports.StoredEvidence{}, fmt.Errorf("s3: could not read the evidence %q: %w", key, err)
	}
	digest, err := hexDigest(aws.ToString(out.ChecksumSHA256))
	if err != nil {
		return ports.StoredEvidence{}, fmt.Errorf("s3: the evidence %q has no usable digest: %w", key, err)
	}
	return ports.StoredEvidence{ByteSize: aws.ToInt64(out.ContentLength), SHA256: digest}, nil
}

// base64Digest turns the hexadecimal digest of the contract into the base64
// form the checksum header carries.
func base64Digest(hexDigest string) (string, error) {
	raw, err := hex.DecodeString(hexDigest)
	if err != nil || len(raw) != 32 {
		return "", fmt.Errorf("s3: %q is not a sha-256 digest", hexDigest)
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

// hexDigest is the inverse: what S3 reports, in the form the contract uses.
func hexDigest(checksum string) (string, error) {
	if checksum == "" {
		return "", errors.New("the object has no sha-256 checksum")
	}
	raw, err := base64.StdEncoding.DecodeString(checksum)
	if err != nil || len(raw) != 32 {
		return "", fmt.Errorf("%q is not a sha-256 checksum", checksum)
	}
	return hex.EncodeToString(raw), nil
}

// notFound recognizes the answer S3 gives for a key it does not hold: the
// typed NoSuchKey of GetObject, or the bare 404 that HeadObject returns.
func notFound(err error) bool {
	var noKey *types.NoSuchKey
	if errors.As(err, &noKey) {
		return true
	}
	var notFoundErr *types.NotFound
	if errors.As(err, &notFoundErr) {
		return true
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) && apiErr.ErrorCode() == "NotFound" {
		return true
	}
	var response *smithyhttp.ResponseError
	return errors.As(err, &response) && response.HTTPStatusCode() == http.StatusNotFound
}
