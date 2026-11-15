package postgres_test

import (
	"context"
	"database/sql"
	"fmt"
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

// The requests for on-site assistance in PostgreSQL (HU-M2.3-01): the key,
// the numbering, the shape of the location and what is never rewritten
// (decision D7).

var assistanceAt = time.Date(2026, 11, 14, 21, 31, 8, 123456000, time.UTC)

func sentAssistance(id, subject, key, fingerprint string) domain.AssistanceRequest {
	accuracy, captured, place := 8.0, assistanceAt.Add(-6*time.Second), "Autopista Norte km 12, <b>Bogotá</b>\n"
	return domain.AssistanceRequest{
		ID: id, Subject: subject, RequesterName: "Carolina Ruiz", Key: key, Fingerprint: fingerprint,
		Policy: domain.AssistancePolicy{ID: "AU-2025-118204", Line: domain.LineAuto, Product: "Autos",
			InsuredObject: "KJT-482"},
		Location: domain.AssistanceLocation{Status: domain.LocationSent,
			Coordinates: &domain.Coordinates{Latitude: 4.7712, Longitude: -74.0452}, AccuracyM: &accuracy,
			CapturedAt: &captured},
		Place: &place, Status: domain.AssistanceNew, RequestedAt: assistanceAt,
	}
}

func unavailableAssistance(id, subject, key string) domain.AssistanceRequest {
	return domain.AssistanceRequest{
		ID: id, Subject: subject, Key: key, Fingerprint: "f-" + id,
		Policy: domain.AssistancePolicy{ID: "HO-2025-000002", Line: domain.LineHome, Product: "Hogar",
			InsuredObject: "Calle 80 # 15-20"},
		Location: domain.AssistanceLocation{Status: domain.LocationUnavailable,
			UnavailableReason: domain.UnavailablePermissionDenied},
		Status: domain.AssistanceNew, RequestedAt: assistanceAt,
	}
}

func newAssistanceStore(t *testing.T) (*postgres.AssistanceStore, *sql.DB) {
	t.Helper()
	requireDocker(t)
	db := newDB(t)
	return postgres.AssistanceStoreOf(postgres.NewClaimRecordStore(db)), db
}

func TestARequestIsReadAsItWasStored(t *testing.T) {
	s, _ := newAssistanceStore(t)
	ctx := context.Background()

	sent := sentAssistance("3b8e2f6a-1c4d-4e7f-9a2b-6c5d8e0f1a2b", "sub-a", "k1", "f1")
	stored, created, err := s.Create(ctx, sent)
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, "AST-2026-0001", stored.Number)
	got, err := s.Get(ctx, sent.ID)
	require.NoError(t, err)
	assert.Equal(t, stored, got, "the answer of the request and the read say the same")
	sent.Number = stored.Number
	assert.Equal(t, sent, got, "every field, the copy of the policy and the place as text")

	unavailable := unavailableAssistance("9c2d1e0f-3a4b-4c5d-8e6f-7a8b9c0d1e2f", "sub-a", "k2")
	stored, created, err = s.Create(ctx, unavailable)
	require.NoError(t, err)
	assert.True(t, created)
	got, err = s.Get(ctx, unavailable.ID)
	require.NoError(t, err)
	unavailable.Number = stored.Number
	assert.Equal(t, unavailable, got)
	assert.Nil(t, got.Location.Coordinates)
	assert.Nil(t, got.Location.AccuracyM)
	assert.Nil(t, got.Location.CapturedAt)
	assert.Nil(t, got.Place)
	assert.Empty(t, got.RequesterName, "a token without a name")

	_, err = s.Get(ctx, "00000000-0000-4000-8000-000000000000")
	assert.ErrorIs(t, err, ports.ErrAssistanceRequestNotFound)
}

