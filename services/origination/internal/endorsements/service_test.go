package endorsements_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/endorsements"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/renewal"
)

// now is 16 September 2026, 07:00 in Colombia.
var now = time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)

func ptr[T any](v T) *T { return &v }

type env struct {
	policies *fakePolicies
	requests *fakeRequests
	origins  fakeOrigins
	clock    *fakeClock
}

func newEnv() *env {
	policy := func(id, owner, email string, start, end time.Time) domain.Policy {
		return domain.Policy{ID: id, AcceptanceID: "acc-" + id, CustomerID: owner,
			Holder:  domain.Applicant{FullName: "Carolina Pérez", DocumentID: "1032456789", Email: email},
			Premium: 142_300, Currency: "COP", InsuredAmount: 68_000_000,
			CoverageStart: start, CoverageEnd: end}
	}
	return &env{
		policies: &fakePolicies{rows: map[string]domain.Policy{
			"pol-carolina": policy("pol-carolina", "sub-carolina", "carolina@example.com", now.AddDate(0, -2, 0), now.AddDate(0, 10, 0)),
			"pol-advisor":  policy("pol-advisor", "sub-carolina", "carolina@example.com", now.AddDate(0, -2, 0), now.AddDate(0, 10, 0)),
			"pol-expired":  policy("pol-expired", "sub-carolina", "carolina@example.com", now.AddDate(-1, -1, 0), now.AddDate(0, -1, 0)),
			"pol-diego":    policy("pol-diego", "sub-diego", "diego@example.com", now.AddDate(0, -2, 0), now.AddDate(0, 10, 0)),
		}},
		requests: newFakeRequests(),
		origins: fakeOrigins{rows: map[string]renewal.Origin{
			"pol-carolina": {PartnerID: "aliado-banco-1"},
			"pol-advisor":  {PartnerID: domain.AdvisorPartnerID},
			"pol-diego":    {PartnerID: "aliado-banco-2"},
		}},
		clock: &fakeClock{now: now},
	}
}

func (e *env) service(t *testing.T) *endorsements.Service {
	t.Helper()
	svc, err := endorsements.NewService(endorsements.Config{Policies: e.policies, Requests: e.requests, Origins: e.origins,
		PartnerNames: map[string]string{"aliado-banco-1": "Banco Aliado"}, Clock: e.clock})
	require.NoError(t, err)
	return svc
}

func insured(sub, name string) context.Context {
	return security.WithClaims(context.Background(), security.Claims{Subject: sub, Email: sub[4:] + "@example.com", Name: name,
		Roles: []security.Role{security.RoleInsured}})
}

func withRole(role security.Role) context.Context {
	return security.WithClaims(context.Background(), security.Claims{Subject: "staff", Roles: []security.Role{role}})
}

func beneficiaries() domain.ChangeInput {
	return domain.ChangeInput{Type: "beneficiaries", Channel: "web", Beneficiaries: []domain.BeneficiaryInput{
		{FullName: "Mateo Pérez Gómez", Relationship: "child", DocumentType: "TI", DocumentNumber: "1140223001", SharePercent: ptr(60.0)},
		{FullName: "Lucía Pérez Gómez", Relationship: "child", DocumentType: "TI", DocumentNumber: "1142998310", SharePercent: ptr(40.0)},
	}}
}

func insuredAmount(v float64) domain.ChangeInput {
	return domain.ChangeInput{Type: "insured_amount", Channel: "web", InsuredAmount: &v}
}

func TestNewServiceNeedsItsDependencies(t *testing.T) {
	_, err := endorsements.NewService(endorsements.Config{})
	assert.Error(t, err)
}

func TestTheCurrentTermsAreThePolicyTheLenderAndTheContactEmail(t *testing.T) {
	e := newEnv()

	got, err := e.service(t).CurrentTerms(insured("sub-carolina", "Carolina Pérez"), "pol-carolina")

	require.NoError(t, err)
	assert.Equal(t, "pol-carolina", got.Policy.ID)
	assert.True(t, got.InForce)
	assert.Equal(t, domain.PolicyTerms{InsuredAmount: 68_000_000, Currency: "COP", Beneficiaries: []domain.Beneficiary{},
		Onerous: &domain.OnerousBeneficiary{PartnerID: "aliado-banco-1", Name: "Banco Aliado", Basis: domain.OnerousBasisOutstandingBalance},
		Contact: domain.ContactData{Email: ptr("carolina@example.com")}}, got.Terms)
	assert.Empty(t, got.Open)
}

