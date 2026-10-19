package s3

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// fakeS3 is an in-memory bucket with the conditional write of S3: a put with
// If-None-Match: * on an existing key fails with 412. putErrs and getErrs are
// returned (in order) before the normal behaviour.
type fakeS3 struct {
	mu          sync.Mutex
	objects     map[string][]byte
	types       map[string]string
	putErrs     []error
	getErrs     []error
	headErr     error
	puts        int
	lastPut     *awss3.PutObjectInput
	reportedLen *int64
}

func newFake() *fakeS3 {
	return &fakeS3{objects: map[string][]byte{}, types: map[string]string{}}
}

func (f *fakeS3) PutObject(_ context.Context, in *awss3.PutObjectInput, _ ...func(*awss3.Options)) (*awss3.PutObjectOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.puts++
	f.lastPut = in
	if len(f.putErrs) > 0 {
		err := f.putErrs[0]
		f.putErrs = f.putErrs[1:]
		return nil, err
	}
	key := aws.ToString(in.Key)
	if _, ok := f.objects[key]; ok && aws.ToString(in.IfNoneMatch) == "*" {
		return nil, apiError(http.StatusPreconditionFailed, "PreconditionFailed")
	}
	body, err := io.ReadAll(in.Body)
	if err != nil {
		return nil, err
	}
	f.objects[key] = body
	f.types[key] = aws.ToString(in.ContentType)
	return &awss3.PutObjectOutput{}, nil
}

func (f *fakeS3) GetObject(_ context.Context, in *awss3.GetObjectInput, _ ...func(*awss3.Options)) (*awss3.GetObjectOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.getErrs) > 0 {
		err := f.getErrs[0]
		f.getErrs = f.getErrs[1:]
		return nil, err
	}
	body, ok := f.objects[aws.ToString(in.Key)]
	if !ok {
		return nil, &types.NoSuchKey{}
	}
	length := aws.Int64(int64(len(body)))
	if f.reportedLen != nil {
		length = f.reportedLen
	}
	return &awss3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(body)), ContentLength: length}, nil
}

func (f *fakeS3) HeadBucket(context.Context, *awss3.HeadBucketInput, ...func(*awss3.Options)) (*awss3.HeadBucketOutput, error) {
	return &awss3.HeadBucketOutput{}, f.headErr
}

// apiError builds the error the SDK returns for an S3 error response: an API
// error with its code inside a response error with the HTTP status.
func apiError(status int, code string) error {
	return &smithyhttp.ResponseError{
		Response: &smithyhttp.Response{Response: &http.Response{StatusCode: status}},
		Err:      &smithy.GenericAPIError{Code: code, Message: code},
	}
}

func newTestStore(t *testing.T, f *fakeS3) *Store {
	t.Helper()
	s, err := newStore(f, "solventa-stg-documents")
	require.NoError(t, err)
	return s
}

func TestNewNeedsAClientAndABucket(t *testing.T) {
	_, err := New(nil, "bucket")
	assert.Error(t, err)
	_, err = New(awss3.New(awss3.Options{Region: "us-east-2"}), "")
	assert.Error(t, err)

	s, err := New(awss3.New(awss3.Options{Region: "us-east-2"}), "solventa-stg-documents")
	require.NoError(t, err)
	assert.Equal(t, "solventa-stg-documents", s.Bucket())
}

func TestPutThenGet(t *testing.T) {
	f := newFake()
	s := newTestStore(t, f)
	ctx := context.Background()

	require.NoError(t, s.Put(ctx, "policies/pol-1/contract-v1.pdf", []byte("%PDF-1"), "application/pdf"))

	got, err := s.Get(ctx, "policies/pol-1/contract-v1.pdf")
	require.NoError(t, err)
	assert.Equal(t, []byte("%PDF-1"), got)
	assert.Equal(t, "application/pdf", f.types["policies/pol-1/contract-v1.pdf"])
	assert.Equal(t, "solventa-stg-documents", aws.ToString(f.lastPut.Bucket))
	assert.Equal(t, "*", aws.ToString(f.lastPut.IfNoneMatch), "the write never replaces an object")
	assert.Equal(t, int64(6), aws.ToInt64(f.lastPut.ContentLength))
}

func TestPutWithoutContentTypeLeavesTheDefault(t *testing.T) {
	f := newFake()
	require.NoError(t, newTestStore(t, f).Put(context.Background(), "k/a.bin", []byte("x"), ""))
	assert.Nil(t, f.lastPut.ContentType)
}

