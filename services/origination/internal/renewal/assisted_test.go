package renewal_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/formalization"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/profiling"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/rating"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/renewal"
)

// HU-W1.5-03: the advisor requotes, gets the customer's acceptance and
// formalizes the renewal of a policy about to expire.

var buyerBirth = time.Date(1985, 5, 1, 0, 0, 0, 0, time.UTC)

type assistedRig struct {
	*rig
	assisted   *renewal.AssistedService
	requotes   *fakeRequotes
	records    *fakeAssisted
	profiles   *fakeFreshProfiles
	consents   *fakeConsents
	challenges ports.ConsentChallengeStore
	sender     *fakeSender
	offers     fakeOffers
	codes      []string
}

type assistedOption func(*renewal.AssistedConfig)

func newAssistedRig(t *testing.T, opts ...assistedOption) *assistedRig {
	t.Helper()
	base := newRig(t)
	a := &assistedRig{rig: base, requotes: newFakeRequotes(), records: &fakeAssisted{renewals: base.renewals, rows: map[string]domain.RenewalFormalization{}},
		profiles: &fakeFreshProfiles{res: domain.ProfileResolution{Profile: domain.RiskProfile{Score: 0.4}, Mode: domain.ModeNormal,
			Source: domain.SourceExternal, Sources: []domain.SourceOutcome{{Source: "open_finance", Status: domain.SourceStatusOK, Latency: 96 * time.Millisecond}}}},
		consents:   &fakeConsents{status: domain.ConsentStatus{Active: true, ConsentID: "con-1"}},
		challenges: memory.NewConsentChallengeStore(), sender: &fakeSender{},
		offers: fakeOffers{rows: map[string]domain.PersonalizedOffer{
			"off-embedded": {ID: "off-embedded", CustomerID: "partner-cust-1", Channel: domain.OfferChannelEmbedded, BirthDate: buyerBirth},
			"off-q-q1":     {ID: "off-q-q1", CustomerID: "cus-q1", Channel: domain.OfferChannelAssisted, BirthDate: buyerBirth},
		}},
	}
	n := 0
	cfg := renewal.AssistedConfig{
		Renewals: base.svc, Requotes: a.requotes, Assisted: a.records,
		Origins:  renewal.OriginFinder{Policies: base.policies, Charges: base.charges, Offers: a.offers},
		Profiles: a.profiles, Consents: a.consents, Challenges: a.challenges, Sender: a.sender,
		NewCode: func() (string, error) {
			n++
			code := fmt.Sprintf("%06d", 100000+n)
			a.codes = append(a.codes, code)
			return code, nil
		},
	}
	ids := 0
	cfg.NewID = func() (string, error) { ids++; return fmt.Sprintf("rq-%03d", ids), nil }
	for _, o := range opts {
		o(&cfg)
	}
	var err error
	a.assisted, err = renewal.NewAssistedService(cfg)
	require.NoError(t, err)
	return a
}

// seedSoldIn creates a policy in its renewal window whose original offer was
// sold in the given channel ("" leaves the charge without a stored offer).
func (a *assistedRig) seedSoldIn(t *testing.T, id string, channel domain.OfferChannel) domain.Policy {
	t.Helper()
	p := a.seed(t, id, policyEnd)
	c := a.charges.rows[p.AcceptanceID]
	switch channel {
	case domain.OfferChannelEmbedded:
		c.OfferID = "off-embedded"
	case domain.OfferChannelAssisted:
		c.OfferID = "off-q-q1"
	default:
		c.OfferID = "demo-1"
	}
	a.charges.rows[p.AcceptanceID] = c
	return p
}

func advisorCtx(sub, name string) context.Context {
	return security.WithClaims(context.Background(), security.Claims{Subject: sub, Email: sub + "@example.com", Name: name,
		Roles: []security.Role{security.RoleAdvisor}})
}

var laura = advisorCtx("sub-advisor", "Laura Gómez")

func recordedCall(at time.Time) domain.ConsentAcceptance {
	return domain.ConsentAcceptance{Method: domain.ConsentRecordedCall, AcceptedAt: at, Reference: "GRAB-2026-88931"}
}

func TestNewAssistedServiceRequiresItsDependencies(t *testing.T) {
	_, err := renewal.NewAssistedService(renewal.AssistedConfig{})
	assert.Error(t, err)
	_, err = renewal.NewAssistedService(renewal.AssistedConfig{Renewals: newRig(t).svc, Requotes: newFakeRequotes(),
		Assisted: &fakeAssisted{}, Profiles: &fakeFreshProfiles{}, Challenges: memory.NewConsentChallengeStore(), Sender: &fakeSender{}})
	assert.NoError(t, err, "the code and the id have defaults")
}

func TestOnlyAnAdvisorUsesTheAssistedRenewal(t *testing.T) {
	a := newAssistedRig(t)
	p := a.seedSoldIn(t, "pol-1", domain.OfferChannelEmbedded)

	for name, ctx := range map[string]context.Context{
		"anonymous":  context.Background(),
		"insured":    insured("sub-1"),
		"operations": security.WithClaims(context.Background(), security.Claims{Subject: "ops", Roles: []security.Role{security.RoleOperations}}),
	} {
		_, err := a.assisted.View(ctx, p.ID)
		assert.Error(t, err, name)
		_, err = a.assisted.Requote(ctx, p.ID, renewal.RequoteInput{})
		assert.Error(t, err, name)
		_, err = a.assisted.RequestConsentCode(ctx, p.ID, "rq-001")
		assert.Error(t, err, name)
		_, _, err = a.assisted.Formalize(ctx, p.ID, renewal.FormalizeInput{})
		assert.Error(t, err, name)
	}
	assert.Empty(t, a.requotes.rows)
	_, err := a.assisted.View(laura, "pol-unknown")
	assert.ErrorIs(t, err, ports.ErrPolicyNotFound)
}

// CA-1: the advisor requotes with the rating engine and sees the conditions.

