package domain

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// EvidenceKind is what the insured captured (HU-M2.1-01).
type EvidenceKind string

const (
	KindPhoto EvidenceKind = "photo"
	KindVideo EvidenceKind = "video"
)

// Formats and limits of the evidence.
const (
	PhotoContentType = "image/jpeg"
	VideoContentType = "video/mp4"

	MinEvidenceBytes = 1 << 10
	MaxPhotoBytes    = 2_000_000
	MaxVideoBytes    = 60_000_000
	MaxVideoSeconds  = 30

	// MinPhotos is the evidence a filing cannot do without, and MaxPhotos the
	// slots the assistant offers. The video is extra and does not count.
	MinPhotos = 4
	MaxPhotos = 6
	MaxVideos = 1

	// TrustedAccuracyM is the precision beyond which the location of a capture
	// is not trustworthy.
	TrustedAccuracyM = 50
)

// evidencePrefix is the directory of the evidence inside the store. Each
// insured has their own, so a key of someone else is recognizable.
const evidencePrefix = "evidence/"

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// UploadRequest is what the client declares before uploading one evidence.
type UploadRequest struct {
	Kind        string
	ContentType string
	ByteSize    int64
	SHA256      string
}

// Upload is a declared evidence that passed its rules. It is what the store
// signs: an object of that size, of that type and with that digest.
type Upload struct {
	Kind        EvidenceKind
	ContentType string
	ByteSize    int64
	SHA256      string
}

// Validate checks the declaration and returns the upload it describes, or a
// *ValidationError naming every invalid field.
func (r UploadRequest) Validate() (Upload, error) {
	var fields []FieldError
	bad := func(field, code string) { fields = append(fields, FieldError{Field: field, Code: code}) }

	kind := EvidenceKind(strings.TrimSpace(r.Kind))
	contentType := strings.TrimSpace(r.ContentType)
	switch {
	case kind == "":
		bad("kind", CodeRequired)
	case kind != KindPhoto && kind != KindVideo:
		bad("kind", CodeInvalidFormat)
	case contentType != kind.contentType():
		// Each kind travels in one format: the capture produces no other.
		bad("content_type", CodeInvalidFormat)
	}

	if kind == KindPhoto || kind == KindVideo {
		switch {
		case r.ByteSize < MinEvidenceBytes, r.ByteSize > kind.maxBytes():
			bad("byte_size", CodeOutOfRange)
		}
	}

	digest := strings.TrimSpace(r.SHA256)
	switch {
	case digest == "":
		bad("sha256", CodeRequired)
	case !sha256Pattern.MatchString(digest):
		bad("sha256", CodeInvalidFormat)
	}

	if len(fields) > 0 {
		return Upload{}, &ValidationError{Fields: fields}
	}
	return Upload{Kind: kind, ContentType: contentType, ByteSize: r.ByteSize, SHA256: digest}, nil
}

// Key is where this upload goes in the evidence store, under the directory of
// whoever captured it. The name is the digest, so the same file asked twice
// lands on the same object.
func (u Upload) Key(subject string) string {
	return evidencePrefix + subject + "/" + u.SHA256 + u.Kind.extension()
}

// ContentType is the media type the kind travels in.
func (k EvidenceKind) ContentType() string { return k.contentType() }

func (k EvidenceKind) contentType() string {
	if k == KindVideo {
		return VideoContentType
	}
	return PhotoContentType
}

func (k EvidenceKind) maxBytes() int64 {
	if k == KindVideo {
		return MaxVideoBytes
	}
	return MaxPhotoBytes
}

func (k EvidenceKind) extension() string {
	if k == KindVideo {
		return ".mp4"
	}
	return ".jpg"
}

// OwnedBy reports whether the key belongs to the directory of that subject.
func OwnedBy(key, subject string) bool {
	return strings.HasPrefix(key, evidencePrefix+subject+"/")
}

