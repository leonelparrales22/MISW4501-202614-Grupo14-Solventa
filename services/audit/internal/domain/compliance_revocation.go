package domain

import (
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// The compliance portal revokes a consent with the process of HU-M1.3-02
// (HU-W4.2-02): it only adds its own channel, actor and confirmation method.
const (
	// RevocationChannelCompliancePortal is the web portal of the compliance
	// role of Solventa.
	RevocationChannelCompliancePortal RevocationChannel = "compliance_portal"
	// ActorOperations is an analyst of the operations role, identified by the
	// subject of their token.
	ActorOperations ActorType = "operations"
	// ConfirmationRoleAttestation is how the analyst confirmed it: the box of
	// the modal in which they state they act with the compliance role.
	ConfirmationRoleAttestation ConfirmationMethod = "role_attestation"
)

// Errors of the retry of the compliance portal.
var (
	// ErrConsentNotRevoked: there is no revocation whose notices could be
	// retried (HTTP 409 consent_not_revoked).
	ErrConsentNotRevoked = errors.New("the consent is not revoked")
	// ErrNothingToRetry: no entity is waiting for a retry: the notice is in
	// progress or completed, every entity that did not confirm is already in a
	// retry window, or another retry took them (HTTP 409 nothing_to_retry).
	ErrNothingToRetry = errors.New("there is nothing to retry")
)

// RevocationReason is why the portal revokes a consent.
type RevocationReason string

const (
	// ReasonCustomerRequest: the customer asked for it.
	ReasonCustomerRequest RevocationReason = "customer_request"
	// ReasonRegulatoryOrder: a regulatory order.
	ReasonRegulatoryOrder RevocationReason = "regulatory_order"
)

// Valid tells whether r is one of the reasons above.
func (r RevocationReason) Valid() bool {
	return r == ReasonCustomerRequest || r == ReasonRegulatoryOrder
}

// Limits of the reference of a portal revocation: the number of the ticket or
// order that supports it.
const (
	MinReferenceLength = 3
	MaxReferenceLength = 64
)

// referencePattern allows letters, digits, space and - _ . / # (ASCII only).
var referencePattern = regexp.MustCompile(`^[A-Za-z0-9 ./#_-]+$`)

// ComplianceRevokeRequest is what the compliance portal sends when the analyst
// confirms the revocation (HU-W4.2-02). UserAgent comes from the header of the
// request, never from the body.
type ComplianceRevokeRequest struct {
	Reason        string
	Reference     string
	RoleConfirmed bool
	UserAgent     string
}

// Validate checks the request. It returns a *ValidationError naming every
// invalid field at once: reason (required, invalid_format), reference
// (required, out_of_range, invalid_format) and role_confirmed (must_be_true).
func (r ComplianceRevokeRequest) Validate() error {
	var fields []FieldError
	bad := func(field, code string) { fields = append(fields, FieldError{Field: field, Code: code}) }

	switch {
	case r.Reason == "":
		bad("reason", CodeRequired)
	case !RevocationReason(r.Reason).Valid():
		bad("reason", CodeInvalidFormat)
	}
	if code := referenceCode(r.Reference); code != "" {
		bad("reference", code)
	}
	if !r.RoleConfirmed {
		bad("role_confirmed", CodeMustBeTrue)
	}

	if len(fields) > 0 {
		return &ValidationError{Fields: fields}
	}
	return nil
}

// NormalizedReference is the reference as it is recorded: spaces collapsed.
func (r ComplianceRevokeRequest) NormalizedReference() string { return normalizeName(r.Reference) }

// Device is the user agent of the browser as the device of the record: control
// characters removed, spaces collapsed and at most MaxDeviceLength characters.
// It is empty when the request carried none.
func (r ComplianceRevokeRequest) Device() string {
	clean := strings.Map(func(c rune) rune {
		if unicode.IsControl(c) && !unicode.IsSpace(c) {
			return -1
		}
		return c
	}, strings.ToValidUTF8(r.UserAgent, ""))
	runes := []rune(normalizeName(clean))
	if len(runes) > MaxDeviceLength {
		runes = runes[:MaxDeviceLength]
	}
	return strings.TrimSpace(string(runes))
}

// ValidatePortalCommand is the defense in depth of the revocation of the
// portal channel: the reason is valid, the reference is acceptable and the
// time of the request is there. The route validates the request first, so a
// *ValidationError here means a caller skipped it.
func ValidatePortalCommand(reason, reference string, requestedAt time.Time) error {
	var fields []FieldError
	bad := func(field, code string) { fields = append(fields, FieldError{Field: field, Code: code}) }

	switch {
	case reason == "":
		bad("reason", CodeRequired)
	case !RevocationReason(reason).Valid():
		bad("reason", CodeInvalidFormat)
	}
	if code := referenceCode(reference); code != "" {
		bad("reference", code)
	}
	if requestedAt.IsZero() {
		bad("requested_at", CodeRequired)
	}

	if len(fields) > 0 {
		return &ValidationError{Fields: fields}
	}
	return nil
}

// referenceCode returns the validation code of a reference, or "" when it is
// acceptable: 3 to 64 characters once repeated spaces are collapsed, made of
// letters, digits, space and - _ . / #.
func referenceCode(reference string) string {
	ref := normalizeName(reference)
	switch {
	case ref == "":
		return CodeRequired
	case !referencePattern.MatchString(ref):
		return CodeInvalidFormat
	case len(ref) < MinReferenceLength || len(ref) > MaxReferenceLength:
		return CodeOutOfRange
	}
	return ""
}
