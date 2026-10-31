package postgres_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/migrations"
)

// The web filing in PostgreSQL (HU-W2.1-01): the claims of both channels in
// one table, the uploaded supports and their attachment to one claim only.

// requireDocker fails the test when REQUIRE_DOCKER=1 and Docker is not
// healthy, and skips it otherwise: the certification of the story runs with
// REQUIRE_DOCKER=1, so nothing is skipped silently.
func requireDocker(t *testing.T) {
	t.Helper()
	provider, err := testcontainers.NewDockerProvider()
	if err == nil {
		defer func() { _ = provider.Close() }()
		err = provider.Health(context.Background())
	}
	if err == nil {
		return
	}
	if os.Getenv("REQUIRE_DOCKER") == "1" {
		t.Fatalf("Docker is required (REQUIRE_DOCKER=1) but is not healthy: %v", err)
	}
	t.Skipf("Docker is not available: %v", err)
}

type webStores struct {
	db       *sql.DB
	claims   *postgres.ClaimRecordStore
	supports *postgres.SupportStore
}

func newWebStores(t *testing.T) webStores {
	t.Helper()
	requireDocker(t)
	db := newDB(t)
	return webStores{db: db, claims: postgres.NewClaimRecordStore(db), supports: postgres.NewSupportStore(db)}
}

var uploadedAt = time.Date(2026, 10, 23, 14, 0, 0, 0, time.UTC)

func (s webStores) upload(t *testing.T, owner string, n int) []domain.SupportFile {
	t.Helper()
	out := make([]domain.SupportFile, 0, n)
	for i := 0; i < n; i++ {
		f := domain.SupportFile{ID: "sup-" + randomHex(t, 16), OwnerSubject: owner, FileName: fmt.Sprintf("foto %d.jpg", i),
			Format: domain.FormatJPEG, ByteSize: 4, SHA256: strings.Repeat("c", 64), UploadedAt: uploadedAt}
		require.NoError(t, s.supports.Save(context.Background(), f))
		confirmed, err := s.supports.Confirm(context.Background(), f.ID, uploadedAt.Add(time.Minute))
		require.NoError(t, err)
		out = append(out, confirmed)
	}
	return out
}

func webClaim(id, subject, key string, files ...domain.SupportFile) domain.ClaimRecord {
	policy := domain.Policy{
		ID: "AU-2025-118204", Line: domain.LineAuto, Product: "Autos", InsuredObject: "KJT-482", Status: domain.PolicyActive,
		ValidFrom: time.Date(2025, 11, 1, 5, 0, 0, 0, time.UTC), ValidTo: time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC),
		CoveredIncidents: []domain.IncidentType{domain.IncidentCollision, domain.IncidentGlass},
	}
	c := domain.ClaimRecord{Claim: claim(id, subject, key), Channel: domain.ChannelWeb, Policy: &policy,
		CatalogVersion: domain.CatalogVersionV1}
	c.Details = map[string]string{}
	for i, f := range files {
		c.Supports = append(c.Supports, domain.AttachedSupport{RequirementCode: "vehicle_photos", Position: i, File: f})
	}
	return c
}

func TestAWebClaimIsStoredWithItsPolicyCopyAndSupports(t *testing.T) {
	s := newWebStores(t)
	ctx := context.Background()
	files := s.upload(t, "sub-1", 4)
	want := webClaim("8f14e45f-ceea-467a-9f3a-5a2d0b1c7e90", "sub-1", "form-1", files...)

	stored, created, err := s.claims.CreateWeb(ctx, want)
	require.NoError(t, err)
	assert.True(t, created)
	assert.Regexp(t, `^SIN-2026-\d{5}$`, stored.FilingNumber)

	got, err := s.claims.Get(ctx, want.ID)
	require.NoError(t, err)
	assert.Equal(t, stored.FilingNumber, got.FilingNumber)
	assert.Equal(t, domain.ChannelWeb, got.Channel)
	assert.Equal(t, domain.CatalogVersionV1, got.CatalogVersion)
	assert.Equal(t, want.Policy, got.Policy)
	assert.Equal(t, want.OccurredAt, got.OccurredAt)
	assert.Equal(t, filedAt, got.FiledAt)
	require.Len(t, got.Supports, 4)
	for i, a := range got.Supports {
		assert.Equal(t, i, a.Position)
		assert.Equal(t, "vehicle_photos", a.RequirementCode)
		assert.Equal(t, files[i], a.File)
	}

	byKey, err := s.claims.FindByKey(ctx, "sub-1", "form-1")
	require.NoError(t, err)
	assert.Equal(t, got, byKey)

	_, err = s.claims.Get(ctx, "missing")
	assert.ErrorIs(t, err, ports.ErrClaimNotFound)
	_, err = s.claims.FindByKey(ctx, "sub-2", "form-1")
	assert.ErrorIs(t, err, ports.ErrClaimNotFound, "the key is per insured")
}

