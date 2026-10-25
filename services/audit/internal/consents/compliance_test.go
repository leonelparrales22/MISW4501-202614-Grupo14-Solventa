package consents_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/consents"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/ports"
)

// The compliance portal revokes a consent and retries the entities that did
// not confirm (HU-W4.2-02), over the doubles of the revocation process
// (revocation_test.go).

var opsActor = domain.Actor{Type: domain.ActorOperations, ID: "sub-ops"}

type complianceFixture struct {
	*revocationFixture
	compliance *consents.ComplianceService
}

func newComplianceFixture(t *testing.T) *complianceFixture {
	t.Helper()
	f := newRevocationFixture(t)
	return &complianceFixture{
		revocationFixture: f,
		compliance: consents.NewComplianceService(consents.ComplianceDeps{
			Revocations: f.svc, Consents: f.consents, Events: f.events, Records: f.revocations, Clock: f.clock,
			Names: domain.EntityNames{"aliado-banco-1": "Banco Aliado"},
		}),
	}
}

func portalRequest() domain.ComplianceRevokeRequest {
	return domain.ComplianceRevokeRequest{
		Reason: "customer_request", Reference: "TCK-2026-0412", RoleConfirmed: true, UserAgent: "Mozilla/5.0 (Macintosh)",
	}
}

// failPending claims the notice of the consent to the entity and fails it, as
// the worker does when the window ends without a confirmation.
func (f *complianceFixture) failPending(t *testing.T, consentID, entity string) {
	t.Helper()
	rec, claimed, err := f.notices.Claim(context.Background(), ports.NoticeID(consentID, entity), f.clock.Now(), time.Second)
	require.NoError(t, err)
	require.True(t, claimed)
	_, err = f.notices.Fail(context.Background(), rec, f.clock.Now(), idempotency.ReasonWindowExpired, "status 503")
	require.NoError(t, err)
}

func (f *complianceFixture) confirmPending(t *testing.T, consentID, entity string) {
	t.Helper()
	rec, claimed, err := f.notices.Claim(context.Background(), ports.NoticeID(consentID, entity), f.clock.Now(), time.Second)
	require.NoError(t, err)
	require.True(t, claimed)
	_, err = f.notices.Confirm(context.Background(), rec, f.clock.Now(), idempotency.Outcome{Reference: "ok"})
	require.NoError(t, err)
}

// seedTwoEntities records a revocation of the consent that notifies two
// entities (a consent authorizes one today, so only a seed has two), then
// confirms the first and fails the second: a partial revocation.
func (f *complianceFixture) seedTwoEntities(t *testing.T, consentID string) {
	t.Helper()
	now := f.clock.Now().UTC().Truncate(time.Microsecond)
	c, err := f.consents.Get(context.Background(), consentID)
	require.NoError(t, err)
	rev := domain.Revocation{
		ID: "rvk-two", ConsentID: consentID, Holder: c.Holder, Scope: c.Scope,
		Entities:    []domain.Entity{{Code: "aliado-a", Name: "aliado-a"}, {Code: "aliado-b", Name: "aliado-b"}},
		RequestedAt: now, EffectiveAt: now, NoticeDeadlineAt: now.Add(domain.RevocationNoticeDeadline),
		Channel: domain.RevocationChannelCompliancePortal, Actor: opsActor, Reason: "regulatory_order", Reference: "ORD-1", RequestID: "req-two",
	}
	var notices []ports.NoticeRecord
	for _, e := range []string{"aliado-a", "aliado-b"} {
		notices = append(notices, ports.NoticeRecord{
			ConsentID: consentID, EntityCode: e, Key: "revocation-" + consentID + "-" + e, WindowEndsAt: rev.NoticeDeadlineAt, NextAttemptAt: now,
		})
	}
	_, created, err := f.revocations.Record(context.Background(), rev, notices)
	require.NoError(t, err)
	require.True(t, created)
	f.confirmPending(t, consentID, "aliado-a")
	f.failPending(t, consentID, "aliado-b")
}