func TestTheLenderOfAnAdvisorSaleOrWithoutANameIsShownAsItIsKnown(t *testing.T) {
	e := newEnv()
	ctx := insured("sub-carolina", "")

	advisor, err := e.service(t).CurrentTerms(ctx, "pol-advisor")
	require.NoError(t, err)
	assert.Equal(t, &domain.OnerousBeneficiary{Basis: domain.OnerousBasisOutstandingBalance}, advisor.Terms.Onerous,
		"the advisor is not the lender: generic label")

	e.origins.rows["pol-carolina"] = renewal.Origin{PartnerID: "aliado-sin-nombre"}
	unnamed, err := e.service(t).CurrentTerms(ctx, "pol-carolina")
	require.NoError(t, err)
	assert.Equal(t, "aliado-sin-nombre", unnamed.Terms.Onerous.PartnerID)
	assert.Empty(t, unnamed.Terms.Onerous.Name, "the screen shows the code")
}

func TestTheCurrentTermsSayWhatIsOpenAndWhetherThePolicyIsInForce(t *testing.T) {
	e := newEnv()
	ctx := insured("sub-carolina", "")
	svc := e.service(t)
	filed, err := svc.File(ctx, "pol-carolina", insuredAmount(80_000_000))
	require.NoError(t, err)

	got, err := svc.CurrentTerms(ctx, "pol-carolina")
	require.NoError(t, err)
	require.Len(t, got.Open, 1)
	assert.Equal(t, filed.ID, got.Open[0].ID)

	expired, err := svc.CurrentTerms(ctx, "pol-expired")
	require.NoError(t, err)
	assert.False(t, expired.InForce, "screen A3 shows the policy is not in force")
}

func TestOnlyTheInsuredWhoOwnsThePolicyReadsOrFilesOnIt(t *testing.T) {
	for name, ctx := range map[string]context.Context{
		"advisor":    withRole(security.RoleAdvisor),
		"operations": withRole(security.RoleOperations),
		"partner":    withRole(security.RolePartner),
	} {
		e := newEnv()
		svc := e.service(t)
		_, err := svc.CurrentTerms(ctx, "pol-carolina")
		assert.ErrorIs(t, err, security.ErrForbidden, name)
		_, err = svc.File(ctx, "pol-carolina", insuredAmount(80_000_000))
		assert.ErrorIs(t, err, security.ErrForbidden, name)
		_, err = svc.List(ctx, domain.ChangeRequestFilter{})
		assert.ErrorIs(t, err, security.ErrForbidden, name)
		_, err = svc.Get(ctx, "SOL-MOD-2026-0001")
		assert.ErrorIs(t, err, security.ErrForbidden, name)
		assert.Zero(t, e.policies.reads, "%s: nothing is read", name)
		assert.Zero(t, e.requests.creates, name)
	}

	e := newEnv()
	svc := e.service(t)
	diego := insured("sub-diego", "Diego Ruiz")
	_, err := svc.CurrentTerms(diego, "pol-carolina")
	assert.ErrorIs(t, err, security.ErrForbidden, "another insured's policy (CA-4)")
	_, err = svc.File(diego, "pol-carolina", insuredAmount(80_000_000))
	assert.ErrorIs(t, err, security.ErrForbidden)
	assert.Zero(t, e.requests.creates, "nothing is filed on another insured's policy")

	_, err = svc.CurrentTerms(context.Background(), "pol-carolina")
	assert.ErrorIs(t, err, security.ErrUnauthenticated)
	_, err = svc.CurrentTerms(insured("sub-carolina", ""), "pol-404")
	assert.ErrorIs(t, err, ports.ErrPolicyNotFound, "a policy of the policy core is not origination's (D-2)")
}

