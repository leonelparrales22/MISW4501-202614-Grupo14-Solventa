// Package domain holds the business model of the partners service: the
// commercial partners, their credentials and the history of what happened to
// them (HU-W3.1-01). It has no infrastructure dependencies.
package domain

import (
	"errors"
	"net/mail"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Type is the kind of business of a partner.
type Type string

// Partner types of the contract (docs/contratos/HU-W3.1-01-api.md).
const (
	TypeBank        Type = "bank"
	TypeAirline     Type = "airline"
	TypeEcommerce   Type = "ecommerce"
	TypeFintech     Type = "fintech"
	TypeCooperative Type = "cooperative"
	TypeRetail      Type = "retail"
	TypeOther       Type = "other"
)

// Valid reports whether t is a known type.
func (t Type) Valid() bool {
	switch t {
	case TypeBank, TypeAirline, TypeEcommerce, TypeFintech, TypeCooperative, TypeRetail, TypeOther:
		return true
	}
	return false
}

// Status is the lifecycle status of a partner.
type Status string

const (
	// StatusPendingApproval is a registered partner without credentials.
	StatusPendingApproval Status = "pending_approval"
	// StatusActive is an approved partner with credentials.
	StatusActive Status = "active"
	// StatusOffboarded is reserved for the offboarding of HU-W3.1-02.
	StatusOffboarded Status = "offboarded"
)

// Errors of the partner use cases.
var (
	ErrPartnerNotFound = errors.New("partner not found")
	ErrCodeTaken       = errors.New("partner code already taken")
	ErrNotPending      = errors.New("partner is not pending approval")
	ErrNotActive       = errors.New("partner is not active")
	ErrRotationPending = errors.New("a previous credential is still in its transition period")
	// ErrGatewayUnavailable means the API Gateway did not answer or is not
	// configured; nothing was changed.
	ErrGatewayUnavailable = errors.New("api gateway unavailable")
)

// Field error codes.
const (
	CodeRequired      = "required"
	CodeInvalidFormat = "invalid_format"
	CodeOutOfRange    = "out_of_range"
)

// FieldError names one invalid field.
type FieldError struct {
	Field string
	Code  string
}

// ValidationError lists every invalid field of a request.
type ValidationError struct {
	Fields []FieldError
}

func (e *ValidationError) Error() string {
	parts := make([]string, 0, len(e.Fields))
	for _, f := range e.Fields {
		parts = append(parts, f.Field+": "+f.Code)
	}
	return "invalid partner request (" + strings.Join(parts, ", ") + ")"
}

// Limits of a partner registration.
const (
	MaxDailyLimit   = 10_000_000
	MaxRateLimit    = 10_000
	MaxBurstLimit   = 20_000
	MaxProducts     = 10
	MaxEmailLength  = 254
	MaxPartnerIDLen = 64
)

var codePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{2,39}$`)

// Quota is the consumption limit of a partner, applied by its usage plan in
// the API Gateway: a daily quota plus a rate limit with its burst (requests
// per second).
type Quota struct {
	DailyLimit int
	RateLimit  int
	BurstLimit int
}

// Actor is who did something: an operations analyst (the sub and a label
// from their token) or the system.
type Actor struct {
	Type  string
	ID    string
	Label string
}

// Actor types.
const (
	ActorOperations = "operations"
	ActorSystem     = "system"
)

// SystemActor is the actor of what the service does on its own (the
// revocation of a credential when its transition period ends).
var SystemActor = Actor{Type: ActorSystem, ID: "partners", Label: "Sistema"}

// RegisterRequest is what an analyst sends to register a partner.
type RegisterRequest struct {
	Name                  string
	Code                  string
	Type                  Type
	Products              []string
	TechnicalContactEmail string
	Quota                 Quota
}

// Normalize trims the free-text fields.
func (r RegisterRequest) Normalize() RegisterRequest {
	r.Name = strings.TrimSpace(r.Name)
	r.Code = strings.TrimSpace(r.Code)
	r.TechnicalContactEmail = strings.TrimSpace(r.TechnicalContactEmail)
	products := make([]string, 0, len(r.Products))
	for _, p := range r.Products {
		products = append(products, strings.TrimSpace(p))
	}
	r.Products = products
	return r
}

// Validate returns a *ValidationError naming every invalid field.
func (r RegisterRequest) Validate() error {
	var fields []FieldError
	bad := func(field, code string) { fields = append(fields, FieldError{Field: field, Code: code}) }

	switch n := utf8.RuneCountInString(r.Name); {
	case n == 0:
		bad("name", CodeRequired)
	case n < 3 || n > 80:
		bad("name", CodeOutOfRange)
	}
	switch {
	case r.Code == "":
		bad("code", CodeRequired)
	case !codePattern.MatchString(r.Code):
		bad("code", CodeInvalidFormat)
	}
	switch {
	case r.Type == "":
		bad("type", CodeRequired)
	case !r.Type.Valid():
		bad("type", CodeInvalidFormat)
	}
	switch {
	case len(r.Products) == 0:
		bad("products", CodeRequired)
	case len(r.Products) > MaxProducts:
		bad("products", CodeOutOfRange)
	default:
		for _, p := range r.Products {
			if n := utf8.RuneCountInString(p); n < 2 || n > 60 {
				bad("products", CodeOutOfRange)
				break
			}
		}
	}
	switch {
	case r.TechnicalContactEmail == "":
		bad("technical_contact_email", CodeRequired)
	case !validEmail(r.TechnicalContactEmail):
		bad("technical_contact_email", CodeInvalidFormat)
	}
	q := r.Quota
	if q.DailyLimit < 1 || q.DailyLimit > MaxDailyLimit {
		bad("quota.daily_limit", CodeOutOfRange)
	}
	if q.RateLimit < 1 || q.RateLimit > MaxRateLimit {
		bad("quota.rate_limit", CodeOutOfRange)
	}
	if q.BurstLimit < q.RateLimit || q.BurstLimit < 1 || q.BurstLimit > MaxBurstLimit {
		bad("quota.burst_limit", CodeOutOfRange)
	}
	if len(fields) > 0 {
		return &ValidationError{Fields: fields}
	}
	return nil
}

func validEmail(s string) bool {
	if len(s) > MaxEmailLength || strings.ContainsAny(s, " <>") {
		return false
	}
	a, err := mail.ParseAddress(s)
	return err == nil && a.Address == s && strings.Contains(s[strings.LastIndex(s, "@"):], ".")
}

// Partner is a commercial partner.
type Partner struct {
	ID                    string
	Code                  string
	Name                  string
	Type                  Type
	Products              []string
	TechnicalContactEmail string
	Status                Status
	Quota                 Quota
	// UsagePlanID is the usage plan of the partner in the API Gateway; empty
	// until it is approved.
	UsagePlanID  string
	RegisteredAt time.Time
	RegisteredBy Actor
	ApprovedAt   *time.Time
}
