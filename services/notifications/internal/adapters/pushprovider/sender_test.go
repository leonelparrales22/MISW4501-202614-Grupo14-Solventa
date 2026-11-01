package pushprovider_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/push"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/adapters/pushprovider"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/domain"
)

type fakeClient struct {
	got push.Message
	err error
}

func (f *fakeClient) Send(_ context.Context, m push.Message) (push.Receipt, error) {
	f.got = m
	return push.Receipt{ID: "push-7"}, f.err
}

func delivery(locale string) domain.Delivery {
	p := domain.ParametricPayout{PayoutID: "ppo-1", ClaimID: "SIN-2026-01702", Subject: "sub-a", AmountMinor: 18_000_000,
		Currency: "COP", Trigger: domain.Trigger{Parameter: "flight_delay", EventReference: "AV-8421", Observed: 215},
		PaidAt: time.Date(2026, 9, 11, 21, 40, 0, 0, time.UTC)}
	n := domain.NewPayoutNotification(p, p.PaidAt)
	return domain.NewDelivery(n, domain.Device{InstallationID: "inst-1", Token: "tok-1", Locale: locale}, p.PaidAt)
}

func TestThePushTellsThePaymentAndWhatToOpen(t *testing.T) {
	client := &fakeClient{}

	out, err := pushprovider.NewSender(client).Send(context.Background(), delivery(domain.LocaleSpanish))

	require.NoError(t, err)
	assert.Equal(t, "push-7", out.Reference)
	m := client.got
	assert.Equal(t, "tok-1", m.Token)
	assert.Equal(t, "ntf-ppo-1:inst-1", m.IdempotencyKey)
	assert.Equal(t, domain.GroupParametricPayouts, m.Group)
	assert.Equal(t, "Pago automático recibido: $ 180.000 COP", m.Title)
	assert.Contains(t, m.Body, "AV-8421")
	assert.Equal(t, map[string]string{"notification_id": "ntf-ppo-1", "kind": domain.KindParametricPayout,
		"claim_id": "SIN-2026-01702"}, m.Data)
}

func TestThePushIsWrittenInTheLanguageOfTheDevice(t *testing.T) {
	client := &fakeClient{}

	_, err := pushprovider.NewSender(client).Send(context.Background(), delivery(domain.LocaleEnglish))

	require.NoError(t, err)
	assert.Equal(t, "Automatic payment received: $ 180,000 COP", client.got.Title)
}

func TestTheClientErrorKeepsItsClassification(t *testing.T) {
	_, err := pushprovider.NewSender(&fakeClient{err: push.ErrAmbiguous}).Send(context.Background(), delivery("es"))

	require.ErrorIs(t, err, idempotency.ErrAmbiguous)
}
