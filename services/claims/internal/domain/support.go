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
)

// SupportFile is an uploaded support. It belongs to whoever uploaded it and can
// be attached to one claim only.
type SupportFile struct {
	ID           string
	OwnerSubject string
	FileName     string
	Format       FileFormat
	ByteSize     int64
	// SHA256 is the digest of the content, in lowercase hexadecimal.
	SHA256     string
	UploadedAt time.Time
}

// ContentType is the media type of the file, told by its content.
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
