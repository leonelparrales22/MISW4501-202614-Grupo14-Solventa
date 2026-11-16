package domain

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// HU-W1.4-02: an operations analyst reviews the change requests the insured
// filed (HU-W1.4-01) and applies or rejects them. Applying runs the business
// validations of the type of the change (decision D-4 a): when one fails the
// endorsement is not applied and the attempt is recorded with it; when all
// pass, the endorsement is recorded with the photo of the terms that result,
// which every channel reads from then on (decision D-3 a).

// ValidationCode names a business validation of an endorsement.
type ValidationCode string

// The validations of decision D-4 a. The guards are evaluated always but are
// listed only when they do not pass, so a change of beneficiaries keeps the
// five validations of the screen.
const (
	ValidationSharesTotal100            ValidationCode = "shares_total_100"
	ValidationOnerousIntact             ValidationCode = "onerous_beneficiary_intact"
	ValidationIdentityDocuments         ValidationCode = "identity_documents_valid"
	ValidationNoOpenClaim               ValidationCode = "no_open_claim"
	ValidationRequesterIsHolder         ValidationCode = "requester_is_holder"
	ValidationInsuredAmountWithinLimits ValidationCode = "insured_amount_within_limits"
	ValidationPersonalDataValid         ValidationCode = "personal_data_valid"
	ValidationPolicyInForce             ValidationCode = "policy_in_force"
	ValidationPreviousValuesCurrent     ValidationCode = "previous_values_current"
	ValidationNoRenewalInProgress       ValidationCode = "no_renewal_in_progress"
	ValidationPremiumRecalculated       ValidationCode = "premium_recalculated"
)

// Names of the values of the share validation.
const (
	validationParamShares = "shares"
	validationParamTotal  = "total"
)

// ValidationOutcome is the result of one validation.
type ValidationOutcome string

const (
	// ValidationPassed: the validation holds ("Cumple").
	ValidationPassed ValidationOutcome = "passed"
	// ValidationFailed: the validation does not hold ("No cumple"): the
	// endorsement is not applied.
	ValidationFailed ValidationOutcome = "failed"
	// ValidationUnverifiable: the source of the validation did not answer
	// ("No verificable"): nothing can be decided, so nothing changes.
	ValidationUnverifiable ValidationOutcome = "unverifiable"
)

// ValidationResult is one validation of a request with the values its text
// shows (for example the shares, the verifications or the open claims).
type ValidationResult struct {
	Code    ValidationCode
	Outcome ValidationOutcome
	// Guard tells that the validation is listed only when it does not pass.
	Guard  bool
	Params map[string]string
}

// Validations are the validations of a request, in the order of the screen.
type Validations []ValidationResult

// Failed tells whether any validation does not hold.
func (v Validations) Failed() bool { return v.any(ValidationFailed) }

// Unverifiable tells whether any validation could not be verified.
func (v Validations) Unverifiable() bool { return v.any(ValidationUnverifiable) }

func (v Validations) any(o ValidationOutcome) bool {
	for _, r := range v {
		if r.Outcome == o {
			return true
		}
	}
	return false
}

// Listed drops the guards that pass: what the analyst sees.
func (v Validations) Listed() Validations {
	out := Validations{}
	for _, r := range v {
		if r.Guard && r.Outcome == ValidationPassed {
			continue
		}
		out = append(out, r)
	}
	return out
}

// FirstFailed is the code of the first validation that does not hold, or
// empty when all hold.
func (v Validations) FirstFailed() ValidationCode {
	for _, r := range v {
		if r.Outcome == ValidationFailed {
			return r.Code
		}
	}
	return ""
}

// Of returns the codes of the validations with the outcome.
func (v Validations) Of(o ValidationOutcome) []ValidationCode {
	var out []ValidationCode
	for _, r := range v {
		if r.Outcome == o {
			out = append(out, r.Code)
		}
	}
	return out
}

// DocumentStatus is what the registry says of the identity document of a
// beneficiary.
type DocumentStatus string

const (
	DocumentValid        DocumentStatus = "valid"
	DocumentNotFound     DocumentStatus = "not_found"
	DocumentUnverifiable DocumentStatus = "unverifiable"
)

// DocumentCheck is the answer of the registry for one beneficiary.
type DocumentCheck struct {
	Beneficiary Beneficiary
	Status      DocumentStatus
}

