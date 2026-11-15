package domain_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

// HU-W1.4-02: the business validations of an endorsement by type of change
// (decision D-4 a), the transitions of a request and the observations.

func filedRequest(t *testing.T, in domain.ChangeInput) domain.ChangeRequest {
	t.Helper()
	r, err := domain.NewChangeRequest(policyInForce(), currentTerms(), in, carolina, changeNow)
	require.NoError(t, err)
	return r
}

func beneficiariesRequest(t *testing.T) domain.ChangeRequest {
	return filedRequest(t, beneficiariesInput(beneficiary("Mateo Pérez Gómez", "TI", "1140223001", 60),
		beneficiary("Lucía Pérez Gómez", "TI", "1142998310", 40)))
}

func insuredAmountRequest(t *testing.T, amount float64) domain.ChangeRequest {
	return filedRequest(t, domain.ChangeInput{Type: "insured_amount", Channel: "web", InsuredAmount: ptr(amount)})
}

func personalDataRequest(t *testing.T) domain.ChangeRequest {
	return filedRequest(t, domain.ChangeInput{Type: "personal_data", Channel: "mobile",
		PersonalData: &domain.ContactInput{Phone: ptr("3012345678"), Address: ptr("Calle 100 # 15-20, Bogotá")}})
}

// allPass are the facts under which every validation holds.
func allPass(r domain.ChangeRequest) domain.EndorsementFacts {
	zero := 0
	f := domain.EndorsementFacts{Policy: policyInForce(), Current: currentTerms(), Now: changeNow, OpenClaims: &zero,
		PremiumPriced: true}
	for _, b := range domain.BeneficiariesToCheck(r, f.Current) {
		f.Documents = append(f.Documents, domain.DocumentCheck{Beneficiary: b, Status: domain.DocumentValid})
	}
	return f
}

func outcomes(v domain.Validations) map[domain.ValidationCode]domain.ValidationOutcome {
	out := map[domain.ValidationCode]domain.ValidationOutcome{}
	for _, r := range v {
		out[r.Code] = r.Outcome
	}
	return out
}

func codes(v domain.Validations) []domain.ValidationCode {
	out := []domain.ValidationCode{}
	for _, r := range v {
		out = append(out, r.Code)
	}
	return out
}

func TestEachTypeListsItsValidationsAndHidesTheGuardsThatPass(t *testing.T) {
	for _, tc := range []struct {
		name string
		r    domain.ChangeRequest
		want []domain.ValidationCode
	}{
		{"beneficiaries: the five of the screen", beneficiariesRequest(t), []domain.ValidationCode{
			domain.ValidationSharesTotal100, domain.ValidationOnerousIntact, domain.ValidationIdentityDocuments,
			domain.ValidationNoOpenClaim, domain.ValidationRequesterIsHolder}},
		{"insured amount", insuredAmountRequest(t, 120_000_000), []domain.ValidationCode{
			domain.ValidationInsuredAmountWithinLimits, domain.ValidationNoOpenClaim, domain.ValidationRequesterIsHolder}},
		{"personal data", personalDataRequest(t), []domain.ValidationCode{
			domain.ValidationPersonalDataValid, domain.ValidationRequesterIsHolder}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := domain.EvaluateEndorsement(tc.r, allPass(tc.r))

			assert.False(t, v.Failed())
			assert.False(t, v.Unverifiable())
			assert.Equal(t, tc.want, codes(v.Listed()))
			assert.Empty(t, v.FirstFailed())
		})
	}
}

func TestTheValuesOfTheValidationsAreTheOnesOfTheScreen(t *testing.T) {
	r := beneficiariesRequest(t)
	r.RequestedBy.Name = "Carolina Pérez Ruiz"

	v := domain.EvaluateEndorsement(r, allPass(r)).Listed()

	assert.Equal(t, map[string]string{"shares": "60|40", "total": "100"}, v[0].Params)
	assert.Equal(t, map[string]string{"lender": "Banco Aliado", "basis": domain.OnerousBasisOutstandingBalance}, v[1].Params)
	assert.Equal(t, map[string]string{"checks": "2"}, v[2].Params, "the two new beneficiaries are checked")
	assert.Equal(t, map[string]string{"open_claims": "0"}, v[3].Params)
	assert.Equal(t, map[string]string{"requester": "Carolina Pérez Ruiz", "channel": "web"}, v[4].Params)
}

