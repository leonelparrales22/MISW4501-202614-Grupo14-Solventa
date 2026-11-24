package domain

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// HU-W1.4-01: the change request an insured files on one of their policies
// in force (screen A3): new beneficiaries, a new insured value or new contact
// data. This story files it and shows its status (screens A3b and A11);
// reviewing and applying it is HU-W1.4-02, which reuses this model,
// ValidateRequested and the statuses below.

// ChangeType is what the insured asks to change.
type ChangeType string

const (
	// ChangeBeneficiaries designates who receives the remainder of the
	// insured value after the lender's outstanding balance.
	ChangeBeneficiaries ChangeType = "beneficiaries"
	// ChangeInsuredAmount asks for another insured value.
	ChangeInsuredAmount ChangeType = "insured_amount"
	// ChangePersonalData updates the contact data of the policy: address,
	// contact e-mail and phone (decision D-5 a: never the sign-in e-mail).
	ChangePersonalData ChangeType = "personal_data"
)

// ChangeTypes are the types in the order the screen offers them.
var ChangeTypes = []ChangeType{ChangeBeneficiaries, ChangeInsuredAmount, ChangePersonalData}

// ParseChangeType returns the type named by raw, or false if raw names none.
func ParseChangeType(raw string) (ChangeType, bool) {
	switch t := ChangeType(raw); t {
	case ChangeBeneficiaries, ChangeInsuredAmount, ChangePersonalData:
		return t, true
	default:
		return "", false
	}
}

// ChangeRequestStatus is where a change request stands. This story creates
// filed; HU-W1.4-02 moves it to the others (decision D-8 a).
type ChangeRequestStatus string

const (
	// ChangeRequestFiled: the insured filed it and nobody took it yet.
	ChangeRequestFiled ChangeRequestStatus = "filed"
	// ChangeRequestInReview: an operations analyst is reviewing it.
	ChangeRequestInReview ChangeRequestStatus = "in_review"
	// ChangeRequestApplied: the endorsement was applied.
	ChangeRequestApplied ChangeRequestStatus = "applied"
	// ChangeRequestRejected: the analyst rejected it, with a reason the
	// insured sees.
	ChangeRequestRejected ChangeRequestStatus = "rejected"
	// ChangeRequestNotApplied: a business validation of the analyst failed.
	// Without a decision reason it is still being worked on; with one, the
	// analyst returned it to the insured (adjustment A-1).
	ChangeRequestNotApplied ChangeRequestStatus = "not_applied"
)

// Relationship is how a beneficiary is related to the insured (decision D-4 a).
type Relationship string

const (
	RelationshipSpouseOrPartner Relationship = "spouse_or_partner"
	RelationshipChild           Relationship = "child"
	RelationshipParent          Relationship = "parent"
	RelationshipSibling         Relationship = "sibling"
	RelationshipGrandchild      Relationship = "grandchild"
	RelationshipOther           Relationship = "other"
)

// Relationships is the catalog of the relationships, in the order the screen
// offers them.
var Relationships = []Relationship{RelationshipSpouseOrPartner, RelationshipChild, RelationshipParent,
	RelationshipSibling, RelationshipGrandchild, RelationshipOther}

// DocumentType is the kind of identity document of a beneficiary (decision
// D-4 a): the Colombian ones and the passport.
type DocumentType string

const (
	// DocumentCC is the citizenship card (cédula de ciudadanía).
	DocumentCC DocumentType = "CC"
	// DocumentTI is the identity card of a minor (tarjeta de identidad).
	DocumentTI DocumentType = "TI"
	// DocumentRC is the civil registry of a child (registro civil).
	DocumentRC DocumentType = "RC"
	// DocumentCE is the foreigner's card (cédula de extranjería).
	DocumentCE DocumentType = "CE"
	// DocumentPA is a passport.
	DocumentPA DocumentType = "PA"
)

// DocumentTypes is the catalog of the document types.
var DocumentTypes = []DocumentType{DocumentCC, DocumentTI, DocumentRC, DocumentCE, DocumentPA}

// numericOnly tells whether the number of a document of this type has only
// digits (the Colombian documents issued by the Registraduría).
func (d DocumentType) numericOnly() bool {
	return d == DocumentCC || d == DocumentTI || d == DocumentRC
}