func TestComplianceRevokeRecordsTheChannelTheActorAndTheMethod(t *testing.T) {
	f := newComplianceFixture(t)
	req := portalRequest()
	req.Reference = "  TCK-2026   0412 "

	state, created, err := f.compliance.Revoke(context.Background(), "cns-a1", "sub-ops", "req-1", req)

	require.NoError(t, err)
	assert.True(t, created)
	rev, err := f.revocations.GetByConsent(context.Background(), "cns-a1")
	require.NoError(t, err, "criterion 5: recorded before the answer")
	assert.Equal(t, domain.RevocationChannelCompliancePortal, rev.Channel)
	assert.Equal(t, opsActor, rev.Actor)
	assert.Equal(t, domain.ConfirmationRoleAttestation, rev.ConfirmationMethod)
	assert.Equal(t, "customer_request", rev.Reason)
	assert.Equal(t, "TCK-2026 0412", rev.Reference, "the reference is recorded with the spaces collapsed")
	assert.Equal(t, "Mozilla/5.0 (Macintosh)", rev.Device)
	assert.Equal(t, "req-1", rev.RequestID)
	assert.Equal(t, rev.EffectiveAt, rev.RequestedAt, "the portal has no offline queue: the request is the server time")
	assert.Equal(t, f.clock.Now().UTC().Truncate(time.Microsecond), rev.EffectiveAt)
	assert.Equal(t, f.consents.rows[0].Scope, rev.Scope)
	assert.Equal(t, 1, f.wakes, "the worker is woken up")

	assert.Equal(t, domain.StatusRevoked, state.View.Status)
	assert.False(t, state.View.Revocable)
	require.NotNil(t, state.Record)
	assert.Equal(t, rev.ReceiptNumber, state.Record.ReceiptNumber)
	require.NotNil(t, state.View.Revocation)
	assert.Equal(t, domain.NoticeInProgress, state.View.Revocation.Summary)
	assert.Equal(t, string(domain.RevocationChannelCompliancePortal), state.View.Revocation.Channel)
	require.Len(t, state.History, 3)
	assert.Equal(t, []string{domain.HistoryGranted, domain.HistoryRevocationRequested, domain.HistoryRevoked},
		[]string{state.History[0].Status, state.History[1].Status, state.History[2].Status})
	assert.Equal(t, string(domain.ActorOperations), state.History[2].ActorType)
	assert.Equal(t, "sub-ops", state.History[2].ActorID)
}

func TestComplianceRevokeWithoutUserAgentRecordsNoDevice(t *testing.T) {
	f := newComplianceFixture(t)
	req := portalRequest()
	req.UserAgent = ""

	_, created, err := f.compliance.Revoke(context.Background(), "cns-a1", "sub-ops", "req-1", req)

	require.NoError(t, err)
	assert.True(t, created)
	rev, err := f.revocations.GetByConsent(context.Background(), "cns-a1")
	require.NoError(t, err)
	assert.Empty(t, rev.Device)
}

func TestComplianceRevokeWithTheSameKeyReturnsTheStoredRevocation(t *testing.T) {
	f := newComplianceFixture(t)
	ctx := context.Background()
	first, created, err := f.compliance.Revoke(ctx, "cns-a1", "sub-ops", "req-1", portalRequest())
	require.NoError(t, err)
	require.True(t, created)

	again, created, err := f.compliance.Revoke(ctx, "cns-a1", "sub-ops", "req-1", portalRequest())

	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, first.Record.ReceiptNumber, again.Record.ReceiptNumber)
	assert.Equal(t, 1, f.revocations.records, "nothing is recorded twice")
	assert.Equal(t, 1, f.wakes)
}

