package domain_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
)

// HU-M1.3-02: the revocation request, the receipt number and the status
// derived from the revocation and the notice to each entity.

func TestRevokeRequestValidate(t *testing.T) {
	c := holderConsent("cns-1", viewGrantedAt)
	now := viewGrantedAt.AddDate(0, 2, 0)
	valid := domain.RevokeRequest{RequestedAt: now.Add(-time.Hour), Device: "Google Pixel 8", ConfirmationMethod: "biometric"}
	long := make([]rune, domain.MaxDeviceLength+1)
	for i := range long {
		long[i] = 'x'
	}

	cases := []struct {
		name   string
		mutate func(*domain.RevokeRequest)
		want   []domain.FieldError
	}{
		{name: "valid with biometrics", mutate: func(*domain.RevokeRequest) {}},
		{name: "valid with the password", mutate: func(r *domain.RevokeRequest) { r.ConfirmationMethod = "password" }},
		{name: "requested_at exactly at the clock skew", mutate: func(r *domain.RevokeRequest) { r.RequestedAt = now.Add(domain.MaxClockSkew) }},
		{name: "requested_at exactly at granted_at", mutate: func(r *domain.RevokeRequest) { r.RequestedAt = viewGrantedAt }},
		{name: "requested_at hours ago (offline queue)", mutate: func(r *domain.RevokeRequest) { r.RequestedAt = now.Add(-6 * time.Hour) }},
		{
			name: "requested_at missing", mutate: func(r *domain.RevokeRequest) { r.RequestedAt = time.Time{} },
			want: []domain.FieldError{{Field: "requested_at", Code: domain.CodeRequired}},
		},
		{
			name:   "requested_at more than the clock skew in the future",
			mutate: func(r *domain.RevokeRequest) { r.RequestedAt = now.Add(domain.MaxClockSkew + time.Millisecond) },
			want:   []domain.FieldError{{Field: "requested_at", Code: domain.CodeOutOfRange}},
		},
		{
			name: "requested_at before the consent was granted", mutate: func(r *domain.RevokeRequest) { r.RequestedAt = viewGrantedAt.Add(-time.Second) },
			want: []domain.FieldError{{Field: "requested_at", Code: domain.CodeOutOfRange}},
		},
		{
			name: "device missing", mutate: func(r *domain.RevokeRequest) { r.Device = "   " },
			want: []domain.FieldError{{Field: "device", Code: domain.CodeRequired}},
		},
		{
			name: "device too long", mutate: func(r *domain.RevokeRequest) { r.Device = string(long) },
			want: []domain.FieldError{{Field: "device", Code: domain.CodeOutOfRange}},
		},
		{name: "device at the limit", mutate: func(r *domain.RevokeRequest) { r.Device = string(long[1:]) }},
		{
			name: "confirmation method missing", mutate: func(r *domain.RevokeRequest) { r.ConfirmationMethod = "" },
			want: []domain.FieldError{{Field: "confirmation_method", Code: domain.CodeRequired}},
		},
		{
			name: "confirmation method unknown", mutate: func(r *domain.RevokeRequest) { r.ConfirmationMethod = "pin" },
			want: []domain.FieldError{{Field: "confirmation_method", Code: domain.CodeInvalidFormat}},
		},
		{
			name: "every field at once",
			mutate: func(r *domain.RevokeRequest) {
				*r = domain.RevokeRequest{}
			},
			want: []domain.FieldError{
				{Field: "requested_at", Code: domain.CodeRequired},
				{Field: "device", Code: domain.CodeRequired},
				{Field: "confirmation_method", Code: domain.CodeRequired},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := valid
			tc.mutate(&r)
			err := r.Validate(c, now)
			if tc.want == nil {
				assert.NoError(t, err)
				return
			}
			var verr *domain.ValidationError
			require.ErrorAs(t, err, &verr)
			assert.Equal(t, tc.want, verr.Fields)
			assert.Contains(t, verr.Error(), tc.want[0].Field)
		})
	}
}

func TestNormalizedDeviceCollapsesSpaces(t *testing.T) {
	assert.Equal(t, "Google Pixel 8", domain.RevokeRequest{Device: "  Google   Pixel 8 "}.NormalizedDevice())
}