func TestARetryFindsTheRequestAndAReusedKeyIsRejected(t *testing.T) {
	s, db := newAssistanceStore(t)
	ctx := context.Background()
	first, _, err := s.Create(ctx, sentAssistance("3b8e2f6a-1c4d-4e7f-9a2b-6c5d8e0f1a2b", "sub-a", "k1", "f1"))
	require.NoError(t, err)

	retry := sentAssistance("4c9f3a7b-2d5e-4f80-8b3c-7d6e9f1a2b3c", "sub-a", "k1", "f1")
	retry.RequestedAt = assistanceAt.Add(time.Minute)
	again, created, err := s.Create(ctx, retry)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, first, again, "same id, number and instant")

	_, _, err = s.Create(ctx, sentAssistance("5d0a4b8c-3e6f-4a91-9c4d-8e7f0a2b3c4d", "sub-a", "k1", "other"))
	assert.ErrorIs(t, err, ports.ErrAssistanceKeyReused)

	theirs, created, err := s.Create(ctx, sentAssistance("6e1b5c9d-4f70-4ba2-8d5e-9f801b3c4d5e", "sub-b", "k1", "f1"))
	require.NoError(t, err)
	assert.True(t, created, "the same key of another insured")
	assert.NotEqual(t, first.Number, theirs.Number)

	var rows int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM assistance_request`).Scan(&rows))
	assert.Equal(t, 2, rows)
}

func TestTwentyRetriesAtOnceRecordOneRequest(t *testing.T) {
	s, db := newAssistanceStore(t)
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		created int
		ids     = map[string]bool{}
	)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r, c, err := s.Create(context.Background(),
				sentAssistance(fmt.Sprintf("7f2c6dae-5081-4cb3-9e6f-%012d", i), "sub-a", "same-key", "f1"))
			if !assert.NoError(t, err) {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			ids[r.ID] = true
			if c {
				created++
			}
		}(i)
	}
	wg.Wait()
	assert.Equal(t, 1, created, "one call created it")
	assert.Len(t, ids, 1, "every call answered the same request")
	var rows int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM assistance_request`).Scan(&rows))
	assert.Equal(t, 1, rows)
}

func TestTwentyRequestsAtOnceTakeTwentyNumbers(t *testing.T) {
	s, _ := newAssistanceStore(t)
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		numbers = map[string]bool{}
	)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r, _, err := s.Create(context.Background(), sentAssistance(fmt.Sprintf("8a3d7ebf-6192-4dc4-8f70-%012d", i),
				"sub-a", fmt.Sprintf("k-%d", i), "f1"))
			if !assert.NoError(t, err) {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			numbers[r.Number] = true
		}(i)
	}
	wg.Wait()
	assert.Len(t, numbers, 20)
}

func TestWhatWasSentIsNeverRewritten(t *testing.T) {
	s, db := newAssistanceStore(t)
	ctx := context.Background()
	r, _, err := s.Create(ctx, sentAssistance("3b8e2f6a-1c4d-4e7f-9a2b-6c5d8e0f1a2b", "sub-a", "k1", "f1"))
	require.NoError(t, err)

	for name, statement := range map[string]string{
		"coordinates":     `UPDATE assistance_request SET latitude = 4.8, longitude = -74.1`,
		"accuracy":        `UPDATE assistance_request SET accuracy_m = 3`,
		"instant of fix":  `UPDATE assistance_request SET location_captured_at = now()`,
		"policy":          `UPDATE assistance_request SET policy_id = 'AU-2025-000001'`,
		"policy snapshot": `UPDATE assistance_request SET policy_snapshot = '{"policy_id": "x"}'`,
		"location status": `UPDATE assistance_request SET location_status = 'unavailable', latitude = NULL, longitude = NULL, accuracy_m = NULL, location_captured_at = NULL, unavailable_reason = 'no_fix'`,
		"requested at":    `UPDATE assistance_request SET requested_at = now()`,
		"place":           `UPDATE assistance_request SET place = 'otro lugar'`,
		"owner":           `UPDATE assistance_request SET cognito_sub = 'sub-b'`,
		"number":          `UPDATE assistance_request SET request_number = 'AST-2026-9999'`,
		"requester":       `UPDATE assistance_request SET requester_name = 'otro'`,
		"status and more": `UPDATE assistance_request SET status = 'closed', place = 'otro lugar'`,
		"delete":          `DELETE FROM assistance_request`,
		"truncate":        `TRUNCATE assistance_request`,
	} {
		_, err := db.ExecContext(ctx, statement)
		assert.ErrorContains(t, err, "keeps what was sent", name)
	}

	// Only the status moves: it is what the inbox of HU-W2.3-03 will write.
	_, err = db.ExecContext(ctx, `UPDATE assistance_request SET status = 'in_call'`)
	require.NoError(t, err)
	got, err := s.Get(ctx, r.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.AssistanceStatus("in_call"), got.Status)
	got.Status = r.Status
	assert.Equal(t, r, got, "the rest is as it was sent")

	_, err = db.ExecContext(ctx, `UPDATE assistance_request SET status = 'lost'`)
	assert.Error(t, err, "a status outside the four of the inbox")
}

