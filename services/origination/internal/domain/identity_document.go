package domain

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
)

// DocumentFace is the side of the identity document the person captured
// during the sign-up (HU-M1.1-01).
type DocumentFace string

const (
	FaceFront DocumentFace = "front"
	FaceBack  DocumentFace = "back"
)

// Limits and format of a captured face.
const (
	// IdentityDocumentContentType is the only format the capture produces.
	IdentityDocumentContentType = "image/jpeg"
	// MinIdentityDocumentBytes rejects what is too small to be a photograph
	// of a document.
	MinIdentityDocumentBytes = 1 << 10
	// MaxIdentityDocumentBytes is the largest face accepted. The mobile client
	// scales the capture before sending it, so a face arrives well below it.
	MaxIdentityDocumentBytes = 1_500_000
)

// identityDocumentPrefix is the directory of the identity documents inside the
// store, which also holds the contractual documents.
const identityDocumentPrefix = "identity-documents/"

// jpegMagic are the first bytes of a JPEG: start of image and the first marker.
var jpegMagic = []byte{0xFF, 0xD8, 0xFF}

// IdentityDocument is one face of the document, as it is stored.
//
// The key is derived from the content, so the same face sent twice lands on
// the same object: a retried sign-up neither duplicates the image nor collides
// with the immutability of the store. The key carries no personal data either,
// which the document number or the name would.
type IdentityDocument struct {
	Face DocumentFace
	// Content is the JPEG itself, already decoded.
	Content []byte
	// SHA256 is the hex digest of Content.
	SHA256 string
}

// Key is where this face goes in the document store.
func (d IdentityDocument) Key() string {
	return identityDocumentPrefix + d.SHA256 + ".jpg"
}

// ContentType is the media type the store records for this face.
func (d IdentityDocument) ContentType() string { return IdentityDocumentContentType }

// decodeIdentityDocument reads one face as the client sends it: base64 of the
// JPEG. It returns the validation code of the first rule the face breaks, or
// an empty code when it is valid.
func decodeIdentityDocument(face DocumentFace, encoded string) (IdentityDocument, string) {
	if encoded == "" {
		return IdentityDocument{}, CodeRequired
	}
	content, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return IdentityDocument{}, CodeInvalidFormat
	}
	switch {
	case len(content) < MinIdentityDocumentBytes, len(content) > MaxIdentityDocumentBytes:
		return IdentityDocument{}, CodeOutOfRange
	case !isJPEG(content):
		return IdentityDocument{}, CodeInvalidFormat
	}
	sum := sha256.Sum256(content)
	return IdentityDocument{Face: face, Content: content, SHA256: hex.EncodeToString(sum[:])}, ""
}

// isJPEG reports whether the content begins as a JPEG. The capture of the
// mobile client is the only producer, so the first bytes settle the format:
// anything else is not the photograph the sign-up asks for.
func isJPEG(content []byte) bool {
	if len(content) < len(jpegMagic) {
		return false
	}
	for i, b := range jpegMagic {
		if content[i] != b {
			return false
		}
	}
	return true
}