// Limits of a change request (decision D-4 a).
const (
	// MaxBeneficiaries bounds the beneficiaries of a policy; at least one
	// is designated, so the remainder is never left undesignated.
	MaxBeneficiaries = 10
	// FullShareBasisPoints is 100 %: the shares of the beneficiaries add up
	// to exactly this.
	FullShareBasisPoints = 10_000
	minAddressLength     = 5
	maxAddressLength     = 160
	maxEmailLength       = 254
)

// Field validation codes of a change request, besides the ones of quote.go.
const (
	// CodeShareTotalNot100: the shares of the beneficiaries do not add up
	// to 100 %.
	CodeShareTotalNot100 = "share_total_not_100"
	// CodeDuplicate: the same document appears twice.
	CodeDuplicate = "duplicate"
	// CodeNoChange: the requested value is the current one.
	CodeNoChange = "no_change"
)

var (
	digitsPattern = regexp.MustCompile(`^[0-9]+$`)
	// phonePattern is a Colombian number of 10 digits, with the country
	// code +57 or without it, once spaces and hyphens are removed.
	phonePattern = regexp.MustCompile(`^(\+57)?([0-9]{10})$`)
)

// Beneficiary is a person who receives a share of the remainder of the
// insured value, after the lender's outstanding balance.
type Beneficiary struct {
	FullName       string
	Relationship   Relationship
	DocumentType   DocumentType
	DocumentNumber string
	// ShareBasisPoints is the share of the remainder in hundredths of a
	// percent (1 to 10 000), so the shares add up without floating point
	// errors. The API shows it as a percentage with two decimals.
	ShareBasisPoints int
}

// SharePercent is the share as a percentage.
func (b Beneficiary) SharePercent() float64 { return float64(b.ShareBasisPoints) / 100 }

// documentKey pairs the beneficiaries of two lists: the type and the number of
// their document.
func (b Beneficiary) documentKey() string {
	return string(b.DocumentType) + " " + strings.ToUpper(b.DocumentNumber)
}

// OnerousBasisOutstandingBalance is the basis of the onerous beneficiary of a
// mortgage life policy: the lender receives the outstanding balance of the
// credit.
const OnerousBasisOutstandingBalance = "outstanding_balance"

// OnerousBeneficiary is the lender of the credit the policy secures. Every
// policy origination issues has one (condition onerous_beneficiary_lender of
// the offer), and the insured cannot change it.
type OnerousBeneficiary struct {
	// PartnerID is the partner that sold the policy; empty when it is not
	// known (an advisor sale or a demonstration offer).
	PartnerID string
	// Name is its display name (PARTNER_DISPLAY_NAMES); empty when it has
	// none.
	Name  string
	Basis string
}

// ContactData is the contact data of a policy. A nil member is not
// registered (the policy has no address nor phone yet).
type ContactData struct {
	Address *string
	Email   *string
	Phone   *string
}

// PolicyTerms are the terms of a policy the insured can change, as they stand
// now. endorsements.Terms builds them; HU-W1.4-02 lays the applied
// endorsements over them.
type PolicyTerms struct {
	InsuredAmount float64
	Currency      string
	Beneficiaries []Beneficiary
	Onerous       *OnerousBeneficiary
	Contact       ContactData
}

// ChangeContent is one side of a change: what the policy had when the request
// was filed (previous) or what the insured asks for (requested). Only the
// members of its type are set: Beneficiaries (and, on the previous side, the
// onerous beneficiary), InsuredAmount and Currency, or Contact. A requested
// Contact carries only the members that change.
type ChangeContent struct {
	Beneficiaries []Beneficiary
	Onerous       *OnerousBeneficiary
	InsuredAmount float64
	Currency      string
	Contact       ContactData
}

// Requester is who filed a change request: the subject, the e-mail and the
// name of their token (the name is empty when the account has none).
type Requester struct {
	Subject string
	Email   string
	Name    string
}

