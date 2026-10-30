package domain

import "time"

// IdentityStatus is where the identity check of a customer stands
// (HU-M1.1-01). A customer an advisor quoted for was never checked and has the
// empty status.
type IdentityStatus string

const (
	// IdentityUnchecked is the customer nobody asked to verify.
	IdentityUnchecked IdentityStatus = ""
	// IdentityPending is the sign-up whose check has not answered yet: the
	// provider was unavailable, and retrying the sign-up asks again.
	IdentityPending IdentityStatus = "pending"
	// IdentityVerified is the only status that gets an account (criterion 5).
	IdentityVerified IdentityStatus = "verified"
	// IdentityRejected is the sign-up the provider refused.
	IdentityRejected IdentityStatus = "rejected"
)

// IdentityRejection is why the provider refused. It is recorded for the
// platform and NEVER reaches the person (criterion 3).
type IdentityRejection string

const (
	RejectionRestrictedList IdentityRejection = "restricted_list"
	RejectionUnreadable     IdentityRejection = "unreadable"
	RejectionUnverifiable   IdentityRejection = "unverifiable"
)

// Final tells whether the rejection closes the sign-up for that document for
// good. A person on a restrictive list must not be able to retry until they
// get through; an unreadable photo or an identity that could not be verified
// is retried with other photos or corrected data.
func (r IdentityRejection) Final() bool { return r == RejectionRestrictedList }

// IdentityCheck is what the provider verifies: the data the person confirmed
// and the digest of each face, so the check is about the images that were
// kept.
type IdentityCheck struct {
	DocumentID  string
	FullName    string
	BirthDate   time.Time
	FrontSHA256 string
	BackSHA256  string
}

// IdentityVerdict is the provider's answer.
type IdentityVerdict struct {
	VerificationID string
	Approved       bool
	// Rejection is set when Approved is false.
	Rejection IdentityRejection
	CheckedAt time.Time
}

// DocumentData is what the provider read on the document, for the person to
// confirm or correct before signing up (criterion 1). BirthDate is
// YYYY-MM-DD, as the sign-up receives it.
type DocumentData struct {
	DocumentType string
	DocumentID   string
	GivenNames   string
	Surnames     string
	BirthDate    string
}

// DocumentFaces checks the two faces of a document read the way the sign-up
// checks them, and returns them decoded, or a *ValidationError.
func DocumentFaces(front, back string) (IdentityDocument, IdentityDocument, error) {
	var fields []FieldError
	f, frontCode := decodeIdentityDocument(FaceFront, front)
	if frontCode != "" {
		fields = append(fields, FieldError{Field: "document_front", Code: frontCode})
	}
	b, backCode := decodeIdentityDocument(FaceBack, back)
	switch {
	case backCode != "":
		fields = append(fields, FieldError{Field: "document_back", Code: backCode})
	case frontCode == "" && b.SHA256 == f.SHA256:
		fields = append(fields, FieldError{Field: "document_back", Code: CodeInvalidFormat})
	}
	if len(fields) > 0 {
		return IdentityDocument{}, IdentityDocument{}, &ValidationError{Fields: fields}
	}
	return f, b, nil
}