func TestComplianceRevokeWithAnotherKeyOnARevokedConsentIsRefused(t *testing.T) {
	ctx := context.Background()

	t.Run("revoked from the portal", func(t *testing.T) {
		f := newComplianceFixture(t)
		_, _, err := f.compliance.Revoke(ctx, "cns-a1", "sub-ops", "req-1", portalRequest())
		require.NoError(t, err)

		_, created, err := f.compliance.Revoke(ctx, "cns-a1", "sub-ops", "req-2", portalRequest())

		assert.ErrorIs(t, err, domain.ErrAlreadyRevoked)
		assert.False(t, created)
	})
	t.Run("partially revoked", func(t *testing.T) {
		f := newComplianceFixture(t)
		_, _, err := f.compliance.Revoke(ctx, "cns-a1", "sub-ops", "req-1", portalRequest())
		require.NoError(t, err)
		f.failPending(t, "cns-a1", "aliado-banco-1")

		_, _, err = f.compliance.Revoke(ctx, "cns-a1", "sub-ops", "req-2", portalRequest())

		assert.ErrorIs(t, err, domain.ErrAlreadyRevoked)
	})
	t.Run("revoked by the holder from the mobile app", func(t *testing.T) {
		f := newComplianceFixture(t)
		_, _, err := f.svc.RevokeAsHolder(ctx, "1020304050", "sub-a", "cns-a1", "req-app", holderRequest(f.clock.Now()))
		require.NoError(t, err)

		_, created, err := f.compliance.Revoke(ctx, "cns-a1", "sub-ops", "req-portal", portalRequest())

		assert.ErrorIs(t, err, domain.ErrAlreadyRevoked)
		assert.False(t, created)
		assert.Equal(t, 1, f.revocations.records)
	})
	t.Run("the key of another consent", func(t *testing.T) {
		f := newComplianceFixture(t)
		_, _, err := f.compliance.Revoke(ctx, "cns-a1", "sub-ops", "req-1", portalRequest())
		require.NoError(t, err)

		_, _, err = f.compliance.Revoke(ctx, "cns-a2", "sub-ops", "req-1", portalRequest())

		assert.ErrorIs(t, err, domain.ErrIdempotencyKeyReused)
	})
}

func TestComplianceRevokeAnExpiredConsentIsNotActive(t *testing.T) {
	// cns-a2 was granted at revGrantedAt: it expires 12 months later.
	expires := revGrantedAt.AddDate(0, domain.ConsentValidityMonths, 0)
	cases := []struct {
		name    string
		now     time.Time
		revoked bool
	}{
		{name: "1 ns before it expires", now: expires.Add(-time.Nanosecond), revoked: true},
		{name: "exactly when it expires", now: expires},
		{name: "1 ns after it expires", now: expires.Add(time.Nanosecond)},
		{name: "a year after", now: expires.AddDate(1, 0, 0)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newComplianceFixture(t)
			f.clock.now = tc.now

			_, created, err := f.compliance.Revoke(context.Background(), "cns-a2", "sub-ops", "req-1", portalRequest())

			if tc.revoked {
				require.NoError(t, err)
				assert.True(t, created)
				return
			}
			assert.ErrorIs(t, err, domain.ErrConsentNotActive)
			assert.False(t, created)
			assert.Zero(t, f.revocations.records, "nothing is recorded")
		})
	}
}

func TestComplianceRevokeAnInvalidRequestDoesNotCallTheRevocation(t *testing.T) {
	f := newComplianceFixture(t)

	_, created, err := f.compliance.Revoke(context.Background(), "cns-a1", "sub-ops", "req-1", domain.ComplianceRevokeRequest{})

	var verr *domain.ValidationError
	require.ErrorAs(t, err, &verr)
	assert.Equal(t, []domain.FieldError{
		{Field: "reason", Code: domain.CodeRequired},
		{Field: "reference", Code: domain.CodeRequired},
		{Field: "role_confirmed", Code: domain.CodeMustBeTrue},
	}, verr.Fields, "criterion 1: every missing field at once")
	assert.False(t, created)
	assert.Zero(t, f.revocations.records, "nothing is recorded")
	assert.Zero(t, f.wakes)
	assert.Empty(t, f.events.statuses("cns-a1"), "no event either")
}

func TestComplianceRevokeAnUnknownConsent(t *testing.T) {
	f := newComplianceFixture(t)

	_, created, err := f.compliance.Revoke(context.Background(), "cns-missing", "sub-ops", "req-1", portalRequest())

	assert.ErrorIs(t, err, ports.ErrConsentNotFound)
	assert.False(t, created)
	assert.Zero(t, f.revocations.records)
}