// ChangeRequest is a change request of a policy. Its content (everything but
// the status, when it changed and the decision reason) never changes once
// filed.
type ChangeRequest struct {
	// ID is SOL-MOD-<year>-<number>, assigned when it is stored.
	ID       string
	PolicyID string
	// CustomerID is the owner of the policy when the request was filed.
	CustomerID string
	Type       ChangeType
	// Previous is the photo of the current value when it was filed.
	Previous  ChangeContent
	Requested ChangeContent
	Channel   Channel
	// RequestedBy is who filed it; HU-W1.4-02 checks that its subject is
	// the owner of the policy.
	RequestedBy     Requester
	FiledAt         time.Time
	Status          ChangeRequestStatus
	StatusChangedAt time.Time
	// DecisionReason is what the analyst tells the insured when rejecting
	// or returning the request; empty otherwise.
	DecisionReason string
}

// Open tells whether the request still holds the place of its policy and type
// (decision D-6 a, adjustment A-2): filed, in review, or not applied and not
// returned to the insured.
func (r ChangeRequest) Open() bool {
	switch r.Status {
	case ChangeRequestFiled, ChangeRequestInReview:
		return true
	case ChangeRequestNotApplied:
		return r.DecisionReason == ""
	default:
		return false
	}
}

// InsuredStatus is the status as the insured sees it (adjustment A-1): a not
// applied request is still in review until the analyst returns it with a
// reason, and then it reads as rejected.
func (r ChangeRequest) InsuredStatus() ChangeRequestStatus {
	if r.Status != ChangeRequestNotApplied {
		return r.Status
	}
	if r.DecisionReason == "" {
		return ChangeRequestInReview
	}
	return ChangeRequestRejected
}

// Field names of the changes of a request.
const (
	FieldBeneficiaries = "beneficiaries"
	FieldInsuredAmount = "insured_amount"
	FieldAddress       = "personal_data.address"
	FieldEmail         = "personal_data.email"
	FieldPhone         = "personal_data.phone"
)

// FieldChange is one line of the difference between the previous and the
// requested values: a beneficiary (Beneficiary values; nil Previous for a new
// one and nil Requested for a removed one), the insured value (float64) or a
// contact member (string, nil when it was not registered).
type FieldChange struct {
	Field     string
	Previous  any
	Requested any
}

// Changes is the difference, field by field, between the previous and the
// requested values. Beneficiaries are paired by the type and number of their
// document: one line for each one removed, changed (name, relationship or
// share) or added, in the order of the previous list and then of the new one.
func (r ChangeRequest) Changes() []FieldChange {
	switch r.Type {
	case ChangeBeneficiaries:
		return beneficiaryChanges(r.Previous.Beneficiaries, r.Requested.Beneficiaries)
	case ChangeInsuredAmount:
		if r.Previous.InsuredAmount == r.Requested.InsuredAmount {
			return []FieldChange{}
		}
		return []FieldChange{{Field: FieldInsuredAmount, Previous: r.Previous.InsuredAmount, Requested: r.Requested.InsuredAmount}}
	case ChangePersonalData:
		out := []FieldChange{}
		for _, m := range contactMembers(&r.Previous.Contact, &r.Requested.Contact) {
			if *m.requested == nil {
				continue
			}
			out = append(out, FieldChange{Field: m.field, Previous: valueOrNil(*m.previous), Requested: **m.requested})
		}
		return out
	default:
		return []FieldChange{}
	}
}

func beneficiaryChanges(previous, requested []Beneficiary) []FieldChange {
	out := []FieldChange{}
	wanted := make(map[string]Beneficiary, len(requested))
	for _, b := range requested {
		wanted[b.documentKey()] = b
	}
	had := make(map[string]bool, len(previous))
	for _, p := range previous {
		had[p.documentKey()] = true
		n, kept := wanted[p.documentKey()]
		switch {
		case !kept:
			out = append(out, FieldChange{Field: FieldBeneficiaries, Previous: p, Requested: nil})
		case n != p:
			out = append(out, FieldChange{Field: FieldBeneficiaries, Previous: p, Requested: n})
		}
	}
	for _, n := range requested {
		if !had[n.documentKey()] {
			out = append(out, FieldChange{Field: FieldBeneficiaries, Previous: nil, Requested: n})
		}
	}
	return out
}