func TestTheSameWebFilingKeyReturnsTheClaimWithoutANewNumber(t *testing.T) {
	s := newWebStores(t)
	ctx := context.Background()
	c := webClaim("c1", "sub-1", "form-1", s.upload(t, "sub-1", 4)...)
	first, _, err := s.claims.CreateWeb(ctx, c)
	require.NoError(t, err)

	retry := c
	retry.ID = "c2"
	again, created, err := s.claims.CreateWeb(ctx, retry)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, first.FilingNumber, again.FilingNumber)
	assert.Equal(t, "c1", again.ID)

	other := c
	other.ID, other.Place = "c3", "Otro lugar de los hechos"
	_, _, err = s.claims.CreateWeb(ctx, other)
	assert.ErrorIs(t, err, ports.ErrFilingKeyReused)
}

func TestTheFilingKeysAreSharedWithTheMobileFiling(t *testing.T) {
	s := newWebStores(t)
	ctx := context.Background()
	_, _, err := s.claims.Create(ctx, claim("m1", "sub-1", "key-1"))
	require.NoError(t, err)
	_, _, err = s.claims.CreateWeb(ctx, webClaim("w1", "sub-1", "key-2"))
	require.NoError(t, err)

	_, _, err = s.claims.CreateWeb(ctx, webClaim("w2", "sub-1", "key-1"))
	assert.ErrorIs(t, err, ports.ErrFilingKeyReused)
	_, _, err = s.claims.Create(ctx, claim("m2", "sub-1", "key-2"))
	assert.ErrorIs(t, err, ports.ErrFilingKeyReused)
}

func TestTwoFilingsRaceForTheSameSupportAndOnlyOneWins(t *testing.T) {
	s := newWebStores(t)
	ctx := context.Background()
	files := s.upload(t, "sub-1", 4)

	const racers = 6
	var wg sync.WaitGroup
	errs := make([]error, racers)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c := webClaim(fmt.Sprintf("c%d", i), "sub-1", fmt.Sprintf("form-%d", i), files...)
			c.Description = fmt.Sprintf("Radicación número %d del mismo hecho.", i)
			_, _, errs[i] = s.claims.CreateWeb(ctx, c)
		}(i)
	}
	wg.Wait()

	won := 0
	for _, err := range errs {
		if err == nil {
			won++
			continue
		}
		assert.ErrorIs(t, err, ports.ErrSupportAlreadyAttached)
	}
	assert.Equal(t, 1, won)

	var claims, attached int
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM claim`).Scan(&claims))
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM claim_support`).Scan(&attached))
	assert.Equal(t, 1, claims, "a refused filing leaves no claim behind")
	assert.Equal(t, 4, attached)
}

func TestConcurrentRetriesOfOneWebFilingCreateOneClaim(t *testing.T) {
	s := newWebStores(t)
	ctx := context.Background()
	c := webClaim("c1", "sub-1", "form-1", s.upload(t, "sub-1", 4)...)

	const retries = 6
	var wg sync.WaitGroup
	created := make([]bool, retries)
	numbers := make([]string, retries)
	for i := 0; i < retries; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			retry := c
			retry.ID = fmt.Sprintf("c%d", i)
			got, isNew, err := s.claims.CreateWeb(ctx, retry)
			assert.NoError(t, err)
			created[i], numbers[i] = isNew, got.FilingNumber
		}(i)
	}
	wg.Wait()

	n := 0
	for i := range created {
		if created[i] {
			n++
		}
		assert.Equal(t, numbers[0], numbers[i])
	}
	assert.Equal(t, 1, n)
}