func TestComplianceRevokeReportsTheErrorsOfTheStores(t *testing.T) {
	f := newComplianceFixture(t)
	ctx := context.Background()

	f.revocations.err = errors.New("db down")
	_, _, err := f.compliance.Revoke(ctx, "cns-a1", "sub-ops", "req-1", portalRequest())
	assert.ErrorContains(t, err, "record the revocation")

	f.revocations.err = nil
	f.events.err = errors.New("events down")
	_, _, err = f.compliance.Revoke(ctx, "cns-a1", "sub-ops", "req-1", portalRequest())
	assert.ErrorContains(t, err, "events down")
}

// The portal channel is validated with its own rules, whoever builds the
// command; any other channel keeps the rules of HU-M1.3-02 (decision D3).
func TestTheValidationDependsOnTheChannel(t *testing.T) {
	ctx := context.Background()
	fieldsOf := func(t *testing.T, err error) []string {
		t.Helper()
		var verr *domain.ValidationError
		require.ErrorAs(t, err, &verr)
		var out []string
		for _, f := range verr.Fields {
			out = append(out, f.Field)
		}
		return out
	}

	t.Run("the mobile app without device is still refused", func(t *testing.T) {
		f := newComplianceFixture(t)
		req := holderRequest(f.clock.Now())
		req.Device = ""

		_, _, err := f.svc.RevokeAsHolder(ctx, "1020304050", "sub-a", "cns-a1", "req-1", req)

		assert.Equal(t, []string{"device"}, fieldsOf(t, err))
		assert.Zero(t, f.revocations.records)
	})
	t.Run("the mobile app without confirmation method is still refused", func(t *testing.T) {
		f := newComplianceFixture(t)
		req := holderRequest(f.clock.Now())
		req.ConfirmationMethod = "role_attestation"

		_, _, err := f.svc.RevokeAsHolder(ctx, "1020304050", "sub-a", "cns-a1", "req-1", req)

		assert.Equal(t, []string{"confirmation_method"}, fieldsOf(t, err), "the method of the portal is not valid for the app")
	})
	t.Run("a command with no channel is checked like the app", func(t *testing.T) {
		f := newComplianceFixture(t)

		_, _, err := f.svc.Revoke(ctx, consents.RevokeCommand{ConsentID: "cns-a1", RequestID: "req-1"})

		assert.Equal(t, []string{"requested_at", "device", "confirmation_method"}, fieldsOf(t, err))
	})
	t.Run("the portal command without reason nor reference is refused", func(t *testing.T) {
		f := newComplianceFixture(t)

		_, _, err := f.svc.Revoke(ctx, consents.RevokeCommand{
			ConsentID: "cns-a1", Channel: domain.RevocationChannelCompliancePortal, Actor: opsActor, RequestID: "req-1",
			Request: domain.RevokeRequest{RequestedAt: f.clock.Now()},
		})

		assert.Equal(t, []string{"reason", "reference"}, fieldsOf(t, err))
		assert.Zero(t, f.revocations.records)
	})
	t.Run("the portal command without device nor method is accepted", func(t *testing.T) {
		f := newComplianceFixture(t)

		_, created, err := f.svc.Revoke(ctx, consents.RevokeCommand{
			ConsentID: "cns-a1", Channel: domain.RevocationChannelCompliancePortal, Actor: opsActor, RequestID: "req-1",
			Reason: "regulatory_order", Reference: "ORD-1", Request: domain.RevokeRequest{RequestedAt: f.clock.Now()},
		})

		require.NoError(t, err)
		assert.True(t, created)
	})
}

func TestComplianceStateOfAnActiveConsent(t *testing.T) {
	f := newComplianceFixture(t)

	state, err := f.compliance.State(context.Background(), "cns-a1")

	require.NoError(t, err)
	assert.Equal(t, domain.StatusActive, state.View.Status)
	assert.True(t, state.View.Revocable)
	assert.Nil(t, state.View.Revocation)
	assert.Nil(t, state.Record)
	assert.Equal(t, "1020304050", state.View.Consent.Holder.DocumentID, "the domain keeps it; the route masks it")
	require.Len(t, state.History, 1)
	assert.Equal(t, domain.HistoryGranted, state.History[0].Status)
	assert.Equal(t, []domain.Entity{{Code: "aliado-banco-1", Name: "Banco Aliado"}}, state.View.Entities)
}

