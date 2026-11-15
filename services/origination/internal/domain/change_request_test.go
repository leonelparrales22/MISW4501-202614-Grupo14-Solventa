package domain_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

// changeNow is the instant of the requests: 16 September 2026, 07:00 in Colombia.
var changeNow = time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)

func policyInForce() domain.Policy {
	return domain.Policy{ID: "pol-acc-1", CustomerID: "sub-carolina",
		Holder:        domain.Applicant{FullName: "Carolina Pérez", DocumentID: "1032456789", Email: "carolina@example.com"},
		InsuredAmount: 68_000_000, Currency: "COP",
		CoverageStart: changeNow.AddDate(0, -2, 0), CoverageEnd: changeNow.AddDate(0, 10, 0)}
}

func currentTerms() domain.PolicyTerms {
	return domain.PolicyTerms{InsuredAmount: 68_000_000, Currency: "COP",
		Onerous: &domain.OnerousBeneficiary{PartnerID: "aliado-banco-1", Name: "Banco Aliado", Basis: domain.OnerousBasisOutstandingBalance},
		Contact: domain.ContactData{Email: ptr("carolina@example.com")}}
}

var carolina = domain.Requester{Subject: "sub-carolina", Email: "carolina@example.com"}

func beneficiary(name, docType, number string, share float64) domain.BeneficiaryInput {
	return domain.BeneficiaryInput{FullName: name, Relationship: "child", DocumentType: docType, DocumentNumber: number,
		SharePercent: ptr(share)}
}

func beneficiariesInput(bs ...domain.BeneficiaryInput) domain.ChangeInput {
	return domain.ChangeInput{Type: "beneficiaries", Channel: "web", Beneficiaries: bs}
}

// changeFieldsOf files the input and returns the invalid fields with their
// codes; each field is reported once.
func changeFieldsOf(t *testing.T, in domain.ChangeInput, current domain.PolicyTerms) map[string]string {
	t.Helper()
	_, err := domain.NewChangeRequest(policyInForce(), current, in, carolina, changeNow)
	var invalid *domain.ValidationError
	require.True(t, errors.As(err, &invalid), "expected a validation error, got %v", err)
	out := fieldsOf(t, err)
	require.Len(t, out, len(invalid.Fields), "a field is reported once")
	return out
}

func TestABeneficiariesRequestKeepsThePhotoOfTheCurrentValueAndTheOnerousBeneficiary(t *testing.T) {
	in := beneficiariesInput(beneficiary("  Mateo   Pérez Gómez ", "ti", "1140223001", 60), beneficiary("Lucía Pérez Gómez", "TI", "1142998310", 40))

	r, err := domain.NewChangeRequest(policyInForce(), currentTerms(), in, carolina, changeNow)

	require.NoError(t, err)
	assert.Empty(t, r.ID, "the id is assigned when it is stored")
	assert.Equal(t, "pol-acc-1", r.PolicyID)
	assert.Equal(t, "sub-carolina", r.CustomerID)
	assert.Equal(t, domain.ChangeBeneficiaries, r.Type)
	assert.Equal(t, domain.ChannelWeb, r.Channel)
	assert.Equal(t, carolina, r.RequestedBy)
	assert.Equal(t, domain.ChangeRequestFiled, r.Status)
	assert.Equal(t, changeNow, r.FiledAt)
	assert.Equal(t, changeNow, r.StatusChangedAt)
	assert.Equal(t, []domain.Beneficiary{
		{FullName: "Mateo Pérez Gómez", Relationship: domain.RelationshipChild, DocumentType: domain.DocumentTI, DocumentNumber: "1140223001", ShareBasisPoints: 6000},
		{FullName: "Lucía Pérez Gómez", Relationship: domain.RelationshipChild, DocumentType: domain.DocumentTI, DocumentNumber: "1142998310", ShareBasisPoints: 4000},
	}, r.Requested.Beneficiaries, "normalized: one space between words and the document type in upper case")
	assert.Empty(t, r.Previous.Beneficiaries)
	require.NotNil(t, r.Previous.Onerous, "the photo keeps the onerous beneficiary")
	assert.Equal(t, "Banco Aliado", r.Previous.Onerous.Name)
	assert.InDelta(t, 60.0, r.Requested.Beneficiaries[0].SharePercent(), 1e-9)
}

