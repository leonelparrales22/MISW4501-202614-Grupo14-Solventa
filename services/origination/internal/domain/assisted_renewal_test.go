package domain_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

var requoteAt = time.Date(2026, 9, 16, 21, 2, 30, 0, time.UTC)

func sampleRequote() domain.RenewalRequote {
	return domain.RenewalRequote{
		ID: "rq-0123456789abcdef0123456789abcdef", PolicyID: "pol-1", PeriodEnd: policyEnd, AdvisorID: "sub-advisor",
		Terms:   domain.CoverageTerms{InsuredAmount: 68_000_000, TermMonths: 12},
		Premium: 149_800, Currency: "COP", DiscountRate: domain.DiscountRate, Requoted: true, RatingVersion: "v1",
		Mode: domain.ModeNormal, ProfileSource: domain.SourceExternal,
		ProfileCustomerID: "cust-1", OriginChannel: domain.OfferChannelEmbedded,
		ConsentBasis: domain.ConsentBasisAuditVerified, ConsentID: "con-1",
		CreatedAt: requoteAt, ExpiresAt: domain.RenewalRequoteExpiry(requoteAt),
	}
}

func TestTheRequoteIsValidForExactly24Hours(t *testing.T) {
	r := sampleRequote()

	assert.Equal(t, 24*time.Hour, r.ExpiresAt.Sub(r.CreatedAt))
	assert.Equal(t, domain.RequoteValid, r.Status(requoteAt))
	assert.Equal(t, domain.RequoteValid, r.Status(requoteAt.Add(24*time.Hour-time.Second)))
	assert.Equal(t, domain.RequoteExpired, r.Status(requoteAt.Add(24*time.Hour)), "it expires at the exact deadline")
}

func TestOnlyTheLastRequoteOfThePeriodCounts(t *testing.T) {
	r := sampleRequote()

	assert.Equal(t, domain.RequoteValid, r.StatusAmong(r.ID, requoteAt))
	assert.Equal(t, domain.RequoteValid, r.StatusAmong("", requoteAt), "with no other requote it is the last")
	assert.Equal(t, domain.RequoteSuperseded, r.StatusAmong("rq-newer", requoteAt))
	assert.Equal(t, domain.RequoteSuperseded, r.StatusAmong("rq-newer", requoteAt.Add(48*time.Hour)), "superseded wins over expired")
	assert.Equal(t, domain.RequoteExpired, r.StatusAmong(r.ID, requoteAt.Add(48*time.Hour)))
}

func TestTheNoticeTellsWhichDegradedProfilePricedTheRequote(t *testing.T) {
	assert.Equal(t, domain.ProfileNoticeNone, domain.ProfileNoticeOf(domain.ModeNormal, domain.SourceExternal))
	assert.Equal(t, domain.ProfileNoticeNone, domain.ProfileNoticeOf(domain.ModeNormal, domain.SourceCache), "a reused profile is not degraded")
	assert.Equal(t, domain.ProfileNoticeLastProfileInUse, domain.ProfileNoticeOf(domain.ModeProvisional, domain.SourceCache))
	assert.Equal(t, domain.ProfileNoticePersonalizationUnavailable, domain.ProfileNoticeOf(domain.ModeProvisional, domain.SourceBaseActuarial))
	assert.Equal(t, domain.ProfileNoticeNone, domain.ProfileNoticeOf("", ""), "no profile was resolved")
}

func TestARequoteSnapshotIsValidated(t *testing.T) {
	require.NoError(t, sampleRequote().Validate())

	for name, mutate := range map[string]func(*domain.RenewalRequote){
		"id prefix": func(r *domain.RenewalRequote) { r.ID = "q-1" },
		"policy":    func(r *domain.RenewalRequote) { r.PolicyID = "" },
		"period":    func(r *domain.RenewalRequote) { r.PeriodEnd = time.Time{} },
		"advisor":   func(r *domain.RenewalRequote) { r.AdvisorID = "" },
		"premium":   func(r *domain.RenewalRequote) { r.Premium = 0 },
		"currency":  func(r *domain.RenewalRequote) { r.Currency = "" },
		"term":      func(r *domain.RenewalRequote) { r.Terms.TermMonths = 0 },
		"created":   func(r *domain.RenewalRequote) { r.CreatedAt = time.Time{} },
		"expires":   func(r *domain.RenewalRequote) { r.ExpiresAt = r.CreatedAt },
		"basis":     func(r *domain.RenewalRequote) { r.ConsentBasis = "implicit" },
	} {
		r := sampleRequote()
		mutate(&r)
		assert.Error(t, r.Validate(), name)
	}
}

func TestTheConsentBasisSaysOnWhatAuthorityTheDataWasConsulted(t *testing.T) {
	audit := sampleRequote()
	require.NoError(t, audit.Validate(), "audit verified with the consent audit answered")

	affirmed := sampleRequote()
	affirmed.OriginChannel, affirmed.ConsentID = domain.OfferChannelAssisted, ""
	affirmed.ConsentBasis, affirmed.ConsentAffirmedBy, affirmed.ConsentAffirmedAt = domain.ConsentBasisAdvisorAffirmed, "sub-advisor", requoteAt
	require.NoError(t, affirmed.Validate())

	noWho := affirmed
	noWho.ConsentAffirmedBy = ""
	assert.Error(t, noWho.Validate(), "an affirmed consent needs who affirmed it")

	noWhen := affirmed
	noWhen.ConsentAffirmedAt = time.Time{}
	assert.Error(t, noWhen.Validate(), "an affirmed consent needs when")

	nothing := sampleRequote()
	nothing.Requoted, nothing.ConsentBasis, nothing.ConsentID, nothing.ProfileCustomerID = false, "", "", ""
	assert.NoError(t, nothing.Validate(), "no basis when no external data was consulted")
}