func TestTheKeyIsCleaned(t *testing.T) {
	f := newFake()
	s := newTestStore(t, f)
	ctx := context.Background()
	require.NoError(t, s.Put(ctx, "policies//pol-1/./contract-v1.pdf", []byte("one"), "application/pdf"))

	assert.Contains(t, f.objects, "policies/pol-1/contract-v1.pdf")
	got, err := s.Get(ctx, "policies/pol-1/contract-v1.pdf")
	require.NoError(t, err)
	assert.Equal(t, []byte("one"), got)
}

func TestGetUnknownKey(t *testing.T) {
	_, err := newTestStore(t, newFake()).Get(context.Background(), "policies/none/contract-v1.pdf")
	assert.ErrorIs(t, err, ports.ErrDocumentNotFound)
}

func TestNotFoundIsReadFromTheCodeAndTheStatus(t *testing.T) {
	for name, cause := range map[string]error{
		"code NoSuchKey": &smithy.GenericAPIError{Code: "NoSuchKey"},
		"code NotFound":  &smithy.GenericAPIError{Code: "NotFound"},
		"status 404":     apiError(http.StatusNotFound, ""),
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake()
			f.getErrs = []error{cause}
			_, err := newTestStore(t, f).Get(context.Background(), "k/a.pdf")
			assert.ErrorIs(t, err, ports.ErrDocumentNotFound)
		})
	}
}

func TestAccessDeniedIsNotANotFound(t *testing.T) {
	f := newFake()
	f.getErrs = []error{apiError(http.StatusForbidden, "AccessDenied")}

	_, err := newTestStore(t, f).Get(context.Background(), "k/a.pdf")

	require.Error(t, err)
	assert.NotErrorIs(t, err, ports.ErrDocumentNotFound)
	assert.Contains(t, err.Error(), "k/a.pdf")
}

func TestPutTheSameContentAgainIsANoOp(t *testing.T) {
	f := newFake()
	s := newTestStore(t, f)
	ctx := context.Background()
	require.NoError(t, s.Put(ctx, "k/a.pdf", []byte("one"), "application/pdf"))

	assert.NoError(t, s.Put(ctx, "k/a.pdf", []byte("one"), "application/pdf"))
	assert.Equal(t, 2, f.puts, "the second write is refused by S3 and resolved by reading")
}

func TestPutDifferentContentUnderAnExistingKeyIsRefused(t *testing.T) {
	f := newFake()
	s := newTestStore(t, f)
	ctx := context.Background()
	require.NoError(t, s.Put(ctx, "k/a.pdf", []byte("one"), "application/pdf"))

	err := s.Put(ctx, "k/a.pdf", []byte("two"), "application/pdf")

	assert.ErrorIs(t, err, ErrImmutable)
	got, _ := s.Get(ctx, "k/a.pdf")
	assert.Equal(t, []byte("one"), got, "the stored document is untouched")
}

func TestAPreconditionIsReadFromTheCodeOrTheStatus(t *testing.T) {
	for name, refused := range map[string]error{
		"code only":   &smithy.GenericAPIError{Code: "PreconditionFailed"},
		"status only": apiError(http.StatusPreconditionFailed, ""),
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake()
			f.objects["k/a.pdf"] = []byte("one")
			f.putErrs = []error{refused}
			s := newTestStore(t, f)

			assert.NoError(t, s.Put(context.Background(), "k/a.pdf", []byte("one"), ""))
			f.putErrs = []error{refused}
			assert.ErrorIs(t, s.Put(context.Background(), "k/a.pdf", []byte("two"), ""), ErrImmutable)
		})
	}
}

func TestARefusedWriteThatCannotBeReadIsAnError(t *testing.T) {
	f := newFake()
	f.putErrs = []error{apiError(http.StatusPreconditionFailed, "PreconditionFailed")}
	f.getErrs = []error{apiError(http.StatusInternalServerError, "InternalError")}

	err := newTestStore(t, f).Put(context.Background(), "k/a.pdf", []byte("one"), "")

	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrImmutable)
	assert.Contains(t, err.Error(), "after a refused write")
}

func TestAConcurrentConflictIsRetriedOnce(t *testing.T) {
	f := newFake()
	f.putErrs = []error{apiError(http.StatusConflict, "ConditionalRequestConflict")}
	s := newTestStore(t, f)

	require.NoError(t, s.Put(context.Background(), "k/a.pdf", []byte("one"), "application/pdf"))
	assert.Equal(t, 2, f.puts)
	assert.Equal(t, []byte("one"), f.objects["k/a.pdf"])
}