// EndorsementFacts are what the validations of a request need, gathered from
// the policy and the external sources before deciding.
type EndorsementFacts struct {
	// Policy is the policy as it stands (with its applied endorsements).
	Policy Policy
	// Current are its current terms.
	Current PolicyTerms
	// Now is the instant of the evaluation.
	Now time.Time
	// InForce tells whether the policy can change at Now: its coverage is
	// running and it was not cancelled for non-payment (HU-W1.6-02). The
	// caller reads it from endorsements.Terms.InForce, the rule of every
	// change of a policy (adjustment A-9).
	InForce bool
	// RenewalInProgress tells that the current period has an accepted
	// renewal not yet active (guard of a change of insured value).
	RenewalInProgress bool
	// Documents are the checks of BeneficiariesToCheck, in that order.
	Documents []DocumentCheck
	// OpenClaims is the number of claims in progress of the policy; nil
	// when claims did not answer.
	OpenClaims *int
	// PremiumPriced tells that the rating engine priced the new insured
	// value (a change of insured value only): false when the original offer
	// of the policy is not found.
	PremiumPriced bool
}

// BeneficiariesToCheck are the requested beneficiaries whose document the
// registry must check: the new ones and the ones with another document. The
// beneficiaries already designated with the same document were checked when
// they were applied.
func BeneficiariesToCheck(r ChangeRequest, current PolicyTerms) []Beneficiary {
	if r.Type != ChangeBeneficiaries {
		return nil
	}
	known := make(map[string]bool, len(current.Beneficiaries))
	for _, b := range current.Beneficiaries {
		known[b.documentKey()] = true
	}
	var out []Beneficiary
	for _, b := range r.Requested.Beneficiaries {
		if !known[b.documentKey()] {
			out = append(out, b)
		}
	}
	return out
}

// NeedsOpenClaims tells whether the validations of a request of type t ask
// claims for the claims in progress of the policy.
func NeedsOpenClaims(t ChangeType) bool { return t == ChangeBeneficiaries || t == ChangeInsuredAmount }

// EvaluateEndorsement runs the validations of the type of the request
// (decision D-4 a) against the facts. It never decides alone whether to apply:
// the caller applies only when nothing failed and nothing is unverifiable.
func EvaluateEndorsement(r ChangeRequest, f EndorsementFacts) Validations {
	content := contentProblems(r, f.Current)
	var out Validations
	switch r.Type {
	case ChangeBeneficiaries:
		out = Validations{
			sharesTotal(r, content),
			onerousIntact(r, f.Current),
			identityDocuments(f.Documents),
			noOpenClaim(f.OpenClaims),
			requesterIsHolder(r, f.Policy),
		}
	case ChangeInsuredAmount:
		out = Validations{
			insuredAmountWithinLimits(r, content),
			noOpenClaim(f.OpenClaims),
			requesterIsHolder(r, f.Policy),
		}
	default:
		out = Validations{
			personalDataValid(content),
			requesterIsHolder(r, f.Policy),
		}
	}
	out = append(out, policyInForce(f.Policy, f.InForce), previousValuesCurrent(r, f.Current))
	if r.Type == ChangeInsuredAmount {
		out = append(out, noRenewalInProgress(f.RenewalInProgress), premiumRecalculated(f.PremiumPriced))
	}
	return out
}

// contentProblems runs ValidateRequested of HU-W1.4-01 again, against the
// terms of now. The content was valid when it was filed; only no_change
// depends on the current terms, and previous_values_current answers for it.
func contentProblems(r ChangeRequest, current PolicyTerms) []FieldError {
	var out []FieldError
	for _, f := range ValidateRequested(r.Type, r.Requested, current) {
		if f.Code != CodeNoChange {
			out = append(out, f)
		}
	}
	return out
}

func passIf(ok bool) ValidationOutcome {
	if ok {
		return ValidationPassed
	}
	return ValidationFailed
}

func sharesTotal(r ChangeRequest, problems []FieldError) ValidationResult {
	total := 0
	shares := make([]string, 0, len(r.Requested.Beneficiaries))
	for _, b := range r.Requested.Beneficiaries {
		total += b.ShareBasisPoints
		shares = append(shares, formatBasisPoints(b.ShareBasisPoints))
	}
	return ValidationResult{Code: ValidationSharesTotal100, Outcome: passIf(len(problems) == 0 && total == FullShareBasisPoints),
		Params: map[string]string{validationParamShares: strings.Join(shares, "|"), validationParamTotal: formatBasisPoints(total)}}
}

