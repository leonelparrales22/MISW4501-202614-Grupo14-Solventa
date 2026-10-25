package consents_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/consents"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/ports"
)

// memEvents is a test double of the consent event reader.
type memEvents struct {
	rows []domain.ConsentEvent
	err  error
}

func (m *memEvents) ListByConsents(_ context.Context, ids []string) (map[string][]domain.ConsentEvent, error) {
	if m.err != nil {
		return nil, m.err
	}
	out := map[string][]domain.ConsentEvent{}
	for _, e := range m.rows {
		for _, id := range ids {
			if e.ConsentID == id {
				out[id] = append(out[id], e)
			}
		}
	}
	return out, nil
}

// memLinks is a test double of the holder link store.
type memLinks struct {
	mu   sync.Mutex
	rows map[string]domain.HolderLink
	err  error
}

func newMemLinks() *memLinks { return &memLinks{rows: map[string]domain.HolderLink{}} }

func (m *memLinks) Link(_ context.Context, l domain.HolderLink) (domain.HolderLink, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return domain.HolderLink{}, false, m.err
	}
	if stored, ok := m.rows[l.Subject]; ok {
		return stored, false, nil
	}
	m.rows[l.Subject] = l
	return l, true, nil
}

func (m *memLinks) DocumentOf(_ context.Context, subject string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return "", m.err
	}
	l, ok := m.rows[subject]
	if !ok {
		return "", ports.ErrHolderNotLinked
	}
	return l.DocumentID, nil
}

var holderNow = time.Date(2026, 10, 24, 20, 0, 0, 0, time.UTC)

func holderConsent(id, document string, grantedAt time.Time) domain.Consent {
	terms := domain.TermsV1()
	return domain.Consent{
		ID: id, CustomerID: "cust-" + id, Holder: domain.Holder{DocumentID: document, FullName: "Ana Pérez"},
		TermsVersion: terms.Version, Scope: terms.Sources, Purpose: terms.Purpose, Channel: domain.ChannelEmbedded,
		PartnerID: "aliado-banco-1", ApplicationID: "app-" + id, GrantedAt: grantedAt,
	}
}

func link(subject, document string) domain.HolderLink {
	return domain.HolderLink{Subject: subject, DocumentID: document, CustomerID: "cus-" + subject, LinkedAt: holderNow,
		EventID: "customer-linked-cus-" + subject}
}

func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func TestLinkIsIdempotentAndNormalizesTheDocument(t *testing.T) {
	links := newMemLinks()
	svc := consents.NewHolderService(&memStore{}, &memEvents{}, links, &fakeClock{now: holderNow}, nil)
	ctx := context.Background()

	require.NoError(t, svc.Link(ctx, link("sub-a", " ab12345 ")))
	require.NoError(t, svc.Link(ctx, link("sub-a", "AB12345")))

	doc, err := svc.Resolve(ctx, "sub-a")
	require.NoError(t, err)
	assert.Equal(t, "AB12345", doc)
	assert.Len(t, links.rows, 1)
}

func TestLinkKeepsTheFirstDocumentOnAConflictAndLogsIt(t *testing.T) {
	logs := captureSlog(t)
	links := newMemLinks()
	svc := consents.NewHolderService(&memStore{}, &memEvents{}, links, &fakeClock{now: holderNow}, nil)
	ctx := context.Background()
	require.NoError(t, svc.Link(ctx, link("sub-a", "1020304050")))

	other := link("sub-a", "9999999999")
	other.EventID = "customer-linked-cus-other"
	require.NoError(t, svc.Link(ctx, other), "no error, so the message is not retried forever")

	doc, err := svc.Resolve(ctx, "sub-a")
	require.NoError(t, err)
	assert.Equal(t, "1020304050", doc)
	assert.Contains(t, logs.String(), "holder link conflict")
	assert.Contains(t, logs.String(), "customer-linked-cus-other")
	assert.NotContains(t, logs.String(), "9999999999", "the document is never logged")
}

func TestLinkRefusesAnInvalidLinkAndReportsStoreErrors(t *testing.T) {
	links := newMemLinks()
	svc := consents.NewHolderService(&memStore{}, &memEvents{}, links, &fakeClock{now: holderNow}, nil)

	err := svc.Link(context.Background(), domain.HolderLink{Subject: "sub-a"})
	assert.ErrorIs(t, err, domain.ErrInvalidHolderLink)

	links.err = errors.New("db down")
	assert.ErrorContains(t, svc.Link(context.Background(), link("sub-a", "1020304050")), "db down")
}

func TestResolveWithoutALinkIsErrHolderNotLinked(t *testing.T) {
	links := newMemLinks()
	svc := consents.NewHolderService(&memStore{}, &memEvents{}, links, &fakeClock{now: holderNow}, nil)

	_, err := svc.Resolve(context.Background(), "sub-unknown")
	assert.ErrorIs(t, err, ports.ErrHolderNotLinked)

	links.err = errors.New("db down")
	_, err = svc.Resolve(context.Background(), "sub-a")
	assert.ErrorContains(t, err, "db down")
	assert.NotErrorIs(t, err, ports.ErrHolderNotLinked)
}