func TestTheSupportsOfAFiledClaimCannotBeChanged(t *testing.T) {
	s := newWebStores(t)
	ctx := context.Background()
	_, _, err := s.claims.CreateWeb(ctx, webClaim("c1", "sub-1", "form-1", s.upload(t, "sub-1", 2)...))
	require.NoError(t, err)

	for _, stmt := range []string{
		`UPDATE claim_support SET requirement_code = 'other'`,
		`DELETE FROM claim_support`,
		`TRUNCATE claim_support`,
	} {
		_, err := s.db.ExecContext(ctx, stmt)
		require.Error(t, err, stmt)
		assert.Contains(t, err.Error(), "append-only", stmt)
	}
}

func TestTheDatabaseRefusesASupportLargerThan10MiB(t *testing.T) {
	s := newWebStores(t)
	f := domain.SupportFile{ID: "sup-" + randomHex(t, 16), OwnerSubject: "sub-1", FileName: "big.pdf",
		Format: domain.FormatPDF, ByteSize: domain.MaxSupportBytes + 1, SHA256: strings.Repeat("d", 64), UploadedAt: uploadedAt}

	assert.Error(t, s.supports.Save(context.Background(), f))

	f.ByteSize = domain.MaxSupportBytes
	assert.NoError(t, s.supports.Save(context.Background(), f))
}

func TestTheSupportsAreFoundAndCountedUntilAttached(t *testing.T) {
	s := newWebStores(t)
	ctx := context.Background()
	mine := s.upload(t, "sub-1", 3)
	theirs := s.upload(t, "sub-2", 1)

	found, err := s.supports.Find(ctx, []string{mine[0].ID, theirs[0].ID, "sup-" + strings.Repeat("0", 32)})
	require.NoError(t, err)
	assert.Equal(t, map[string]domain.SupportFile{mine[0].ID: mine[0], theirs[0].ID: theirs[0]}, found)
	empty, err := s.supports.Find(ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, empty)

	pending, err := s.supports.CountPending(ctx, "sub-1")
	require.NoError(t, err)
	assert.Equal(t, 3, pending)

	_, _, err = s.claims.CreateWeb(ctx, webClaim("c1", "sub-1", "form-1", mine[:2]...))
	require.NoError(t, err)
	pending, err = s.supports.CountPending(ctx, "sub-1")
	require.NoError(t, err)
	assert.Equal(t, 1, pending)
}

func TestTheListHasBothChannelsNewestFirstWithFiltersAndCursor(t *testing.T) {
	s := newWebStores(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		c := claim(fmt.Sprintf("m%d", i), "sub-1", fmt.Sprintf("draft-%d", i))
		c.FiledAt = filedAt.Add(time.Duration(i) * time.Hour)
		_, _, err := s.claims.Create(ctx, c)
		require.NoError(t, err)
	}
	w := webClaim("w1", "sub-1", "form-1", s.upload(t, "sub-1", 1)...)
	w.FiledAt = filedAt.Add(5 * time.Hour)
	w.PolicyID = "HG-2025-880231"
	_, _, err := s.claims.CreateWeb(ctx, w)
	require.NoError(t, err)
	_, _, err = s.claims.Create(ctx, claim("x", "sub-2", "draft-x"))
	require.NoError(t, err)

	all, err := s.claims.ListByOwner(ctx, "sub-1", ports.ClaimFilter{}, nil, 10)
	require.NoError(t, err)
	ids := make([]string, 0, len(all))
	for _, c := range all {
		ids = append(ids, c.ID)
	}
	assert.Equal(t, []string{"w1", "m2", "m1", "m0"}, ids)
	assert.Equal(t, domain.ChannelWeb, all[0].Channel)
	assert.NotNil(t, all[0].Policy)
	assert.Empty(t, all[0].Supports, "the list does not load the supports")
	assert.Equal(t, domain.ChannelMobile, all[1].Channel)
	assert.Nil(t, all[1].Policy)

	page, err := s.claims.ListByOwner(ctx, "sub-1", ports.ClaimFilter{PolicyID: "AU-2025-118204", Status: domain.StatusActive},
		&ports.ClaimCursor{FiledAt: all[1].FiledAt, ID: all[1].ID}, 1)
	require.NoError(t, err)
	require.Len(t, page, 1)
	assert.Equal(t, "m1", page[0].ID)

	recent, err := s.claims.ListByOwner(ctx, "sub-1", ports.ClaimFilter{FiledFrom: filedAt.Add(2 * time.Hour)}, nil, 10)
	require.NoError(t, err)
	assert.Len(t, recent, 2)
}

