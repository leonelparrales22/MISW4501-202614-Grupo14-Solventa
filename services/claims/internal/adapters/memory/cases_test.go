package memory_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// The in-memory store of both channels (HU-W2.1-01) behaves like the
// PostgreSQL one: one numbering and one set of filing keys for the mobile and
// the web filings, and a support attached to one claim only.

func support(id, owner string) domain.SupportFile {
	return domain.SupportFile{ID: id, OwnerSubject: owner, FileName: id + ".pdf", Format: domain.FormatPDF, ByteSize: 10}
}

func webRecord(id, subject, key string, supports ...domain.SupportFile) domain.ClaimRecord {
	policy := domain.Policy{ID: "AU-2025-118204", Line: domain.LineAuto, CoveredIncidents: []domain.IncidentType{"collision"}}
	c := domain.ClaimRecord{Claim: claim(id, subject, key), Channel: domain.ChannelWeb, Policy: &policy,
		CatalogVersion: domain.CatalogVersionV1}
	for i, s := range supports {
		c.Supports = append(c.Supports, domain.AttachedSupport{RequirementCode: "traffic_report", Position: i, File: s})
	}
	return c
}

func TestBothChannelsShareTheNumbering(t *testing.T) {
	s := memory.NewCaseStore()

	m, created, err := s.Create(context.Background(), claim("c1", "sub-1", "draft-1"))
	require.NoError(t, err)
	assert.True(t, created)
	w, created, err := s.CreateWeb(context.Background(), webRecord("c2", "sub-1", "form-1"))
	require.NoError(t, err)
	assert.True(t, created)

	assert.Equal(t, "SIN-2026-00001", m.FilingNumber)
	assert.Equal(t, "SIN-2026-00002", w.FilingNumber)
	assert.Equal(t, domain.ChannelWeb, w.Channel)
}

func TestARepeatedFilingKeyAcrossChannels(t *testing.T) {
	s := memory.NewCaseStore()
	_, _, err := s.Create(context.Background(), claim("c1", "sub-1", "key"))
	require.NoError(t, err)
	_, _, err = s.CreateWeb(context.Background(), webRecord("c2", "sub-2", "web-key"))
	require.NoError(t, err)

	_, _, err = s.CreateWeb(context.Background(), webRecord("c3", "sub-1", "key"))
	assert.ErrorIs(t, err, ports.ErrFilingKeyReused, "a mobile key is not reused on the web")
	_, _, err = s.Create(context.Background(), claim("c4", "sub-2", "web-key"))
	assert.ErrorIs(t, err, ports.ErrFilingKeyReused, "nor a web key on the mobile app")

	again, created, err := s.Create(context.Background(), claim("c5", "sub-1", "key"))
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, "c1", again.ID)

	sameWeb, created, err := s.CreateWeb(context.Background(), webRecord("c6", "sub-2", "web-key"))
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, "c2", sameWeb.ID)

	other := webRecord("c7", "sub-2", "web-key")
	other.Place = "Otro lugar"
	_, _, err = s.CreateWeb(context.Background(), other)
	assert.ErrorIs(t, err, ports.ErrFilingKeyReused)
}

func TestASupportIsAttachedToOneClaimOnly(t *testing.T) {
	s := memory.NewCaseStore()
	sup := support("sup-1", "sub-1")
	require.NoError(t, s.Save(context.Background(), sup, []byte("%PDF-")))

	pending, err := s.CountPending(context.Background(), "sub-1")
	require.NoError(t, err)
	assert.Equal(t, 1, pending)

	_, _, err = s.CreateWeb(context.Background(), webRecord("c1", "sub-1", "form-1", sup))
	require.NoError(t, err)
	_, _, err = s.CreateWeb(context.Background(), webRecord("c2", "sub-1", "form-2", sup))
	assert.ErrorIs(t, err, ports.ErrSupportAlreadyAttached)

	pending, err = s.CountPending(context.Background(), "sub-1")
	require.NoError(t, err)
	assert.Zero(t, pending)
	_, err = s.FindByKey(context.Background(), "sub-1", "form-2")
	assert.ErrorIs(t, err, ports.ErrClaimNotFound, "nothing of the refused filing is kept")
}