func TestARequoteOfAnEmbeddedPolicyPricesItWithTheEngineAfterAuditVerifiedTheConsent(t *testing.T) {
	a := newAssistedRig(t)
	p := a.seedSoldIn(t, "pol-1", domain.OfferChannelEmbedded)

	got, err := a.assisted.Requote(laura, p.ID, renewal.RequoteInput{ConsentAffirmed: true})

	require.NoError(t, err)
	r := got.Requote
	assert.Equal(t, "rq-001", r.ID)
	assert.Equal(t, p, got.Policy)
	assert.True(t, r.Requoted)
	assert.InDelta(t, 138_000, r.Premium, 0.01, "150.000 with the 8 %% good-experience discount")
	assert.InDelta(t, domain.DiscountRate, r.DiscountRate, 1e-9)
	assert.Equal(t, 41, a.rater.got.Age, "priced with the age of today")
	assert.Equal(t, rating.Explain(a.rater.got), r.AppliedRules)
	assert.Equal(t, "test", r.RatingVersion)
	assert.Equal(t, policyEnd, r.PeriodEnd)
	assert.Equal(t, t0.Add(24*time.Hour), r.ExpiresAt)
	assert.Equal(t, "sub-advisor", r.AdvisorID)
	assert.Equal(t, []string{"partner-cust-1"}, a.consents.got, "the consent of the partner's customer (D-6, D-8)")
	assert.Equal(t, []string{"partner-cust-1"}, a.profiles.got, "queried fresh (D-5 b)")
	assert.Equal(t, domain.ConsentBasisAuditVerified, r.ConsentBasis)
	assert.Equal(t, "con-1", r.ConsentID)
	assert.Empty(t, r.ConsentAffirmedBy, "the checkbox is ignored for an embedded policy")
	assert.Equal(t, domain.ModeNormal, r.Mode)
	assert.Equal(t, domain.ProfileNoticeNone, r.ProfileNotice)
	assert.Len(t, r.Sources, 1)
	assert.Equal(t, r, a.requotes.rows["rq-001"], "the snapshot is stored")
}

func TestARequoteOfAnEmbeddedPolicyWithoutConsentConsultsNothing(t *testing.T) {
	a := newAssistedRig(t)
	a.consents.status = domain.ConsentStatus{Active: false}
	p := a.seedSoldIn(t, "pol-1", domain.OfferChannelEmbedded)

	_, err := a.assisted.Requote(laura, p.ID, renewal.RequoteInput{ConsentAffirmed: true})

	assert.ErrorIs(t, err, profiling.ErrConsentRequired)
	assert.Zero(t, a.profiles.calls, "neither the sources nor the cache")
	assert.Empty(t, a.requotes.rows)
}

func TestARequoteWhoseConsentCannotBeVerifiedFailsClosed(t *testing.T) {
	a := newAssistedRig(t)
	a.consents.err = fmt.Errorf("%w: timeout", ports.ErrConsentUnverifiable)
	p := a.seedSoldIn(t, "pol-1", domain.OfferChannelEmbedded)

	_, err := a.assisted.Requote(laura, p.ID, renewal.RequoteInput{})
	assert.ErrorIs(t, err, ports.ErrConsentUnverifiable)

	a.consents.err = errBoom
	_, err = a.assisted.Requote(laura, p.ID, renewal.RequoteInput{})
	assert.ErrorIs(t, err, ports.ErrConsentUnverifiable)

	noAudit := newAssistedRig(t, func(c *renewal.AssistedConfig) { c.Consents = nil })
	q := noAudit.seedSoldIn(t, "pol-1", domain.OfferChannelEmbedded)
	_, err = noAudit.assisted.Requote(laura, q.ID, renewal.RequoteInput{})
	assert.ErrorIs(t, err, ports.ErrConsentUnverifiable, "without audit nothing is consulted")

	assert.Zero(t, a.profiles.calls+noAudit.profiles.calls)
	assert.Empty(t, a.requotes.rows)
}

func TestAPolicyOfUnknownChannelIsVerifiedWithAudit(t *testing.T) {
	a := newAssistedRig(t, func(c *renewal.AssistedConfig) {
		c.Origins.BySubject = fakeCustomers{customer: domain.Customer{ID: "cust-by-sub", BirthDate: buyerBirth}}
	})
	p := a.seedSoldIn(t, "pol-1", "")

	got, err := a.assisted.Requote(laura, p.ID, renewal.RequoteInput{})

	require.NoError(t, err)
	assert.Equal(t, []string{"cust-by-sub"}, a.consents.got)
	assert.Equal(t, domain.ConsentBasisAuditVerified, got.Requote.ConsentBasis)
	assert.Empty(t, got.Requote.OriginChannel)
}

// D-13 (c): a policy sold by an advisor is requoted on the advisor's
// affirmation of the customer's consent.

func TestARequoteOfAnAssistedPolicyNeedsTheAdvisorsAffirmation(t *testing.T) {
	a := newAssistedRig(t)
	p := a.seedSoldIn(t, "pol-1", domain.OfferChannelAssisted)

	_, err := a.assisted.Requote(laura, p.ID, renewal.RequoteInput{})

	var invalid *domain.ValidationError
	require.ErrorAs(t, err, &invalid)
	assert.Equal(t, []domain.FieldError{{Field: "consent_affirmed", Code: domain.CodeRequired}}, invalid.Fields)
	assert.Zero(t, a.consents.calls, "audit keeps no consent of the assisted channel")
	assert.Zero(t, a.profiles.calls, "nothing is consulted")
	assert.Empty(t, a.requotes.rows)
}

func TestARequoteOfAnAssistedPolicyRecordsWhoAffirmedTheConsentAndWhen(t *testing.T) {
	a := newAssistedRig(t)
	p := a.seedSoldIn(t, "pol-1", domain.OfferChannelAssisted)

	got, err := a.assisted.Requote(laura, p.ID, renewal.RequoteInput{ConsentAffirmed: true})

	require.NoError(t, err)
	r := got.Requote
	assert.Equal(t, domain.ConsentBasisAdvisorAffirmed, r.ConsentBasis)
	assert.Equal(t, "sub-advisor", r.ConsentAffirmedBy)
	assert.Equal(t, t0, r.ConsentAffirmedAt)
	assert.Equal(t, domain.OfferChannelAssisted, r.OriginChannel)
	assert.Equal(t, "cus-q1", r.ProfileCustomerID)
	assert.Zero(t, a.consents.calls)
	assert.Equal(t, 1, a.profiles.calls)
}