func TestComplianceStateOfAnExpiredConsentShowsTheExpiry(t *testing.T) {
	f := newComplianceFixture(t)

	state, err := f.compliance.State(context.Background(), "cns-old")

	require.NoError(t, err)
	assert.Equal(t, domain.StatusExpired, state.View.Status)
	assert.False(t, state.View.Revocable, "criterion 4")
	require.Len(t, state.History, 2)
	assert.Equal(t, domain.HistoryExpired, state.History[1].Status)
}

func TestComplianceStateOfAPartialRevocationShowsTheEntitiesThatDidNotConfirm(t *testing.T) {
	f := newComplianceFixture(t)
	f.seedTwoEntities(t, "cns-a1")

	state, err := f.compliance.State(context.Background(), "cns-a1")

	require.NoError(t, err)
	assert.Equal(t, domain.StatusPartiallyRevoked, state.View.Status)
	require.NotNil(t, state.View.Revocation)
	assert.Equal(t, domain.NoticePartial, state.View.Revocation.Summary)
	got := map[string]domain.EntityNoticeStatus{}
	for _, e := range state.View.Revocation.Entities {
		got[e.Entity.Code] = e.Status
		assert.False(t, e.DeadlineAt.IsZero(), "criterion 3: the time its window ended")
	}
	assert.Equal(t, map[string]domain.EntityNoticeStatus{"aliado-a": domain.NoticeConfirmed, "aliado-b": domain.NoticeNotConfirmed}, got)
	require.NotNil(t, state.Record)
	assert.Equal(t, "ORD-1", state.Record.Reference)
	assert.Equal(t, opsActor, state.Record.Actor)
}

func TestComplianceStateOfARevokedEventWithoutRecord(t *testing.T) {
	f := newComplianceFixture(t)
	f.events.add(domain.ConsentEvent{ConsentID: "cns-a1", Status: domain.HistoryRevoked, OccurredAt: revGrantedAt.Add(time.Hour), ReceiptID: "REV-0001"})

	state, err := f.compliance.State(context.Background(), "cns-a1")

	require.NoError(t, err)
	assert.Equal(t, domain.StatusRevoked, state.View.Status)
	assert.Nil(t, state.Record)
}

func TestComplianceStateReportsTheErrorsOfTheStores(t *testing.T) {
	ctx := context.Background()

	f := newComplianceFixture(t)
	_, err := f.compliance.State(ctx, "cns-missing")
	assert.ErrorIs(t, err, ports.ErrConsentNotFound)

	f.consents.err = errors.New("db down")
	_, err = f.compliance.State(ctx, "cns-a1")
	assert.ErrorContains(t, err, "db down")
	assert.NotErrorIs(t, err, ports.ErrConsentNotFound)
	f.consents.err = nil

	f.events.err = errors.New("events down")
	_, err = f.compliance.State(ctx, "cns-a1")
	assert.ErrorContains(t, err, "events down")
	f.events.err = nil

	_, _, err = f.compliance.Revoke(ctx, "cns-a1", "sub-ops", "req-1", portalRequest())
	require.NoError(t, err)
	broken := consents.NewComplianceService(consents.ComplianceDeps{
		Revocations: f.svc, Consents: f.consents, Events: f.events, Records: failingRecords{}, Clock: f.clock,
	})
	_, err = broken.State(ctx, "cns-a1")
	assert.ErrorContains(t, err, "records down")
}

// failingRecords is a revocation store that cannot read.
type failingRecords struct{ ports.RevocationStore }

func (failingRecords) GetByConsent(context.Context, string) (domain.Revocation, error) {
	return domain.Revocation{}, errors.New("records down")
}

