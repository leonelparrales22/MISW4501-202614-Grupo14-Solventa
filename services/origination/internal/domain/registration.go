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
}

// Validate checks the request at the given time and returns the customer it
// describes, or a *ValidationError naming every invalid field.
//
// The password is never part of the customer: it travels to the user pool and
// is not stored.
func (r RegistrationRequest) Validate(now time.Time) (Customer, error) {
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

	if len(fields) > 0 {
		return Customer{}, &ValidationError{Fields: fields}
	}
	return Customer{DocumentID: doc, FullName: name, Email: email, BirthDate: birth}, nil
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