// formatBasisPoints writes basis points as a percentage: 6000 → "60", 3350 → "33.5".
func formatBasisPoints(bp int) string {
	return strconv.FormatFloat(float64(bp)/100, 'f', -1, 64)
}

func onerousIntact(r ChangeRequest, current PolicyTerms) ValidationResult {
	res := ValidationResult{Code: ValidationOnerousIntact, Params: map[string]string{}}
	prev, cur := r.Previous.Onerous, current.Onerous
	if cur != nil {
		res.Params["lender"] = cur.Name
		if res.Params["lender"] == "" {
			res.Params["lender"] = cur.PartnerID
		}
		res.Params["basis"] = cur.Basis
	}
	// The lender keeps the outstanding balance: the change neither removes it
	// nor replaces it (the requested list never carries it, HU-W1.4-01).
	intact := cur != nil && cur.Basis == OnerousBasisOutstandingBalance &&
		(prev == nil || (prev.PartnerID == cur.PartnerID && prev.Basis == cur.Basis))
	res.Outcome = passIf(intact)
	return res
}

func identityDocuments(checks []DocumentCheck) ValidationResult {
	res := ValidationResult{Code: ValidationIdentityDocuments, Outcome: ValidationPassed,
		Params: map[string]string{"checks": strconv.Itoa(len(checks))}}
	notFound := 0
	for _, c := range checks {
		switch c.Status {
		case DocumentNotFound:
			notFound++
			if _, named := res.Params["document"]; !named {
				res.Params["document"] = string(c.Beneficiary.DocumentType) + " " + c.Beneficiary.DocumentNumber
			}
		case DocumentUnverifiable:
			if res.Outcome == ValidationPassed {
				res.Outcome = ValidationUnverifiable
			}
		}
	}
	if notFound > 0 {
		res.Outcome = ValidationFailed
		res.Params["not_found"] = strconv.Itoa(notFound)
	}
	return res
}

func noOpenClaim(open *int) ValidationResult {
	res := ValidationResult{Code: ValidationNoOpenClaim, Params: map[string]string{}}
	if open == nil {
		res.Outcome = ValidationUnverifiable
		return res
	}
	res.Params["open_claims"] = strconv.Itoa(*open)
	res.Outcome = passIf(*open == 0)
	return res
}

func requesterIsHolder(r ChangeRequest, p Policy) ValidationResult {
	res := ValidationResult{Code: ValidationRequesterIsHolder, Params: map[string]string{"channel": string(r.Channel)}}
	if r.RequestedBy.Name != "" {
		res.Params["requester"] = r.RequestedBy.Name
	}
	res.Outcome = passIf(r.RequestedBy.Subject != "" && r.RequestedBy.Subject == p.CustomerID)
	return res
}

func insuredAmountWithinLimits(r ChangeRequest, problems []FieldError) ValidationResult {
	return ValidationResult{Code: ValidationInsuredAmountWithinLimits, Outcome: passIf(len(problems) == 0),
		Params: map[string]string{
			"requested": strconv.FormatFloat(r.Requested.InsuredAmount, 'f', -1, 64),
			"min":       strconv.FormatFloat(MinInsuredAmount, 'f', -1, 64),
			"max":       strconv.FormatFloat(MaxInsuredAmount, 'f', -1, 64),
		}}
}

func personalDataValid(problems []FieldError) ValidationResult {
	res := ValidationResult{Code: ValidationPersonalDataValid, Outcome: passIf(len(problems) == 0), Params: map[string]string{}}
	if len(problems) > 0 {
		fields := make([]string, 0, len(problems))
		for _, p := range problems {
			fields = append(fields, p.Field)
		}
		res.Params["fields"] = strings.Join(fields, "|")
	}
	return res
}

// policyInForce fails for a policy that cannot change: its coverage ended or
// has not started, or it was cancelled for non-payment (adjustment A-9).
func policyInForce(p Policy, inForce bool) ValidationResult {
	return ValidationResult{Code: ValidationPolicyInForce, Guard: true, Outcome: passIf(inForce),
		Params: map[string]string{"coverage_end": p.CoverageEnd.UTC().Format(time.RFC3339)}}
}