func TestTheSharesMustAddUpToExactly100(t *testing.T) {
	for _, tc := range []struct {
		name   string
		second float64
		ok     bool
	}{
		{"99.99", 39.99, false},
		{"100", 40, true},
		{"100.01", 40.01, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := beneficiariesInput(beneficiary("Mateo Pérez", "CC", "1140223001", 60), beneficiary("Lucía Pérez", "CC", "1142998310", tc.second))
			_, err := domain.NewChangeRequest(policyInForce(), currentTerms(), in, carolina, changeNow)
			if tc.ok {
				assert.NoError(t, err)
				return
			}
			assert.Equal(t, map[string]string{"beneficiaries": domain.CodeShareTotalNot100}, changeFieldsOf(t, in, currentTerms()))
		})
	}
}

func TestOneToTenBeneficiaries(t *testing.T) {
	list := func(n int) domain.ChangeInput {
		// Shares of whole basis points that add up to 100 %.
		each := domain.FullShareBasisPoints / n
		bs := make([]domain.BeneficiaryInput, n)
		for i := range bs {
			bp := each
			if i == 0 {
				bp = domain.FullShareBasisPoints - each*(n-1)
			}
			bs[i] = beneficiary(fmt.Sprintf("Beneficiario %c", 'A'+i), "CC", fmt.Sprintf("10000%d", i+10), float64(bp)/100)
		}
		return beneficiariesInput(bs...)
	}

	_, err := domain.NewChangeRequest(policyInForce(), currentTerms(), list(10), carolina, changeNow)
	assert.NoError(t, err, "ten is the limit")

	assert.Equal(t, domain.CodeOutOfRange, changeFieldsOf(t, list(11), currentTerms())["beneficiaries"])
	assert.Equal(t, map[string]string{"beneficiaries": domain.CodeRequired}, changeFieldsOf(t, beneficiariesInput(), currentTerms()),
		"the remainder is never left undesignated")
}

func TestEveryInvalidBeneficiaryFieldIsReportedAtOnce(t *testing.T) {
	in := beneficiariesInput(
		domain.BeneficiaryInput{FullName: "", Relationship: "", DocumentType: "", DocumentNumber: "", SharePercent: nil},
		domain.BeneficiaryInput{FullName: "Al", Relationship: "cousin", DocumentType: "NIT", DocumentNumber: "12-34", SharePercent: ptr(0.0)},
		domain.BeneficiaryInput{FullName: "Ana María Pérez", Relationship: "parent", DocumentType: "CC", DocumentNumber: "1234A6", SharePercent: ptr(33.333)},
		domain.BeneficiaryInput{FullName: "Juan Pérez", Relationship: "sibling", DocumentType: "PA", DocumentNumber: "AB12345", SharePercent: ptr(100.5)},
	)

	assert.Equal(t, map[string]string{
		"beneficiaries[0].full_name":       domain.CodeRequired,
		"beneficiaries[0].relationship":    domain.CodeRequired,
		"beneficiaries[0].document_type":   domain.CodeRequired,
		"beneficiaries[0].document_number": domain.CodeRequired,
		"beneficiaries[0].share_percent":   domain.CodeRequired,
		"beneficiaries[1].full_name":       domain.CodeInvalidFormat,
		"beneficiaries[1].relationship":    domain.CodeInvalidFormat,
		"beneficiaries[1].document_type":   domain.CodeInvalidFormat,
		"beneficiaries[1].document_number": domain.CodeInvalidFormat,
		"beneficiaries[1].share_percent":   domain.CodeOutOfRange,
		"beneficiaries[2].document_number": domain.CodeInvalidFormat, // letters in a CC
		"beneficiaries[2].share_percent":   domain.CodeInvalidFormat, // three decimals
		"beneficiaries[3].share_percent":   domain.CodeOutOfRange,
	}, changeFieldsOf(t, in, currentTerms()), "no total is reported while a share is invalid")
}

func TestDocumentNumbersFollowTheirType(t *testing.T) {
	for _, tc := range []struct {
		docType, number string
		ok              bool
	}{
		{"CC", "12345", true},
		{"CC", "1234", false},
		{"TI", "A1234567", false},
		{"RC", "12345678901234567890", true},
		{"RC", "123456789012345678901", false},
		{"CE", "E1234567", true},
		{"PA", "AB123456", true},
		{"PA", "AB 12345", false},
	} {
		in := beneficiariesInput(beneficiary("Mateo Pérez", tc.docType, tc.number, 100))
		_, err := domain.NewChangeRequest(policyInForce(), currentTerms(), in, carolina, changeNow)
		if tc.ok {
			assert.NoError(t, err, "%s %s", tc.docType, tc.number)
			continue
		}
		assert.Equal(t, domain.CodeInvalidFormat, changeFieldsOf(t, in, currentTerms())["beneficiaries[0].document_number"], "%s %s", tc.docType, tc.number)
	}
}