// CA-5: with the sources down the requote uses the last cached profile and
// says so; without a recent one, the base actuarial profile.

func TestADegradedRequoteRecordsAndTellsWhichProfileWasUsed(t *testing.T) {
	cachedAt := t0.Add(-20 * 24 * time.Hour)
	for name, tc := range map[string]struct {
		res        domain.ProfileResolution
		wantNotice domain.ProfileNotice
		wantDate   time.Time
	}{
		"cache": {
			res: domain.ProfileResolution{Profile: domain.RiskProfile{Score: 0.31, Category: "standard", CalculatedAt: cachedAt},
				Mode: domain.ModeProvisional, Source: domain.SourceCache, UnavailableSource: "open_finance"},
			wantNotice: domain.ProfileNoticeLastProfileInUse, wantDate: cachedAt,
		},
		"base actuarial": {
			res: domain.ProfileResolution{Profile: domain.BaseActuarialProfile("partner-cust-1"), Mode: domain.ModeProvisional,
				Source: domain.SourceBaseActuarial, UnavailableSource: "open_finance"},
			wantNotice: domain.ProfileNoticePersonalizationUnavailable,
		},
	} {
		t.Run(name, func(t *testing.T) {
			a := newAssistedRig(t)
			a.profiles.res = tc.res
			p := a.seedSoldIn(t, "pol-1", domain.OfferChannelEmbedded)

			got, err := a.assisted.Requote(laura, p.ID, renewal.RequoteInput{})

			require.NoError(t, err)
			r := got.Requote
			assert.Equal(t, domain.ModeProvisional, r.Mode)
			assert.Equal(t, tc.res.Source, r.ProfileSource)
			assert.Equal(t, tc.wantNotice, r.ProfileNotice)
			assert.Equal(t, tc.wantDate, r.ProfileDate)
			assert.Equal(t, "open_finance", r.UnavailableSource)
			assert.True(t, r.Requoted, "a degraded requote still prices")
			assert.Equal(t, tc.wantNotice, a.requotes.rows[r.ID].ProfileNotice, "the snapshot keeps the case used")
		})
	}
}

func TestWithoutBirthDateOrCustomerTheRequoteRepeatsTheCurrentConditions(t *testing.T) {
	noCustomer := newAssistedRig(t)
	p := noCustomer.seedSoldIn(t, "pol-1", "")

	got, err := noCustomer.assisted.Requote(laura, p.ID, renewal.RequoteInput{})

	require.NoError(t, err)
	assert.False(t, got.Requote.Requoted)
	assert.InDelta(t, p.Premium, got.Requote.Premium, 0.01)
	assert.Empty(t, got.Requote.ConsentBasis, "nothing was consulted")
	assert.Zero(t, noCustomer.consents.calls+noCustomer.profiles.calls)

	noBirth := newAssistedRig(t)
	noBirth.offers.rows["off-embedded"] = domain.PersonalizedOffer{ID: "off-embedded", CustomerID: "partner-cust-1", Channel: domain.OfferChannelEmbedded}
	q := noBirth.seedSoldIn(t, "pol-1", domain.OfferChannelEmbedded)
	got, err = noBirth.assisted.Requote(laura, q.ID, renewal.RequoteInput{})
	require.NoError(t, err)
	assert.False(t, got.Requote.Requoted)
	assert.Equal(t, 1, noBirth.consents.calls, "the consent still comes first")
	assert.Zero(t, noBirth.profiles.calls)

	noProfile := newAssistedRig(t)
	noProfile.profiles.err = errBoom
	s := noProfile.seedSoldIn(t, "pol-1", domain.OfferChannelEmbedded)
	got, err = noProfile.assisted.Requote(laura, s.ID, renewal.RequoteInput{})
	require.NoError(t, err)
	assert.False(t, got.Requote.Requoted)
	assert.Empty(t, got.Requote.Mode)
	assert.Empty(t, got.Requote.AppliedRules)
}

func TestARequoteNeedsAnOpenPeriod(t *testing.T) {
	a := newAssistedRig(t)
	far := a.seed(t, "pol-far", policyEnd.AddDate(0, 3, 0))
	_, err := a.assisted.Requote(laura, far.ID, renewal.RequoteInput{})
	assert.ErrorIs(t, err, domain.ErrRenewalNotAvailable)

	p := a.seedSoldIn(t, "pol-1", domain.OfferChannelEmbedded)
	_, err = a.svc.Accept(insured("sub-1"), p.ID)
	require.NoError(t, err)
	_, err = a.assisted.Requote(laura, p.ID, renewal.RequoteInput{})
	assert.ErrorIs(t, err, domain.ErrRenewalClosed, "the insured renewed online")
	assert.Zero(t, a.consents.calls)
}

func TestARequoteThatCannotBeStoredOrIdentifiedFails(t *testing.T) {
	a := newAssistedRig(t)
	a.requotes.err = errBoom
	p := a.seedSoldIn(t, "pol-1", domain.OfferChannelEmbedded)
	_, err := a.assisted.Requote(laura, p.ID, renewal.RequoteInput{})
	assert.ErrorIs(t, err, errBoom)

	b := newAssistedRig(t, func(c *renewal.AssistedConfig) { c.NewID = func() (string, error) { return "", errBoom } })
	q := b.seedSoldIn(t, "pol-1", domain.OfferChannelEmbedded)
	_, err = b.assisted.Requote(laura, q.ID, renewal.RequoteInput{})
	assert.ErrorIs(t, err, errBoom)

	c := newAssistedRig(t, func(c *renewal.AssistedConfig) { c.NewID = func() (string, error) { return "bad-id", nil } })
	s := c.seedSoldIn(t, "pol-1", domain.OfferChannelEmbedded)
	_, err = c.assisted.Requote(laura, s.ID, renewal.RequoteInput{})
	assert.Error(t, err, "the snapshot is validated")

	d := newAssistedRig(t, func(c *renewal.AssistedConfig) { c.Origins.Offers = fakeOffers{err: errBoom} })
	u := d.seedSoldIn(t, "pol-1", domain.OfferChannelEmbedded)
	_, err = d.assisted.Requote(laura, u.ID, renewal.RequoteInput{})
	assert.ErrorIs(t, err, errBoom)
}