func TestListReturnsTheHolderConsentsOrderedWithTheirStatus(t *testing.T) {
	store := &memStore{rows: []domain.Consent{
		holderConsent("cns-old", "1020304050", holderNow.AddDate(-2, 0, 0)),
		holderConsent("cns-new", "1020304050", holderNow.AddDate(0, -1, 0)),
		holderConsent("cns-revoked", "1020304050", holderNow.AddDate(0, -3, 0)),
		holderConsent("cns-other", "9988776655", holderNow.AddDate(0, -1, 0)),
	}}
	events := &memEvents{rows: []domain.ConsentEvent{{
		EventID: "e1", ConsentID: "cns-revoked", Status: domain.HistoryRevoked, OccurredAt: holderNow.AddDate(0, 0, -2),
		RecordedAt: holderNow.AddDate(0, 0, -2), ReceiptID: "REV-0001",
	}}}
	svc := consents.NewHolderService(store, events, newMemLinks(), &fakeClock{now: holderNow},
		domain.EntityNames{"aliado-banco-1": "Banco Aliado"})

	views, err := svc.List(context.Background(), "1020304050")

	require.NoError(t, err)
	require.Len(t, views, 3, "never another holder's consent")
	assert.Equal(t, "cns-new", views[0].Consent.ID)
	assert.Equal(t, domain.StatusActive, views[0].Status)
	assert.Equal(t, "cns-revoked", views[1].Consent.ID)
	assert.Equal(t, "REV-0001", views[1].RevocationReceiptID)
	assert.Equal(t, "cns-old", views[2].Consent.ID)
	assert.Equal(t, domain.StatusExpired, views[2].Status)
	assert.Equal(t, "Banco Aliado", views[0].Entities[0].Name)
}

func TestListReportsStoreErrors(t *testing.T) {
	svc := consents.NewHolderService(&memStore{err: errors.New("db down")}, &memEvents{}, newMemLinks(), &fakeClock{now: holderNow}, nil)
	_, err := svc.List(context.Background(), "1020304050")
	assert.ErrorContains(t, err, "db down")

	store := &memStore{rows: []domain.Consent{holderConsent("cns-1", "1020304050", holderNow)}}
	svc = consents.NewHolderService(store, &memEvents{err: errors.New("events down")}, newMemLinks(), &fakeClock{now: holderNow}, nil)
	_, err = svc.List(context.Background(), "1020304050")
	assert.ErrorContains(t, err, "events down")
}

func TestGetReturnsTheHolderConsentWithItsHistory(t *testing.T) {
	store := &memStore{rows: []domain.Consent{holderConsent("cns-1", "ab12345", holderNow.AddDate(0, -1, 0))}}
	svc := consents.NewHolderService(store, &memEvents{}, newMemLinks(), &fakeClock{now: holderNow}, nil)

	v, err := svc.Get(context.Background(), "AB12345", "cns-1")

	require.NoError(t, err)
	assert.Equal(t, "cns-1", v.Consent.ID)
	assert.Equal(t, domain.StatusActive, v.Status)
	require.Len(t, v.History, 1)
	assert.Equal(t, domain.HistoryGranted, v.History[0].Status)
}

func TestGetOfAForeignConsentIsNotFound(t *testing.T) {
	store := &memStore{rows: []domain.Consent{holderConsent("cns-of-a", "1020304050", holderNow)}}
	svc := consents.NewHolderService(store, &memEvents{}, newMemLinks(), &fakeClock{now: holderNow}, nil)

	_, err := svc.Get(context.Background(), "9988776655", "cns-of-a")
	assert.ErrorIs(t, err, ports.ErrConsentNotFound, "another holder's consent looks like a missing one")

	_, err = svc.Get(context.Background(), "9988776655", "cns-none")
	assert.ErrorIs(t, err, ports.ErrConsentNotFound)
}

func TestGetReportsStoreErrors(t *testing.T) {
	svc := consents.NewHolderService(&memStore{err: errors.New("db down")}, &memEvents{}, newMemLinks(), &fakeClock{now: holderNow}, nil)
	_, err := svc.Get(context.Background(), "1020304050", "cns-1")
	assert.ErrorContains(t, err, "db down")
	assert.NotErrorIs(t, err, ports.ErrConsentNotFound)

	store := &memStore{rows: []domain.Consent{holderConsent("cns-1", "1020304050", holderNow)}}
	svc = consents.NewHolderService(store, &memEvents{err: errors.New("events down")}, newMemLinks(), &fakeClock{now: holderNow}, nil)
	_, err = svc.Get(context.Background(), "1020304050", "cns-1")
	assert.ErrorContains(t, err, "events down")
}