func TestADocumentCannotRepeat(t *testing.T) {
	in := beneficiariesInput(beneficiary("Mateo Pérez", "CC", "1140223001", 50), beneficiary("Mateo P.", "CC", "1140223001", 50))

	assert.Equal(t, map[string]string{"beneficiaries[1].document_number": domain.CodeDuplicate}, changeFieldsOf(t, in, currentTerms()))

	in = beneficiariesInput(beneficiary("Mateo Pérez", "CC", "1140223001", 50), beneficiary("Lucía Pérez", "TI", "1140223001", 50))
	_, err := domain.NewChangeRequest(policyInForce(), currentTerms(), in, carolina, changeNow)
	assert.NoError(t, err, "the same number with another document type is another person")
}

func TestTheSameBeneficiariesAreNoChange(t *testing.T) {
	current := currentTerms()
	current.Beneficiaries = []domain.Beneficiary{
		{FullName: "Mateo Pérez", Relationship: domain.RelationshipChild, DocumentType: domain.DocumentCC, DocumentNumber: "1140223001", ShareBasisPoints: 6000},
		{FullName: "Lucía Pérez", Relationship: domain.RelationshipChild, DocumentType: domain.DocumentCC, DocumentNumber: "1142998310", ShareBasisPoints: 4000},
	}
	same := beneficiariesInput(beneficiary("Lucía Pérez", "CC", "1142998310", 40), beneficiary("mateo pérez", "CC", "1140223001", 60))

	assert.Equal(t, map[string]string{"beneficiaries": domain.CodeNoChange}, changeFieldsOf(t, same, current), "in any order")

	other := beneficiariesInput(beneficiary("Lucía Pérez", "CC", "1142998310", 50), beneficiary("Mateo Pérez", "CC", "1140223001", 50))
	_, err := domain.NewChangeRequest(policyInForce(), current, other, carolina, changeNow)
	assert.NoError(t, err)
}

func TestTheInsuredAmountIsAWholeValueInsideTheLimitsAndDifferentFromTheCurrentOne(t *testing.T) {
	amount := func(v *float64) domain.ChangeInput {
		return domain.ChangeInput{Type: "insured_amount", Channel: "web", InsuredAmount: v}
	}
	for _, tc := range []struct {
		value *float64
		code  string
	}{
		{nil, domain.CodeRequired},
		{ptr(domain.MinInsuredAmount - 1), domain.CodeOutOfRange},
		{ptr(domain.MaxInsuredAmount + 1), domain.CodeOutOfRange},
		{ptr(80_000_000.5), domain.CodeInvalidFormat},
		{ptr(68_000_000.0), domain.CodeNoChange},
	} {
		assert.Equal(t, map[string]string{"insured_amount": tc.code}, changeFieldsOf(t, amount(tc.value), currentTerms()))
	}
	for _, ok := range []float64{domain.MinInsuredAmount, domain.MaxInsuredAmount, 80_000_000} {
		r, err := domain.NewChangeRequest(policyInForce(), currentTerms(), amount(ptr(ok)), carolina, changeNow)
		require.NoError(t, err)
		assert.Equal(t, ok, r.Requested.InsuredAmount)
		assert.Equal(t, "COP", r.Requested.Currency)
		assert.Equal(t, domain.ChangeContent{InsuredAmount: 68_000_000, Currency: "COP"}, r.Previous)
	}
}