func TestTheDefaultRequoteIDIsRandom(t *testing.T) {
	a := newAssistedRig(t, func(c *renewal.AssistedConfig) { c.NewID = nil })
	p := a.seedSoldIn(t, "pol-1", domain.OfferChannelEmbedded)

	first, err := a.assisted.Requote(laura, p.ID, renewal.RequoteInput{})
	require.NoError(t, err)
	second, err := a.assisted.Requote(laura, p.ID, renewal.RequoteInput{})
	require.NoError(t, err)

	assert.True(t, strings.HasPrefix(first.Requote.ID, "rq-"))
	assert.Len(t, first.Requote.ID, len("rq-")+32, "128 random bits")
	assert.NotEqual(t, first.Requote.ID, second.Requote.ID)
}

// The view of the screen (S7, S7b and "Ver").

func TestTheViewOfARenewablePolicy(t *testing.T) {
	a := newAssistedRig(t)
	p := a.seedSoldIn(t, "pol-1", domain.OfferChannelEmbedded)

	v, err := a.assisted.View(laura, p.ID)
	require.NoError(t, err)
	assert.True(t, v.WindowOpen)
	assert.True(t, v.CanRequote)
	assert.False(t, v.CanFormalize, "nothing requoted yet")
	assert.Equal(t, renewal.BlockedNone, v.BlockedReason)
	assert.Equal(t, renewal.ConsentModeAudit, v.ConsentMode)
	require.NotNil(t, v.PaymentMethod)
	assert.Empty(t, v.PaymentMethod.Token)
	assert.Nil(t, v.Requote)
	assert.Nil(t, v.Renewal)

	_, err = a.assisted.Requote(laura, p.ID, renewal.RequoteInput{})
	require.NoError(t, err)
	v, err = a.assisted.View(laura, p.ID)
	require.NoError(t, err)
	require.NotNil(t, v.Requote)
	assert.Equal(t, domain.RequoteValid, v.RequoteStatus)
	assert.True(t, v.CanFormalize)

	a.clock.now = t0.Add(25 * time.Hour)
	v, err = a.assisted.View(laura, p.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.RequoteExpired, v.RequoteStatus)
	assert.False(t, v.CanFormalize)
}

func TestTheViewTellsTheConsentModeOfAnAssistedPolicy(t *testing.T) {
	a := newAssistedRig(t)
	p := a.seedSoldIn(t, "pol-1", domain.OfferChannelAssisted)

	v, err := a.assisted.View(laura, p.ID)

	require.NoError(t, err)
	assert.Equal(t, renewal.ConsentModeAdvisorAffirmation, v.ConsentMode, "S7 shows the checkbox")
}

func TestTheViewTellsWhyThePolicyCannotBeRenewedHere(t *testing.T) {
	a := newAssistedRig(t)
	far := a.seed(t, "pol-far", policyEnd.AddDate(0, 3, 0))
	v, err := a.assisted.View(laura, far.ID)
	require.NoError(t, err)
	assert.Equal(t, renewal.BlockedNotAvailable, v.BlockedReason)
	assert.False(t, v.CanRequote)

	noMethod := a.seedSoldIn(t, "pol-nomethod", domain.OfferChannelEmbedded)
	a.charges.set(noMethod.AcceptanceID, func(c *domain.Charge) { c.PaymentMethod = domain.PaymentMethod{} })
	v, err = a.assisted.View(laura, noMethod.ID)
	require.NoError(t, err)
	assert.Equal(t, renewal.BlockedPaymentMethodMissing, v.BlockedReason)
	assert.True(t, v.CanRequote, "the requote is shown; only the formalization is blocked (D-10)")
	assert.Nil(t, v.PaymentMethod)
}

func TestTheViewOfARenewalByTheInsuredShowsItsCharge(t *testing.T) {
	a := newAssistedRig(t)
	p := a.seedSoldIn(t, "pol-1", domain.OfferChannelEmbedded)
	accepted, err := a.svc.Accept(insured("sub-1"), p.ID)
	require.NoError(t, err)
	a.charges.set(accepted.Renewal.ChargeID, func(c *domain.Charge) {
		c.Status, c.FailureReason, c.Attempts, c.LastFailureAt = domain.ChargeFailed, domain.ChargePaymentRejected, 1, t0.Add(time.Minute)
	})

	v, err := a.assisted.View(laura, p.ID)

	require.NoError(t, err)
	assert.Equal(t, renewal.ChannelInsured, v.Channel)
	assert.Nil(t, v.Formalization)
	assert.Equal(t, renewal.BlockedClosed, v.BlockedReason)
	require.NotNil(t, v.Charge)
	assert.Equal(t, domain.ChargeFailed, v.Charge.Status)
	assert.Equal(t, domain.ChargePaymentRejected, v.Charge.FailureReason)
	assert.Empty(t, v.Charge.PaymentMethod.Token, "the token never leaves the service")
	assert.Equal(t, t0.Add(72*time.Hour), v.Renewal.RetryUntil, "until when the insured can retry (01·D-3 a+)")
}

func TestTheViewSettlesTheRenewalFirst(t *testing.T) {
	a := newAssistedRig(t)
	p := a.seedSoldIn(t, "pol-1", domain.OfferChannelEmbedded)
	_, err := a.assisted.Requote(laura, p.ID, renewal.RequoteInput{})
	require.NoError(t, err)
	_, _, err = a.assisted.Formalize(laura, p.ID, renewal.FormalizeInput{RequoteID: "rq-001", Acceptance: recordedCall(t0)})
	require.NoError(t, err)
	id := domain.RenewalID(p.ID, p.CoverageEnd)
	a.charges.set(id, func(c *domain.Charge) { c.Status, c.PolicyID = domain.ChargeConfirmed, "pol-new" })

	v, err := a.assisted.View(laura, p.ID)

	require.NoError(t, err)
	assert.Equal(t, renewal.ChannelAdvisor, v.Channel)
	assert.Equal(t, domain.RenewalRenewed, v.Renewal.Status)
	assert.Equal(t, "pol-new", v.Renewal.NewPolicyID)

	a.clock.now = t0.Add(80 * time.Hour)
	q := a.seedSoldIn(t, "pol-2", domain.OfferChannelEmbedded)
	_, err = a.svc.Accept(insured("sub-1"), q.ID)
	require.NoError(t, err)
	a.charges.set(domain.RenewalID(q.ID, q.CoverageEnd), func(c *domain.Charge) { c.Status = domain.ChargeFailed })
	a.clock.now = t0.Add(80*time.Hour + 73*time.Hour)
	v, err = a.assisted.View(laura, q.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.RenewalNotRenewed, v.Renewal.Status, "past retry_until the payment failed")
	assert.Equal(t, renewal.BlockedNotAvailable, v.BlockedReason)
}