// previousValuesCurrent checks that the values the request started from are
// still the current ones: another endorsement of the same policy may have
// changed them since it was filed.
func previousValuesCurrent(r ChangeRequest, current PolicyTerms) ValidationResult {
	same := true
	switch r.Type {
	case ChangeBeneficiaries:
		same = sameBeneficiaries(r.Previous.Beneficiaries, current.Beneficiaries)
	case ChangeInsuredAmount:
		same = ToMinorUnits(r.Previous.InsuredAmount) == ToMinorUnits(current.InsuredAmount)
	case ChangePersonalData:
		for _, m := range contactMembers(&r.Previous.Contact, &r.Requested.Contact) {
			if *m.requested == nil {
				continue
			}
			now := currentContactMember(current.Contact, m.name)
			if !sameOptional(m.name, *m.previous, now) {
				same = false
			}
		}
	}
	return ValidationResult{Code: ValidationPreviousValuesCurrent, Guard: true, Outcome: passIf(same), Params: map[string]string{}}
}

func currentContactMember(c ContactData, name string) *string {
	switch name {
	case "address":
		return c.Address
	case "email":
		return c.Email
	default:
		return c.Phone
	}
}

func sameOptional(member string, a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return sameContactValue(member, *a, *b)
}

func noRenewalInProgress(inProgress bool) ValidationResult {
	return ValidationResult{Code: ValidationNoRenewalInProgress, Guard: true, Outcome: passIf(!inProgress), Params: map[string]string{}}
}

// premiumRecalculated is not a business rule but a precondition: a new insured
// value is applied with the premium the engine prices for it (decision D-7 a),
// so without the original offer nothing can be decided.
func premiumRecalculated(priced bool) ValidationResult {
	res := ValidationResult{Code: ValidationPremiumRecalculated, Guard: true, Outcome: ValidationPassed, Params: map[string]string{}}
	if !priced {
		res.Outcome = ValidationUnverifiable
	}
	return res
}

// EndorsedTerms are the contractual terms that result from an endorsement:
// the terms of HU-W1.4-01 plus the premium and the coverages. The last
// endorsement of a policy is its current state (decision D-3 a).
type EndorsedTerms struct {
	PolicyTerms
	Premium   float64
	Coverages []Coverage
}

// TermsOf joins the current terms of a policy with its premium and coverages.
func TermsOf(p Policy, current PolicyTerms) EndorsedTerms {
	return EndorsedTerms{PolicyTerms: current, Premium: p.Premium, Coverages: append([]Coverage(nil), p.Coverages...)}
}

// ApplyChange returns the terms with the change of the request applied. A new
// insured value takes the premium and the coverages the engine priced
// (decision D-7 a); rated is ignored for the other types.
func ApplyChange(t EndorsedTerms, r ChangeRequest, rated Rating) EndorsedTerms {
	out := t
	out.Beneficiaries = append([]Beneficiary{}, t.Beneficiaries...)
	out.Coverages = append([]Coverage(nil), t.Coverages...)
	out.Contact = copyContact(t.Contact)
	if t.Onerous != nil {
		o := *t.Onerous
		out.Onerous = &o
	}
	switch r.Type {
	case ChangeBeneficiaries:
		out.Beneficiaries = append([]Beneficiary{}, r.Requested.Beneficiaries...)
	case ChangeInsuredAmount:
		out.InsuredAmount = r.Requested.InsuredAmount
		out.Premium = rated.Premium
		if len(rated.Coverages) > 0 {
			out.Coverages = append([]Coverage(nil), rated.Coverages...)
		}
	case ChangePersonalData:
		for _, m := range contactMembers(&out.Contact, &r.Requested.Contact) {
			if v := *m.requested; v != nil {
				s := *v
				*m.previous = &s
			}
		}
	}
	return out
}

// Actor is who did something on a request: the subject, name and e-mail of the
// token of the analyst.
type Actor struct {
	Subject string
	Name    string
	Email   string
}

// DisplayName is the name of the actor, or the e-mail when the account has no
// name (#105).
func (a Actor) DisplayName() string {
	if a.Name != "" {
		return a.Name
	}
	return a.Email
}

// EndorsementOutcome is what an attempt to apply a request ended in.
type EndorsementOutcome string

const (
	OutcomeApplied    EndorsementOutcome = "applied"
	OutcomeNotApplied EndorsementOutcome = "not_applied"
)

// EndorsementAttempt is one press of "Aplicar endoso" that reached a decision,
// with the validations it ran. It is never changed nor deleted.
type EndorsementAttempt struct {
	ID          string
	RequestID   string
	PolicyID    string
	Outcome     EndorsementOutcome
	Validations Validations
	By          Actor
	At          time.Time
}