func TestFormatReceiptNumber(t *testing.T) {
	at := time.Date(2026, 10, 24, 15, 0, 0, 0, time.UTC)
	assert.Equal(t, "REV-2026-0088", domain.FormatReceiptNumber(at, 88))
	assert.Equal(t, "REV-2026-0001", domain.FormatReceiptNumber(at, 1))
	assert.Equal(t, "REV-2026-123456", domain.FormatReceiptNumber(at, 123456), "the sequence is never truncated")
	// 2027-01-01 03:00 UTC is still 2026-12-31 in Colombia (UTC-5).
	assert.Equal(t, "REV-2026-0007", domain.FormatReceiptNumber(time.Date(2027, 1, 1, 3, 0, 0, 0, time.UTC), 7))
	assert.Equal(t, "REV-2027-0008", domain.FormatReceiptNumber(time.Date(2027, 1, 1, 5, 0, 0, 0, time.UTC), 8))

	assert.True(t, domain.ValidReceiptNumber("REV-2026-0088"))
	assert.True(t, domain.ValidReceiptNumber("REV-2026-123456"))
	for _, bad := range []string{"", "REV-2026-88", "rev-2026-0088", "REV-26-0088", "REV-2026-0088x"} {
		assert.False(t, domain.ValidReceiptNumber(bad), bad)
	}
	assert.Equal(t, -5*60*60, offset(domain.ReceiptZone()))
}

func offset(loc *time.Location) int {
	_, off := time.Date(2026, 6, 1, 0, 0, 0, 0, loc).Zone()
	return off
}

// revokedEvent is the revoked event of a revocation written by HU-M1.3-02,
// with its entities and their windows in the details.
func revokedEvent(t *testing.T, id, consent string, at time.Time, windows ...domain.EntityWindow) domain.ConsentEvent {
	t.Helper()
	raw, err := json.Marshal(domain.RevokedDetails{RevocationID: "rvk-1", Entities: windows})
	require.NoError(t, err)
	e := withReceipt(event(id, consent, domain.HistoryRevoked, at), "REV-2026-0001")
	e.Channel, e.Device, e.Details = string(domain.RevocationChannelMobileApp), "Pixel 8", raw
	return e
}

func entityStatusEvent(id, consent, entity, status string, at time.Time) domain.ConsentEvent {
	return entityEvent(event(id, consent, status, at), entity)
}

func retryEvent(t *testing.T, id, consent, entity string, at, deadline time.Time) domain.ConsentEvent {
	t.Helper()
	raw, err := json.Marshal(domain.RetryDetails{DeadlineAt: deadline})
	require.NoError(t, err)
	e := entityStatusEvent(id, consent, entity, domain.HistoryRevocationRetryRequested, at)
	e.Details = raw
	return e
}