func TestTheShapeOfTheLocationIsCheckedByTheDatabase(t *testing.T) {
	_, db := newAssistanceStore(t)
	insert := func(id, status string, latitude, longitude, accuracy, captured, reason any) error {
		_, err := db.Exec(`INSERT INTO assistance_request (id, request_number, cognito_sub, request_key, fingerprint,
			policy_id, policy_snapshot, location_status, latitude, longitude, accuracy_m, location_captured_at,
			unavailable_reason, requested_at)
			VALUES ($1, $1, 'sub-a', $1, 'f', 'AU-1', '{}', $2, $3, $4, $5, $6, $7, now())`,
			id, status, latitude, longitude, accuracy, captured, reason)
		return err
	}
	now := time.Now()
	require.NoError(t, insert("ok-sent", "sent", 4.7, -74.0, 8.0, now, nil))
	require.NoError(t, insert("ok-unavailable", "unavailable", nil, nil, nil, nil, "no_fix"))

	for name, err := range map[string]error{
		"sent without coordinates":     insert("a", "sent", nil, nil, 8.0, now, nil),
		"sent without accuracy":        insert("b", "sent", 4.7, -74.0, nil, now, nil),
		"sent without the instant":     insert("c", "sent", 4.7, -74.0, 8.0, nil, nil),
		"sent with a reason":           insert("d", "sent", 4.7, -74.0, 8.0, now, "no_fix"),
		"unavailable with coordinates": insert("e", "unavailable", 4.7, -74.0, nil, nil, "no_fix"),
		"unavailable without a reason": insert("f", "unavailable", nil, nil, nil, nil, nil),
		"latitude 91":                  insert("g", "sent", 91.0, -74.0, 8.0, now, nil),
		"longitude 181":                insert("h", "sent", 4.7, 181.0, 8.0, now, nil),
		"accuracy 0":                   insert("i", "sent", 4.7, -74.0, 0.0, now, nil),
		"accuracy over 10 km":          insert("j", "sent", 4.7, -74.0, 10000.1, now, nil),
		"an unknown reason":            insert("k", "unavailable", nil, nil, nil, nil, "airplane_mode"),
		"an unknown status":            insert("l", "approximate", nil, nil, nil, nil, nil),
	} {
		assert.ErrorContains(t, err, "violates check constraint", name)
	}
}

// Another story developed at the same time may also number its migration
// 0013: both are applied, each by its own name.
func TestTheMigrationOfTheAssistanceLivesWithAnotherOfTheSameNumber(t *testing.T) {
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
	files["0013_another_story.sql"] = &fstest.MapFile{Data: []byte(`CREATE TABLE another_story (id TEXT PRIMARY KEY)`)}
	require.NoError(t, postgres.MigrateFS(context.Background(), db, files))

	rows, err := db.Query(`SELECT version FROM schema_migrations WHERE version LIKE '0013_%' ORDER BY version`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var applied []string
	for rows.Next() {
		var v string
		require.NoError(t, rows.Scan(&v))
		applied = append(applied, v)
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, []string{"0013_another_story", "0013_assistance_request"}, applied)
}