func TestTheAdvisorMustAffirmTheConsentOfAnAssistedPolicy(t *testing.T) {
	require.NoError(t, domain.RequireConsentAffirmation(true))
	assert.Equal(t, map[string]string{"consent_affirmed": "required"}, fieldsOf(t, domain.RequireConsentAffirmation(false)))
}

func TestTheRenewalAcceptanceAcceptsTheThreeMethods(t *testing.T) {
	now := requoteAt.Add(3 * time.Minute)
	for _, a := range []domain.ConsentAcceptance{
		{Method: domain.ConsentRecordedCall, AcceptedAt: now, Reference: "GRAB-2026-88931"},
		{Method: domain.ConsentRemoteSignature, AcceptedAt: now, Reference: "FIRMA/2026:0001"},
		{Method: domain.ConsentConfirmationCode, AcceptedAt: now},
	} {
		assert.NoError(t, domain.ValidateRenewalAcceptance("rq-1", a, now, requoteAt), string(a.Method))
	}
}

func TestTheRenewalAcceptanceReportsEveryInvalidFieldAtOnce(t *testing.T) {
	now := requoteAt.Add(3 * time.Minute)

	assert.Equal(t, map[string]string{
		"requote_id": "required", "acceptance.method": "required", "acceptance.accepted_at": "required", "acceptance.reference": "required",
	}, fieldsOf(t, domain.ValidateRenewalAcceptance(" ", domain.ConsentAcceptance{}, now, requoteAt)))

	assert.Equal(t, map[string]string{"acceptance.method": "invalid_format", "acceptance.reference": "required"},
		fieldsOf(t, domain.ValidateRenewalAcceptance("rq-1", domain.ConsentAcceptance{Method: "fax", AcceptedAt: now}, now, requoteAt)))
}

func TestTheRenewalAcceptanceDateIsBoundedByTheRequoteAndTheClock(t *testing.T) {
	now := requoteAt.Add(time.Hour)
	call := func(at time.Time) domain.ConsentAcceptance {
		return domain.ConsentAcceptance{Method: domain.ConsentRecordedCall, AcceptedAt: at, Reference: "GRAB-1"}
	}

	assert.NoError(t, domain.ValidateRenewalAcceptance("rq-1", call(requoteAt.Truncate(time.Minute)), now, requoteAt),
		"the minute the requote was made counts")
	assert.Equal(t, map[string]string{"acceptance.accepted_at": "out_of_range"},
		fieldsOf(t, domain.ValidateRenewalAcceptance("rq-1", call(requoteAt.Truncate(time.Minute).Add(-time.Second)), now, requoteAt)),
		"before the requote")
	assert.NoError(t, domain.ValidateRenewalAcceptance("rq-1", call(now.Add(5*time.Minute)), now, requoteAt), "5 minutes of clock skew")
	assert.Equal(t, map[string]string{"acceptance.accepted_at": "out_of_range"},
		fieldsOf(t, domain.ValidateRenewalAcceptance("rq-1", call(now.Add(5*time.Minute+time.Second)), now, requoteAt)),
		"more than 5 minutes in the future")
}

func TestTheRenewalAcceptanceReferenceIsBounded(t *testing.T) {
	now := requoteAt.Add(time.Minute)
	ref := func(r string, m domain.ConsentMethod) domain.ConsentAcceptance {
		return domain.ConsentAcceptance{Method: m, AcceptedAt: now, Reference: r}
	}

	assert.NoError(t, domain.ValidateRenewalAcceptance("rq-1", ref(strings.Repeat("a", 64), domain.ConsentRecordedCall), now, requoteAt))
	assert.Equal(t, map[string]string{"acceptance.reference": "invalid_format"},
		fieldsOf(t, domain.ValidateRenewalAcceptance("rq-1", ref(strings.Repeat("a", 65), domain.ConsentRecordedCall), now, requoteAt)),
		"65 characters")
	assert.Equal(t, map[string]string{"acceptance.reference": "invalid_format"},
		fieldsOf(t, domain.ValidateRenewalAcceptance("rq-1", ref("GRAB 2026;", domain.ConsentRemoteSignature), now, requoteAt)),
		"invalid characters")
	assert.Equal(t, map[string]string{"acceptance.reference": "required"},
		fieldsOf(t, domain.ValidateRenewalAcceptance("rq-1", ref("", domain.ConsentRemoteSignature), now, requoteAt)))
	assert.NoError(t, domain.ValidateRenewalAcceptance("rq-1", ref("", domain.ConsentConfirmationCode), now, requoteAt),
		"the code is the proof: the reference is optional")
	assert.Equal(t, map[string]string{"acceptance.reference": "invalid_format"},
		fieldsOf(t, domain.ValidateRenewalAcceptance("rq-1", ref("<script>", domain.ConsentConfirmationCode), now, requoteAt)),
		"an optional reference is still checked")
}

func TestTheFormalizationNamesTheAdvisorByNameOrEmail(t *testing.T) {
	f := domain.RenewalFormalization{AdvisorEmail: "asesor@example.com", AdvisorName: "Laura Gómez"}
	assert.Equal(t, "Laura Gómez", f.AdvisorDisplayName())

	f.AdvisorName = "  "
	assert.Equal(t, "asesor@example.com", f.AdvisorDisplayName(), "the token carried no name")
}

func TestThePremiumChangeRateComparesTheRenewedWithTheCurrentPremium(t *testing.T) {
	assert.InDelta(t, 0.0527, domain.PremiumChangeRate(142_300, 149_800), 0.0001)
	assert.InDelta(t, -0.08, domain.PremiumChangeRate(100_000, 92_000), 1e-9)
	assert.Zero(t, domain.PremiumChangeRate(0, 92_000))
}