func TestDeriveTheRevocationAndTheNoticeToEachEntity(t *testing.T) {
	c := holderConsent("cns-1", viewGrantedAt)
	requested := viewGrantedAt.AddDate(0, 3, 0)
	effective := requested.Add(2 * time.Second)
	deadline := effective.Add(domain.RevocationNoticeDeadline)
	bank, other := "aliado-banco-1", "aliado-banco-2"
	revoked := revokedEvent(t, "e2", "cns-1", effective,
		domain.EntityWindow{Code: bank, DeadlineAt: deadline}, domain.EntityWindow{Code: other, DeadlineAt: deadline})
	requestedEvent := event("e1", "cns-1", domain.HistoryRevocationRequested, requested)

	type entityWant struct {
		status      domain.EntityNoticeStatus
		confirmedAt time.Time
		deadline    time.Time
	}
	cases := []struct {
		name        string
		extra       []domain.ConsentEvent
		now         time.Time
		wantStatus  domain.ConsentStatus
		wantSummary domain.NoticeSummary
		wantBank    entityWant
		wantOther   entityWant
	}{
		{
			name: "revoked at once, the entities still have time", now: effective.Add(time.Second),
			wantStatus: domain.StatusRevoked, wantSummary: domain.NoticeInProgress,
			wantBank: entityWant{status: domain.NoticePending, deadline: deadline}, wantOther: entityWant{status: domain.NoticePending, deadline: deadline},
		},
		{
			name: "one entity confirmed", now: effective.Add(10 * time.Second),
			extra:      []domain.ConsentEvent{entityStatusEvent("e3", "cns-1", bank, domain.HistoryRevocationConfirmed, effective.Add(3*time.Second))},
			wantStatus: domain.StatusRevoked, wantSummary: domain.NoticeInProgress,
			wantBank:  entityWant{status: domain.NoticeConfirmed, confirmedAt: effective.Add(3 * time.Second), deadline: deadline},
			wantOther: entityWant{status: domain.NoticePending, deadline: deadline},
		},
		{
			name: "every entity confirmed", now: deadline.Add(time.Hour),
			extra: []domain.ConsentEvent{
				entityStatusEvent("e3", "cns-1", bank, domain.HistoryRevocationConfirmed, effective.Add(3*time.Second)),
				entityStatusEvent("e4", "cns-1", other, domain.HistoryRevocationConfirmed, effective.Add(4*time.Second)),
			},
			wantStatus: domain.StatusRevoked, wantSummary: domain.NoticeCompleted,
			wantBank:  entityWant{status: domain.NoticeConfirmed, confirmedAt: effective.Add(3 * time.Second), deadline: deadline},
			wantOther: entityWant{status: domain.NoticeConfirmed, confirmedAt: effective.Add(4 * time.Second), deadline: deadline},
		},
		{
			name: "partial by event: the worker wrote not confirmed", now: deadline.Add(time.Second),
			extra: []domain.ConsentEvent{
				entityStatusEvent("e3", "cns-1", bank, domain.HistoryRevocationConfirmed, effective.Add(3*time.Second)),
				entityStatusEvent("e4", "cns-1", other, domain.HistoryRevocationNotConfirmed, deadline),
			},
			wantStatus: domain.StatusPartiallyRevoked, wantSummary: domain.NoticePartial,
			wantBank:  entityWant{status: domain.NoticeConfirmed, confirmedAt: effective.Add(3 * time.Second), deadline: deadline},
			wantOther: entityWant{status: domain.NoticeNotConfirmed, deadline: deadline},
		},
		{
			name: "partial by date exactly at the end of the window, without the worker", now: deadline,
			extra:      []domain.ConsentEvent{entityStatusEvent("e3", "cns-1", bank, domain.HistoryRevocationConfirmed, effective.Add(3*time.Second))},
			wantStatus: domain.StatusPartiallyRevoked, wantSummary: domain.NoticePartial,
			wantBank:  entityWant{status: domain.NoticeConfirmed, confirmedAt: effective.Add(3 * time.Second), deadline: deadline},
			wantOther: entityWant{status: domain.NoticeNotConfirmed, deadline: deadline},
		},
		{
			name: "still revoked one instant before the end of the window", now: deadline.Add(-time.Nanosecond),
			wantStatus: domain.StatusRevoked, wantSummary: domain.NoticeInProgress,
			wantBank: entityWant{status: domain.NoticePending, deadline: deadline}, wantOther: entityWant{status: domain.NoticePending, deadline: deadline},
		},
		{
			name: "a retry keeps it partial until the entity confirms", now: deadline.Add(time.Minute),
			extra: []domain.ConsentEvent{
				entityStatusEvent("e3", "cns-1", bank, domain.HistoryRevocationConfirmed, effective.Add(3*time.Second)),
				entityStatusEvent("e4", "cns-1", other, domain.HistoryRevocationNotConfirmed, deadline),
				retryEvent(t, "e5", "cns-1", other, deadline.Add(30*time.Second), deadline.Add(30*time.Second+domain.RevocationNoticeDeadline)),
			},
			wantStatus: domain.StatusPartiallyRevoked, wantSummary: domain.NoticePartial,
			wantBank:  entityWant{status: domain.NoticeConfirmed, confirmedAt: effective.Add(3 * time.Second), deadline: deadline},
			wantOther: entityWant{status: domain.NoticePending, deadline: deadline.Add(30*time.Second + domain.RevocationNoticeDeadline)},
		},
		{
			name: "from partial to revoked after a confirmed retry", now: deadline.Add(2 * time.Minute),
			extra: []domain.ConsentEvent{
				entityStatusEvent("e3", "cns-1", bank, domain.HistoryRevocationConfirmed, effective.Add(3*time.Second)),
				entityStatusEvent("e4", "cns-1", other, domain.HistoryRevocationNotConfirmed, deadline),
				retryEvent(t, "e5", "cns-1", other, deadline.Add(30*time.Second), deadline.Add(30*time.Second+domain.RevocationNoticeDeadline)),
				entityStatusEvent("e6", "cns-1", other, domain.HistoryRevocationConfirmed, deadline.Add(time.Minute)),
			},
			wantStatus: domain.StatusRevoked, wantSummary: domain.NoticeCompleted,
			wantBank:  entityWant{status: domain.NoticeConfirmed, confirmedAt: effective.Add(3 * time.Second), deadline: deadline},
			wantOther: entityWant{status: domain.NoticeConfirmed, confirmedAt: deadline.Add(time.Minute), deadline: deadline.Add(30*time.Second + domain.RevocationNoticeDeadline)},
		},
		{
			name: "a confirmation is final", now: deadline.Add(time.Hour),
			extra: []domain.ConsentEvent{
				entityStatusEvent("e3", "cns-1", bank, domain.HistoryRevocationConfirmed, effective.Add(3*time.Second)),
				entityStatusEvent("e4", "cns-1", bank, domain.HistoryRevocationNotConfirmed, deadline),
				entityStatusEvent("e5", "cns-1", other, domain.HistoryRevocationConfirmed, effective.Add(4*time.Second)),
			},
			wantStatus: domain.StatusRevoked, wantSummary: domain.NoticeCompleted,
			wantBank:  entityWant{status: domain.NoticeConfirmed, confirmedAt: effective.Add(3 * time.Second), deadline: deadline},
			wantOther: entityWant{status: domain.NoticeConfirmed, confirmedAt: effective.Add(4 * time.Second), deadline: deadline},
		},
		{
			name: "an entity event before the revocation is not part of it", now: effective.Add(time.Second),
			extra:      []domain.ConsentEvent{entityStatusEvent("e0", "cns-1", bank, domain.HistoryRevocationConfirmed, requested.Add(-time.Hour))},
			wantStatus: domain.StatusRevoked, wantSummary: domain.NoticeInProgress,
			wantBank: entityWant{status: domain.NoticePending, deadline: deadline}, wantOther: entityWant{status: domain.NoticePending, deadline: deadline},
		},
		{
			name: "a retry with an unreadable deadline changes nothing", now: deadline.Add(time.Minute),
			extra: []domain.ConsentEvent{
				entityStatusEvent("e3", "cns-1", bank, domain.HistoryRevocationConfirmed, effective.Add(3*time.Second)),
				entityStatusEvent("e4", "cns-1", other, domain.HistoryRevocationNotConfirmed, deadline),
				entityStatusEvent("e5", "cns-1", other, domain.HistoryRevocationRetryRequested, deadline.Add(time.Second)),
			},
			wantStatus: domain.StatusPartiallyRevoked, wantSummary: domain.NoticePartial,
			wantBank:  entityWant{status: domain.NoticeConfirmed, confirmedAt: effective.Add(3 * time.Second), deadline: deadline},
			wantOther: entityWant{status: domain.NoticeNotConfirmed, deadline: deadline},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			events := append([]domain.ConsentEvent{revoked, requestedEvent}, tc.extra...)

			status, ended, receipt := domain.DeriveStatus(c, events, tc.now)
			v := domain.View(c, events, tc.now, domain.EntityNames{bank: "Banco Aliado"})

			assert.Equal(t, tc.wantStatus, status)
			assert.Equal(t, effective, ended, "the consent ended when the revocation took effect")
			assert.Equal(t, "REV-2026-0001", receipt)
			assert.Equal(t, tc.wantStatus, v.Status)
			assert.False(t, v.Current)
			assert.False(t, v.Revocable, "criterion 6: a revoked consent cannot be revoked again")
			require.NotNil(t, v.Revocation)
			r := v.Revocation
			assert.Equal(t, tc.wantStatus, r.Status)
			assert.Equal(t, tc.wantSummary, r.Summary)
			assert.Equal(t, "REV-2026-0001", r.ReceiptNumber)
			assert.Equal(t, requested, r.RequestedAt)
			assert.Equal(t, effective, r.EffectiveAt)
			assert.Equal(t, "mobile_app", r.Channel)
			assert.Equal(t, "Pixel 8", r.Device)
			require.Len(t, r.Entities, 2)
			assert.Equal(t, domain.Entity{Code: bank, Name: "Banco Aliado"}, r.Entities[0].Entity)
			assert.Equal(t, domain.Entity{Code: other, Name: other}, r.Entities[1].Entity)
			for i, want := range []entityWant{tc.wantBank, tc.wantOther} {
				got := r.Entities[i]
				assert.Equal(t, want.status, got.Status, "entity %d", i)
				assert.True(t, want.confirmedAt.Equal(got.ConfirmedAt), "entity %d confirmed at %s, want %s", i, got.ConfirmedAt, want.confirmedAt)
				assert.True(t, want.deadline.Equal(got.DeadlineAt), "entity %d deadline %s, want %s", i, got.DeadlineAt, want.deadline)
			}
		})
	}
}