// EvidenceRequest is one evidence as the assistant sends it with the filing:
// the key it already has in the store, and what was true when it was captured.
type EvidenceRequest struct {
	ObjectKey string
	Kind      string
	// CapturedAt is RFC 3339.
	CapturedAt      string
	Latitude        float64
	Longitude       float64
	AltitudeM       *float64
	AccuracyM       float64
	LocationTrusted bool
	SHA256          string
	ByteSize        int64
	DurationS       int
}

// Evidence is one capture associated with the case.
type Evidence struct {
	ObjectKey       string
	Kind            EvidenceKind
	CapturedAt      time.Time
	Latitude        float64
	Longitude       float64
	AltitudeM       *float64
	AccuracyM       float64
	LocationTrusted bool
	SHA256          string
	ByteSize        int64
	DurationS       int
}

// validateEvidence checks the list that comes with a filing. occurredAt is the
// instant of the incident: nothing can have been captured before it happened.
func validateEvidence(in []EvidenceRequest, occurredAt time.Time, now time.Time,
	bad func(field, code string)) []Evidence {
	photos, videos := 0, 0
	out := make([]Evidence, 0, len(in))

	for i, e := range in {
		field := func(name string) string { return "evidence." + strconv.Itoa(i) + "." + name }
		kind := EvidenceKind(strings.TrimSpace(e.Kind))
		switch kind {
		case KindPhoto:
			photos++
		case KindVideo:
			videos++
		default:
			bad(field("kind"), CodeInvalidFormat)
			continue
		}

		key := strings.TrimSpace(e.ObjectKey)
		switch {
		case key == "":
			bad(field("object_key"), CodeRequired)
		case !strings.HasPrefix(key, evidencePrefix):
			bad(field("object_key"), CodeInvalidFormat)
		}

		digest := strings.TrimSpace(e.SHA256)
		if !sha256Pattern.MatchString(digest) {
			bad(field("sha256"), CodeInvalidFormat)
		}
		if e.ByteSize < MinEvidenceBytes || e.ByteSize > kind.maxBytes() {
			bad(field("byte_size"), CodeOutOfRange)
		}

		captured := capturedAt(e.CapturedAt, occurredAt, now, field, bad)

		switch {
		case e.Latitude < -90 || e.Latitude > 90:
			bad(field("latitude"), CodeOutOfRange)
		case e.Longitude < -180 || e.Longitude > 180:
			bad(field("longitude"), CodeOutOfRange)
		}
		if e.AccuracyM < 0 {
			bad(field("accuracy_m"), CodeOutOfRange)
		}
		if kind == KindVideo && (e.DurationS <= 0 || e.DurationS > MaxVideoSeconds) {
			bad(field("duration_s"), CodeOutOfRange)
		}

		out = append(out, Evidence{
			ObjectKey: key, Kind: kind, CapturedAt: captured,
			Latitude: e.Latitude, Longitude: e.Longitude, AltitudeM: e.AltitudeM,
			AccuracyM: e.AccuracyM,
			// A precision worse than the limit is not trustworthy, whatever
			// the caller says.
			LocationTrusted: e.LocationTrusted && e.AccuracyM > 0 && e.AccuracyM <= TrustedAccuracyM,
			SHA256:          digest, ByteSize: e.ByteSize, DurationS: e.DurationS,
		})
	}

	if photos < MinPhotos || photos > MaxPhotos || videos > MaxVideos {
		bad("evidence", CodeOutOfRange)
	}
	return out
}

// capturedAt parses the instant of a capture: it cannot be in the future nor
// before the incident it documents.
func capturedAt(value string, occurredAt, now time.Time, field func(string) string,
	bad func(field, code string)) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		bad(field("captured_at"), CodeRequired)
		return time.Time{}
	}
	at, err := time.Parse(time.RFC3339, value)
	if err != nil {
		bad(field("captured_at"), CodeInvalidFormat)
		return time.Time{}
	}
	at = at.UTC()
	if at.After(now) || (!occurredAt.IsZero() && at.Before(occurredAt)) {
		bad(field("captured_at"), CodeOutOfRange)
	}
	return at
}
