package domain

import (
	"strings"
	"time"
	"unicode"
)

// MinPasswordLength is the minimum the user pool accepts. The password policy
// also requires lowercase, uppercase, a digit and a symbol; checking it here
// turns a rejection by Cognito into a field error the person can act on.
const MinPasswordLength = 12

// RegistrationRequest is what the insured sends to sign up (HU-M1.1-01).
type RegistrationRequest struct {
	DocumentID string
	FullName   string
	Email      string
	// BirthDate is YYYY-MM-DD.
	BirthDate string
	Password  string
	// DocumentFront and DocumentBack are the two faces of the identity
	// document the person captured, as base64 of their JPEG.
	DocumentFront string
	DocumentBack  string
}

// Registration is what a valid request describes: the customer and the two
// faces of the document that back the sign-up.
type Registration struct {
	Customer Customer
	Front    IdentityDocument
	Back     IdentityDocument
}

// Documents returns the two faces, the front first.
func (r Registration) Documents() []IdentityDocument {
	return []IdentityDocument{r.Front, r.Back}
}

// Validate checks the request at the given time and returns the registration
// it describes, or a *ValidationError naming every invalid field.
//
// The password is never part of the customer: it travels to the user pool and
// is not stored. The faces are not part of it either: the customer only keeps
// the key each one got in the document store.
func (r RegistrationRequest) Validate(now time.Time) (Registration, error) {
	var fields []FieldError
	bad := func(field, code string) { fields = append(fields, FieldError{Field: field, Code: code}) }

	doc := strings.TrimSpace(r.DocumentID)
	switch {
	case doc == "":
		bad("document_id", CodeRequired)
	case !ValidDocumentID(doc):
		bad("document_id", CodeInvalidFormat)
	}

	name := strings.Join(strings.Fields(r.FullName), " ")
	switch {
	case name == "":
		bad("full_name", CodeRequired)
	case len([]rune(name)) < minFullNameLength || len([]rune(name)) > maxFullNameLength || !hasLetter(name):
		bad("full_name", CodeInvalidFormat)
	}

	email := strings.TrimSpace(r.Email)
	switch {
	case email == "":
		bad("email", CodeRequired)
	case !validEmail(email):
		bad("email", CodeInvalidFormat)
	}

	birth, birthErr := parseBirthDate(r.BirthDate, now)
	if birthErr != "" {
		bad("birth_date", birthErr)
	}

	switch {
	case r.Password == "":
		bad("password", CodeRequired)
	case len([]rune(r.Password)) < MinPasswordLength:
		bad("password", CodeOutOfRange)
	case !meetsPasswordPolicy(r.Password):
		bad("password", CodeInvalidFormat)
	}

	front, frontCode := decodeIdentityDocument(FaceFront, r.DocumentFront)
	if frontCode != "" {
		bad("document_front", frontCode)
	}
	back, backCode := decodeIdentityDocument(FaceBack, r.DocumentBack)
	switch {
	case backCode != "":
		bad("document_back", backCode)
	// The same image under both faces would store one object and claim two:
	// the sign-up asks for the front and the back of the document.
	case frontCode == "" && back.SHA256 == front.SHA256:
		bad("document_back", CodeInvalidFormat)
	}

	if len(fields) > 0 {
		return Registration{}, &ValidationError{Fields: fields}
	}
	return Registration{
		Customer: Customer{
			DocumentID:       doc,
			FullName:         name,
			Email:            email,
			BirthDate:        birth,
			DocumentFrontKey: front.Key(),
			DocumentBackKey:  back.Key(),
		},
		Front: front,
		Back:  back,
	}, nil
}

// meetsPasswordPolicy mirrors the policy of the user pool: lowercase,
// uppercase, a digit and a symbol.
func meetsPasswordPolicy(password string) bool {
	var lower, upper, digit, symbol bool
	for _, r := range password {
		switch {
		case unicode.IsLower(r):
			lower = true
		case unicode.IsUpper(r):
			upper = true
		case unicode.IsDigit(r):
			digit = true
		case unicode.IsPunct(r) || unicode.IsSymbol(r):
			symbol = true
		}
	}
	return lower && upper && digit && symbol
}
