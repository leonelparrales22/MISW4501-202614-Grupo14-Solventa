package s3

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// The evidence store over S3, with the client replaced: what is checked here
// is what gets signed and how the answers of S3 are read.

const (
	llave  = "evidence/sub-carolina/aaaa.jpg"
	digest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

type headFalso struct {
	out    *awss3.HeadObjectOutput
	err    error
	bucket error

	gotKey string
}

func (h *headFalso) HeadObject(_ context.Context, in *awss3.HeadObjectInput,
	_ ...func(*awss3.Options)) (*awss3.HeadObjectOutput, error) {
	h.gotKey = aws.ToString(in.Key)
	if h.err != nil {
		return nil, h.err
	}
	return h.out, nil
}

func (h *headFalso) HeadBucket(context.Context, *awss3.HeadBucketInput,
	...func(*awss3.Options)) (*awss3.HeadBucketOutput, error) {
	return &awss3.HeadBucketOutput{}, h.bucket
}

type presignFalso struct {
	err error

	gotInput *awss3.PutObjectInput
}

func (p *presignFalso) PresignPutObject(_ context.Context, in *awss3.PutObjectInput,
	_ ...func(*awss3.PresignOptions)) (*v4.PresignedHTTPRequest, error) {
	p.gotInput = in
	if p.err != nil {
		return nil, p.err
	}
	return &v4.PresignedHTTPRequest{
		URL:    "https://bucket.s3.us-east-2.amazonaws.com/" + aws.ToString(in.Key) + "?X-Amz-Signature=abc",
		Method: http.MethodPut,
		SignedHeader: http.Header{
			"Host":                  []string{"bucket.s3.us-east-2.amazonaws.com"},
			"Content-Type":          []string{aws.ToString(in.ContentType)},
			"Content-Length":        []string{"312044"},
			"X-Amz-Checksum-Sha256": []string{aws.ToString(in.ChecksumSHA256)},
		},
	}, nil
}

func tienda(t *testing.T, head *headFalso, presign *presignFalso) *Store {
	t.Helper()
	s, err := newStore(head, presign, "solventa-evidence")
	require.NoError(t, err)
	return s
}

func subida() domain.Upload {
	return domain.Upload{
		Kind: domain.KindPhoto, ContentType: "image/jpeg", ByteSize: 312044, SHA256: digest,
	}
}

// The signature pins the type, the length and the digest: the storage refuses
// a body that is not the declared one.
func TestTheSignaturePinsTheObject(t *testing.T) {
	presign := &presignFalso{}

	signed, err := tienda(t, &headFalso{}, presign).SignUpload(
		context.Background(), llave, subida(), 10*time.Minute)

	require.NoError(t, err)
	assert.Equal(t, "image/jpeg", aws.ToString(presign.gotInput.ContentType))
	assert.Equal(t, int64(312044), aws.ToInt64(presign.gotInput.ContentLength))

	raw, _ := hex.DecodeString(digest)
	assert.Equal(t, base64.StdEncoding.EncodeToString(raw),
		aws.ToString(presign.gotInput.ChecksumSHA256), "the checksum travels in base64")
	assert.Contains(t, signed.URL, "X-Amz-Signature")
	assert.WithinDuration(t, time.Now().UTC().Add(10*time.Minute), signed.ExpiresAt, time.Minute)
}

// The client has to send exactly the signed headers; the host is not one of
// them, it is the URL.
func TestTheSignedHeadersTravelBackWithoutTheHost(t *testing.T) {
	signed, err := tienda(t, &headFalso{}, &presignFalso{}).SignUpload(
		context.Background(), llave, subida(), time.Minute)

	require.NoError(t, err)
	assert.Equal(t, "image/jpeg", signed.Headers["Content-Type"])
	assert.Equal(t, "312044", signed.Headers["Content-Length"])
	assert.NotEmpty(t, signed.Headers["X-Amz-Checksum-Sha256"])
	assert.NotContains(t, signed.Headers, "Host")
}

func TestADigestThatIsNotOneIsNotSigned(t *testing.T) {
	u := subida()
	u.SHA256 = "nada"

	_, err := tienda(t, &headFalso{}, &presignFalso{}).SignUpload(
		context.Background(), llave, u, time.Minute)

	assert.Error(t, err)
}

// Stat reports the digest S3 computed when it received the object, in the
// form the contract uses.
func TestWhatTheStorageComputedComesBackAsHexadecimal(t *testing.T) {
	raw, _ := hex.DecodeString(digest)
	head := &headFalso{out: &awss3.HeadObjectOutput{
		ContentLength:  aws.Int64(312044),
		ChecksumSHA256: aws.String(base64.StdEncoding.EncodeToString(raw)),
	}}

	stored, err := tienda(t, head, &presignFalso{}).Stat(context.Background(), llave)

	require.NoError(t, err)
	assert.Equal(t, int64(312044), stored.ByteSize)
	assert.Equal(t, digest, stored.SHA256)
	assert.Equal(t, llave, head.gotKey)
}

func TestAKeyWithoutAnObject(t *testing.T) {
	for name, err := range map[string]error{
		"typed not found": &types.NotFound{},
		"bare 404": &smithyhttp.ResponseError{
			Response: &smithyhttp.Response{Response: &http.Response{StatusCode: http.StatusNotFound}},
			Err:      errors.New("not found"),
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, got := tienda(t, &headFalso{err: err}, &presignFalso{}).Stat(context.Background(), llave)

			assert.ErrorIs(t, got, ports.ErrEvidenceNotFound)
		})
	}
}