func TestOnlyTheNewDocumentsAreCheckedInTheRegistry(t *testing.T) {
	r := beneficiariesRequest(t)
	current := currentTerms()
	current.Beneficiaries = []domain.Beneficiary{{FullName: "Mateo Pérez Gómez", Relationship: domain.RelationshipChild,
		DocumentType: domain.DocumentTI, DocumentNumber: "1140223001", ShareBasisPoints: 10_000}}

	got := domain.BeneficiariesToCheck(r, current)

	require.Len(t, got, 1, "Mateo keeps his document: only Lucía is checked")
	assert.Equal(t, "1142998310", got[0].DocumentNumber)
	assert.Empty(t, domain.BeneficiariesToCheck(insuredAmountRequest(t, 120_000_000), current))
}

func TestTheIdentityDocumentsFailWhenOneIsNotInTheRegistry(t *testing.T) {
	r := beneficiariesRequest(t)
	for _, tc := range []struct {
		name     string
		statuses []domain.DocumentStatus
		want     domain.ValidationOutcome
	}{
		{"all valid", []domain.DocumentStatus{domain.DocumentValid, domain.DocumentValid}, domain.ValidationPassed},
		{"one not found", []domain.DocumentStatus{domain.DocumentValid, domain.DocumentNotFound}, domain.ValidationFailed},
		{"one unverifiable", []domain.DocumentStatus{domain.DocumentUnverifiable, domain.DocumentValid}, domain.ValidationUnverifiable},
		{"not found wins over unverifiable", []domain.DocumentStatus{domain.DocumentUnverifiable, domain.DocumentNotFound}, domain.ValidationFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := allPass(r)
			for i, s := range tc.statuses {
				f.Documents[i].Status = s
			}

			v := domain.EvaluateEndorsement(r, f)

			assert.Equal(t, tc.want, outcomes(v)[domain.ValidationIdentityDocuments])
			if tc.want == domain.ValidationFailed {
				assert.Equal(t, domain.ValidationIdentityDocuments, v.FirstFailed())
				assert.Equal(t, "TI 1142998310", v[2].Params["document"])
				assert.Equal(t, "1", v[2].Params["not_found"])
			}
		})
	}
}

func TestAClaimInProgressBlocksBeneficiariesAndInsuredAmountButNotContactData(t *testing.T) {
	one := 1
	for _, tc := range []struct {
		r    domain.ChangeRequest
		open *int
		want domain.ValidationOutcome
	}{
		{beneficiariesRequest(t), &one, domain.ValidationFailed},
		{insuredAmountRequest(t, 120_000_000), &one, domain.ValidationFailed},
		{insuredAmountRequest(t, 120_000_000), nil, domain.ValidationUnverifiable},
	} {
		f := allPass(tc.r)
		f.OpenClaims = tc.open

		assert.Equal(t, tc.want, outcomes(domain.EvaluateEndorsement(tc.r, f))[domain.ValidationNoOpenClaim], tc.r.Type)
	}
	r := personalDataRequest(t)
	f := allPass(r)
	f.OpenClaims = nil
	v := domain.EvaluateEndorsement(r, f)
	assert.NotContains(t, outcomes(v), domain.ValidationNoOpenClaim)
	assert.False(t, v.Unverifiable(), "contact data does not ask claims")
	assert.False(t, domain.NeedsOpenClaims(domain.ChangePersonalData))
}

func TestTheOnerousBeneficiaryMustStayIntact(t *testing.T) {
	r := beneficiariesRequest(t)
	for name, mutate := range map[string]func(*domain.PolicyTerms){
		"removed": func(c *domain.PolicyTerms) { c.Onerous = nil },
		"another lender": func(c *domain.PolicyTerms) {
			c.Onerous = &domain.OnerousBeneficiary{PartnerID: "otro-banco", Basis: domain.OnerousBasisOutstandingBalance}
		},
		"another basis":     func(c *domain.PolicyTerms) { c.Onerous.Basis = "fixed_amount" },
		"unknown basis set": func(c *domain.PolicyTerms) { c.Onerous = &domain.OnerousBeneficiary{PartnerID: "aliado-banco-1"} },
	} {
		t.Run(name, func(t *testing.T) {
			f := allPass(r)
			o := *f.Current.Onerous
			f.Current.Onerous = &o
			mutate(&f.Current)

			assert.Equal(t, domain.ValidationFailed, outcomes(domain.EvaluateEndorsement(r, f))[domain.ValidationOnerousIntact])
		})
	}
}

func TestOnlyTheHolderCanHaveTheirRequestApplied(t *testing.T) {
	r := personalDataRequest(t)
	r.RequestedBy.Subject = "sub-otro"

	v := domain.EvaluateEndorsement(r, allPass(r))

	assert.Equal(t, domain.ValidationFailed, outcomes(v)[domain.ValidationRequesterIsHolder])
	assert.NotContains(t, v[1].Params, "requester", "without a name the screen shows only the channel")
	assert.Equal(t, "mobile", v[1].Params["channel"])
}