func TestPersonalDataNeedsOneValidMemberThatChanges(t *testing.T) {
	data := func(c *domain.ContactInput) domain.ChangeInput {
		return domain.ChangeInput{Type: "personal_data", Channel: "web", PersonalData: c}
	}
	assert.Equal(t, map[string]string{"personal_data": domain.CodeRequired}, changeFieldsOf(t, data(nil), currentTerms()))
	assert.Equal(t, map[string]string{"personal_data": domain.CodeRequired}, changeFieldsOf(t, data(&domain.ContactInput{}), currentTerms()))
	assert.Equal(t, map[string]string{
		"personal_data.address": domain.CodeInvalidFormat,
		"personal_data.email":   domain.CodeInvalidFormat,
		"personal_data.phone":   domain.CodeInvalidFormat,
	}, changeFieldsOf(t, data(&domain.ContactInput{Address: ptr("Cl 1"), Email: ptr("carolina@"), Phone: ptr("300 123 456")}), currentTerms()))
	assert.Equal(t, map[string]string{
		"personal_data.address": domain.CodeRequired,
		"personal_data.email":   domain.CodeNoChange,
	}, changeFieldsOf(t, data(&domain.ContactInput{Address: ptr("   "), Email: ptr(" Carolina@Example.com ")}), currentTerms()),
		"the e-mail compares without case")

	r, err := domain.NewChangeRequest(policyInForce(), currentTerms(),
		data(&domain.ContactInput{Address: ptr(" Calle 10  # 43-20, Medellín "), Phone: ptr("+57 300-123-4567")}), carolina, changeNow)
	require.NoError(t, err)
	assert.Equal(t, "Calle 10 # 43-20, Medellín", *r.Requested.Contact.Address)
	assert.Equal(t, "3001234567", *r.Requested.Contact.Phone, "the 10 digits, without the country code")
	assert.Nil(t, r.Requested.Contact.Email, "only what changes")
	assert.Equal(t, "carolina@example.com", *r.Previous.Contact.Email, "the photo keeps every member")
	assert.Nil(t, r.Previous.Contact.Address)
}

func TestTheTypeAndTheChannelAreRequired(t *testing.T) {
	assert.Equal(t, map[string]string{"change_type": domain.CodeRequired, "channel": domain.CodeRequired},
		changeFieldsOf(t, domain.ChangeInput{}, currentTerms()))
	assert.Equal(t, map[string]string{"change_type": domain.CodeInvalidFormat, "channel": domain.CodeInvalidFormat},
		changeFieldsOf(t, domain.ChangeInput{Type: "holder_name", Channel: "advisor"}, currentTerms()), "nobody files on behalf of the insured")

	r, err := domain.NewChangeRequest(policyInForce(), currentTerms(),
		domain.ChangeInput{Type: "insured_amount", Channel: "mobile", InsuredAmount: ptr(80_000_000.0)}, carolina, changeNow)
	require.NoError(t, err)
	assert.Equal(t, domain.ChannelMobile, r.Channel)
}

func TestOnlyAPolicyInForceCanBeChanged(t *testing.T) {
	in := domain.ChangeInput{Type: "insured_amount", Channel: "web", InsuredAmount: ptr(80_000_000.0)}
	p := policyInForce()

	p.CoverageEnd = changeNow
	_, err := domain.NewChangeRequest(p, currentTerms(), in, carolina, changeNow)
	assert.ErrorIs(t, err, domain.ErrPolicyNotInForce, "the end is excluded")

	p = policyInForce()
	p.CoverageStart = changeNow.Add(time.Second)
	_, err = domain.NewChangeRequest(p, currentTerms(), in, carolina, changeNow)
	assert.ErrorIs(t, err, domain.ErrPolicyNotInForce, "a renewal that has not started yet")

	p.CoverageStart = changeNow
	assert.True(t, p.InForceAt(changeNow), "the start is included")
}

func TestChangesPairTheBeneficiariesByDocument(t *testing.T) {
	mateo := domain.Beneficiary{FullName: "Mateo Pérez", Relationship: domain.RelationshipChild, DocumentType: domain.DocumentTI, DocumentNumber: "1140223001", ShareBasisPoints: 10_000}
	ana := domain.Beneficiary{FullName: "Ana Gómez", Relationship: domain.RelationshipParent, DocumentType: domain.DocumentCC, DocumentNumber: "52123456", ShareBasisPoints: 10_000}
	lucia := domain.Beneficiary{FullName: "Lucía Pérez", Relationship: domain.RelationshipChild, DocumentType: domain.DocumentTI, DocumentNumber: "1142998310", ShareBasisPoints: 4000}
	mateo60 := mateo
	mateo60.ShareBasisPoints = 6000

	r := domain.ChangeRequest{Type: domain.ChangeBeneficiaries,
		Previous:  domain.ChangeContent{Beneficiaries: []domain.Beneficiary{mateo, ana}},
		Requested: domain.ChangeContent{Beneficiaries: []domain.Beneficiary{mateo60, lucia}}}

	assert.Equal(t, []domain.FieldChange{
		{Field: "beneficiaries", Previous: mateo, Requested: mateo60},
		{Field: "beneficiaries", Previous: ana, Requested: nil},
		{Field: "beneficiaries", Previous: nil, Requested: lucia},
	}, r.Changes())
}

