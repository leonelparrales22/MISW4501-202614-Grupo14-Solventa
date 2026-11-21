package postgres_test

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/assistance"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/migrations"
)

// The inbox of assistance in PostgreSQL (HU-W2.3-03): what is decided reads
// back, two analysts never both decide, nothing decided is rewritten, and the
// notices are held by one task.

const (
	inboxGPS   = "3b8e2f6a-1c4d-4e7f-9a2b-6c5d8e0f1a2b"
	inboxNoGPS = "9c2d1e0f-3a4b-4c5d-8e6f-7a8b9c0d1e2f"
)

var (
	inboxAnalyst   = domain.Analyst{Email: "andres.ruiz@solventa.example", Name: "Andrés Ruiz"}
	inboxColleague = domain.Analyst{Email: "laura.gomez@solventa.example", Name: "Laura Gómez"}
)

type inboxClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *inboxClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *inboxClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type inboxStores struct {
	db        *sql.DB
	requests  *postgres.AssistanceStore
	store     *postgres.AssistanceInboxStore
	providers *postgres.ProviderStore
	clock     *inboxClock
	svc       *assistance.InboxService
}

// newInboxStores is a migrated database with a request with GPS (the one of
// the mockup) and one without, from the home line.
func newInboxStores(t *testing.T) inboxStores {
	t.Helper()
	requireDocker(t)
	db := newDB(t)
	records := postgres.NewClaimRecordStore(db)
	s := inboxStores{db: db, requests: postgres.AssistanceStoreOf(records),
		store: postgres.AssistanceInboxStoreOf(records), providers: postgres.ProviderStoreOf(records),
		clock: &inboxClock{now: assistanceAt.Add(time.Minute)}}
	s.svc = assistance.NewInboxService(s.store, s.providers, s.clock, nil)
	ctx := context.Background()
	_, _, err := s.requests.Create(ctx, sentAssistance(inboxGPS, "sub-a", "k1", "f1"))
	require.NoError(t, err)
	_, _, err = s.requests.Create(ctx, unavailableAssistance(inboxNoGPS, "sub-b", "k2"))
	require.NoError(t, err)
	return s
}

func (s inboxStores) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, s.db.QueryRow(query, args...).Scan(&n))
	return n
}

// towed attends the request with GPS, records a tow truck and dispatches the
// suggested one; Auxilio Vial Norte is unavailable until tomorrow.
func (s inboxStores) towed(t *testing.T) assistance.CaseView {
	t.Helper()
	ctx := context.Background()
	tomorrow := time.Date(2026, 11, 15, 0, 0, 0, 0, domain.ColombiaTime)
	_, err := s.providers.SetAvailability(ctx, "prv-auxilio-vial-norte", domain.AvailabilityUnavailable, &tomorrow,
		inboxAnalyst.Email, s.clock.Now())
	require.NoError(t, err)
	_, err = s.svc.Attend(ctx, inboxGPS, inboxAnalyst)
	require.NoError(t, err)
	_, err = s.svc.RecordNeed(ctx, inboxGPS, "towing", inboxAnalyst)
	require.NoError(t, err)
	s.clock.advance(3*time.Minute + 123456*time.Microsecond)
	view, err := s.svc.Dispatch(ctx, inboxGPS, "prv-gruas-autopista-norte", inboxAnalyst)
	require.NoError(t, err)
	return view
}