func TestTheGuardsAreListedWhenTheyFail(t *testing.T) {
	expired := func(f *domain.EndorsementFacts) { f.Now = f.Policy.CoverageEnd }
	for _, tc := range []struct {
		name   string
		r      domain.ChangeRequest
		facts  func(*domain.EndorsementFacts)
		guard  domain.ValidationCode
		result domain.ValidationOutcome
	}{
		{"a policy that ended", personalDataRequest(t), expired, domain.ValidationPolicyInForce, domain.ValidationFailed},
		{"beneficiaries changed by another endorsement", beneficiariesRequest(t), func(f *domain.EndorsementFacts) {
			f.Current.Beneficiaries = []domain.Beneficiary{{FullName: "Ana Ruiz", Relationship: domain.RelationshipParent,
				DocumentType: domain.DocumentCC, DocumentNumber: "52334981", ShareBasisPoints: 10_000}}
		}, domain.ValidationPreviousValuesCurrent, domain.ValidationFailed},
		{"insured value changed by another endorsement", insuredAmountRequest(t, 120_000_000), func(f *domain.EndorsementFacts) {
			f.Current.InsuredAmount = 90_000_000
		}, domain.ValidationPreviousValuesCurrent, domain.ValidationFailed},
		{"phone changed by another endorsement", personalDataRequest(t), func(f *domain.EndorsementFacts) {
			f.Current.Contact.Phone = ptr("3009998877")
		}, domain.ValidationPreviousValuesCurrent, domain.ValidationFailed},
		{"a renewal accepted and not active", insuredAmountRequest(t, 120_000_000), func(f *domain.EndorsementFacts) {
			f.RenewalInProgress = true
		}, domain.ValidationNoRenewalInProgress, domain.ValidationFailed},
		{"no original offer to price the premium", insuredAmountRequest(t, 120_000_000), func(f *domain.EndorsementFacts) {
			f.PremiumPriced = false
		}, domain.ValidationPremiumRecalculated, domain.ValidationUnverifiable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := allPass(tc.r)
			tc.facts(&f)

			v := domain.EvaluateEndorsement(tc.r, f)

			assert.Equal(t, tc.result, outcomes(v)[tc.guard])
			assert.Contains(t, codes(v.Listed()), tc.guard)
		})
	}
}

func TestARenewalInProgressDoesNotBlockOtherTypes(t *testing.T) {
	r := beneficiariesRequest(t)
	f := allPass(r)
	f.RenewalInProgress = true

	assert.False(t, domain.EvaluateEndorsement(r, f).Failed())
}

func TestTheInsuredAmountIsCheckedAgainstTheLimitsOfTheProduct(t *testing.T) {
	r := insuredAmountRequest(t, 120_000_000)
	v := domain.EvaluateEndorsement(r, allPass(r))
	assert.Equal(t, map[string]string{"requested": "120000000", "min": "10000000", "max": "2000000000"}, v[0].Params)

	r.Requested.InsuredAmount = domain.MaxInsuredAmount + 1
	assert.Equal(t, domain.ValidationFailed, outcomes(domain.EvaluateEndorsement(r, allPass(r)))[domain.ValidationInsuredAmountWithinLimits])
}

func TestContactDataWithAnInvalidFormatFails(t *testing.T) {
	r := personalDataRequest(t)
	r.Requested.Contact.Phone = ptr("123")

	v := domain.EvaluateEndorsement(r, allPass(r))

	assert.Equal(t, domain.ValidationFailed, outcomes(v)[domain.ValidationPersonalDataValid])
	assert.Equal(t, "personal_data.phone", v[0].Params["fields"])
}

func TestSharesThatDoNotAddUpTo100Fail(t *testing.T) {
	r := beneficiariesRequest(t)
	r.Requested.Beneficiaries[1].ShareBasisPoints = 3000

	v := domain.EvaluateEndorsement(r, allPass(r))

	assert.Equal(t, domain.ValidationSharesTotal100, v.FirstFailed())
	assert.Equal(t, map[string]string{"shares": "60|30", "total": "90"}, v[0].Params)
}