// Endorsement is an applied request: the photo of the terms that result and
// the version of the policy it makes. It is never changed nor deleted.
type Endorsement struct {
	// ID is END-<year>-<number>, assigned when it is stored.
	ID        string
	RequestID string
	PolicyID  string
	Type      ChangeType
	// Version is the number of the endorsement in its policy (1, 2, …): two
	// endorsements never start from the same version.
	Version int
	Terms   EndorsedTerms
	// PreviousPremium is the premium before the endorsement.
	PreviousPremium float64
	AttemptID       string
	By              Actor
	At              time.Time
}

// LatestTerms are the terms of the last endorsement that governs a policy:
// its own (Own, with its version) or, for a renewal without endorsements of
// its own, the last one of the policy it renews (decision D-3 a+).
type LatestTerms struct {
	Found bool
	Own   bool
	// Version is the version of the last own endorsement (0 without any).
	Version       int
	Terms         EndorsedTerms
	EndorsementID string
	Type          ChangeType
	At            time.Time
}

// RequestActionType is what an analyst did on a request besides applying it.
type RequestActionType string

const (
	ActionReview    RequestActionType = "review"
	ActionRejection RequestActionType = "rejection"
	ActionReturn    RequestActionType = "return"
)

// RequestAction is the taking, the rejection or the return of a request, with
// who and when; it is never changed nor deleted.
type RequestAction struct {
	RequestID    string
	Type         RequestActionType
	Observations string
	By           Actor
	At           time.Time
}

// RequestRecord is what was done on a request: its endorsement (applied), its
// last attempt and its actions, the oldest first.
type RequestRecord struct {
	Endorsement *Endorsement
	LastAttempt *EndorsementAttempt
	Actions     []RequestAction
}

// Last returns the last action of the type, or nil.
func (r RequestRecord) Last(t RequestActionType) *RequestAction {
	for i := len(r.Actions) - 1; i >= 0; i-- {
		if r.Actions[i].Type == t {
			a := r.Actions[i]
			return &a
		}
	}
	return nil
}

// Transitions of a request (decision D-8 a, D-9 a).

// CanReview tells whether an analyst can take the request: it is filed, or
// already in review (taking it again changes nothing).
func (r ChangeRequest) CanReview() bool {
	return r.Status == ChangeRequestFiled || r.Status == ChangeRequestInReview
}

// CanDecide tells whether the request can still be applied or rejected: filed
// or in review. A request not applied is returned to the insured, not applied
// again: they file a new one with the right values.
func (r ChangeRequest) CanDecide() bool {
	return r.Status == ChangeRequestFiled || r.Status == ChangeRequestInReview
}

// CanReturn tells whether the request can be returned to the insured: it was
// not applied and nobody returned it yet.
func (r ChangeRequest) CanReturn() bool {
	return r.Status == ChangeRequestNotApplied && r.DecisionReason == ""
}

// Returned tells whether the analyst returned the request to the insured.
func (r ChangeRequest) Returned() bool {
	return r.Status == ChangeRequestNotApplied && r.DecisionReason != ""
}

// Limits of the observations of a rejection or a return.
const (
	MinEndorsementObservations = 10
	MaxEndorsementObservations = 500
)

// ErrEndorsementObservations: a rejection or a return needs observations of 10 to
// 500 characters, which the insured reads.
var ErrEndorsementObservations = errors.New("observations of 10 to 500 characters are required")

// NormalizeObservations trims the observations and checks their length (in
// characters, after trimming). Line breaks and tabs are allowed; any other
// control character is not.
func NormalizeObservations(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	n := len([]rune(s))
	if n < MinEndorsementObservations || n > MaxEndorsementObservations {
		return "", ErrEndorsementObservations
	}
	for _, r := range s {
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' {
			return "", ErrEndorsementObservations
		}
	}
	return s, nil
}

// endorsementIDPattern is END-<year>-<number of at least four digits>.
var endorsementIDPattern = regexp.MustCompile(`^END-([0-9]{4})-([0-9]{4,9})$`)

// FormatEndorsementID is the id of the n-th endorsement of a year: END-2026-0102.
func FormatEndorsementID(year, n int) string { return fmt.Sprintf("END-%04d-%04d", year, n) }

// ValidEndorsementID tells whether raw is an endorsement id.
func ValidEndorsementID(raw string) bool { return endorsementIDPattern.MatchString(raw) }

// EndorsementYear is the civil year of Colombia of an endorsement applied at
// at: its number restarts every year.
func EndorsementYear(at time.Time) int { return at.In(ColombiaTime).Year() }