// A storage that computes no SHA-256 is not an error: it returns the size
// and an empty digest, and the use case settles it with that.
func TestAnObjectWithoutItsChecksum(t *testing.T) {
	head := &headFalso{out: &awss3.HeadObjectOutput{ContentLength: aws.Int64(10)}}

	stored, err := tienda(t, head, &presignFalso{}).Stat(context.Background(), llave)

	require.NoError(t, err)
	assert.Equal(t, int64(10), stored.ByteSize)
	assert.Empty(t, stored.SHA256)
}

func TestABucketThatIsNotReachable(t *testing.T) {
	head := &headFalso{bucket: errors.New("no such bucket")}

	err := tienda(t, head, &presignFalso{}).Check(context.Background())

	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "solventa-evidence"))
}

func TestTheStoreNeedsItsBucket(t *testing.T) {
	_, err := newStore(&headFalso{}, &presignFalso{}, "")

	assert.Error(t, err)
}

// --- Reads of the objects (HU-W2.1-01) -----------------------------------------

type getFalso struct {
	body   string
	err    error
	gotIn  *awss3.GetObjectInput
	closed bool
}

type closeSpy struct {
	*strings.Reader
	spy *getFalso
}

func (c closeSpy) Close() error { c.spy.closed = true; return nil }

func (g *getFalso) GetObject(_ context.Context, in *awss3.GetObjectInput, _ ...func(*awss3.Options)) (*awss3.GetObjectOutput, error) {
	g.gotIn = in
	if g.err != nil {
		return nil, g.err
	}
	return &awss3.GetObjectOutput{Body: closeSpy{strings.NewReader(g.body), g}}, nil
}

type presignGetFalso struct {
	err   error
	gotIn *awss3.GetObjectInput
}

func (p *presignGetFalso) PresignGetObject(_ context.Context, in *awss3.GetObjectInput,
	_ ...func(*awss3.PresignOptions)) (*v4.PresignedHTTPRequest, error) {
	p.gotIn = in
	if p.err != nil {
		return nil, p.err
	}
	return &v4.PresignedHTTPRequest{URL: "https://bucket.s3.us-east-2.amazonaws.com/" + aws.ToString(in.Key) + "?X-Amz-Signature=abc",
		Method: http.MethodGet}, nil
}

func tiendaDeLectura(t *testing.T, get *getFalso, presign *presignGetFalso) *Store {
	t.Helper()
	s := tienda(t, &headFalso{}, &presignFalso{})
	s.get, s.presGet = get, presign
	return s
}

func TestHeadReadsOnlyTheStartOfTheObject(t *testing.T) {
	get := &getFalso{body: "%PDF-1.7 and then a lot more"}
	s := tiendaDeLectura(t, get, &presignGetFalso{})

	head, err := s.Head(context.Background(), "supports/sub-ana/sup-1", 8)

	require.NoError(t, err)
	assert.Equal(t, "%PDF-1.7", string(head))
	assert.Equal(t, "bytes=0-7", aws.ToString(get.gotIn.Range), "a ranged read, never the whole file")
	assert.Equal(t, "supports/sub-ana/sup-1", aws.ToString(get.gotIn.Key))
	assert.True(t, get.closed)
}

func TestHeadOfAMissingObjectIsNotFound(t *testing.T) {
	s := tiendaDeLectura(t, &getFalso{err: &types.NoSuchKey{}}, &presignGetFalso{})

	_, err := s.Head(context.Background(), "supports/sub-ana/none", 8)

	assert.ErrorIs(t, err, ports.ErrEvidenceNotFound)
}

func TestHeadReportsOtherFailuresAndBadArguments(t *testing.T) {
	s := tiendaDeLectura(t, &getFalso{err: errors.New("throttled")}, &presignGetFalso{})
	_, err := s.Head(context.Background(), "k", 8)
	require.Error(t, err)
	assert.NotErrorIs(t, err, ports.ErrEvidenceNotFound)

	_, err = s.Head(context.Background(), "k", 0)
	assert.Error(t, err)

	_, err = tienda(t, &headFalso{}, &presignFalso{}).Head(context.Background(), "k", 8)
	assert.Error(t, err, "a store built without the read client says so")
}

func TestADownloadIsSignedWithAnExpiry(t *testing.T) {
	presign := &presignGetFalso{}
	s := tiendaDeLectura(t, &getFalso{}, presign)

	signed, err := s.SignDownload(context.Background(), llave, 10*time.Minute)

	require.NoError(t, err)
	assert.Contains(t, signed.URL, llave)
	assert.WithinDuration(t, time.Now().Add(10*time.Minute), signed.ExpiresAt, 5*time.Second)
	assert.Equal(t, llave, aws.ToString(presign.gotIn.Key))
}

func TestADownloadThatCannotBeSignedIsAnError(t *testing.T) {
	s := tiendaDeLectura(t, &getFalso{}, &presignGetFalso{err: errors.New("no credentials")})
	_, err := s.SignDownload(context.Background(), llave, time.Minute)
	assert.Error(t, err)

	_, err = tienda(t, &headFalso{}, &presignFalso{}).SignDownload(context.Background(), llave, time.Minute)
	assert.Error(t, err)
}