func TestFindReturnsOnlyTheSupportsThatExist(t *testing.T) {
	s := memory.NewCaseStore()
	require.NoError(t, s.Save(context.Background(), support("sup-1", "sub-1"), nil))
	require.NoError(t, s.Save(context.Background(), support("sup-2", "sub-2"), nil))

	found, err := s.Find(context.Background(), []string{"sup-1", "sup-2", "sup-3"})

	require.NoError(t, err)
	assert.Len(t, found, 2)
	assert.Equal(t, "sub-2", found["sup-2"].OwnerSubject)
}

func TestGetReturnsTheClaimWithItsSupports(t *testing.T) {
	s := memory.NewCaseStore()
	_, _, err := s.CreateWeb(context.Background(), webRecord("c1", "sub-1", "form-1", support("sup-1", "sub-1")))
	require.NoError(t, err)

	got, err := s.Get(context.Background(), "c1")
	require.NoError(t, err)
	require.Len(t, got.Supports, 1)
	require.NotNil(t, got.Policy)

	got.Policy.CoveredIncidents[0] = "theft"
	got.Supports[0].File.ID = "changed"
	again, err := s.Get(context.Background(), "c1")
	require.NoError(t, err)
	assert.Equal(t, domain.IncidentType("collision"), again.Policy.CoveredIncidents[0], "the store keeps its own copy")
	assert.Equal(t, "sup-1", again.Supports[0].File.ID)

	_, err = s.Get(context.Background(), "missing")
	assert.ErrorIs(t, err, ports.ErrClaimNotFound)
}

func TestTheListIsNewestFirstFilteredAndPaged(t *testing.T) {
	s := memory.NewCaseStore()
	for i := 0; i < 4; i++ {
		c := claim(fmt.Sprintf("c%d", i), "sub-1", fmt.Sprintf("draft-%d", i))
		c.FiledAt = filedAt.Add(time.Duration(i) * time.Hour)
		if i == 3 {
			c.PolicyID = "HG-2025-880231"
		}
		_, _, err := s.Create(context.Background(), c)
		require.NoError(t, err)
	}
	w := webRecord("w1", "sub-1", "form-1", support("sup-1", "sub-1"))
	w.FiledAt = filedAt.Add(-time.Hour)
	_, _, err := s.CreateWeb(context.Background(), w)
	require.NoError(t, err)
	_, _, err = s.Create(context.Background(), claim("x", "sub-2", "draft-x"))
	require.NoError(t, err)

	all, err := s.ListByOwner(context.Background(), "sub-1", ports.ClaimFilter{}, nil, 10)
	require.NoError(t, err)
	var ids []string
	for _, c := range all {
		ids = append(ids, c.ID)
		assert.Empty(t, c.Supports, "the list does not load the supports")
	}
	assert.Equal(t, []string{"c3", "c2", "c1", "c0", "w1"}, ids)

	page, err := s.ListByOwner(context.Background(), "sub-1", ports.ClaimFilter{PolicyID: "AU-2025-118204"},
		&ports.ClaimCursor{FiledAt: all[1].FiledAt, ID: all[1].ID}, 2)
	require.NoError(t, err)
	require.Len(t, page, 2)
	assert.Equal(t, "c1", page[0].ID)
	assert.Equal(t, "c0", page[1].ID)

	recent, err := s.ListByOwner(context.Background(), "sub-1",
		ports.ClaimFilter{Status: domain.StatusActive, FiledFrom: filedAt.Add(2 * time.Hour)}, nil, 10)
	require.NoError(t, err)
	assert.Len(t, recent, 2)
}

func TestTheListBreaksTiesByID(t *testing.T) {
	s := memory.NewCaseStore()
	for _, id := range []string{"a", "c", "b"} {
		_, _, err := s.Create(context.Background(), claim(id, "sub-1", "draft-"+id))
		require.NoError(t, err)
	}

	first, err := s.ListByOwner(context.Background(), "sub-1", ports.ClaimFilter{}, nil, 2)
	require.NoError(t, err)
	rest, err := s.ListByOwner(context.Background(), "sub-1", ports.ClaimFilter{},
		&ports.ClaimCursor{FiledAt: first[1].FiledAt, ID: first[1].ID}, 2)
	require.NoError(t, err)

	assert.Equal(t, []string{"c", "b"}, []string{first[0].ID, first[1].ID})
	require.Len(t, rest, 1)
	assert.Equal(t, "a", rest[0].ID)
}