func TestADispatchReadsBackAsItWasDecided(t *testing.T) {
	s := newInboxStores(t)
	view := s.towed(t)

	got, err := s.store.Case(context.Background(), inboxGPS)
	require.NoError(t, err)
	assert.Equal(t, view.AssistanceRecord, got, "the answer of the dispatch and the read say the same")

	d := got.Case.Dispatch
	require.NotNil(t, d)
	assert.Equal(t, domain.AssistanceDispatched, got.Case.Request.Status)
	assert.Equal(t, "prv-gruas-autopista-norte", d.Provider.ID)
	assert.Equal(t, "+576015550301", *d.Provider.ContactPhone)
	assert.Equal(t, 8.0, *d.Location.AccuracyM)
	assert.Equal(t, "Autopista Norte km 12, <b>Bogotá</b>\n", *d.Location.Place, "the place is kept as text")
	assert.Len(t, d.Attempts, 1)
	require.Len(t, d.Candidates, 4, "what the analyst saw")
	var until *time.Time
	for _, c := range d.Candidates {
		if c.ProviderID == "prv-auxilio-vial-norte" {
			until = c.UnavailableUntil
		}
	}
	require.NotNil(t, until)
	assert.Equal(t, "2026-11-15", until.Format(time.DateOnly))
	assert.Equal(t, s.clock.Now(), d.At)
	assert.Equal(t, []domain.AssistanceActionCode{domain.ActionAttended, domain.ActionNeedRecorded,
		domain.ActionDispatched}, []domain.AssistanceActionCode{got.Case.History[0].Code, got.Case.History[1].Code,
		got.Case.History[2].Code})
	assert.Equal(t, "prv-gruas-autopista-norte", got.Case.History[2].ProviderID)
	require.NotNil(t, got.Notice)
	assert.Equal(t, ports.NoticePending, got.Notice.Status)
	assert.Equal(t, "Asistencia AST-2026-0001 · Grúa", got.Notice.Subject)
}

func TestAClosingAndALocationByVoiceReadBack(t *testing.T) {
	s := newInboxStores(t)
	ctx := context.Background()
	_, err := s.svc.Attend(ctx, inboxNoGPS, inboxAnalyst)
	require.NoError(t, err)
	_, err = s.svc.Attend(ctx, inboxNoGPS, inboxColleague)
	require.NoError(t, err)
	lat, lon := 4.6912, -74.0451
	_, err = s.svc.RecordVoiceLocation(ctx, inboxNoGPS,
		domain.VoiceLocationInput{Address: "Calle 80 # 15-20, Bogotá", Latitude: &lat, Longitude: &lon}, inboxColleague)
	require.NoError(t, err)
	view, err := s.svc.Close(ctx, inboxNoGPS, domain.CloseInput{Reason: "other", Note: ptrTo("Llegó un vecino plomero")},
		inboxColleague)
	require.NoError(t, err)

	got, err := s.store.Case(ctx, inboxNoGPS)
	require.NoError(t, err)
	assert.Equal(t, view.AssistanceRecord, got)
	assert.Equal(t, domain.AssistanceClosed, got.Case.Request.Status)
	assert.Equal(t, domain.VoiceLocation{Seq: 1, Address: "Calle 80 # 15-20, Bogotá",
		Point: domain.Coordinates{Latitude: lat, Longitude: lon}, By: inboxColleague, At: s.clock.Now()}, *got.Case.Voice)
	assert.Equal(t, domain.AssistanceClosure{Reason: domain.CloseOther, Note: ptrTo("Llegó un vecino plomero"),
		By: inboxColleague, At: s.clock.Now()}, *got.Case.Closure)
	require.Len(t, got.Case.History, 4)
	assert.Equal(t, inboxAnalyst.Email, got.Case.History[1].From)
	assert.Equal(t, domain.CloseOther, got.Case.History[3].Reason)
	assert.Nil(t, got.Notice)
	assert.Nil(t, got.Case.Dispatch)

	_, err = s.store.Case(ctx, "no-existe")
	assert.ErrorIs(t, err, ports.ErrAssistanceRequestNotFound)
	assert.ErrorIs(t, s.store.Attend(ctx, "no-existe", 0, domain.AssistanceAction{Code: domain.ActionAttended,
		By: inboxAnalyst, At: s.clock.Now()}), ports.ErrAssistanceRequestNotFound)
}

func ptrTo[T any](v T) *T { return &v }