func TestComplianceRetryResumesOnlyTheEntitiesThatDidNotConfirm(t *testing.T) {
	f := newComplianceFixture(t)
	ctx := context.Background()
	f.seedTwoEntities(t, "cns-a1")
	confirmed := f.notices.get(ports.NoticeID("cns-a1", "aliado-a"))
	wakes := f.wakes

	retried, state, err := f.compliance.Retry(ctx, "cns-a1", "sub-ops")

	require.NoError(t, err)
	assert.Equal(t, []string{"aliado-b"}, retried, "criterion 3: only the pending one")
	assert.Equal(t, confirmed, f.notices.get(ports.NoticeID("cns-a1", "aliado-a")), "the confirmed entity is never notified again")
	b := f.notices.get(ports.NoticeID("cns-a1", "aliado-b"))
	assert.Equal(t, idempotency.StatusPending, b.Status)
	assert.Equal(t, f.clock.Now().UTC().Add(f.svc.NoticeDeadline()), b.WindowEndsAt, "a new window of 5 minutes")
	assert.Equal(t, wakes+1, f.wakes)

	// A retry keeps the revocation partial until the entity confirms
	// (HU-M1.3-02): the entity waits in its new window.
	require.NotNil(t, state.View.Revocation)
	assert.Equal(t, domain.NoticePartial, state.View.Revocation.Summary)
	assert.Equal(t, domain.StatusPartiallyRevoked, state.View.Status)
	got := map[string]domain.EntityNoticeStatus{}
	for _, e := range state.View.Revocation.Entities {
		got[e.Entity.Code] = e.Status
	}
	assert.Equal(t, map[string]domain.EntityNoticeStatus{"aliado-a": domain.NoticeConfirmed, "aliado-b": domain.NoticePending}, got)
	statuses := f.events.statuses("cns-a1")
	assert.Contains(t, statuses, domain.HistoryRevocationRetryRequested+"|aliado-b")
	assert.NotContains(t, statuses, domain.HistoryRevocationRetryRequested+"|aliado-a")
	last := state.History[len(state.History)-1]
	assert.Equal(t, domain.HistoryRevocationRetryRequested, last.Status)
	assert.Equal(t, "sub-ops", last.ActorID)
	assert.Equal(t, string(domain.ActorOperations), last.ActorType)

	events := len(f.events.statuses("cns-a1"))
	wakes = f.wakes
	_, again, err := f.compliance.Retry(ctx, "cns-a1", "sub-ops")
	assert.ErrorIs(t, err, domain.ErrNothingToRetry, "a second retry with the window open")
	assert.Equal(t, domain.NoticePartial, again.View.Revocation.Summary, "the state tells why")
	assert.Equal(t, events, len(f.events.statuses("cns-a1")), "no second retry event")
	assert.Equal(t, wakes, f.wakes)
}

func TestComplianceRetryOnceTheRetriedEntityConfirms(t *testing.T) {
	f := newComplianceFixture(t)
	ctx := context.Background()
	f.seedTwoEntities(t, "cns-a1")
	_, _, err := f.compliance.Retry(ctx, "cns-a1", "sub-ops")
	require.NoError(t, err)
	f.confirmPending(t, "cns-a1", "aliado-b")

	state, err := f.compliance.State(ctx, "cns-a1")
	require.NoError(t, err)
	assert.Equal(t, domain.StatusRevoked, state.View.Status, "criterion 2: revoked when every entity confirms")
	assert.Equal(t, domain.NoticeCompleted, state.View.Revocation.Summary)

	_, _, err = f.compliance.Retry(ctx, "cns-a1", "sub-ops")
	assert.ErrorIs(t, err, domain.ErrNothingToRetry)
}

func TestComplianceRetryAfterTheNewWindowFailsAgain(t *testing.T) {
	f := newComplianceFixture(t)
	ctx := context.Background()
	_, _, err := f.compliance.Revoke(ctx, "cns-a1", "sub-ops", "req-1", portalRequest())
	require.NoError(t, err)
	f.failPending(t, "cns-a1", "aliado-banco-1")
	_, _, err = f.compliance.Retry(ctx, "cns-a1", "sub-ops")
	require.NoError(t, err)
	f.failPending(t, "cns-a1", "aliado-banco-1")

	retried, state, err := f.compliance.Retry(ctx, "cns-a1", "sub-ops")

	require.NoError(t, err)
	assert.Equal(t, []string{"aliado-banco-1"}, retried)
	assert.Equal(t, domain.NoticePartial, state.View.Revocation.Summary, "partial until the entity confirms")
	assert.Equal(t, domain.NoticePending, state.View.Revocation.Entities[0].Status)
}

