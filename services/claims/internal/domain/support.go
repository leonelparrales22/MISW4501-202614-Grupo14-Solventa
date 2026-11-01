package domain

import (
	"bytes"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// Limits of the supports of a web filing (HU-W2.1-01 criterion 5).
const (
	// MaxSupportBytes is 10 MiB: the limit of one file.
	MaxSupportBytes = 10 << 20
	// MaxSupportsPerClaim bounds the files of a claim.
	MaxSupportsPerClaim = 20
	// MaxPendingSupports bounds the files an insured has uploaded and not
	// attached to any claim yet, so nobody can fill the storage.
	MaxPendingSupports = 50

	maxFileNameRunes = 120
)

// SupportIDPrefix starts every support id; 128 random bits follow, so an id
// cannot be guessed.
const SupportIDPrefix = "sup-"

var supportIDPattern = regexp.MustCompile(`^sup-[0-9a-f]{32}$`)

// ValidSupportID tells whether the id has the shape of a support id.
func ValidSupportID(id string) bool { return supportIDPattern.MatchString(id) }

// Why an upload is refused.
var (
	ErrUnsupportedFileType = errors.New("claims: the file is not JPG, PNG or PDF")
	ErrFileTooLarge        = errors.New("claims: the file is larger than 10 MiB")
	ErrEmptyFile           = errors.New("claims: the file is empty")
	// ErrTooManyPendingSupports means the insured already has
	// MaxPendingSupports supports not attached to any claim.
	ErrTooManyPendingSupports = errors.New("claims: too many supports not attached to a claim")
	// ErrSupportNotFound is a support that does not exist or belongs to
	// another insured: both look the same.
	ErrSupportNotFound = errors.New("claims: the support does not exist")
	// ErrSupportNotUploaded means the confirmation arrived before the file
	// reached the storage.
	ErrSupportNotUploaded = errors.New("claims: the file of the support is not in the storage")
	// ErrSupportMismatch means the stored object is not the declared one: its
	// size or its digest differ.
	ErrSupportMismatch = errors.New("claims: the stored file is not the declared one")
)

// SupportObjectKey is where the file of a support lives in the store, under
// the directory of whoever declared it.
func SupportObjectKey(subject, supportID string) string {
	return "supports/" + subject + "/" + supportID
}

// SupportUploadRequest is what the portal declares before uploading one
// support to the store (HU-W2.1-01 criterion 5): the file itself never
// crosses the service.
type SupportUploadRequest struct {
	FileName    string
	ContentType string
	ByteSize    int64
	SHA256      string
}

// SupportUpload is a declared support that passed its rules.
type SupportUpload struct {
	FileName string
	Format   FileFormat
	ByteSize int64
	SHA256   string
}

// Validate checks the declaration. The content type and the size are refused
// with the sentinel errors above; a digest that is not SHA-256 comes back as
// a *ValidationError. The real format is checked against the first bytes of
// the stored file when the upload is confirmed.
func (r SupportUploadRequest) Validate() (SupportUpload, error) {
	format, ok := FormatOf(strings.TrimSpace(r.ContentType))
	switch {
	case !ok:
		return SupportUpload{}, ErrUnsupportedFileType
	case r.ByteSize <= 0:
		return SupportUpload{}, ErrEmptyFile
	case r.ByteSize > MaxSupportBytes:
		return SupportUpload{}, ErrFileTooLarge
	}
	digest := strings.TrimSpace(r.SHA256)
	switch {
	case digest == "":
		return SupportUpload{}, &ValidationError{Fields: []FieldError{{Field: "sha256", Code: CodeRequired}}}
	case !sha256Pattern.MatchString(digest):
		return SupportUpload{}, &ValidationError{Fields: []FieldError{{Field: "sha256", Code: CodeInvalidFormat}}}
	}
	return SupportUpload{FileName: SanitizeFileName(r.FileName), Format: format, ByteSize: r.ByteSize, SHA256: digest}, nil
}

// SupportFile is an uploaded support. It belongs to whoever uploaded it and can
// be attached to one claim only.
type SupportFile struct {
	ID           string
	OwnerSubject string
	FileName     string
	Format       FileFormat
	ByteSize     int64
	// SHA256 is the digest of the content, in lowercase hexadecimal.
	SHA256 string
	// UploadedAt is when the portal declared the file.
	UploadedAt time.Time
	// ConfirmedAt is when the stored file was checked against its
	// declaration; zero until then. Only a confirmed support can be attached
	// to a claim.
	ConfirmedAt time.Time
}

// ObjectKey is where the file lives in the store.
func (s SupportFile) ObjectKey() string { return SupportObjectKey(s.OwnerSubject, s.ID) }

// Confirmed tells whether the stored file was checked.
func (s SupportFile) Confirmed() bool { return !s.ConfirmedAt.IsZero() }

// ContentType is the media type of the file, checked against its content.
func (s SupportFile) ContentType() string { return s.Format.ContentType() }

// Signatures of the accepted formats: the first bytes of the file.
var (
	signatureJPEG = []byte{0xFF, 0xD8, 0xFF}
	signaturePNG  = []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}
	signaturePDF  = []byte("%PDF-")
)

// DetectFormat tells the format of a file by its first bytes. The extension
// and the declared media type are never trusted: a renamed executable is not
// a PDF.
func DetectFormat(head []byte) (FileFormat, bool) {
	switch {
	case bytes.HasPrefix(head, signatureJPEG):
		return FormatJPEG, true
	case bytes.HasPrefix(head, signaturePNG):
		return FormatPNG, true
	case bytes.HasPrefix(head, signaturePDF):
		return FormatPDF, true
	}
	return "", false
}

// SanitizeFileName keeps the name the insured recognizes without anything that
// could be read as a path: no directories, no control characters, at most 120
// characters. An empty result becomes "support".
func SanitizeFileName(name string) string {
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == unicode.ReplacementChar {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if r := []rune(name); len(r) > maxFileNameRunes {
		name = strings.TrimSpace(string(r[:maxFileNameRunes]))
	}
	if name == "" || name == "." || name == ".." {
		return "support"
	}
	return name
}
