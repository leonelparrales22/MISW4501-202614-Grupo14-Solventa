package fcm_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pkgfcm "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/fcm"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/adapters/fcm"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/domain"
)

// HU-M3.3-01: the push through FCM is a data message that tells the app what
// to show and what to open.

type fakeClient struct {
	got pkgfcm.Message
	err error
}

func (f *fakeClient) Send(_ context.Context, m pkgfcm.Message) (pkgfcm.Receipt, error) {
	f.got = m
	return pkgfcm.Receipt{ID: "projects/solventa-94126/messages/0:1"}, f.err
}

var at = time.Date(2026, 9, 5, 15, 2, 0, 0, time.UTC)

func device() domain.Device {
	return domain.Device{InstallationID: "inst-1", Token: "tok-fcm-1", Locale: domain.LocaleSpanish}
}

func TestAChangeOfStageTellsWhichClaimToOpen(t *testing.T) {
	client := &fakeClient{}
	c := domain.ClaimStageChange{ChangeID: 42, ClaimID: "cl-1", FilingNumber: "SIN-2026-01655", Subject: "sub-a",
		Stage: domain.StageInEvaluation, ChangedAt: at}
	d := domain.NewDelivery(domain.NewStageNotification(c, at), device(), at)

	out, err := fcm.NewSender(client).Send(context.Background(), d)

	require.NoError(t, err)
	assert.Equal(t, "projects/solventa-94126/messages/0:1", out.Reference)
	assert.Equal(t, pkgfcm.Message{Token: "tok-fcm-1", CollapseKey: "ntf-stage-42", Data: map[string]string{
		fcm.DataNotificationID: "ntf-stage-42", fcm.DataKind: domain.KindClaimStageChanged, fcm.DataClaimID: "cl-1",
	}}, client.got)
}

func TestAPayoutTellsItsNotificationAndClaim(t *testing.T) {
	client := &fakeClient{}
	p := domain.ParametricPayout{PayoutID: "ppo-1", ClaimID: "SIN-2026-01702", Subject: "sub-a", AmountMinor: 1,
		Currency: "COP", Trigger: domain.Trigger{Parameter: "flight_delay", EventReference: "AV-8421"}, PaidAt: at}
	d := domain.NewDelivery(domain.NewPayoutNotification(p, at), device(), at)

	_, err := fcm.NewSender(client).Send(context.Background(), d)

	require.NoError(t, err)
	assert.Equal(t, map[string]string{fcm.DataNotificationID: "ntf-ppo-1", fcm.DataKind: domain.KindParametricPayout,
		fcm.DataClaimID: "SIN-2026-01702"}, client.got.Data)
}

func TestTheErrorOfFCMKeepsItsClass(t *testing.T) {
	c := domain.ClaimStageChange{ChangeID: 42, ClaimID: "cl-1", FilingNumber: "SIN-2026-01655", Subject: "sub-a",
		Stage: domain.StageInEvaluation, ChangedAt: at}
	d := domain.NewDelivery(domain.NewStageNotification(c, at), device(), at)

	_, err := fcm.NewSender(&fakeClient{err: pkgfcm.ErrRejected}).Send(context.Background(), d)

	assert.ErrorIs(t, err, idempotency.ErrRejected)
}

// HU-M3.2-01: an expiry alert tells which policy to open.
func TestAnExpiryAlertTellsWhichPolicyToOpen(t *testing.T) {
	client := &fakeClient{}
	e := domain.ExpiryAlert{PolicyID: "AU-2025-118204", Subject: "sub-a", Source: domain.ExpirySourcePolicyCore,
		Line: "auto", CoverageEnd: time.Date(2026, 11, 30, 5, 0, 0, 0, time.UTC), MilestoneDays: 30, DaysLeft: 16,
		DetectedAt: at}
	d := domain.NewDelivery(domain.NewExpiryNotification(e, at), device(), at)

	_, err := fcm.NewSender(client).Send(context.Background(), d)

	require.NoError(t, err)
	assert.Equal(t, pkgfcm.Message{Token: "tok-fcm-1", CollapseKey: "ntf-expiry-AU-2025-118204-20261130-30",
		Data: map[string]string{fcm.DataNotificationID: "ntf-expiry-AU-2025-118204-20261130-30",
			fcm.DataKind: domain.KindPolicyExpiry, fcm.DataPolicyID: "AU-2025-118204"}}, client.got)
}
