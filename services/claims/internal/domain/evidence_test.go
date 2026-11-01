package domain_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
)

// The evidence of the claim (HU-M2.1-01): what the client may declare before
// uploading, and what the filing accepts afterwards.

func validUpload() domain.UploadRequest {
	return domain.UploadRequest{
		Kind:        "photo",
		ContentType: "image/jpeg",
		ByteSize:    312044,
		SHA256:      strings.Repeat("a", 64),
	}
}

func TestAnUploadLandsUnderTheSubjectThatAskedForIt(t *testing.T) {
	u, err := validUpload().Validate()

	require.NoError(t, err)
	assert.Equal(t, "evidence/"+subject+"/"+strings.Repeat("a", 64)+".jpg", u.Key(subject))
	assert.Equal(t, "evidence/otro/"+strings.Repeat("a", 64)+".jpg", u.Key("otro"))
}

// The key comes from the digest, so asking twice for the same file points at
// the same object instead of leaving two copies.
func TestTheSameFileAlwaysGetsTheSameKey(t *testing.T) {
	first, err := validUpload().Validate()
	require.NoError(t, err)
	second, err := validUpload().Validate()
	require.NoError(t, err)

	assert.Equal(t, first.Key(subject), second.Key(subject))
}

func TestAVideoIsAnMp4AndEndsAsOne(t *testing.T) {
	r := validUpload()
	r.Kind, r.ContentType, r.ByteSize = "video", "video/mp4", 18_000_000

	u, err := r.Validate()

	require.NoError(t, err)
	assert.True(t, strings.HasSuffix(u.Key(subject), ".mp4"))
}

// Each kind travels in one format: the capture of the application produces no
// other one.
func TestAKindAndAFormatThatDoNotMatch(t *testing.T) {
	for name, change := range map[string]func(*domain.UploadRequest){
		"photo as mp4":   func(r *domain.UploadRequest) { r.ContentType = "video/mp4" },
		"video as jpeg":  func(r *domain.UploadRequest) { r.Kind = "video" },
		"another format": func(r *domain.UploadRequest) { r.ContentType = "image/png" },
	} {
		t.Run(name, func(t *testing.T) {
			r := validUpload()
			change(&r)

			_, err := r.Validate()

			assert.Equal(t, domain.CodeInvalidFormat, codes(t, err)["content_type"])
		})
	}
}

func TestAnUploadOutOfSize(t *testing.T) {
	for name, size := range map[string]int64{
		"empty":               0,
		"too small":           512,
		"bigger than a photo": domain.MaxPhotoBytes + 1,
	} {
		t.Run(name, func(t *testing.T) {
			r := validUpload()
			r.ByteSize = size

			_, err := r.Validate()

			assert.Equal(t, domain.CodeOutOfRange, codes(t, err)["byte_size"])
		})
	}
}

func TestADigestThatIsNotOne(t *testing.T) {
	for name, digest := range map[string]string{
		"empty":     "",
		"short":     strings.Repeat("a", 32),
		"uppercase": strings.Repeat("A", 64),
		"not hex":   strings.Repeat("z", 64),
	} {
		t.Run(name, func(t *testing.T) {
			r := validUpload()
			r.SHA256 = digest

			_, err := r.Validate()

			assert.NotEmpty(t, codes(t, err)["sha256"])
		})
	}
}

func TestAKeyOfSomebodyElseIsNotOwned(t *testing.T) {
	assert.True(t, domain.OwnedBy("evidence/"+subject+"/abc.jpg", subject))
	assert.False(t, domain.OwnedBy("evidence/otro/abc.jpg", subject))
	assert.False(t, domain.OwnedBy("identity-documents/abc.jpg", subject))
}

// Criterion 3 of HU-M2.2-01: the minimum evidence is part of what the filing
// needs, like any other required field.
func TestAFilingWithoutTheMinimumEvidence(t *testing.T) {
	r := validRequest()
	r.Evidence = r.Evidence[:domain.MinPhotos-1]

	_, err := r.Validate(now)

	assert.Equal(t, domain.CodeOutOfRange, codes(t, err)["evidence"])
}

func TestAFilingWithMorePhotosThanSlots(t *testing.T) {
	r := validRequest()
	for i := 0; i <= domain.MaxPhotos-domain.MinPhotos; i++ {
		r.Evidence = append(r.Evidence, photo(byte('0'+i)))
	}

	_, err := r.Validate(now)

	assert.Equal(t, domain.CodeOutOfRange, codes(t, err)["evidence"])
}