func TestTheInboxOfPostgreSQLListsTheLatestFirstAndCounts(t *testing.T) {
	s := newInboxStores(t)
	ctx := context.Background()
	later := sentAssistance("5e7a9c1b-2d4f-4a6b-8c0d-1e3f5a7b9c2d", "sub-c", "k3", "f3")
	later.RequestedAt = assistanceAt.Add(time.Minute)
	_, _, err := s.requests.Create(ctx, later)
	require.NoError(t, err)
	s.towed(t)

	ids := func(f ports.InboxFilter) []string {
		list, err := s.store.List(ctx, f)
		require.NoError(t, err)
		var out []string
		for _, r := range list {
			out = append(out, r.Case.Request.ID)
		}
		return out
	}
	assert.Equal(t, []string{later.ID, inboxNoGPS, inboxGPS}, ids(ports.InboxFilter{Limit: 50}),
		"the latest first, the id breaking the tie")
	assert.Equal(t, []string{later.ID, inboxNoGPS},
		ids(ports.InboxFilter{Statuses: []domain.AssistanceStatus{domain.AssistanceNew}, Limit: 50}))
	assert.Equal(t, []string{inboxGPS}, ids(ports.InboxFilter{Statuses: []domain.AssistanceStatus{
		domain.AssistanceDispatched, domain.AssistanceClosed}, Limit: 50}))
	assert.Equal(t, []string{later.ID}, ids(ports.InboxFilter{Limit: 1}))
	assert.Empty(t, ids(ports.InboxFilter{Statuses: []domain.AssistanceStatus{domain.AssistanceInCall}, Limit: 50}))

	newCount, inCall, took, err := s.store.Counts(ctx, domain.ColombiaDayStart(s.clock.Now()))
	require.NoError(t, err)
	assert.Equal(t, 2, newCount)
	assert.Zero(t, inCall)
	require.Len(t, took, 1)
	assert.Equal(t, 4*time.Minute+123456*time.Microsecond, took[0], "from the request to the dispatch")
	_, _, took, err = s.store.Counts(ctx, s.clock.Now().Add(time.Second))
	require.NoError(t, err)
	assert.Empty(t, took, "a dispatch before the day does not count")

	today, err := s.store.DispatchesSince(ctx, []string{"prv-gruas-autopista-norte", "prv-gruas-calle-80"},
		domain.ColombiaDayStart(s.clock.Now()))
	require.NoError(t, err)
	assert.Equal(t, map[string]int{"prv-gruas-autopista-norte": 1}, today)
	today, err = s.store.DispatchesSince(ctx, nil, assistanceAt)
	require.NoError(t, err)
	assert.Empty(t, today)
}

func TestOfTwoAnalystsDecidingOnARequestAtOnceOneWins(t *testing.T) {
	s := newInboxStores(t)
	ctx := context.Background()
	_, err := s.svc.Attend(ctx, inboxGPS, inboxAnalyst)
	require.NoError(t, err)
	r, err := s.store.Case(ctx, inboxGPS)
	require.NoError(t, err)

	var wg sync.WaitGroup
	errs := make([]error, 10)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			at := s.clock.Now()
			errs[i] = s.store.Close(ctx, inboxGPS, r.Version(), domain.AssistanceClosure{Reason: domain.CloseDuplicate,
				By: inboxAnalyst, At: at}, domain.AssistanceAction{Code: domain.ActionClosed, By: inboxAnalyst, At: at,
				Reason: domain.CloseDuplicate})
		}(i)
	}
	wg.Wait()
	won := 0
	for _, err := range errs {
		if err == nil {
			won++
			continue
		}
		assert.ErrorIs(t, err, ports.ErrAssistanceChanged)
	}
	assert.Equal(t, 1, won, "one analyst decides; the others find the request changed")
	assert.Equal(t, 1, s.count(t, `SELECT count(*) FROM assistance_closure`))
	assert.Equal(t, 2, s.count(t, `SELECT count(*) FROM assistance_action WHERE request_id = $1`, inboxGPS))
}