func TestApplyingAChangeGivesTheTermsThatResult(t *testing.T) {
	base := domain.TermsOf(domain.Policy{Premium: 68_500, Coverages: []domain.Coverage{{Code: domain.CoverageDeath, InsuredAmount: 68_000_000}}},
		currentTerms())
	rated := domain.Rating{Premium: 82_200, Coverages: []domain.Coverage{{Code: domain.CoverageDeath, InsuredAmount: 120_000_000}}}

	got := domain.ApplyChange(base, insuredAmountRequest(t, 120_000_000), rated)
	assert.InDelta(t, 120_000_000, got.InsuredAmount, 0)
	assert.InDelta(t, 82_200, got.Premium, 0)
	assert.Equal(t, rated.Coverages, got.Coverages)
	assert.InDelta(t, 68_500, base.Premium, 0, "the terms it started from do not change")

	got = domain.ApplyChange(base, beneficiariesRequest(t), rated)
	assert.Len(t, got.Beneficiaries, 2)
	assert.InDelta(t, 68_500, got.Premium, 0, "only a new insured value takes the rated premium")
	assert.Equal(t, base.Onerous, got.Onerous)

	got = domain.ApplyChange(base, personalDataRequest(t), rated)
	assert.Equal(t, "3012345678", *got.Contact.Phone)
	assert.Equal(t, "Calle 100 # 15-20, Bogotá", *got.Contact.Address)
	assert.Equal(t, "carolina@example.com", *got.Contact.Email, "a member not requested keeps its value")
	assert.Nil(t, base.Contact.Phone)
}

func TestTheTransitionsOfARequest(t *testing.T) {
	for _, tc := range []struct {
		status                        domain.ChangeRequestStatus
		reason                        string
		review, decide, ret, returned bool
	}{
		{domain.ChangeRequestFiled, "", true, true, false, false},
		{domain.ChangeRequestInReview, "", true, true, false, false},
		{domain.ChangeRequestNotApplied, "", false, false, true, false},
		{domain.ChangeRequestNotApplied, "Corrige los porcentajes", false, false, false, true},
		{domain.ChangeRequestApplied, "", false, false, false, false},
		{domain.ChangeRequestRejected, "No procede", false, false, false, false},
	} {
		r := domain.ChangeRequest{Status: tc.status, DecisionReason: tc.reason}
		assert.Equal(t, tc.review, r.CanReview(), "%s review", tc.status)
		assert.Equal(t, tc.decide, r.CanDecide(), "%s decide", tc.status)
		assert.Equal(t, tc.ret, r.CanReturn(), "%s return", tc.status)
		assert.Equal(t, tc.returned, r.Returned(), "%s returned", tc.status)
	}
}

func TestTheObservationsHaveTenToFiveHundredCharacters(t *testing.T) {
	for _, tc := range []struct {
		raw string
		ok  bool
	}{
		{strings.Repeat("a", 9), false},
		{"  " + strings.Repeat("a", 9) + "  ", false},
		{strings.Repeat("á", 10), true},
		{strings.Repeat("a", 500), true},
		{strings.Repeat("a", 501), false},
		{"Corrige los\nporcentajes", true},
		{"Corrige los \x00 porcentajes", false},
	} {
		got, err := domain.NormalizeObservations(tc.raw)
		if tc.ok {
			assert.NoError(t, err, tc.raw)
			assert.Equal(t, strings.TrimSpace(tc.raw), got)
		} else {
			assert.ErrorIs(t, err, domain.ErrEndorsementObservations, tc.raw)
		}
	}
}

func TestTheEndorsementNumberRestartsEveryCivilYearOfColombia(t *testing.T) {
	assert.Equal(t, "END-2026-0102", domain.FormatEndorsementID(2026, 102))
	assert.True(t, domain.ValidEndorsementID("END-2026-0102"))
	assert.False(t, domain.ValidEndorsementID("END-26-1"))
	assert.Equal(t, 2026, domain.EndorsementYear(time.Date(2027, 1, 1, 4, 0, 0, 0, time.UTC)), "31-dic 23:00 in Colombia")
	assert.Equal(t, 2027, domain.EndorsementYear(time.Date(2027, 1, 1, 5, 0, 0, 0, time.UTC)))
}

func TestTheInboxFilter(t *testing.T) {
	f := domain.EndorsementInboxFilter{}.WithDefaults()
	require.NoError(t, f.Validate())
	assert.Equal(t, domain.InboxPending, f.Status)
	assert.True(t, f.Matches(domain.ChangeRequest{Status: domain.ChangeRequestNotApplied}), "not applied and not returned is pending")
	assert.False(t, f.Matches(domain.ChangeRequest{Status: domain.ChangeRequestNotApplied, DecisionReason: "Corrige"}))

	_, err := domain.ParseInboxStatus("returned")
	assert.ErrorIs(t, err, domain.ErrInvalidInboxQuery)
	for _, bad := range []domain.EndorsementInboxFilter{
		{ChangeType: "otro", Status: domain.InboxAll, Page: 1, PageSize: 1},
		{Status: domain.InboxAll, Page: 1, PageSize: domain.MaxInboxPageSize + 1},
	} {
		assert.ErrorIs(t, bad.Validate(), domain.ErrInvalidInboxQuery)
	}
	page := domain.EndorsementInboxPage{Total: 21, PageSize: 20}
	assert.Equal(t, 2, page.TotalPages())
}