func TestChangesOfTheInsuredAmountAndOfThePersonalData(t *testing.T) {
	amount := domain.ChangeRequest{Type: domain.ChangeInsuredAmount,
		Previous: domain.ChangeContent{InsuredAmount: 68_000_000}, Requested: domain.ChangeContent{InsuredAmount: 80_000_000}}
	assert.Equal(t, []domain.FieldChange{{Field: "insured_amount", Previous: 68_000_000.0, Requested: 80_000_000.0}}, amount.Changes())

	data := domain.ChangeRequest{Type: domain.ChangePersonalData,
		Previous:  domain.ChangeContent{Contact: domain.ContactData{Email: ptr("carolina@example.com")}},
		Requested: domain.ChangeContent{Contact: domain.ContactData{Email: ptr("caro@example.com"), Phone: ptr("3001234567")}}}
	assert.Equal(t, []domain.FieldChange{
		{Field: "personal_data.email", Previous: "carolina@example.com", Requested: "caro@example.com"},
		{Field: "personal_data.phone", Previous: nil, Requested: "3001234567"},
	}, data.Changes())

	assert.Empty(t, domain.ChangeRequest{Type: "unknown"}.Changes())
}

func TestValidateRequestedRunsAgainstTheTermsOfTheMoment(t *testing.T) {
	requested := domain.ChangeContent{InsuredAmount: 80_000_000}
	assert.Empty(t, domain.ValidateRequested(domain.ChangeInsuredAmount, requested, currentTerms()))

	applied := currentTerms()
	applied.InsuredAmount = 80_000_000
	assert.Equal(t, []domain.FieldError{{Field: "insured_amount", Code: domain.CodeNoChange}},
		domain.ValidateRequested(domain.ChangeInsuredAmount, requested, applied), "HU-W1.4-02 sees that it was already applied")

	assert.Equal(t, []domain.FieldError{{Field: "change_type", Code: domain.CodeInvalidFormat}},
		domain.ValidateRequested("unknown", requested, currentTerms()))
}

func TestTheOpenRequestsAndTheStatusTheInsuredSees(t *testing.T) {
	for _, tc := range []struct {
		status domain.ChangeRequestStatus
		reason string
		open   bool
		sees   domain.ChangeRequestStatus
	}{
		{domain.ChangeRequestFiled, "", true, domain.ChangeRequestFiled},
		{domain.ChangeRequestInReview, "", true, domain.ChangeRequestInReview},
		{domain.ChangeRequestApplied, "", false, domain.ChangeRequestApplied},
		{domain.ChangeRequestRejected, "falta avalúo", false, domain.ChangeRequestRejected},
		{domain.ChangeRequestNotApplied, "", true, domain.ChangeRequestInReview},
		{domain.ChangeRequestNotApplied, "documento no válido", false, domain.ChangeRequestRejected},
	} {
		r := domain.ChangeRequest{Status: tc.status, DecisionReason: tc.reason}
		assert.Equal(t, tc.open, r.Open(), "%s %q", tc.status, tc.reason)
		assert.Equal(t, tc.sees, r.InsuredStatus(), "%s %q", tc.status, tc.reason)
	}
}

func TestTheIDHasTheYearAndAtLeastFourDigits(t *testing.T) {
	assert.Equal(t, "SOL-MOD-2026-0042", domain.FormatChangeRequestID(2026, 42))
	assert.Equal(t, "SOL-MOD-2026-12345", domain.FormatChangeRequestID(2026, 12345))

	for raw, want := range map[string][2]int{"SOL-MOD-2026-0042": {2026, 42}, "SOL-MOD-2027-12345": {2027, 12345}} {
		year, n, ok := domain.ParseChangeRequestID(raw)
		require.True(t, ok, raw)
		assert.Equal(t, want, [2]int{year, n})
	}
	for _, raw := range []string{"SOL-MOD-2026-042", "SOL-MOD-2026-0000", "sol-mod-2026-0042", "SOL-MOD-26-0042", "pol-acc-1", ""} {
		_, _, ok := domain.ParseChangeRequestID(raw)
		assert.False(t, ok, raw)
	}

	// 31 December, 20:00 in Colombia is already 1 January in UTC.
	assert.Equal(t, 2026, domain.ChangeRequestYear(time.Date(2027, 1, 1, 1, 0, 0, 0, time.UTC)))
}