func TestTheDecisionsOfTheInboxAreNeverRewritten(t *testing.T) {
	s := newInboxStores(t)
	s.towed(t)
	ctx := context.Background()
	_, err := s.svc.Close(ctx, inboxNoGPS, domain.CloseInput{Reason: "duplicate"}, inboxAnalyst)
	require.NoError(t, err)
	_, err = s.db.Exec(`INSERT INTO assistance_voice_location (request_id, seq, address, latitude, longitude,
		recorded_by, recorded_at) VALUES ($1, 1, 'Calle 80 # 15-20', 4.6, -74.1, 'x', now())`, inboxNoGPS)
	require.NoError(t, err)

	for table, statements := range map[string][]string{
		"assistance_action": {`UPDATE assistance_action SET request_id = request_id`,
			`DELETE FROM assistance_action`, `TRUNCATE assistance_action`},
		"assistance_need": {`UPDATE assistance_need SET request_id = request_id`,
			`DELETE FROM assistance_need`, `TRUNCATE assistance_need`},
		"assistance_voice_location": {`UPDATE assistance_voice_location SET request_id = request_id`,
			`DELETE FROM assistance_voice_location`, `TRUNCATE assistance_voice_location`},
		"assistance_dispatch": {`UPDATE assistance_dispatch SET request_id = request_id`,
			`DELETE FROM assistance_dispatch`, `TRUNCATE assistance_dispatch`},
		"assistance_closure": {`UPDATE assistance_closure SET request_id = request_id`,
			`DELETE FROM assistance_closure`, `TRUNCATE assistance_closure`},
	} {
		_, err := s.db.Exec(statements[0])
		assert.ErrorContains(t, err, "append-only", "update of "+table)
		for _, stmt := range statements[1:] {
			_, err = s.db.Exec(stmt)
			assert.Error(t, err, stmt)
		}
	}
	_, err = s.db.Exec(`DELETE FROM assistance_closure`)
	assert.ErrorContains(t, err, "append-only")
	_, err = s.db.Exec(`TRUNCATE assistance_need`)
	assert.ErrorContains(t, err, "append-only")

	_, err = s.db.Exec(`TRUNCATE assistance_request`)
	assert.ErrorContains(t, err, "keeps what was sent", "the request keeps its own guard (HU-M2.3-01)")
	_, err = s.db.Exec(`UPDATE assistance_request SET latitude = 4.6, longitude = -74.1 WHERE id = $1`, inboxGPS)
	assert.ErrorContains(t, err, "keeps what was sent", "the inbox writes the status and nothing else of the request")
	_, err = s.db.Exec(`UPDATE assistance_notice SET attempts = attempts + 1`)
	assert.NoError(t, err, "only the notice moves")
}