func TestComplianceRetryNeedsAPartialRevocation(t *testing.T) {
	ctx := context.Background()

	t.Run("a consent that is not revoked", func(t *testing.T) {
		f := newComplianceFixture(t)

		retried, state, err := f.compliance.Retry(ctx, "cns-a1", "sub-ops")

		assert.ErrorIs(t, err, domain.ErrConsentNotRevoked)
		assert.Empty(t, retried)
		assert.Equal(t, domain.StatusActive, state.View.Status)
	})
	t.Run("a notice in progress", func(t *testing.T) {
		f := newComplianceFixture(t)
		_, _, err := f.compliance.Revoke(ctx, "cns-a1", "sub-ops", "req-1", portalRequest())
		require.NoError(t, err)
		wakes := f.wakes

		retried, state, err := f.compliance.Retry(ctx, "cns-a1", "sub-ops")

		assert.ErrorIs(t, err, domain.ErrNothingToRetry)
		assert.Empty(t, retried)
		assert.Equal(t, domain.NoticeInProgress, state.View.Revocation.Summary, "the state tells why")
		assert.Equal(t, wakes, f.wakes)
	})
	t.Run("a completed notice", func(t *testing.T) {
		f := newComplianceFixture(t)
		_, _, err := f.compliance.Revoke(ctx, "cns-a1", "sub-ops", "req-1", portalRequest())
		require.NoError(t, err)
		f.confirmPending(t, "cns-a1", "aliado-banco-1")

		_, state, err := f.compliance.Retry(ctx, "cns-a1", "sub-ops")

		assert.ErrorIs(t, err, domain.ErrNothingToRetry)
		assert.Equal(t, domain.NoticeCompleted, state.View.Revocation.Summary)
		assert.NotContains(t, f.events.statuses("cns-a1"), domain.HistoryRevocationRetryRequested+"|aliado-banco-1")
	})
	t.Run("a consent revoked from the app with a partial notice", func(t *testing.T) {
		f := newComplianceFixture(t)
		_, _, err := f.svc.RevokeAsHolder(ctx, "1020304050", "sub-a", "cns-a1", "req-app", holderRequest(f.clock.Now()))
		require.NoError(t, err)
		f.failPending(t, "cns-a1", "aliado-banco-1")

		retried, _, err := f.compliance.Retry(ctx, "cns-a1", "sub-ops")

		require.NoError(t, err, "the portal retries any partial revocation, whoever requested it")
		assert.Equal(t, []string{"aliado-banco-1"}, retried)
	})
	t.Run("an unknown consent", func(t *testing.T) {
		f := newComplianceFixture(t)

		_, _, err := f.compliance.Retry(ctx, "cns-missing", "sub-ops")

		assert.ErrorIs(t, err, ports.ErrConsentNotFound)
	})
	t.Run("the store fails", func(t *testing.T) {
		f := newComplianceFixture(t)
		f.seedTwoEntities(t, "cns-a1")
		f.notices.resumeErr = errors.New("db down")

		_, _, err := f.compliance.Retry(ctx, "cns-a1", "sub-ops")

		assert.ErrorContains(t, err, "db down")
	})
}

// Two analysts click Retry at the same time: one starts the new window and the
// other finds nothing to retry; the entity is notified once.
func TestComplianceTwoSimultaneousRetriesStartOneWindow(t *testing.T) {
	f := newComplianceFixture(t)
	ctx := context.Background()
	_, _, err := f.compliance.Revoke(ctx, "cns-a1", "sub-ops", "req-1", portalRequest())
	require.NoError(t, err)
	f.failPending(t, "cns-a1", "aliado-banco-1")

	const callers = 8
	var wg sync.WaitGroup
	results := make(chan error, callers)
	start := make(chan struct{})
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, _, err := f.compliance.Retry(ctx, "cns-a1", "sub-ops")
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	var ok, nothing int
	for err := range results {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, domain.ErrNothingToRetry):
			nothing++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	assert.Equal(t, 1, ok, "one retry wins")
	assert.Equal(t, callers-1, nothing)
	retries := 0
	for _, s := range f.events.statuses("cns-a1") {
		if s == domain.HistoryRevocationRetryRequested+"|aliado-banco-1" {
			retries++
		}
	}
	assert.Equal(t, 1, retries, "a single retry event")
}