func TestMigration0003KeepsTheClaimsFiledBeforeIt(t *testing.T) {
	requireDocker(t)
	ctx := context.Background()
	db := newDB(t)
	// newDB applied every migration; start again on a fresh database with 0001 only.
	_, err := db.ExecContext(ctx, `DROP TABLE claim_support, support_file, claim, schema_migrations CASCADE;
		DROP SEQUENCE claim_filing_number_seq;
		DROP FUNCTION forbid_claim_support_mutation;`)
	require.NoError(t, err)
	first, err := migrations.FS.ReadFile("0001_claim.sql")
	require.NoError(t, err)
	require.NoError(t, postgres.MigrateFS(ctx, db, fstest.MapFS{"0001_claim.sql": {Data: first}}))
	// A claim filed before 0003, with only the columns of 0001: the store of
	// today writes the ones of 0003 too.
	_, err = db.ExecContext(ctx, `INSERT INTO claim (id, filing_number, cognito_sub, filing_key, fingerprint, policy_id,
		incident_type, occurred_at, place, description, details, status, filed_at)
		VALUES ('old', 'SIN-2026-00001', 'sub-1', 'draft-old', 'f', 'AU-2025-118204', 'collision', $1, 'Calle 80',
		'Choque por alcance.', '{"third_party":"yes"}', 'active', $1)`, filedAt)
	require.NoError(t, err)

	require.NoError(t, postgres.Migrate(ctx, db))

	old, err := postgres.NewClaimRecordStore(db).Get(ctx, "old")
	require.NoError(t, err)
	assert.Equal(t, domain.ChannelMobile, old.Channel)
	assert.Nil(t, old.Policy)
	assert.Empty(t, old.CatalogVersion)
	assert.Equal(t, map[string]string{"third_party": "yes"}, old.Details)
}

func TestASupportIsConfirmedOnceAndOnlyIfItExists(t *testing.T) {
	s := newWebStores(t)
	ctx := context.Background()
	f := domain.SupportFile{ID: "sup-" + randomHex(t, 16), OwnerSubject: "sub-1", FileName: "a.pdf",
		Format: domain.FormatPDF, ByteSize: 10, SHA256: strings.Repeat("e", 64), UploadedAt: uploadedAt}
	require.NoError(t, s.supports.Save(ctx, f))

	found, err := s.supports.Find(ctx, []string{f.ID})
	require.NoError(t, err)
	assert.False(t, found[f.ID].Confirmed(), "a declared support is not confirmed")

	first, err := s.supports.Confirm(ctx, f.ID, uploadedAt.Add(time.Minute))
	require.NoError(t, err)
	assert.Equal(t, uploadedAt.Add(time.Minute), first.ConfirmedAt)
	again, err := s.supports.Confirm(ctx, f.ID, uploadedAt.Add(time.Hour))
	require.NoError(t, err)
	assert.Equal(t, first.ConfirmedAt, again.ConfirmedAt, "the first confirmation stays")

	_, err = s.supports.Confirm(ctx, "sup-"+strings.Repeat("0", 32), uploadedAt)
	assert.ErrorIs(t, err, domain.ErrSupportNotFound)
}