func TestTheViewReportsAFailureReadingItsParts(t *testing.T) {
	a := newAssistedRig(t, func(c *renewal.AssistedConfig) { c.Origins.Offers = fakeOffers{err: errBoom} })
	p := a.seedSoldIn(t, "pol-1", domain.OfferChannelEmbedded)
	_, err := a.assisted.View(laura, p.ID)
	assert.ErrorIs(t, err, errBoom)
}

// D-9 (a): the confirmation code of the renewal.

func TestTheAdvisorAsksForACodeThatGoesToTheInsured(t *testing.T) {
	a := newAssistedRig(t)
	p := a.seedSoldIn(t, "pol-1", domain.OfferChannelEmbedded)
	_, err := a.assisted.Requote(laura, p.ID, renewal.RequoteInput{})
	require.NoError(t, err)

	sent, err := a.assisted.RequestConsentCode(laura, p.ID, "rq-001")

	require.NoError(t, err)
	assert.Equal(t, renewal.ConsentCodeSent{RequoteID: "rq-001", SentTo: "ca***@example.com", ExpiresAt: t0.Add(10 * time.Minute)}, sent)
	n := a.sender.last()
	assert.Equal(t, "carolina@example.com", n.To)
	assert.Equal(t, "Carolina Pérez", n.CustomerName)
	assert.Equal(t, a.codes[0], n.Code)
	assert.Equal(t, "renewal-consent-code-rq-001-1", n.IdempotencyKey)
	c, err := a.challenges.Get(context.Background(), "rq-001")
	require.NoError(t, err)
	assert.True(t, c.Matches(a.codes[0]), "only the hash is kept, bound to the requote")

	_, err = a.assisted.RequestConsentCode(laura, p.ID, "rq-001")
	require.NoError(t, err)
	assert.Equal(t, "renewal-consent-code-rq-001-2", a.sender.last().IdempotencyKey)
	_, err = a.assisted.RequestConsentCode(laura, p.ID, "rq-001")
	require.NoError(t, err)
	_, err = a.assisted.RequestConsentCode(laura, p.ID, "rq-001")
	assert.ErrorIs(t, err, formalization.ErrConsentCodeLimit, "a fourth code is refused")
}

func TestTheCodeNeverOutlivesTheRequote(t *testing.T) {
	a := newAssistedRig(t)
	p := a.seedSoldIn(t, "pol-1", domain.OfferChannelEmbedded)
	_, err := a.assisted.Requote(laura, p.ID, renewal.RequoteInput{})
	require.NoError(t, err)
	a.clock.now = t0.Add(24*time.Hour - 3*time.Minute)

	sent, err := a.assisted.RequestConsentCode(laura, p.ID, "rq-001")

	require.NoError(t, err)
	assert.Equal(t, t0.Add(24*time.Hour), sent.ExpiresAt)
}

func TestTheCodeIsRefusedForARequoteThatCannotBeFormalized(t *testing.T) {
	a := newAssistedRig(t)
	p := a.seedSoldIn(t, "pol-1", domain.OfferChannelEmbedded)
	_, err := a.assisted.Requote(laura, p.ID, renewal.RequoteInput{})
	require.NoError(t, err)
	_, err = a.assisted.Requote(laura, p.ID, renewal.RequoteInput{})
	require.NoError(t, err)
	a.clock.now = t0.Add(time.Second)
	_, err = a.assisted.Requote(laura, p.ID, renewal.RequoteInput{})
	require.NoError(t, err)

	_, err = a.assisted.RequestConsentCode(laura, p.ID, "rq-001")
	assert.ErrorIs(t, err, domain.ErrRequoteSuperseded)
	_, err = a.assisted.RequestConsentCode(laura, p.ID, "rq-404")
	assert.ErrorIs(t, err, ports.ErrRenewalRequoteNotFound)
	other := a.seedSoldIn(t, "pol-2", domain.OfferChannelEmbedded)
	_, err = a.assisted.RequestConsentCode(laura, other.ID, "rq-003")
	assert.ErrorIs(t, err, ports.ErrRenewalRequoteNotFound, "the requote of another policy")
	a.clock.now = t0.Add(25 * time.Hour)
	_, err = a.assisted.RequestConsentCode(laura, p.ID, "rq-003")
	assert.ErrorIs(t, err, domain.ErrRequoteExpired)
	assert.Empty(t, a.sender.notices)
}

func TestTheCodeIsRefusedWithoutPaymentMethodOrAnOpenPeriod(t *testing.T) {
	a := newAssistedRig(t)
	p := a.seedSoldIn(t, "pol-1", domain.OfferChannelEmbedded)
	_, err := a.assisted.Requote(laura, p.ID, renewal.RequoteInput{})
	require.NoError(t, err)
	a.charges.set(p.AcceptanceID, func(c *domain.Charge) { c.PaymentMethod = domain.PaymentMethod{} })
	_, err = a.assisted.RequestConsentCode(laura, p.ID, "rq-001")
	assert.ErrorIs(t, err, domain.ErrPaymentMethodMissing)

	b := newAssistedRig(t)
	q := b.seedSoldIn(t, "pol-1", domain.OfferChannelEmbedded)
	_, err = b.assisted.Requote(laura, q.ID, renewal.RequoteInput{})
	require.NoError(t, err)
	_, _, err = b.assisted.Formalize(laura, q.ID, renewal.FormalizeInput{RequoteID: "rq-001", Acceptance: recordedCall(t0)})
	require.NoError(t, err)
	_, err = b.assisted.RequestConsentCode(laura, q.ID, "rq-001")
	assert.ErrorIs(t, err, domain.ErrAssistedRenewalTaken, "already formalized")

	c := newAssistedRig(t)
	s := c.seedSoldIn(t, "pol-1", domain.OfferChannelEmbedded)
	_, err = c.assisted.Requote(laura, s.ID, renewal.RequoteInput{})
	require.NoError(t, err)
	_, err = c.svc.Accept(insured("sub-1"), s.ID)
	require.NoError(t, err)
	_, err = c.assisted.RequestConsentCode(laura, s.ID, "rq-001")
	assert.ErrorIs(t, err, domain.ErrRenewalClosed, "the insured renewed online")
	assert.Empty(t, a.sender.notices)
}