// The video is extra: it does not replace the photographs the case needs.
func TestAVideoDoesNotCountForTheMinimum(t *testing.T) {
	r := validRequest()
	r.Evidence = append(r.Evidence[:domain.MinPhotos-1], video())

	_, err := r.Validate(now)

	assert.Equal(t, domain.CodeOutOfRange, codes(t, err)["evidence"])
}

func TestAFilingWithItsPhotosAndItsVideo(t *testing.T) {
	r := validRequest()
	r.Evidence = append(r.Evidence, video())

	c, err := r.Validate(now)

	require.NoError(t, err)
	require.Len(t, c.Evidence, domain.MinPhotos+1)
	assert.Equal(t, domain.KindVideo, c.Evidence[domain.MinPhotos].Kind)
	assert.Equal(t, 22, c.Evidence[domain.MinPhotos].DurationS)
}

func TestASecondVideoDoesNotFit(t *testing.T) {
	r := validRequest()
	r.Evidence = append(r.Evidence, video(), video())

	_, err := r.Validate(now)

	assert.Equal(t, domain.CodeOutOfRange, codes(t, err)["evidence"])
}

func TestAVideoLongerThanTheLimit(t *testing.T) {
	r := validRequest()
	long := video()
	long.DurationS = domain.MaxVideoSeconds + 1
	r.Evidence = append(r.Evidence, long)

	_, err := r.Validate(now)

	assert.Equal(t, domain.CodeOutOfRange, codes(t, err)["evidence.4.duration_s"])
}

// Nothing can have been captured before the incident it documents, nor after
// the filing.
func TestACaptureOutsideTheIncident(t *testing.T) {
	for name, instant := range map[string]string{
		"before the incident": "2026-10-19T10:00:00Z",
		"in the future":       "2026-10-24T10:00:00Z",
	} {
		t.Run(name, func(t *testing.T) {
			r := validRequest()
			r.Evidence[0].CapturedAt = instant

			_, err := r.Validate(now)

			assert.Equal(t, domain.CodeOutOfRange, codes(t, err)["evidence.0.captured_at"])
		})
	}
}

// Criterion 3 of HU-M2.1-01: a precision worse than the limit is not
// trustworthy, whatever the client says.
func TestAPoorPrecisionIsNotTrusted(t *testing.T) {
	r := validRequest()
	r.Evidence[0].AccuracyM = domain.TrustedAccuracyM + 70
	r.Evidence[0].LocationTrusted = true

	c, err := r.Validate(now)

	require.NoError(t, err)
	assert.False(t, c.Evidence[0].LocationTrusted, "the evidence is kept, marked as not trustworthy")
	assert.Equal(t, float64(domain.TrustedAccuracyM+70), c.Evidence[0].AccuracyM)
}

func TestACaptureOutOfTheWorld(t *testing.T) {
	r := validRequest()
	r.Evidence[0].Latitude = 120

	_, err := r.Validate(now)

	assert.Equal(t, domain.CodeOutOfRange, codes(t, err)["evidence.0.latitude"])
}

func TestAKeyThatIsNotOfTheEvidenceStore(t *testing.T) {
	r := validRequest()
	r.Evidence[0].ObjectKey = "identity-documents/" + strings.Repeat("a", 64) + ".jpg"

	_, err := r.Validate(now)

	assert.Equal(t, domain.CodeInvalidFormat, codes(t, err)["evidence.0.object_key"])
}

// The evidence is part of what was filed: the same key with other photographs
// is another filing.
func TestTheFingerprintChangesWithTheEvidence(t *testing.T) {
	first, err := validRequest().Validate(now)
	require.NoError(t, err)

	other := validRequest()
	other.Evidence[0] = photo('f')
	second, err := other.Validate(now)
	require.NoError(t, err)

	assert.NotEqual(t, first.Fingerprint(), second.Fingerprint())
}

func video() domain.EvidenceRequest {
	digest := strings.Repeat("e", 64)
	return domain.EvidenceRequest{
		ObjectKey:       "evidence/" + subject + "/" + digest + ".mp4",
		Kind:            "video",
		CapturedAt:      "2026-10-20T23:33:40Z",
		Latitude:        4.7712,
		Longitude:       -74.0452,
		AccuracyM:       12,
		LocationTrusted: true,
		SHA256:          digest,
		ByteSize:        18_000_000,
		DurationS:       22,
	}
}