func TestFilingKeepsThePhotoAndWhoFiledIt(t *testing.T) {
	e := newEnv()

	r, err := e.service(t).File(insured("sub-carolina", "Carolina Pérez"), "pol-carolina", beneficiaries())

	require.NoError(t, err)
	assert.Equal(t, "SOL-MOD-2026-0001", r.ID)
	assert.Equal(t, domain.ChangeRequestFiled, r.Status)
	assert.Equal(t, now, r.FiledAt)
	assert.Equal(t, domain.Requester{Subject: "sub-carolina", Email: "carolina@example.com", Name: "Carolina Pérez"}, r.RequestedBy)
	assert.Equal(t, "sub-carolina", r.CustomerID)
	require.NotNil(t, r.Previous.Onerous)
	assert.Equal(t, "Banco Aliado", r.Previous.Onerous.Name, "the photo keeps the onerous beneficiary")
	assert.Len(t, r.Requested.Beneficiaries, 2)

	without, err := e.service(t).File(insured("sub-carolina", ""), "pol-carolina", insuredAmount(80_000_000))
	require.NoError(t, err)
	assert.Empty(t, without.RequestedBy.Name, "an account without a name: the screen shows the e-mail")
	assert.Equal(t, "carolina@example.com", without.RequestedBy.Email)
	assert.Equal(t, domain.ChangeContent{InsuredAmount: 68_000_000, Currency: "COP"}, without.Previous)
}

func TestAPolicyNotInForceIsNotChanged(t *testing.T) {
	e := newEnv()

	_, err := e.service(t).File(insured("sub-carolina", ""), "pol-expired", insuredAmount(80_000_000))

	assert.ErrorIs(t, err, domain.ErrPolicyNotInForce)
	assert.Zero(t, e.requests.creates)
}

func TestAnOpenRequestOfTheSameTypeAnswersFirst(t *testing.T) {
	e := newEnv()
	ctx := insured("sub-carolina", "")
	svc := e.service(t)
	first, err := svc.File(ctx, "pol-carolina", beneficiaries())
	require.NoError(t, err)

	invalid := beneficiaries()
	invalid.Beneficiaries[0].SharePercent = ptr(80.0)
	_, err = svc.File(ctx, "pol-carolina", invalid)

	var open *ports.ChangeRequestOpenError
	require.ErrorAs(t, err, &open, "even with invalid fields")
	assert.Equal(t, first.ID, open.RequestID)
	assert.Equal(t, 1, e.requests.creates)

	_, err = svc.File(ctx, "pol-carolina", insuredAmount(80_000_000))
	assert.NoError(t, err, "another type can be filed at the same time")
}

func TestTheStoreDecidesBetweenTwoRequestsFiledAtOnce(t *testing.T) {
	e := newEnv()
	e.requests.err = &ports.ChangeRequestOpenError{RequestID: "SOL-MOD-2026-0007"}

	_, err := e.service(t).File(insured("sub-carolina", ""), "pol-carolina", insuredAmount(80_000_000))

	var open *ports.ChangeRequestOpenError
	require.ErrorAs(t, err, &open)
	assert.Equal(t, "SOL-MOD-2026-0007", open.RequestID)
}

func TestInvalidFieldsAreAllReportedAndNothingIsStored(t *testing.T) {
	e := newEnv()
	in := domain.ChangeInput{Type: "beneficiaries", Channel: "web", Beneficiaries: []domain.BeneficiaryInput{
		{FullName: "", Relationship: "child", DocumentType: "CC", DocumentNumber: "12AB45", SharePercent: ptr(120.0)},
	}}

	_, err := e.service(t).File(insured("sub-carolina", ""), "pol-carolina", in)

	var invalid *domain.ValidationError
	require.ErrorAs(t, err, &invalid)
	assert.Len(t, invalid.Fields, 3, "name, document and share at once")
	assert.Zero(t, e.requests.creates, "Create is not called")
}

func TestInfrastructureFailuresArePropagated(t *testing.T) {
	ctx := insured("sub-carolina", "")
	boom := errors.New("database down")

	e := newEnv()
	e.origins.err = boom
	_, err := e.service(t).File(ctx, "pol-carolina", insuredAmount(80_000_000))
	assert.ErrorIs(t, err, boom)
	_, err = e.service(t).CurrentTerms(ctx, "pol-carolina")
	assert.ErrorIs(t, err, boom)
	assert.Zero(t, e.requests.creates)

	e = newEnv()
	e.requests.err = boom
	_, err = e.service(t).File(ctx, "pol-carolina", insuredAmount(80_000_000))
	assert.ErrorIs(t, err, boom)
	_, err = e.service(t).CurrentTerms(ctx, "pol-carolina")
	assert.ErrorIs(t, err, boom)

	e = newEnv()
	e.policies.err = boom
	_, err = e.service(t).File(ctx, "pol-carolina", insuredAmount(80_000_000))
	assert.ErrorIs(t, err, boom)
}