func TestARevokedEventWithoutEntitiesHasNothingToNotify(t *testing.T) {
	c := holderConsent("cns-1", viewGrantedAt)
	at := viewGrantedAt.AddDate(0, 1, 0)
	seeded := withReceipt(event("e1", "cns-1", domain.HistoryRevoked, at), "REV-0001")
	unreadable := seeded
	unreadable.Details = []byte(`{not json`)

	for _, e := range []domain.ConsentEvent{seeded, unreadable} {
		v := domain.View(c, []domain.ConsentEvent{e}, at.AddDate(1, 0, 0), nil)

		assert.Equal(t, domain.StatusRevoked, v.Status)
		require.NotNil(t, v.Revocation)
		assert.Equal(t, domain.NoticeCompleted, v.Revocation.Summary)
		assert.Empty(t, v.Revocation.Entities)
		assert.Equal(t, at, v.Revocation.RequestedAt, "without a request event, it was requested when it took effect")
	}
}

func TestAnActiveOrExpiredConsentHasNoRevocation(t *testing.T) {
	c := holderConsent("cns-1", viewGrantedAt)

	active := domain.View(c, nil, viewGrantedAt.Add(time.Hour), nil)
	expired := domain.View(c, nil, c.ExpiresAt(), nil)

	assert.True(t, active.Revocable)
	assert.Nil(t, active.Revocation)
	assert.False(t, expired.Revocable)
	assert.Nil(t, expired.Revocation)
}