func TestTheDatabaseRefusesAnInconsistentDecision(t *testing.T) {
	s := newInboxStores(t)
	s.towed(t)
	for name, stmt := range map[string]string{
		"other without a note": `INSERT INTO assistance_closure (request_id, reason, closed_by, closed_at)
			VALUES ('` + inboxNoGPS + `', 'other', 'x', now())`,
		"a note of two": `INSERT INTO assistance_closure (request_id, reason, note, closed_by, closed_at)
			VALUES ('` + inboxNoGPS + `', 'duplicate', 'ok', 'x', now())`,
		"an unknown reason": `INSERT INTO assistance_closure (request_id, reason, closed_by, closed_at)
			VALUES ('` + inboxNoGPS + `', 'tired', 'x', now())`,
		"an unknown action": `INSERT INTO assistance_action (request_id, seq, action, actor_email, at)
			VALUES ('` + inboxNoGPS + `', 1, 'ignored', 'x', now())`,
		"a voice address of four": `INSERT INTO assistance_voice_location (request_id, seq, address, latitude, longitude,
			recorded_by, recorded_at) VALUES ('` + inboxNoGPS + `', 1, 'abcd', 4.6, -74.1, 'x', now())`,
		"a dispatch by voice with an accuracy": `INSERT INTO assistance_dispatch (request_id, provider_id, provider_name,
			contact_email, need, specialty, distance_km, eta_minutes, location_source, latitude, longitude, accuracy_m,
			address, suggested_provider_id, candidates, attempts, assistance_policy_version, assignment_policy_version,
			dispatched_by, dispatched_at) VALUES ('` + inboxNoGPS + `', 'prv-gruas-calle-80', 'x', 'x', 'towing',
			'towing', 1, 12, 'voice', 4.6, -74.1, 8, 'Calle 80', 'x', '[]', '[]', 'v', 'v', 'x', now())`,
		"a dispatch to an unknown provider": `INSERT INTO assistance_dispatch (request_id, provider_id, provider_name,
			contact_email, need, specialty, distance_km, eta_minutes, location_source, latitude, longitude, accuracy_m,
			suggested_provider_id, candidates, attempts, assistance_policy_version, assignment_policy_version,
			dispatched_by, dispatched_at) VALUES ('` + inboxNoGPS + `', 'prv-no-existe', 'x', 'x', 'towing',
			'towing', 1, 12, 'app', 4.6, -74.1, 8, 'x', '[]', '[]', 'v', 'v', 'x', now())`,
		"a sent notice without its time": `UPDATE assistance_notice SET status = 'sent'`,
		"a notice of no dispatch": `INSERT INTO assistance_notice (request_id, to_email, subject, body, status)
			VALUES ('` + inboxNoGPS + `', 'x', 'x', 'x', 'pending')`,
	} {
		_, err := s.db.Exec(stmt)
		assert.Error(t, err, name)
	}
}

func TestTheDueNoticesOfTheDispatchesAreHeldByOneTask(t *testing.T) {
	s := newInboxStores(t)
	s.towed(t)
	ctx := context.Background()
	now := s.clock.Now()

	due, err := s.store.ClaimNoticesDue(ctx, now, time.Minute, 10)
	require.NoError(t, err)
	require.Len(t, due, 1)
	assert.Equal(t, inboxGPS, due[0].RequestID)
	assert.Equal(t, "despachos@gruasautopistanorte.example.com", due[0].To)
	again, err := s.store.ClaimNoticesDue(ctx, now, time.Minute, 10)
	require.NoError(t, err)
	assert.Empty(t, again, "held for a minute by the task that took it")

	require.NoError(t, s.store.NoticeFailed(ctx, inboxGPS, now.Add(5*time.Second), "down"))
	got, err := s.store.Case(ctx, inboxGPS)
	require.NoError(t, err)
	assert.Equal(t, 1, got.Notice.Attempts)
	assert.Equal(t, "down", got.Notice.LastError)
	assert.Equal(t, now.Add(5*time.Second), got.Notice.NextAttemptAt)

	require.NoError(t, s.store.NoticeSent(ctx, inboxGPS, now.Add(6*time.Second)))
	require.NoError(t, s.store.NoticeFailed(ctx, inboxGPS, now.Add(time.Hour), "late"), "a sent notice stays sent")
	got, err = s.store.Case(ctx, inboxGPS)
	require.NoError(t, err)
	assert.Equal(t, ports.NoticeSent, got.Notice.Status)
	assert.Equal(t, 2, got.Notice.Attempts)
	assert.Equal(t, now.Add(6*time.Second), got.Notice.SentAt)
	assert.Empty(t, got.Notice.LastError)

	due, err = s.store.ClaimNoticesDue(ctx, now.Add(time.Hour), time.Minute, 10)
	require.NoError(t, err)
	assert.Empty(t, due)
}