func TestASecondConflictIsAnError(t *testing.T) {
	conflict := apiError(http.StatusConflict, "ConditionalRequestConflict")
	f := newFake()
	f.putErrs = []error{conflict, conflict}

	err := newTestStore(t, f).Put(context.Background(), "k/a.pdf", []byte("one"), "")

	require.Error(t, err)
	assert.Equal(t, 2, f.puts)
	assert.Contains(t, err.Error(), "put k/a.pdf")
}

func TestAnyOtherPutErrorIsReturned(t *testing.T) {
	f := newFake()
	f.putErrs = []error{apiError(http.StatusForbidden, "AccessDenied")}

	err := newTestStore(t, f).Put(context.Background(), "k/a.pdf", []byte("one"), "")

	require.Error(t, err)
	assert.Equal(t, 1, f.puts)
	assert.Equal(t, "AccessDenied", errorCode(err))
}

func TestKeysCannotEscapeTheStore(t *testing.T) {
	f := newFake()
	s := newTestStore(t, f)
	ctx := context.Background()
	for _, key := range []string{"", "..", "../outside.pdf", "a/../../outside.pdf", "/etc/passwd", `a\b`, "a\x00b", "."} {
		assert.ErrorIs(t, s.Put(ctx, key, []byte("x"), ""), ErrInvalidKey, "put %q", key)
		_, err := s.Get(ctx, key)
		assert.ErrorIs(t, err, ErrInvalidKey, "get %q", key)
	}
	assert.Zero(t, f.puts, "an invalid key never reaches S3")
}

func TestCanceledContext(t *testing.T) {
	f := newFake()
	s := newTestStore(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	assert.ErrorIs(t, s.Put(ctx, "k/a.pdf", []byte("x"), ""), context.Canceled)
	_, err := s.Get(ctx, "k/a.pdf")
	assert.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, f.puts)
}

func TestAnObjectBiggerThanTheMaximumIsRefused(t *testing.T) {
	t.Run("by its declared length", func(t *testing.T) {
		f := newFake()
		f.objects["k/a.pdf"] = []byte("small")
		f.reportedLen = aws.Int64(MaxDocumentSize + 1)
		_, err := newTestStore(t, f).Get(context.Background(), "k/a.pdf")
		assert.ErrorIs(t, err, ErrTooLarge)
	})
	t.Run("by what it really holds", func(t *testing.T) {
		f := newFake()
		f.objects["k/a.pdf"] = bytes.Repeat([]byte("x"), MaxDocumentSize+1)
		var unknown int64 = -1
		f.reportedLen = &unknown
		_, err := newTestStore(t, f).Get(context.Background(), "k/a.pdf")
		assert.ErrorIs(t, err, ErrTooLarge)
	})
	t.Run("the maximum itself is fine", func(t *testing.T) {
		f := newFake()
		f.objects["k/a.pdf"] = bytes.Repeat([]byte("x"), MaxDocumentSize)
		got, err := newTestStore(t, f).Get(context.Background(), "k/a.pdf")
		require.NoError(t, err)
		assert.Len(t, got, MaxDocumentSize)
	})
}

func TestABodyThatFailsToReadIsAnError(t *testing.T) {
	s := newTestStore(t, newFake())
	s.api = brokenBody{}

	_, err := s.Get(context.Background(), "k/a.pdf")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "read k/a.pdf")
}

func TestCheck(t *testing.T) {
	f := newFake()
	s := newTestStore(t, f)
	assert.NoError(t, s.Check(context.Background()))

	f.headErr = apiError(http.StatusNotFound, "NotFound")
	err := s.Check(context.Background())
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), `"solventa-stg-documents" is not reachable`), err.Error())
}

// brokenBody answers a GetObject whose body fails half way.
type brokenBody struct{}

func (brokenBody) PutObject(context.Context, *awss3.PutObjectInput, ...func(*awss3.Options)) (*awss3.PutObjectOutput, error) {
	return &awss3.PutObjectOutput{}, nil
}

func (brokenBody) GetObject(context.Context, *awss3.GetObjectInput, ...func(*awss3.Options)) (*awss3.GetObjectOutput, error) {
	return &awss3.GetObjectOutput{Body: io.NopCloser(failingReader{})}, nil
}

func (brokenBody) HeadBucket(context.Context, *awss3.HeadBucketInput, ...func(*awss3.Options)) (*awss3.HeadBucketOutput, error) {
	return &awss3.HeadBucketOutput{}, nil
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("connection reset") }