func TestTheObjectKeyOfASupportIsKept(t *testing.T) {
	s := newWebStores(t)
	f := s.upload(t, "sub-1", 1)[0]

	var key string
	require.NoError(t, s.db.QueryRow(`SELECT object_key FROM support_file WHERE support_id = $1`, f.ID).Scan(&key))

	assert.Equal(t, "supports/sub-1/"+f.ID, key)
	var hasContent int
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM information_schema.columns
		WHERE table_name = 'support_file' AND column_name = 'content'`).Scan(&hasContent))
	assert.Zero(t, hasContent, "the file no longer lives in the database")
}

func TestTheEvidenceOfAMobileClaimIsReadWithTheDetail(t *testing.T) {
	s := newWebStores(t)
	ctx := context.Background()
	m := conEvidencia(claim("m1", "sub-1", "draft-1"))
	_, _, err := s.claims.Create(ctx, m)
	require.NoError(t, err)

	got, err := s.claims.Get(ctx, "m1")
	require.NoError(t, err)
	assert.Equal(t, domain.ChannelMobile, got.Channel)
	require.Len(t, got.Evidence, 2)
	assert.Equal(t, domain.KindPhoto, got.Evidence[0].Kind)
	assert.Empty(t, got.Supports)

	list, err := s.claims.ListByOwner(ctx, "sub-1", ports.ClaimFilter{}, nil, 10)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Empty(t, list[0].Evidence, "the list does not load the evidence")
}

// rewindTo0003 leaves the database as it was before 0004: with the content
// column and without the object key and the confirmation.
func rewindTo0003(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`ALTER TABLE support_file ADD COLUMN content BYTEA NOT NULL DEFAULT ''::bytea;
		ALTER TABLE support_file DROP COLUMN object_key, DROP COLUMN confirmed_at;
		DELETE FROM schema_migrations WHERE version = '0004_support_objects'`)
	require.NoError(t, err)
}

func TestMigration0004DropsThePendingFilesAndKeepsTheSchemaTidy(t *testing.T) {
	requireDocker(t)
	ctx := context.Background()
	db := newDB(t)
	rewindTo0003(t, db)
	_, err := db.Exec(`INSERT INTO support_file (support_id, owner_sub, file_name, content_type, byte_size, sha256, content, uploaded_at)
		VALUES ('sup-old', 'sub-1', 'a.pdf', 'application/pdf', 5, $1, '\x255044462d', now())`, strings.Repeat("a", 64))
	require.NoError(t, err)

	require.NoError(t, postgres.Migrate(ctx, db))

	var rows, hasContent int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM support_file`).Scan(&rows))
	assert.Zero(t, rows, "a pending file is declared again, its content was about to disappear")
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM information_schema.columns
		WHERE table_name = 'support_file' AND column_name IN ('content')`).Scan(&hasContent))
	assert.Zero(t, hasContent)
}

func TestMigration0004RefusesToDropTheContentOfAttachedFiles(t *testing.T) {
	requireDocker(t)
	ctx := context.Background()
	db := newDB(t)
	rewindTo0003(t, db)
	_, err := db.Exec(`INSERT INTO support_file (support_id, owner_sub, file_name, content_type, byte_size, sha256, content, uploaded_at)
		VALUES ('sup-old', 'sub-1', 'a.pdf', 'application/pdf', 5, $1, '\x255044462d', now())`, strings.Repeat("a", 64))
	require.NoError(t, err)
	_, _, err = postgres.NewClaimRecordStore(db).Create(ctx, claim("old", "sub-1", "draft-old"))
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO claim_support (claim_id, position, requirement_code, support_id) VALUES ('old', 0, 'x', 'sup-old')`)
	require.NoError(t, err)

	err = postgres.Migrate(ctx, db)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "upload them to the object store")
	var rows int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM support_file`).Scan(&rows))
	assert.Equal(t, 1, rows, "nothing was dropped")
}