func TestTheWorkerSendsTheNoticeOfPostgreSQLOnce(t *testing.T) {
	s := newInboxStores(t)
	s.towed(t)
	var keys []string
	w := assistance.NewNoticeWorker(s.store, mailerFunc(func(_, _, _, key string) error {
		keys = append(keys, key)
		if len(keys) == 1 {
			return errors.New("down")
		}
		return nil
	}), s.clock, assistance.NoticeConfig{})
	assert.Equal(t, 1, w.SendDue(context.Background()))
	s.clock.advance(5 * time.Second)
	assert.Equal(t, 1, w.SendDue(context.Background()))
	s.clock.advance(time.Hour)
	assert.Zero(t, w.SendDue(context.Background()))
	assert.Equal(t, []string{"asistencia-" + inboxGPS, "asistencia-" + inboxGPS}, keys)
}

type mailerFunc func(to, subject, body, key string) error

func (f mailerFunc) Send(_ context.Context, to, subject, body, key string) error {
	return f(to, subject, body, key)
}

func TestTheSeedOfTheAssistanceDoesNotOverwriteWhatOperationsEdited(t *testing.T) {
	s := newInboxStores(t)
	ctx := context.Background()
	_, err := s.providers.SetAvailability(ctx, "prv-gruas-del-valle", domain.AvailabilityUnavailable, nil,
		inboxAnalyst.Email, s.clock.Now())
	require.NoError(t, err)

	_, err = s.db.Exec(`DELETE FROM schema_migrations WHERE version = '0015_assistance_provider_seed'`)
	require.NoError(t, err)
	require.NoError(t, postgres.Migrate(ctx, s.db))

	p, err := s.providers.Get(ctx, "prv-gruas-del-valle")
	require.NoError(t, err)
	assert.Equal(t, domain.AvailabilityUnavailable, p.Availability)
	assert.Equal(t, len(memory.ReferenceProviders()), s.count(t, `SELECT count(*) FROM provider`))
	assert.Zero(t, s.count(t, `SELECT count(*) FROM provider WHERE id IN ('prv-gruas-autopista-norte',
		'prv-auxilio-vial-norte', 'prv-gruas-calle-80', 'prv-asistencia-vial-sur', 'prv-gruas-del-valle')
		AND 'plumbing' = ANY(specialties)`), "the seed of the assistance brings no plumbing")
}