func TestAFailedDeliveryOfTheCodeIsReported(t *testing.T) {
	a := newAssistedRig(t)
	a.sender.err = errBoom
	p := a.seedSoldIn(t, "pol-1", domain.OfferChannelEmbedded)
	_, err := a.assisted.Requote(laura, p.ID, renewal.RequoteInput{})
	require.NoError(t, err)

	_, err = a.assisted.RequestConsentCode(laura, p.ID, "rq-001")
	assert.ErrorIs(t, err, formalization.ErrCodeDeliveryUnavailable)

	a.sender.err = formalization.ErrCodeDeliveryUnavailable
	_, err = a.assisted.RequestConsentCode(laura, p.ID, "rq-001")
	assert.ErrorIs(t, err, formalization.ErrCodeDeliveryUnavailable)

	b := newAssistedRig(t, func(c *renewal.AssistedConfig) { c.NewCode = func() (string, error) { return "", errBoom } })
	q := b.seedSoldIn(t, "pol-1", domain.OfferChannelEmbedded)
	_, err = b.assisted.Requote(laura, q.ID, renewal.RequoteInput{})
	require.NoError(t, err)
	_, err = b.assisted.RequestConsentCode(laura, q.ID, "rq-001")
	assert.ErrorIs(t, err, errBoom)
}

// CA-2, CA-3 and CA-4: the formalization.

func requoted(t *testing.T, a *assistedRig, id string) domain.Policy {
	t.Helper()
	p := a.seedSoldIn(t, id, domain.OfferChannelEmbedded)
	_, err := a.assisted.Requote(laura, p.ID, renewal.RequoteInput{})
	require.NoError(t, err)
	return p
}

func TestFormalizingRecordsTheAdvisorAndChargesTheRequotedPremium(t *testing.T) {
	a := newAssistedRig(t)
	p := requoted(t, a, "pol-1")
	a.clock.now = t0.Add(4 * time.Minute)

	v, created, err := a.assisted.Formalize(laura, p.ID, renewal.FormalizeInput{RequoteID: "rq-001", Acceptance: recordedCall(t0.Add(2 * time.Minute))})

	require.NoError(t, err)
	assert.True(t, created)
	id := domain.RenewalID(p.ID, p.CoverageEnd)
	require.NotNil(t, v.Renewal)
	assert.Equal(t, id, v.Renewal.ID)
	assert.Equal(t, domain.RenewalAccepted, v.Renewal.Status)
	assert.InDelta(t, 138_000, v.Renewal.Premium, 0.01, "the requoted premium, not a new price")
	assert.Equal(t, a.now().Add(72*time.Hour), v.Renewal.RetryUntil)
	assert.Equal(t, renewal.ChannelAdvisor, v.Channel)
	require.NotNil(t, v.Formalization)
	assert.Equal(t, domain.RenewalFormalization{RenewalID: id, RequoteID: "rq-001", AdvisorID: "sub-advisor",
		AdvisorEmail: "sub-advisor@example.com", AdvisorName: "Laura Gómez", Acceptance: recordedCall(t0.Add(2 * time.Minute)),
		FormalizedAt: a.now()}, *v.Formalization, "CA-4: who, when, how the customer accepted and the requote applied")

	charge := a.charges.rows[id]
	assert.Equal(t, domain.ChannelRenewal, charge.Channel, "CA-3: the charge of the issuance flow")
	assert.Equal(t, domain.ToMinorUnits(138_000), charge.AmountMinor)
	assert.Equal(t, policyEnd, charge.CoverageNotBefore)
	assert.Equal(t, []string{id}, a.charges.attached, "with the registered card")
	assert.Equal(t, 1, a.woken, "the charge worker is woken")
	require.NotNil(t, v.Charge)
	assert.False(t, v.CanRequote)
	assert.Equal(t, renewal.BlockedClosed, v.BlockedReason)
}

func (a *assistedRig) now() time.Time { return a.clock.now }

func TestFormalizingWithARemoteSignatureOrWithoutTheAdvisorsName(t *testing.T) {
	a := newAssistedRig(t)
	p := requoted(t, a, "pol-1")
	nameless := advisorCtx("sub-advisor", "")

	v, _, err := a.assisted.Formalize(nameless, p.ID, renewal.FormalizeInput{RequoteID: "rq-001",
		Acceptance: domain.ConsentAcceptance{Method: domain.ConsentRemoteSignature, AcceptedAt: t0, Reference: " FIRMA-1 "}})

	require.NoError(t, err)
	assert.Empty(t, v.Formalization.AdvisorName)
	assert.Equal(t, "sub-advisor@example.com", v.Formalization.AdvisorDisplayName(), "the screen shows the e-mail")
	assert.Equal(t, "FIRMA-1", v.Formalization.Acceptance.Reference, "the reference is trimmed")
}