func valueOrNil(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

type contactMember struct {
	field     string
	name      string
	previous  **string
	requested **string
}

// contactMembers pairs each member of two contacts, in the order of the screen.
func contactMembers(previous, requested *ContactData) []contactMember {
	return []contactMember{
		{field: FieldAddress, name: "address", previous: &previous.Address, requested: &requested.Address},
		{field: FieldEmail, name: "email", previous: &previous.Email, requested: &requested.Email},
		{field: FieldPhone, name: "phone", previous: &previous.Phone, requested: &requested.Phone},
	}
}

// previousOf is the photo of the current terms that a request of type t keeps.
func previousOf(t ChangeType, current PolicyTerms) ChangeContent {
	switch t {
	case ChangeBeneficiaries:
		var onerous *OnerousBeneficiary
		if current.Onerous != nil {
			o := *current.Onerous
			onerous = &o
		}
		return ChangeContent{Beneficiaries: append([]Beneficiary{}, current.Beneficiaries...), Onerous: onerous}
	case ChangeInsuredAmount:
		return ChangeContent{InsuredAmount: current.InsuredAmount, Currency: current.Currency}
	default:
		return ChangeContent{Contact: copyContact(current.Contact)}
	}
}

func copyContact(c ContactData) ContactData {
	cp := func(s *string) *string {
		if s == nil {
			return nil
		}
		v := *s
		return &v
	}
	return ContactData{Address: cp(c.Address), Email: cp(c.Email), Phone: cp(c.Phone)}
}

// ChangeInput is what the insured sends to file a request, before it is
// validated. Only the member of the type is read.
type ChangeInput struct {
	Type          string
	Channel       string
	Beneficiaries []BeneficiaryInput
	InsuredAmount *float64
	PersonalData  *ContactInput
}

// BeneficiaryInput is a beneficiary as the insured typed it.
type BeneficiaryInput struct {
	FullName       string
	Relationship   string
	DocumentType   string
	DocumentNumber string
	// SharePercent is the share in percent with up to two decimals; nil
	// when missing.
	SharePercent *float64
}

// ContactInput is the contact data the insured wants; a nil member does not
// change.
type ContactInput struct {
	Address *string
	Email   *string
	Phone   *string
}

// ErrPolicyNotInForce: the policy is not in force, so it cannot be changed.
var ErrPolicyNotInForce = errors.New("the policy is not in force")

// InForceAt tells whether the coverage of the policy is running at now: from
// its start (included) to its end (excluded). A cancellation for non-payment
// (HU-W1.6-02) keeps these dates: whether a policy can change also needs that
// it was not cancelled, which endorsements.Terms.InForce checks.
func (p Policy) InForceAt(now time.Time) bool {
	return !now.Before(p.CoverageStart) && now.Before(p.CoverageEnd)
}

// NewChangeRequest validates what the insured asks for against the current
// terms of the policy and returns the request to file, with the photo of the
// current value, status filed and no id yet. It returns ErrPolicyNotInForce
// when the policy is not in force at now, or a *ValidationError naming every
// invalid field, so the insured fixes them all at once (CA-3).
func NewChangeRequest(p Policy, current PolicyTerms, in ChangeInput, by Requester, now time.Time) (ChangeRequest, error) {
	if !p.InForceAt(now) {
		return ChangeRequest{}, ErrPolicyNotInForce
	}
	var fields []FieldError
	bad := func(field, code string) { fields = append(fields, FieldError{Field: field, Code: code}) }

	t, typeOK := ParseChangeType(strings.TrimSpace(in.Type))
	switch {
	case strings.TrimSpace(in.Type) == "":
		bad("change_type", CodeRequired)
	case !typeOK:
		bad("change_type", CodeInvalidFormat)
	}
	channel, channelOK := ParseChannel(strings.TrimSpace(in.Channel))
	switch {
	case strings.TrimSpace(in.Channel) == "":
		bad("channel", CodeRequired)
	case !channelOK:
		bad("channel", CodeInvalidFormat)
	}
	if !typeOK {
		return ChangeRequest{}, &ValidationError{Fields: fields}
	}

	requested, parseErrors := parseRequested(t, in)
	fields = append(fields, parseErrors...)
	fields = append(fields, ValidateRequested(t, requested, current)...)
	if fields = dedupeFields(fields); len(fields) > 0 {
		return ChangeRequest{}, &ValidationError{Fields: fields}
	}
	return ChangeRequest{
		PolicyID: p.ID, CustomerID: p.CustomerID, Type: t,
		Previous: previousOf(t, current), Requested: requested,
		Channel: channel, RequestedBy: by,
		FiledAt: now, Status: ChangeRequestFiled, StatusChangedAt: now,
	}, nil
}

// parseRequested turns the input into the requested content, normalized,
// and reports what cannot be read: a missing member, a share with more than
// two decimals or an insured value with cents. The rest is checked by
// ValidateRequested.
func parseRequested(t ChangeType, in ChangeInput) (ChangeContent, []FieldError) {
	var fields []FieldError
	bad := func(field, code string) { fields = append(fields, FieldError{Field: field, Code: code}) }
	switch t {
	case ChangeBeneficiaries:
		out := ChangeContent{Beneficiaries: make([]Beneficiary, 0, len(in.Beneficiaries))}
		for i, b := range in.Beneficiaries {
			nb := Beneficiary{
				FullName:       strings.Join(strings.Fields(b.FullName), " "),
				Relationship:   Relationship(strings.TrimSpace(b.Relationship)),
				DocumentType:   DocumentType(strings.ToUpper(strings.TrimSpace(b.DocumentType))),
				DocumentNumber: strings.TrimSpace(b.DocumentNumber),
			}
			share := fmt.Sprintf("beneficiaries[%d].share_percent", i)
			if b.SharePercent == nil {
				bad(share, CodeRequired)
			} else if bp, exact := toBasisPoints(*b.SharePercent); exact {
				nb.ShareBasisPoints = bp
			} else {
				// A share with more than two decimals counts as no share,
				// so it does not also make the total look wrong.
				bad(share, CodeInvalidFormat)
			}
			out.Beneficiaries = append(out.Beneficiaries, nb)
		}
		return out, fields
	case ChangeInsuredAmount:
		if in.InsuredAmount == nil {
			bad(FieldInsuredAmount, CodeRequired)
			return ChangeContent{Currency: DefaultCurrency}, fields
		}
		if *in.InsuredAmount != math.Trunc(*in.InsuredAmount) {
			bad(FieldInsuredAmount, CodeInvalidFormat)
		}
		return ChangeContent{InsuredAmount: *in.InsuredAmount, Currency: DefaultCurrency}, fields
	default:
		if in.PersonalData == nil {
			return ChangeContent{}, fields
		}
		trim := func(s *string, normalize func(string) string) *string {
			if s == nil {
				return nil
			}
			v := normalize(*s)
			return &v
		}
		return ChangeContent{Contact: ContactData{
			Address: trim(in.PersonalData.Address, func(s string) string { return strings.Join(strings.Fields(s), " ") }),
			Email:   trim(in.PersonalData.Email, strings.TrimSpace),
			Phone:   trim(in.PersonalData.Phone, normalizePhone),
		}}, fields
	}
}

// toBasisPoints converts a percentage to basis points and tells whether it
// had at most two decimals.
func toBasisPoints(percent float64) (int, bool) {
	scaled := percent * 100
	rounded := math.Round(scaled)
	if math.IsInf(rounded, 0) || math.IsNaN(rounded) || math.Abs(rounded) > math.MaxInt32 {
		return 0, false
	}
	return int(rounded), math.Abs(scaled-rounded) < 1e-6
}

// normalizePhone removes the spaces and hyphens of a phone number and, when it
// is a Colombian number, the country code: what is stored is its 10 digits.
func normalizePhone(raw string) string {
	s := strings.NewReplacer(" ", "", "-", "").Replace(strings.TrimSpace(raw))
	if m := phonePattern.FindStringSubmatch(s); m != nil {
		return m[2]
	}
	return s
}

// ValidateRequested checks a requested change of type t against the current
// terms of the policy: the shape of every value, the limits, that the shares
// add up to 100 %, that no document repeats and that something changes. It
// returns every problem found (none when the change is valid). HU-W1.4-02
// runs it again against the terms of the moment it applies the request.
func ValidateRequested(t ChangeType, requested ChangeContent, current PolicyTerms) []FieldError {
	switch t {
	case ChangeBeneficiaries:
		return validateBeneficiaries(requested.Beneficiaries, current.Beneficiaries)
	case ChangeInsuredAmount:
		return validateInsuredAmount(requested.InsuredAmount, current.InsuredAmount)
	case ChangePersonalData:
		return validateContact(requested.Contact, current.Contact)
	default:
		return []FieldError{{Field: "change_type", Code: CodeInvalidFormat}}
	}
}

func validateBeneficiaries(requested, current []Beneficiary) []FieldError {
	var fields []FieldError
	bad := func(field, code string) { fields = append(fields, FieldError{Field: field, Code: code}) }
	switch {
	case len(requested) == 0:
		bad(FieldBeneficiaries, CodeRequired)
		return fields
	case len(requested) > MaxBeneficiaries:
		bad(FieldBeneficiaries, CodeOutOfRange)
	}

	total, sharesOK := 0, true
	seen := make(map[string]bool, len(requested))
	for i, b := range requested {
		at := func(member string) string { return fmt.Sprintf("beneficiaries[%d].%s", i, member) }
		switch {
		case b.FullName == "":
			bad(at("full_name"), CodeRequired)
		case len([]rune(b.FullName)) < minFullNameLength || len([]rune(b.FullName)) > maxFullNameLength || !hasLetter(b.FullName) ||
			HasControl(b.FullName):
			bad(at("full_name"), CodeInvalidFormat)
		}
		switch {
		case b.Relationship == "":
			bad(at("relationship"), CodeRequired)
		case !knownRelationship(b.Relationship):
			bad(at("relationship"), CodeInvalidFormat)
		}
		switch {
		case b.DocumentType == "":
			bad(at("document_type"), CodeRequired)
		case !knownDocumentType(b.DocumentType):
			bad(at("document_type"), CodeInvalidFormat)
		}
		numberOK := false
		switch {
		case b.DocumentNumber == "":
			bad(at("document_number"), CodeRequired)
		case !ValidDocumentID(b.DocumentNumber) || (b.DocumentType.numericOnly() && !digitsPattern.MatchString(b.DocumentNumber)):
			bad(at("document_number"), CodeInvalidFormat)
		default:
			numberOK = true
		}
		if numberOK {
			if seen[b.documentKey()] {
				bad(at("document_number"), CodeDuplicate)
			}
			seen[b.documentKey()] = true
		}
		if b.ShareBasisPoints < 1 || b.ShareBasisPoints > FullShareBasisPoints {
			bad(at("share_percent"), CodeOutOfRange)
			sharesOK = false
		}
		total += b.ShareBasisPoints
	}
	if sharesOK && total != FullShareBasisPoints {
		bad(FieldBeneficiaries, CodeShareTotalNot100)
	}
	if len(fields) == 0 && sameBeneficiaries(requested, current) {
		bad(FieldBeneficiaries, CodeNoChange)
	}
	return fields
}

func knownRelationship(r Relationship) bool {
	for _, k := range Relationships {
		if k == r {
			return true
		}
	}
	return false
}

func knownDocumentType(d DocumentType) bool {
	for _, k := range DocumentTypes {
		if k == d {
			return true
		}
	}
	return false
}

// sameBeneficiaries tells whether two lists designate the same people with
// the same data and shares, whatever their order.
func sameBeneficiaries(a, b []Beneficiary) bool {
	if len(a) != len(b) {
		return false
	}
	byKey := make(map[string]Beneficiary, len(b))
	for _, x := range b {
		byKey[x.documentKey()] = x
	}
	for _, x := range a {
		y, ok := byKey[x.documentKey()]
		if !ok || !strings.EqualFold(x.FullName, y.FullName) || x.Relationship != y.Relationship ||
			x.ShareBasisPoints != y.ShareBasisPoints {
			return false
		}
	}
	return true
}

func validateInsuredAmount(requested, current float64) []FieldError {
	switch {
	case requested < MinInsuredAmount || requested > MaxInsuredAmount:
		return []FieldError{{Field: FieldInsuredAmount, Code: CodeOutOfRange}}
	case requested != math.Trunc(requested):
		return []FieldError{{Field: FieldInsuredAmount, Code: CodeInvalidFormat}}
	case requested == current:
		return []FieldError{{Field: FieldInsuredAmount, Code: CodeNoChange}}
	}
	return nil
}

func validateContact(requested, current ContactData) []FieldError {
	if requested.Address == nil && requested.Email == nil && requested.Phone == nil {
		return []FieldError{{Field: "personal_data", Code: CodeRequired}}
	}
	var fields []FieldError
	for _, m := range contactMembers(&current, &requested) {
		v := *m.requested
		if v == nil {
			continue
		}
		code := ""
		switch {
		case *v == "":
			code = CodeRequired
		case !validContactValue(m.name, *v):
			code = CodeInvalidFormat
		case *m.previous != nil && sameContactValue(m.name, **m.previous, *v):
			code = CodeNoChange
		}
		if code != "" {
			fields = append(fields, FieldError{Field: m.field, Code: code})
		}
	}
	return fields
}

func validContactValue(member, v string) bool {
	switch member {
	case "address":
		n := len([]rune(v))
		return n >= minAddressLength && n <= maxAddressLength && !HasControl(v)
	case "email":
		return len(v) <= maxEmailLength && validEmail(v)
	default:
		return len(v) == 10 && digitsPattern.MatchString(v)
	}
}

func sameContactValue(member, a, b string) bool {
	if member == "phone" {
		return a == b
	}
	return strings.EqualFold(a, b)
}

// HasControl tells whether s has a control character (a NUL, a line break, a
// tab…): none belongs in a name, an address or an identifier, and the
// database refuses a NUL in a text.
func HasControl(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// dedupeFields keeps the first problem of each field.
func dedupeFields(fields []FieldError) []FieldError {
	seen := make(map[string]bool, len(fields))
	out := fields[:0]
	for _, f := range fields {
		if seen[f.Field] {
			continue
		}
		seen[f.Field] = true
		out = append(out, f)
	}
	return out
}

// changeRequestIDPattern is SOL-MOD-<year>-<number of at least four digits>.
var changeRequestIDPattern = regexp.MustCompile(`^SOL-MOD-([0-9]{4})-([0-9]{4,9})$`)

// FormatChangeRequestID is the id of the n-th request of a year: SOL-MOD-2026-0042.
func FormatChangeRequestID(year, n int) string { return fmt.Sprintf("SOL-MOD-%04d-%04d", year, n) }

// ParseChangeRequestID reads the year and the number of an id; it reports
// false when raw is not one.
func ParseChangeRequestID(raw string) (year, n int, ok bool) {
	m := changeRequestIDPattern.FindStringSubmatch(raw)
	if m == nil {
		return 0, 0, false
	}
	year, _ = strconv.Atoi(m[1])
	n, _ = strconv.Atoi(m[2])
	if n < 1 {
		return 0, 0, false
	}
	return year, n, true
}

// ChangeRequestYear is the civil year of Colombia of the request filed at
// filedAt: its number restarts every year.
func ChangeRequestYear(filedAt time.Time) int { return filedAt.In(ColombiaTime).Year() }

// Limits of the list of the change requests of the insured (screen A11).
const (
	DefaultChangeRequestPageSize = 20
	MaxChangeRequestPageSize     = 50
)

// ErrInvalidChangeRequestQuery: a status, page, page size or day the list does
// not understand.
var ErrInvalidChangeRequestQuery = errors.New("the query of the change requests is not valid")

// ParseInsuredStatus reads the status of the filter of the list, as the insured
// sees it: filed, in_review, applied or rejected; empty is every status.
func ParseInsuredStatus(raw string) (ChangeRequestStatus, error) {
	switch s := ChangeRequestStatus(strings.TrimSpace(raw)); s {
	case "", ChangeRequestFiled, ChangeRequestInReview, ChangeRequestApplied, ChangeRequestRejected:
		return s, nil
	default:
		return "", fmt.Errorf("%w: status must be filed, in_review, applied or rejected", ErrInvalidChangeRequestQuery)
	}
}

// ChangeRequestFilter is the query of the list of the change requests of the
// insured.
type ChangeRequestFilter struct {
	// Status is the status as the insured sees it (InsuredStatus); empty for
	// every status.
	Status ChangeRequestStatus
	// PolicyID narrows the list to one policy; empty for all of them.
	PolicyID string
	// FiledFrom is the first instant of the civil day of Colombia from which
	// the requests were filed; zero for no lower bound.
	FiledFrom time.Time
	// Page starts at 1; 0 means the first page.
	Page int
	// PageSize is 1 to MaxChangeRequestPageSize; 0 means the default.
	PageSize int
	// Now is the instant of the query: the indicator of the applied requests
	// counts its civil year.
	Now time.Time
}

// WithDefaults fills the first page and the default page size.
func (f ChangeRequestFilter) WithDefaults() ChangeRequestFilter {
	if f.Page == 0 {
		f.Page = 1
	}
	if f.PageSize == 0 {
		f.PageSize = DefaultChangeRequestPageSize
	}
	return f
}

// Validate checks a filter with its defaults applied.
func (f ChangeRequestFilter) Validate() error {
	switch {
	case f.Page < 1 || f.Page > maxExpiringPage:
		return fmt.Errorf("%w: page must be 1 or more", ErrInvalidChangeRequestQuery)
	case f.PageSize < 1 || f.PageSize > MaxChangeRequestPageSize:
		return fmt.Errorf("%w: page_size must be between 1 and %d", ErrInvalidChangeRequestQuery, MaxChangeRequestPageSize)
	case len(f.PolicyID) > maxIdentifierLength:
		return fmt.Errorf("%w: policy_id is too long", ErrInvalidChangeRequestQuery)
	}
	_, err := ParseInsuredStatus(string(f.Status))
	return err
}

// Offset is the number of matching requests before the page.
func (f ChangeRequestFilter) Offset() int { return (f.Page - 1) * f.PageSize }

// Matches tells whether a request passes the filter. It is the reference in Go
// of the condition of the SQL query.
func (f ChangeRequestFilter) Matches(r ChangeRequest) bool {
	switch {
	case f.Status != "" && r.InsuredStatus() != f.Status:
		return false
	case f.PolicyID != "" && r.PolicyID != f.PolicyID:
		return false
	case !f.FiledFrom.IsZero() && r.FiledAt.Before(CivilDayOf(f.FiledFrom)):
		return false
	}
	return true
}

// YearBounds is the civil year of Colombia of Now, as instants in UTC: its
// first instant (included) and the first one of the next year (excluded).
func (f ChangeRequestFilter) YearBounds() (start, end time.Time) {
	y := f.Now.In(ColombiaTime).Year()
	return time.Date(y, 1, 1, 0, 0, 0, 0, ColombiaTime).UTC(), time.Date(y+1, 1, 1, 0, 0, 0, 0, ColombiaTime).UTC()
}

// ChangeRequestRef is a request named by an indicator.
type ChangeRequestRef struct {
	ID              string
	Type            ChangeType
	FiledAt         time.Time
	StatusChangedAt time.Time
}

// ChangeRequestIndicators are the three counters of screen A11, whatever the
// filter: the open requests with the latest one filed, the requests applied
// in the civil year with the last one applied, and the rejected ones (also
// the not applied returned to the insured) with the last one.
type ChangeRequestIndicators struct {
	Open            int
	LatestOpen      *ChangeRequestRef
	AppliedThisYear int
	LastApplied     *ChangeRequestRef
	Rejected        int
	LastRejected    *ChangeRequestRef
}

// ChangeRequestPage is a page of the list with the total that match the
// filter, the indicators and the policies of the insured that have requests
// (the options of the filter by policy).
type ChangeRequestPage struct {
	Items      []ChangeRequest
	Total      int
	Page       int
	PageSize   int
	Indicators ChangeRequestIndicators
	Policies   []string
}

// TotalPages is how many pages the matching requests take (0 when none match).
func (p ChangeRequestPage) TotalPages() int {
	if p.PageSize <= 0 {
		return 0
	}
	return (p.Total + p.PageSize - 1) / p.PageSize
}