// Another story developed at the same time may also number its migrations
// 0014 and 0015: all are applied, each by its own name.
func TestTheMigrationsOfTheInboxLiveWithOthersOfTheSameNumber(t *testing.T) {
	requireDocker(t)
	db := newDB(t)
	files := fstest.MapFS{}
	entries, err := migrations.FS.ReadDir(".")
	require.NoError(t, err)
	// Every migration numbered 0014 or 0015, of this story and of others
	// (0014_claim_policy_idx of HU-W1.4-02 already is), plus two of a story
	// to come.
	want := []string{"0014_another_story", "0015_yet_another"}
	for _, e := range entries {
		body, err := migrations.FS.ReadFile(e.Name())
		require.NoError(t, err)
		files[e.Name()] = &fstest.MapFile{Data: body}
		if v := strings.TrimSuffix(e.Name(), ".sql"); strings.HasPrefix(v, "0014_") || strings.HasPrefix(v, "0015_") {
			want = append(want, v)
		}
	}
	sort.Strings(want)
	require.Contains(t, want, "0014_assistance_dispatch")
	require.Contains(t, want, "0015_assistance_provider_seed")
	files["0014_another_story.sql"] = &fstest.MapFile{Data: []byte(`CREATE TABLE another_story (id TEXT PRIMARY KEY)`)}
	files["0015_yet_another.sql"] = &fstest.MapFile{Data: []byte(`CREATE TABLE yet_another (id TEXT PRIMARY KEY)`)}
	require.NoError(t, postgres.MigrateFS(context.Background(), db, files))

	rows, err := db.Query(`SELECT version FROM schema_migrations WHERE version LIKE '0014_%' OR version LIKE '0015_%'
		ORDER BY version`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var applied []string
	for rows.Next() {
		var v string
		require.NoError(t, rows.Scan(&v))
		applied = append(applied, v)
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, want, applied)
}

func TestEveryReadAndWriteOfTheInboxFailsWithoutTheDatabase(t *testing.T) {
	requireDocker(t)
	db := newDB(t)
	require.NoError(t, db.Close())
	s := postgres.NewAssistanceInboxStore(db)
	ctx := context.Background()
	at := assistanceAt
	action := domain.AssistanceAction{Code: domain.ActionAttended, By: inboxAnalyst, At: at}

	_, err := s.List(ctx, ports.InboxFilter{Limit: 1})
	assert.Error(t, err)
	_, err = s.Case(ctx, inboxGPS)
	assert.Error(t, err)
	_, _, _, err = s.Counts(ctx, at)
	assert.Error(t, err)
	_, err = s.DispatchesSince(ctx, []string{"prv-gruas-calle-80"}, at)
	assert.Error(t, err)
	_, err = s.ClaimNoticesDue(ctx, at, time.Minute, 1)
	assert.Error(t, err)
	assert.Error(t, s.NoticeSent(ctx, inboxGPS, at))
	assert.Error(t, s.NoticeFailed(ctx, inboxGPS, at, "down"))
	assert.Error(t, s.Attend(ctx, inboxGPS, 0, action))
	assert.Error(t, s.RecordNeed(ctx, inboxGPS, 0, domain.RecordedNeed{}, action))
	assert.Error(t, s.RecordVoiceLocation(ctx, inboxGPS, 0, domain.VoiceLocation{}, action))
	assert.Error(t, s.Dispatch(ctx, inboxGPS, 0, domain.AssistanceDispatch{}, action, ports.AssistanceNotice{}))
	assert.Error(t, s.Close(ctx, inboxGPS, 0, domain.AssistanceClosure{}, action))
}

func TestADecisionWhoseRestFailsWritesNothing(t *testing.T) {
	s := newInboxStores(t)
	ctx := context.Background()
	at := s.clock.Now()
	require.NoError(t, s.store.Attend(ctx, inboxNoGPS, 0, domain.AssistanceAction{Code: domain.ActionAttended,
		By: inboxAnalyst, At: at}))

	bad := []error{
		s.store.RecordNeed(ctx, inboxNoGPS, 1, domain.RecordedNeed{Seq: 0, Need: domain.NeedTowing},
			domain.AssistanceAction{Code: domain.ActionNeedRecorded, By: inboxAnalyst, At: at}),
		s.store.RecordVoiceLocation(ctx, inboxNoGPS, 1, domain.VoiceLocation{Seq: 1, Address: "abc"},
			domain.AssistanceAction{Code: domain.ActionVoiceLocationRecorded, By: inboxAnalyst, At: at}),
		s.store.Dispatch(ctx, inboxNoGPS, 1, domain.AssistanceDispatch{Provider: domain.DispatchedProvider{
			ID: "prv-no-existe"}, Location: domain.SharedLocation{Source: domain.SharedFromApp}},
			domain.AssistanceAction{Code: domain.ActionDispatched, By: inboxAnalyst, At: at}, ports.AssistanceNotice{}),
		s.store.Close(ctx, inboxNoGPS, 1, domain.AssistanceClosure{Reason: "tired"},
			domain.AssistanceAction{Code: domain.ActionClosed, By: inboxAnalyst, At: at}),
		s.store.Attend(ctx, inboxNoGPS, 1, domain.AssistanceAction{Code: "ignored", By: inboxAnalyst, At: at}),
	}
	for i, err := range bad {
		assert.Error(t, err, i)
	}
	got, err := s.store.Case(ctx, inboxNoGPS)
	require.NoError(t, err)
	assert.Equal(t, domain.AssistanceInCall, got.Case.Request.Status, "nothing of a failed decision stays")
	assert.Len(t, got.Case.History, 1)
}