func TestAnInvalidAcceptanceFormalizesNothing(t *testing.T) {
	a := newAssistedRig(t)
	p := requoted(t, a, "pol-1")

	for name, in := range map[string]renewal.FormalizeInput{
		"no requote":       {Acceptance: recordedCall(t0)},
		"no method":        {RequoteID: "rq-001", Acceptance: domain.ConsentAcceptance{AcceptedAt: t0, Reference: "GRAB-1"}},
		"no reference":     {RequoteID: "rq-001", Acceptance: domain.ConsentAcceptance{Method: domain.ConsentRecordedCall, AcceptedAt: t0}},
		"in the future":    {RequoteID: "rq-001", Acceptance: recordedCall(t0.Add(6 * time.Minute))},
		"before the quote": {RequoteID: "rq-001", Acceptance: recordedCall(t0.Add(-time.Minute))},
	} {
		_, _, err := a.assisted.Formalize(laura, p.ID, in)
		var invalid *domain.ValidationError
		assert.ErrorAs(t, err, &invalid, name)
	}
	assert.Empty(t, a.renewals.rows, "CA-2: nothing is created without a valid acceptance")
	assert.Empty(t, a.charges.attached)
}

func TestFormalizingWithTheConfirmationCode(t *testing.T) {
	byCode := func(code string) renewal.FormalizeInput {
		return renewal.FormalizeInput{RequoteID: "rq-001", Code: code,
			Acceptance: domain.ConsentAcceptance{Method: domain.ConsentConfirmationCode, AcceptedAt: t0}}
	}

	t.Run("no code was sent", func(t *testing.T) {
		a := newAssistedRig(t)
		p := requoted(t, a, "pol-1")
		_, _, err := a.assisted.Formalize(laura, p.ID, byCode("123456"))
		assert.ErrorIs(t, err, formalization.ErrConsentCodeRequired)
	})
	t.Run("a wrong code, then the right one", func(t *testing.T) {
		a := newAssistedRig(t)
		p := requoted(t, a, "pol-1")
		_, err := a.assisted.RequestConsentCode(laura, p.ID, "rq-001")
		require.NoError(t, err)

		_, _, err = a.assisted.Formalize(laura, p.ID, byCode("000000"))
		assert.ErrorIs(t, err, formalization.ErrConsentCodeInvalid)
		assert.Empty(t, a.renewals.rows)

		v, created, err := a.assisted.Formalize(laura, p.ID, byCode(a.codes[0]))
		require.NoError(t, err)
		assert.True(t, created)
		assert.Equal(t, domain.ConsentConfirmationCode, v.Formalization.Acceptance.Method)
		assert.Empty(t, v.Formalization.Acceptance.Reference, "the code is the proof")
	})
	t.Run("an expired code", func(t *testing.T) {
		a := newAssistedRig(t)
		p := requoted(t, a, "pol-1")
		_, err := a.assisted.RequestConsentCode(laura, p.ID, "rq-001")
		require.NoError(t, err)
		a.clock.now = t0.Add(10 * time.Minute)
		_, _, err = a.assisted.Formalize(laura, p.ID, byCode(a.codes[0]))
		assert.ErrorIs(t, err, formalization.ErrConsentCodeExpired)
	})
	t.Run("the sixth attempt is locked", func(t *testing.T) {
		a := newAssistedRig(t)
		p := requoted(t, a, "pol-1")
		_, err := a.assisted.RequestConsentCode(laura, p.ID, "rq-001")
		require.NoError(t, err)
		for i := 0; i < domain.MaxConsentCodeAttempts; i++ {
			_, _, err = a.assisted.Formalize(laura, p.ID, byCode("000000"))
			require.ErrorIs(t, err, formalization.ErrConsentCodeInvalid)
		}
		_, _, err = a.assisted.Formalize(laura, p.ID, byCode(a.codes[0]))
		assert.ErrorIs(t, err, formalization.ErrConsentCodeLocked)
		assert.Empty(t, a.renewals.rows)
	})
}

func TestARequoteThatIsNotTheLastOrExpiredCannotBeFormalized(t *testing.T) {
	a := newAssistedRig(t)
	p := requoted(t, a, "pol-1")
	a.clock.now = t0.Add(time.Second)
	_, err := a.assisted.Requote(laura, p.ID, renewal.RequoteInput{})
	require.NoError(t, err)

	_, _, err = a.assisted.Formalize(laura, p.ID, renewal.FormalizeInput{RequoteID: "rq-001", Acceptance: recordedCall(t0)})
	assert.ErrorIs(t, err, domain.ErrRequoteSuperseded)

	a.clock.now = t0.Add(25 * time.Hour)
	_, _, err = a.assisted.Formalize(laura, p.ID, renewal.FormalizeInput{RequoteID: "rq-002", Acceptance: recordedCall(t0)})
	assert.ErrorIs(t, err, domain.ErrRequoteExpired)

	_, _, err = a.assisted.Formalize(laura, p.ID, renewal.FormalizeInput{RequoteID: "rq-404", Acceptance: recordedCall(t0)})
	assert.ErrorIs(t, err, ports.ErrRenewalRequoteNotFound)
	assert.Empty(t, a.renewals.rows)
}

func TestWithoutPaymentMethodTheAdvisorCannotFormalize(t *testing.T) {
	a := newAssistedRig(t)
	p := requoted(t, a, "pol-1")
	a.charges.set(p.AcceptanceID, func(c *domain.Charge) { c.PaymentMethod = domain.PaymentMethod{} })

	_, _, err := a.assisted.Formalize(laura, p.ID, renewal.FormalizeInput{RequoteID: "rq-001", Acceptance: recordedCall(t0)})

	assert.ErrorIs(t, err, domain.ErrPaymentMethodMissing, "D-10 (a)")
	assert.Empty(t, a.renewals.rows)
}

func TestOfTwoAdvisorsTheSecondIsRefusedAndTheFirstCanRepeat(t *testing.T) {
	a := newAssistedRig(t)
	p := requoted(t, a, "pol-1")
	in := renewal.FormalizeInput{RequoteID: "rq-001", Acceptance: recordedCall(t0)}
	_, created, err := a.assisted.Formalize(laura, p.ID, in)
	require.NoError(t, err)
	require.True(t, created)

	_, _, err = a.assisted.Formalize(advisorCtx("sub-other", "Andrés"), p.ID, in)
	assert.ErrorIs(t, err, domain.ErrAssistedRenewalTaken)

	v, created, err := a.assisted.Formalize(laura, p.ID, renewal.FormalizeInput{})
	require.NoError(t, err, "repeating it returns the same renewal, whatever the input")
	assert.False(t, created)
	assert.Equal(t, "sub-advisor", v.Formalization.AdvisorID)
	assert.Len(t, a.charges.attached, 1, "one charge")
}