func TestAPartiallyRevokedLastEventIsNotTakenAsTheStatus(t *testing.T) {
	c := holderConsent("cns-1", viewGrantedAt)
	at := viewGrantedAt.AddDate(0, 1, 0)

	status, _, _ := domain.DeriveStatus(c, []domain.ConsentEvent{event("e1", "cns-1", string(domain.StatusPartiallyRevoked), at)}, at.Add(time.Hour))

	assert.Equal(t, domain.StatusActive, status, "only a revoked event revokes the consent")
}

func TestHistoryShowsTheRevocationAndTheEntityEvents(t *testing.T) {
	c := holderConsent("cns-1", viewGrantedAt)
	requested := viewGrantedAt.AddDate(0, 3, 0)
	effective := requested.Add(2 * time.Second)
	events := []domain.ConsentEvent{
		entityStatusEvent("e3", "cns-1", "aliado-banco-1", domain.HistoryRevocationConfirmed, effective.Add(3*time.Second)),
		revokedEvent(t, "e2", "cns-1", effective, domain.EntityWindow{Code: "aliado-banco-1", DeadlineAt: effective.Add(time.Minute)}),
		event("e1", "cns-1", domain.HistoryRevocationRequested, requested),
	}

	v := domain.View(c, events, effective.Add(time.Hour), nil)

	got := make([]string, 0, len(v.History))
	for _, h := range v.History {
		got = append(got, h.Status+"|"+h.EntityCode)
	}
	assert.Equal(t, []string{"granted|", "revocation_requested|", "revoked|", "revocation_confirmed|aliado-banco-1"}, got)
}

func TestCheckRevocable(t *testing.T) {
	cases := map[domain.ConsentStatus]error{
		domain.StatusActive:            nil,
		domain.StatusRevoked:           domain.ErrAlreadyRevoked,
		domain.StatusPartiallyRevoked:  domain.ErrAlreadyRevoked,
		domain.StatusExpired:           domain.ErrConsentNotActive,
		domain.StatusRevocationPending: domain.ErrConsentNotActive,
	}
	for status, want := range cases {
		err := domain.CheckRevocable(domain.ConsentView{Status: status})
		if want == nil {
			assert.NoError(t, err, status)
			continue
		}
		assert.ErrorIs(t, err, want, status)
	}
	assert.True(t, domain.StatusActive.Revocable())
	for _, s := range []domain.ConsentStatus{domain.StatusExpired, domain.StatusRevoked, domain.StatusPartiallyRevoked, domain.StatusRevocationPending} {
		assert.False(t, s.Revocable(), s)
	}
}