func TestTheLogOfAFilingCarriesNoPersonalData(t *testing.T) {
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	e := newEnv()
	ctx := insured("sub-carolina", "Carolina Pérez")
	svc := e.service(t)

	_, err := svc.File(ctx, "pol-carolina", beneficiaries())
	require.NoError(t, err)
	_, err = svc.File(ctx, "pol-carolina", domain.ChangeInput{Type: "personal_data", Channel: "web",
		PersonalData: &domain.ContactInput{Address: ptr("Calle 10 # 43-20"), Email: ptr("nuevo@example.com"), Phone: ptr("3001234567")}})
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "change request filed")
	assert.Contains(t, out, "request_id=SOL-MOD-2026-0001")
	assert.Contains(t, out, "change_type=personal_data")
	for _, personal := range []string{"Mateo", "Lucía", "1140223001", "Carolina", "carolina@example.com", "nuevo@example.com",
		"3001234567", "Calle 10"} {
		assert.NotContains(t, out, personal)
	}
}

func TestTheListIsTheCallersWithTheClockOfTheService(t *testing.T) {
	e := newEnv()
	ctx := insured("sub-carolina", "")
	svc := e.service(t)
	_, err := svc.File(ctx, "pol-carolina", insuredAmount(80_000_000))
	require.NoError(t, err)
	_, err = svc.File(insured("sub-diego", ""), "pol-diego", insuredAmount(80_000_000))
	require.NoError(t, err)

	page, err := svc.List(ctx, domain.ChangeRequestFilter{})

	require.NoError(t, err)
	assert.Equal(t, "sub-carolina", e.requests.gotOwner)
	assert.Equal(t, now, e.requests.got.Now)
	assert.Equal(t, 1, e.requests.got.Page)
	assert.Equal(t, domain.DefaultChangeRequestPageSize, e.requests.got.PageSize)
	require.Len(t, page.Items, 1)
	assert.Equal(t, "pol-carolina", page.Items[0].PolicyID, "never another insured's")

	_, err = svc.List(ctx, domain.ChangeRequestFilter{Status: "not_applied"})
	assert.ErrorIs(t, err, domain.ErrInvalidChangeRequestQuery)
	_, err = svc.List(ctx, domain.ChangeRequestFilter{PageSize: 51})
	assert.ErrorIs(t, err, domain.ErrInvalidChangeRequestQuery)
}

func TestARequestIsReadOnlyByItsOwner(t *testing.T) {
	e := newEnv()
	ctx := insured("sub-carolina", "")
	svc := e.service(t)
	filed, err := svc.File(ctx, "pol-carolina", insuredAmount(80_000_000))
	require.NoError(t, err)

	got, err := svc.Get(ctx, filed.ID)
	require.NoError(t, err)
	assert.Equal(t, filed, got)

	_, err = svc.Get(insured("sub-diego", ""), filed.ID)
	assert.ErrorIs(t, err, security.ErrForbidden, "another insured's request")
	_, err = svc.Get(ctx, "SOL-MOD-2026-0404")
	assert.ErrorIs(t, err, ports.ErrChangeRequestNotFound)
	_, err = svc.Get(ctx, "pol-carolina")
	assert.ErrorIs(t, err, ports.ErrChangeRequestNotFound, "not an id: not looked up")

	e.requests.err = errors.New("database down")
	_, err = svc.Get(ctx, filed.ID)
	assert.Error(t, err)
}

func TestTheTermsBuilderChecksNoRole(t *testing.T) {
	e := newEnv()
	p := e.policies.rows["pol-carolina"]

	terms, err := e.service(t).Terms().Of(context.Background(), p)

	require.NoError(t, err, "the analyst of HU-W1.4-02 reuses it (adjustment A-3)")
	assert.Equal(t, 68_000_000.0, terms.InsuredAmount)
}
