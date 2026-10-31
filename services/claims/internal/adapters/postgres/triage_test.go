package postgres_test

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/migrations"
)

// The calculations of the level in PostgreSQL (HU-W2.2-01): claim_triage only
// grows, two tasks never record the same calculation twice and the sweep finds
// what any other story changed in a claim.

type triageStores struct {
	db     *sql.DB
	claims *postgres.ClaimRecordStore
	triage *postgres.TriageStore
}

func newTriageStores(t *testing.T) triageStores {
	t.Helper()
	requireDocker(t)
	db := newDB(t)
	return triageStores{db: db, claims: postgres.NewClaimRecordStore(db), triage: postgres.NewTriageStore(db)}
}

func minorCOP(cop int64) *int64 {
	m := cop * 100
	return &m
}

// file stores a web claim of water damage filed minutes after filedAt.
func (s triageStores) file(t *testing.T, id string, minutes int, cop *int64) {
	t.Helper()
	c := webClaim(id, "sub-carolina", "key-"+id)
	c.IncidentType = domain.IncidentWaterDamage
	c.FiledAt = filedAt.Add(time.Duration(minutes) * time.Minute)
	c.EstimatedAmountMinor = cop
	_, _, err := s.claims.CreateWeb(context.Background(), c)
	require.NoError(t, err)
}

func (s triageStores) record(t *testing.T, c ports.TriageCandidate, version string) domain.TriageRecord {
	t.Helper()
	p := domain.TriagePolicyV1()
	p.Version = version
	a, _ := p.Assess(p.InputOf(c.Claim))
	r, ok := domain.NextTriage(c.Claim.ID, a, c.Last, filedAt.Add(time.Hour))
	require.True(t, ok)
	require.NoError(t, s.triage.Record(context.Background(), r))
	return r
}

func (s triageStores) pending(t *testing.T, version string) []ports.TriageCandidate {
	t.Helper()
	got, err := s.triage.Pending(context.Background(), domain.TriageableStatuses(), version, 50)
	require.NoError(t, err)
	return got
}

func TestTheSweepFindsTheClaimsWithoutACalculationOldestFirst(t *testing.T) {
	s := newTriageStores(t)
	s.file(t, "cl-2", 2, nil)
	s.file(t, "cl-1", 1, minorCOP(4_800_000))
	s.file(t, "cl-3", 3, nil)
	_, err := s.db.Exec(`UPDATE claim SET status = 'paid' WHERE id = 'cl-3'`)
	require.NoError(t, err)

	pending := s.pending(t, domain.TriagePolicyVersionV1)
	require.Len(t, pending, 2)
	assert.Equal(t, "cl-1", pending[0].Claim.ID)
	assert.Equal(t, "cl-2", pending[1].Claim.ID)
	assert.Nil(t, pending[0].Last)
	assert.Empty(t, pending[0].Claim.Supports, "without supports nor evidence")

	r := s.record(t, pending[0], domain.TriagePolicyVersionV1)
	pending = s.pending(t, domain.TriagePolicyVersionV1)
	require.Len(t, pending, 1)
	assert.Equal(t, "cl-2", pending[0].Claim.ID)

	c, err := s.triage.Candidate(context.Background(), "cl-1")
	require.NoError(t, err)
	require.NotNil(t, c.Last)
	assert.Equal(t, r, *c.Last, "read back as it was recorded")

	_, err = s.triage.Candidate(context.Background(), "cl-404")
	assert.ErrorIs(t, err, ports.ErrClaimNotFound)
}

// The stories of sprint 3 will change the amount and the type of a claim; the
// sweep sees it without them calling the triage.
func TestTheSweepFindsAClaimThatAnotherStoryChanged(t *testing.T) {
	s := newTriageStores(t)
	s.file(t, "cl-1", 0, nil)
	s.record(t, s.pending(t, domain.TriagePolicyVersionV1)[0], domain.TriagePolicyVersionV1)

	for _, change := range []string{
		`UPDATE claim SET estimated_amount_minor = 480000000 WHERE id = 'cl-1'`,
		`UPDATE claim SET estimated_amount_minor = 4000000000 WHERE id = 'cl-1'`,
		`UPDATE claim SET estimated_amount_minor = NULL WHERE id = 'cl-1'`,
		`UPDATE claim SET incident_type = 'glass' WHERE id = 'cl-1'`,
		`UPDATE claim SET status = 'in_evaluation' WHERE id = 'cl-1'`,
	} {
		_, err := s.db.Exec(change)
		require.NoError(t, err)
		pending := s.pending(t, domain.TriagePolicyVersionV1)
		if change == `UPDATE claim SET status = 'in_evaluation' WHERE id = 'cl-1'` {
			assert.Empty(t, pending, "the same input in another status is not a change")
			continue
		}
		require.Len(t, pending, 1, change)
		s.record(t, pending[0], domain.TriagePolicyVersionV1)
		assert.Empty(t, s.pending(t, domain.TriagePolicyVersionV1), change)
	}
	assert.Len(t, s.pending(t, "triage-policy-v2"), 1, "another version of the policy")

	history, err := s.triage.History(context.Background(), "cl-1")
	require.NoError(t, err)
	require.Len(t, history, 5)
	assert.Equal(t, 5, history[0].Seq, "newest first")
	assert.Equal(t, domain.IncidentGlass, history[0].Assessment.Input.IncidentType)
	assert.Equal(t, []domain.TriageCause{domain.CauseIncidentTypeChanged, domain.CauseCoverageChanged}, history[0].Causes)
	assert.Equal(t, domain.TriageTarget{Kind: domain.TargetImmediate, ResolutionBusinessDays: 2}, history[2].Assessment.Target)
	require.NotNil(t, history[0].PreviousLevel)
	assert.Nil(t, history[4].PreviousLevel)
	assert.Equal(t, time.UTC, history[0].AssessedAt.Location())
}