func TestTheFilterOfTheListSpeaksTheStatusOfTheInsured(t *testing.T) {
	f := domain.ChangeRequestFilter{}.WithDefaults()
	assert.Equal(t, 1, f.Page)
	assert.Equal(t, domain.DefaultChangeRequestPageSize, f.PageSize)
	require.NoError(t, f.Validate())

	for _, bad := range []domain.ChangeRequestFilter{
		{Status: "not_applied", Page: 1, PageSize: 20},
		{Page: 0, PageSize: 20},
		{Page: 1, PageSize: domain.MaxChangeRequestPageSize + 1},
		{Page: 1, PageSize: 20, PolicyID: string(make([]byte, 129))},
	} {
		assert.ErrorIs(t, bad.Validate(), domain.ErrInvalidChangeRequestQuery, "%+v", bad)
	}
	_, err := domain.ParseInsuredStatus("approved")
	assert.ErrorIs(t, err, domain.ErrInvalidChangeRequestQuery)

	returned := domain.ChangeRequest{PolicyID: "pol-1", Status: domain.ChangeRequestNotApplied, DecisionReason: "documento no válido",
		FiledAt: time.Date(2026, 9, 16, 5, 0, 0, 0, time.UTC)} // 16 September, 00:00 in Colombia
	working := domain.ChangeRequest{PolicyID: "pol-2", Status: domain.ChangeRequestNotApplied, FiledAt: returned.FiledAt}

	assert.True(t, domain.ChangeRequestFilter{Status: domain.ChangeRequestRejected}.Matches(returned))
	assert.False(t, domain.ChangeRequestFilter{Status: domain.ChangeRequestInReview}.Matches(returned))
	assert.True(t, domain.ChangeRequestFilter{Status: domain.ChangeRequestInReview}.Matches(working))
	assert.False(t, domain.ChangeRequestFilter{PolicyID: "pol-2"}.Matches(returned))
	assert.True(t, domain.ChangeRequestFilter{FiledFrom: civilDay(t, "2026-09-16")}.Matches(returned), "the day is included")
	assert.False(t, domain.ChangeRequestFilter{FiledFrom: civilDay(t, "2026-09-17")}.Matches(returned))
	assert.Equal(t, 20, domain.ChangeRequestFilter{Page: 2, PageSize: 20}.Offset())

	start, end := domain.ChangeRequestFilter{Now: time.Date(2027, 1, 1, 1, 0, 0, 0, time.UTC)}.YearBounds()
	assert.Equal(t, time.Date(2026, 1, 1, 5, 0, 0, 0, time.UTC), start, "the civil year of Colombia")
	assert.Equal(t, time.Date(2027, 1, 1, 5, 0, 0, 0, time.UTC), end)

	assert.Equal(t, 3, domain.ChangeRequestPage{Total: 41, PageSize: 20}.TotalPages())
	assert.Zero(t, domain.ChangeRequestPage{}.TotalPages())

	_, ok := domain.ParseChangeType("beneficiaries")
	assert.True(t, ok)
	_, ok = domain.ParseChangeType("holder_name")
	assert.False(t, ok)
}

func TestControlCharactersAreNotValidText(t *testing.T) {
	in := beneficiariesInput(beneficiary("Mateo\x00 Pérez", "CC", "1140223001", 100))
	assert.Equal(t, map[string]string{"beneficiaries[0].full_name": domain.CodeInvalidFormat}, changeFieldsOf(t, in, currentTerms()))

	data := domain.ChangeInput{Type: "personal_data", Channel: "web", PersonalData: &domain.ContactInput{Address: ptr("Calle 10\x00 # 43-20")}}
	assert.Equal(t, map[string]string{"personal_data.address": domain.CodeInvalidFormat}, changeFieldsOf(t, data, currentTerms()))

	assert.True(t, domain.HasControl("pol\x00"))
	assert.False(t, domain.HasControl("SOL-MOD-2026-0042"))
}