func TestARaceBetweenTwoAdvisorsHasOneWinner(t *testing.T) {
	// Another advisor records the period between the checks and the write.
	b := newAssistedRig(t)
	q := requoted(t, b, "pol-1")
	racer := &racingAssisted{fakeAssisted: b.records, winner: "sub-other"}
	c, err := renewal.NewAssistedService(renewal.AssistedConfig{Renewals: b.svc, Requotes: b.requotes, Assisted: racer,
		Origins: renewal.OriginFinder{Policies: b.policies, Charges: b.charges, Offers: b.offers}, Profiles: b.profiles,
		Consents: b.consents, Challenges: b.challenges, Sender: b.sender})
	require.NoError(t, err)
	_, _, err = c.Formalize(laura, q.ID, renewal.FormalizeInput{RequoteID: "rq-001", Acceptance: recordedCall(t0)})
	assert.ErrorIs(t, err, domain.ErrAssistedRenewalTaken)

	// The insured renews online between the checks and the write.
	d := newAssistedRig(t)
	s := requoted(t, d, "pol-1")
	racer2 := &racingAssisted{fakeAssisted: d.records, insured: true}
	e, err := renewal.NewAssistedService(renewal.AssistedConfig{Renewals: d.svc, Requotes: d.requotes, Assisted: racer2,
		Origins: renewal.OriginFinder{Policies: d.policies, Charges: d.charges, Offers: d.offers}, Profiles: d.profiles,
		Consents: d.consents, Challenges: d.challenges, Sender: d.sender})
	require.NoError(t, err)
	_, _, err = e.Formalize(laura, s.ID, renewal.FormalizeInput{RequoteID: "rq-001", Acceptance: recordedCall(t0)})
	assert.ErrorIs(t, err, domain.ErrRenewalClosed, "the insured got there first")
}

// racingAssisted lets another advisor, or the insured, record the period just
// before the write.
type racingAssisted struct {
	*fakeAssisted
	winner  string
	insured bool
}

func (r *racingAssisted) CreateAssisted(ctx context.Context, rn domain.Renewal, f domain.RenewalFormalization) (domain.Renewal,
	*domain.RenewalFormalization, bool, error) {
	if r.insured {
		_, _, _ = r.renewals.Create(ctx, rn)
	} else if r.winner != "" {
		won := f
		won.AdvisorID = r.winner
		_, _, _, _ = r.fakeAssisted.CreateAssisted(ctx, rn, won)
	}
	return r.fakeAssisted.CreateAssisted(ctx, rn, f)
}

func TestAfterTheInsuredRenewedTheAdvisorCannotFormalize(t *testing.T) {
	a := newAssistedRig(t)
	p := requoted(t, a, "pol-1")
	_, err := a.svc.Accept(insured("sub-1"), p.ID)
	require.NoError(t, err)

	_, _, err = a.assisted.Formalize(laura, p.ID, renewal.FormalizeInput{RequoteID: "rq-001", Acceptance: recordedCall(t0)})

	assert.ErrorIs(t, err, domain.ErrRenewalClosed)
	assert.Len(t, a.charges.attached, 1, "only the insured's charge")
}

func TestFormalizingOutsideTheWindowOrAfterALapseIsRefused(t *testing.T) {
	a := newAssistedRig(t)
	far := a.seed(t, "pol-far", policyEnd.AddDate(0, 3, 0))
	_, _, err := a.assisted.Formalize(laura, far.ID, renewal.FormalizeInput{RequoteID: "rq-001", Acceptance: recordedCall(t0)})
	assert.ErrorIs(t, err, domain.ErrRenewalNotAvailable)

	p := a.seedSoldIn(t, "pol-1", domain.OfferChannelEmbedded)
	_, _, err = a.renewals.Create(context.Background(), domain.Renewal{ID: domain.RenewalID(p.ID, p.CoverageEnd), PolicyID: p.ID,
		CustomerID: "sub-1", PeriodEnd: p.CoverageEnd, Status: domain.RenewalNotRenewed, Reason: domain.NotRenewedNoResponse, CreatedAt: t0})
	require.NoError(t, err)
	_, _, err = a.assisted.Formalize(laura, p.ID, renewal.FormalizeInput{RequoteID: "rq-001", Acceptance: recordedCall(t0)})
	assert.ErrorIs(t, err, domain.ErrRenewalNotAvailable)
}

func TestAFormalizationThatCannotBeStoredOrChargedIsReported(t *testing.T) {
	a := newAssistedRig(t)
	p := requoted(t, a, "pol-1")
	failing := &failingAssisted{fakeAssisted: a.records}
	b, err := renewal.NewAssistedService(renewal.AssistedConfig{Renewals: a.svc, Requotes: a.requotes, Assisted: failing,
		Origins: renewal.OriginFinder{Policies: a.policies, Charges: a.charges, Offers: a.offers}, Profiles: a.profiles,
		Consents: a.consents, Challenges: a.challenges, Sender: a.sender})
	require.NoError(t, err)

	_, _, err = b.Formalize(laura, p.ID, renewal.FormalizeInput{RequoteID: "rq-001", Acceptance: recordedCall(t0)})
	assert.ErrorIs(t, err, errBoom)
	_, err = b.View(laura, p.ID)
	assert.NoError(t, err)

	_, err = a.svc.Accept(insured("sub-1"), p.ID)
	require.NoError(t, err)
	_, err = b.View(laura, p.ID)
	assert.ErrorIs(t, err, errBoom, "the record of the renewal cannot be read")
}

type failingAssisted struct{ *fakeAssisted }

func (failingAssisted) CreateAssisted(context.Context, domain.Renewal, domain.RenewalFormalization) (domain.Renewal,
	*domain.RenewalFormalization, bool, error) {
	return domain.Renewal{}, nil, false, errBoom
}

func (failingAssisted) GetFormalization(context.Context, string) (domain.RenewalFormalization, error) {
	return domain.RenewalFormalization{}, errBoom
}