func TestTwoTasksRecordingTheSameCalculationRecordItOnce(t *testing.T) {
	s := newTriageStores(t)
	s.file(t, "cl-1", 0, minorCOP(4_800_000))
	c := s.pending(t, domain.TriagePolicyVersionV1)[0]
	p := domain.TriagePolicyV1()
	a, _ := p.Assess(p.InputOf(c.Claim))
	r, _ := domain.NextTriage("cl-1", a, nil, filedAt.Add(time.Hour))

	errs := make([]error, 8)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = s.triage.Record(context.Background(), r)
		}(i)
	}
	wg.Wait()

	won := 0
	for _, err := range errs {
		if err == nil {
			won++
			continue
		}
		assert.ErrorIs(t, err, ports.ErrTriageConflict)
	}
	assert.Equal(t, 1, won)
	var rows int
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM claim_triage`).Scan(&rows))
	assert.Equal(t, 1, rows)
}

func TestTheCalculationsCannotBeChanged(t *testing.T) {
	s := newTriageStores(t)
	s.file(t, "cl-1", 0, nil)
	s.record(t, s.pending(t, domain.TriagePolicyVersionV1)[0], domain.TriagePolicyVersionV1)

	for _, stmt := range []string{
		`UPDATE claim_triage SET level = 3`,
		`DELETE FROM claim_triage`,
		`TRUNCATE claim_triage`,
	} {
		_, err := s.db.Exec(stmt)
		require.Error(t, err, stmt)
		assert.Contains(t, err.Error(), "append-only", stmt)
	}
	_, err := s.db.Exec(`INSERT INTO claim_triage (claim_id, seq, level, severity, amount_band, incident_type,
		policy_line, coverage, policy_version, input_fingerprint, target_kind, target_business_days,
		resolution_business_days, causes, actor, assessed_at)
		VALUES ('cl-1', 2, 2, 'medium', 'unknown', 'collision', 'auto', 'collision', 'v', 'f', 'business_days', 5, 5,
		'[]', 'system', now())`)
	assert.Error(t, err, "a calculation always has a cause")
}

func TestTheCurrentTriageOfAPage(t *testing.T) {
	s := newTriageStores(t)
	s.file(t, "cl-1", 0, minorCOP(4_800_000))
	s.file(t, "cl-2", 1, nil)
	first := s.record(t, s.pending(t, domain.TriagePolicyVersionV1)[0], domain.TriagePolicyVersionV1)
	_, err := s.db.Exec(`UPDATE claim SET estimated_amount_minor = 4000000000 WHERE id = 'cl-1'`)
	require.NoError(t, err)
	second := s.record(t, s.pending(t, domain.TriagePolicyVersionV1)[0], domain.TriagePolicyVersionV1)
	require.Equal(t, "cl-1", first.ClaimID)
	require.Equal(t, "cl-1", second.ClaimID)

	got, err := s.triage.Current(context.Background(), []string{"cl-1", "cl-2", "cl-404"})

	require.NoError(t, err)
	require.Len(t, got, 2)
	require.NotNil(t, got["cl-1"].Last)
	assert.Equal(t, 2, got["cl-1"].Last.Seq, "the last calculation")
	assert.Equal(t, domain.Triage1, got["cl-1"].Last.Assessment.Level)
	assert.Equal(t, domain.StatusActive, got["cl-2"].Claim.Status)
	assert.Nil(t, got["cl-2"].Last)

	empty, err := s.triage.Current(context.Background(), []string{"cl-404"})
	require.NoError(t, err)
	assert.Empty(t, empty)

	var level int
	require.NoError(t, s.db.QueryRow(`SELECT level FROM claim_current_triage WHERE claim_id = 'cl-1'`).Scan(&level))
	assert.Equal(t, 1, level, "the view other stories join")
}

// Another story developed at the same time may also number its migration
// 0005: both are applied, each by its own name.
func TestTheMigrationOfTheTriageLivesWithAnotherOfTheSameNumber(t *testing.T) {
	requireDocker(t)
	db := newDB(t)
	files := fstest.MapFS{}
	entries, err := migrations.FS.ReadDir(".")
	require.NoError(t, err)
	for _, e := range entries {
		body, err := migrations.FS.ReadFile(e.Name())
		require.NoError(t, err)
		files[e.Name()] = &fstest.MapFile{Data: body}
	}
	files["0005_claim_evaluation.sql"] = &fstest.MapFile{Data: []byte(`CREATE TABLE claim_evaluation (claim_id TEXT PRIMARY KEY)`)}

	require.NoError(t, postgres.MigrateFS(context.Background(), db, files))

	var applied []string
	rows, err := db.Query(`SELECT version FROM schema_migrations WHERE version LIKE '0005_%' ORDER BY version`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var v string
		require.NoError(t, rows.Scan(&v))
		applied = append(applied, v)
	}
	assert.Equal(t, []string{"0005_claim_evaluation", "0005_claim_triage"}, applied)
}
