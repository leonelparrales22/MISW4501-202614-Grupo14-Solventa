package evidence_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/evidence"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// The use case that prepares the upload of one evidence (HU-M2.1-01).

var firmadoEn = time.Date(2026, 10, 23, 15, 0, 0, 0, time.UTC)

const sujeto = "9f4ce9cd-85c7-4603-b8e9-0def7c3e1ddd"

type relojFijo struct{ t time.Time }

func (r relojFijo) Now() time.Time { return r.t }

// almacenFalso records what it was asked to sign.
type almacenFalso struct {
	err error

	gotKey    string
	gotUpload domain.Upload
	gotTTL    time.Duration
}

func (a *almacenFalso) SignUpload(_ context.Context, key string, u domain.Upload,
	ttl time.Duration) (ports.SignedUpload, error) {
	a.gotKey, a.gotUpload, a.gotTTL = key, u, ttl
	if a.err != nil {
		return ports.SignedUpload{}, a.err
	}
	return ports.SignedUpload{
		URL:       "https://bucket/" + key,
		Headers:   map[string]string{"Content-Type": u.ContentType},
		ExpiresAt: firmadoEn.Add(ttl),
	}, nil
}

func (a *almacenFalso) Stat(context.Context, string) (ports.StoredEvidence, error) {
	return ports.StoredEvidence{}, ports.ErrEvidenceNotFound
}

func solicitud() domain.UploadRequest {
	return domain.UploadRequest{
		Kind:        "photo",
		ContentType: "image/jpeg",
		ByteSize:    312044,
		SHA256:      strings.Repeat("a", 64),
	}
}

func servicio(almacen *almacenFalso) *evidence.Service {
	return evidence.NewService(almacen, relojFijo{t: firmadoEn})
}

// The key hangs from the subject of the token: one insured cannot write in
// the directory of another.
func TestTheUploadIsSignedUnderTheSubjectOfTheToken(t *testing.T) {
	almacen := &almacenFalso{}

	upload, err := servicio(almacen).SignUpload(context.Background(), sujeto, solicitud())

	require.NoError(t, err)
	assert.Equal(t, "evidence/"+sujeto+"/"+strings.Repeat("a", 64)+".jpg", upload.ObjectKey)
	assert.Equal(t, upload.ObjectKey, almacen.gotKey)
	assert.Equal(t, "image/jpeg", upload.Headers["Content-Type"])
	assert.Equal(t, evidence.UploadTTL, almacen.gotTTL)
	assert.Equal(t, firmadoEn.Add(evidence.UploadTTL), upload.ExpiresAt)
}

// What is signed is the declaration: the size and the digest travel with it,
// so the storage can refuse another file.
func TestWhatIsSignedIsWhatWasDeclared(t *testing.T) {
	almacen := &almacenFalso{}

	_, err := servicio(almacen).SignUpload(context.Background(), sujeto, solicitud())

	require.NoError(t, err)
	assert.Equal(t, int64(312044), almacen.gotUpload.ByteSize)
	assert.Equal(t, strings.Repeat("a", 64), almacen.gotUpload.SHA256)
	assert.Equal(t, domain.KindPhoto, almacen.gotUpload.Kind)
}

func TestADeclarationThatBreaksItsRulesIsNotSigned(t *testing.T) {
	almacen := &almacenFalso{}
	r := solicitud()
	r.ByteSize = domain.MaxPhotoBytes + 1

	_, err := servicio(almacen).SignUpload(context.Background(), sujeto, r)

	var invalid *domain.ValidationError
	require.ErrorAs(t, err, &invalid)
	assert.Empty(t, almacen.gotKey, "the store is not even asked")
}

func TestAStoreThatCannotSign(t *testing.T) {
	almacen := &almacenFalso{err: errors.New("the bucket is not reachable")}

	_, err := servicio(almacen).SignUpload(context.Background(), sujeto, solicitud())

	require.Error(t, err)
	var invalid *domain.ValidationError
	assert.False(t, errors.As(err, &invalid), "it is a failure, not an invalid request")
}
