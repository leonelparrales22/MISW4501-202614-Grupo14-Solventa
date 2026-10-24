// This file checks the persistence of the revocation (HU-M1.3-02) against a
// real PostgreSQL: the record is atomic, the three new tables are
// append-only, their references are enforced by triggers (decision #89), the
// receipt numbers never repeat and the claim of a notice is won by exactly
// one worker per step, as experiment EC-D02 requires.
package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/adapters/pdf"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/consents"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/ports"
)

// revocationFor builds the revocation of the consent id by the holder, with a
// notice to each entity.
func revocationFor(t *testing.T, e *env, consentID, requestID string, entities ...string) (domain.Revocation, []ports.NoticeRecord) {
	t.Helper()
	c, err := postgres.NewConsentStore(e.db).Get(context.Background(), consentID)
	require.NoError(t, err)
	effective := e.clock.Now().UTC()
	deadline := effective.Add(domain.RevocationNoticeDeadline)
	rev := domain.Revocation{
		ID: "rvk-" + requestID, ConsentID: c.ID, Holder: c.Holder, Scope: c.Scope,
		RequestedAt: effective.Add(-time.Second), EffectiveAt: effective, NoticeDeadlineAt: deadline,
		Channel: domain.RevocationChannelMobileApp, Device: "Pixel 8", ConfirmationMethod: domain.ConfirmationBiometric,
		Actor: domain.Actor{Type: domain.ActorHolder, ID: "sub-a"}, RequestID: requestID,
	}
	var notices []ports.NoticeRecord
	for _, code := range entities {
		key, err := idempotency.Key("revocation", c.ID+"-"+code)
		require.NoError(t, err)
		rev.Entities = append(rev.Entities, domain.Entity{Code: code, Name: code})
		notices = append(notices, ports.NoticeRecord{ConsentID: c.ID, EntityCode: code, Key: key, WindowEndsAt: deadline, NextAttemptAt: effective})
	}
	return rev, notices
}

func (e *env) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, e.db.QueryRow(query, args...).Scan(&n))
	return n
}

func pgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

func TestTheRevocationIsRecordedAtomically(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id := e.grantFor(t, "cust-a", "app-a", "1020304050")
	store := postgres.NewRevocationStore(e.db)

	// The second notice repeats the entity: its insert fails after the
	// revocation and its events were written.
	rev, notices := revocationFor(t, e, id, "req-1", "aliado-banco-1", "aliado-banco-1")
	_, _, err := store.Record(ctx, rev, notices)

	require.Error(t, err)
	for _, table := range []string{"consent_revocation", "consent_event", "revocation_notice", "revocation_notice_step"} {
		assert.Equal(t, 0, e.count(t, "SELECT count(*) FROM "+table), "a failure in the middle leaves nothing in %s", table)
	}

	rev, notices = revocationFor(t, e, id, "req-2", "aliado-banco-1")
	stored, created, err := store.Record(ctx, rev, notices)
	require.NoError(t, err)
	assert.True(t, created, "the consent can still be revoked")
	assert.True(t, domain.ValidReceiptNumber(stored.ReceiptNumber))
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM consent_revocation"))
	assert.Equal(t, 2, e.count(t, "SELECT count(*) FROM consent_event"))
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM revocation_notice"))
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM revocation_notice_step"))
}

func TestTheRevocationTablesAreAppendOnly(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id := e.grantFor(t, "cust-a", "app-a", "1020304050")
	rev, notices := revocationFor(t, e, id, "req-1", "aliado-banco-1")
	_, _, err := postgres.NewRevocationStore(e.db).Record(ctx, rev, notices)
	require.NoError(t, err)

	for _, stmt := range []string{
		`UPDATE consent_revocation SET device = 'x'`, `DELETE FROM consent_revocation`, `TRUNCATE consent_revocation`,
		`UPDATE revocation_notice SET idempotency_key = 'x'`, `DELETE FROM revocation_notice`, `TRUNCATE revocation_notice`,
		`UPDATE revocation_notice_step SET status = 'confirmed'`, `DELETE FROM revocation_notice_step`, `TRUNCATE revocation_notice_step`,
		`TRUNCATE consent_record`, `TRUNCATE consent_record CASCADE`,
	} {
		_, err := e.db.ExecContext(ctx, stmt)
		assert.Equal(t, "23001", pgCode(err), "%s: %v", stmt, err)
	}
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM consent_revocation WHERE device = 'Pixel 8'"))
}

func TestTheReferencesAreEnforcedByTriggers(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id := e.grantFor(t, "cust-a", "app-a", "1020304050")

	_, err := e.db.ExecContext(ctx, `INSERT INTO consent_revocation (revocation_id, consent_id, receipt_number, holder_document_id,
		holder_full_name, scope, entities, requested_at, effective_at, notice_deadline_at, channel, actor_type, actor_id, request_id)
		VALUES ('rvk-x', 'cns-missing', 'REV-2026-9999', 'X', 'X', '[]', '[]', now(), now(), now(), 'mobile_app', 'holder', 's', 'r')`)
	assert.Equal(t, "23503", pgCode(err), "a revocation of a consent that does not exist")
	_, err = e.db.ExecContext(ctx, `INSERT INTO revocation_notice (consent_id, entity_code, idempotency_key) VALUES ('cns-missing', 'b', 'k')`)
	assert.Equal(t, "23503", pgCode(err), "a notice of a consent that does not exist")
	_, err = e.db.ExecContext(ctx, `INSERT INTO revocation_notice_step (consent_id, entity_code, seq, status, window_ends_at)
		VALUES ($1, 'no-header', 1, 'pending', now())`, id)
	assert.Equal(t, "23503", pgCode(err), "a step without its notice")
	_, err = e.db.ExecContext(ctx, `INSERT INTO revocation_notice (consent_id, entity_code, idempotency_key) VALUES (NULL, 'b', 'k')`)
	assert.Equal(t, "23502", pgCode(err), "NOT NULL decides on a NULL consent_id, as with a foreign key")

	for _, table := range []string{"consent_revocation", "revocation_notice", "revocation_notice_step"} {
		assert.Equal(t, 0, e.count(t, "SELECT count(*) FROM "+table))
	}
}

func TestTheReceiptNumbersNeverRepeat(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	store := postgres.NewRevocationStore(e.db)
	const n = 20
	ids := make([]string, n)
	for i := range ids {
		ids[i] = e.grantFor(t, fmt.Sprintf("cust-%d", i), fmt.Sprintf("app-%d", i), "1020304050")
	}

	numbers := make(chan string, n)
	var wg sync.WaitGroup
	for i, id := range ids {
		rev, notices := revocationFor(t, e, id, fmt.Sprintf("req-%d", i), "aliado-banco-1")
		wg.Add(1)
		go func() {
			defer wg.Done()
			stored, created, err := store.Record(ctx, rev, notices)
			assert.NoError(t, err)
			assert.True(t, created)
			numbers <- stored.ReceiptNumber
		}()
	}
	wg.Wait()
	close(numbers)

	seen := map[string]bool{}
	for num := range numbers {
		assert.True(t, domain.ValidReceiptNumber(num), num)
		assert.False(t, seen[num], "repeated %s", num)
		seen[num] = true
	}
	assert.Len(t, seen, n)
	assert.Equal(t, n, e.count(t, "SELECT count(DISTINCT receipt_number) FROM consent_revocation"))
}

func TestTheSameRevocationRequestedTwiceAtOnceIsRecordedOnce(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id := e.grantFor(t, "cust-a", "app-a", "1020304050")
	store := postgres.NewRevocationStore(e.db)

	var created, already atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		// Half with the same key (a retried request), half with another key
		// (two devices at once).
		request := "req-same"
		if i%2 == 1 {
			request = fmt.Sprintf("req-%d", i)
		}
		rev, notices := revocationFor(t, e, id, request, "aliado-banco-1")
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, ok, err := store.Record(ctx, rev, notices)
			switch {
			case errors.Is(err, domain.ErrAlreadyRevoked):
				already.Add(1)
			case err != nil:
				t.Errorf("record: %v", err)
			case ok:
				created.Add(1)
			}
		}()
	}
	wg.Wait()

	assert.EqualValues(t, 1, created.Load(), "one revocation")
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM consent_revocation"))
	assert.Equal(t, 2, e.count(t, "SELECT count(*) FROM consent_event"))
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM revocation_notice_step"))
	assert.Positive(t, already.Load(), "the other keys are told the consent is already revoked")
}

// EC-D02: twenty workers claim the same due notice at once; for every step
// exactly one of them wins, so the entity is called once per attempt.
func TestOneWorkerClaimsEachStepOfANotice(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id := e.grantFor(t, "cust-a", "app-a", "1020304050")
	rev, notices := revocationFor(t, e, id, "req-1", "aliado-banco-1")
	_, _, err := postgres.NewRevocationStore(e.db).Record(ctx, rev, notices)
	require.NoError(t, err)
	store := postgres.NewNoticeStore(e.db)
	noticeID := ports.NoticeID(id, "aliado-banco-1")

	race := func(now time.Time) ports.NoticeRecord {
		t.Helper()
		var wins atomic.Int32
		var won ports.NoticeRecord
		var mu sync.Mutex
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				rec, claimed, err := store.Claim(ctx, noticeID, now, 5*time.Second)
				assert.NoError(t, err)
				if claimed {
					wins.Add(1)
					mu.Lock()
					won = rec
					mu.Unlock()
				}
			}()
		}
		close(start)
		wg.Wait()
		require.EqualValues(t, 1, wins.Load(), "exactly one claim at %s", now)
		return won
	}

	now := rev.EffectiveAt
	rec := race(now)
	_, err = store.Reschedule(ctx, rec, now, now.Add(time.Second), "status 503")
	require.NoError(t, err)
	rec = race(now.Add(time.Second))
	_, err = store.Confirm(ctx, rec, now.Add(2*time.Second), idempotency.Outcome{Reference: "ref"})
	require.NoError(t, err)

	assert.Equal(t, 5, e.count(t, "SELECT count(*) FROM revocation_notice_step WHERE consent_id = $1", id),
		"pending, in progress, pending, in progress, confirmed")
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM consent_event WHERE status = 'revocation_confirmed'"))
}

func TestARecordAfterTheLeaseExpiredAndAnotherClaimIsLost(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id := e.grantFor(t, "cust-a", "app-a", "1020304050")
	rev, notices := revocationFor(t, e, id, "req-1", "aliado-banco-1")
	_, _, err := postgres.NewRevocationStore(e.db).Record(ctx, rev, notices)
	require.NoError(t, err)
	store := postgres.NewNoticeStore(e.db)
	noticeID := ports.NoticeID(id, "aliado-banco-1")
	now := rev.EffectiveAt

	slow, claimed, err := store.Claim(ctx, noticeID, now, time.Second)
	require.NoError(t, err)
	require.True(t, claimed)
	_, claimed, err = store.Claim(ctx, noticeID, now.Add(500*time.Millisecond), time.Second)
	require.NoError(t, err)
	assert.False(t, claimed, "the lease still holds")
	fresh, claimed, err := store.Claim(ctx, noticeID, now.Add(1500*time.Millisecond), time.Second)
	require.NoError(t, err)
	require.True(t, claimed, "the lease expired: another worker claims it")

	_, err = store.Confirm(ctx, slow, now.Add(1600*time.Millisecond), idempotency.Outcome{})
	assert.ErrorIs(t, err, idempotency.ErrClaimLost)
	_, err = store.Reschedule(ctx, slow, now, now.Add(time.Second), "late")
	assert.ErrorIs(t, err, idempotency.ErrClaimLost)
	_, err = store.Fail(ctx, slow, now, idempotency.ReasonRejected, "late")
	assert.ErrorIs(t, err, idempotency.ErrClaimLost)
	assert.Equal(t, 0, e.count(t, "SELECT count(*) FROM consent_event WHERE entity_code IS NOT NULL"), "the lost claim wrote no event")

	_, err = store.Confirm(ctx, fresh, now.Add(1700*time.Millisecond), idempotency.Outcome{})
	require.NoError(t, err)
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM consent_event WHERE status = 'revocation_confirmed'"))
}

// Criterion 3: the verification origination asks before any query answers
// active=false as soon as the revocation is recorded; another customer's
// consent is not affected.
func TestAConsentIsNotVerifiedAsActiveRightAfterItsRevocation(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id := e.grantFor(t, "cust-a", "app-a", "1020304050")
	e.grantFor(t, "cust-b", "app-b", "9988776655")

	status, before := e.do(http.MethodGet, "/v1/audit/consent-verifications?customer_id=cust-a", "", "")
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, true, before["active"])

	rev, notices := revocationFor(t, e, id, "req-1", "aliado-banco-1")
	_, _, err := postgres.NewRevocationStore(e.db).Record(ctx, rev, notices)
	require.NoError(t, err)

	status, after := e.do(http.MethodGet, "/v1/audit/consent-verifications?customer_id=cust-a", "", "")
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, false, after["active"], "inactive at once, with no wait for the entities")
	assert.Equal(t, id, after["consent_id"])
	assert.Equal(t, []any{}, after["sources"])

	status, other := e.do(http.MethodGet, "/v1/audit/consent-verifications?customer_id=cust-b", "", "")
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, true, other["active"], "the consent of another customer is not affected")
}

// holderRouter mounts the holder's routes of HU-M1.3-01 and HU-M1.3-02 over
// the real stores, as cmd/audit does, with A (sub-a) and B (sub-b) linked.
func (e *env) holderRouter(t *testing.T) http.Handler {
	t.Helper()
	ctx := context.Background()
	links := postgres.NewHolderLinkStore(e.db)
	for _, l := range []domain.HolderLink{
		{Subject: "sub-a", DocumentID: "1020304050", CustomerID: "cus-a", LinkedAt: e.clock.Now(), EventID: "customer-linked-cus-a"},
		{Subject: "sub-b", DocumentID: "9988776655", CustomerID: "cus-b", LinkedAt: e.clock.Now(), EventID: "customer-linked-cus-b"},
	} {
		_, _, err := links.Link(ctx, l)
		require.NoError(t, err)
	}
	consentStore, events := postgres.NewConsentStore(e.db), postgres.NewConsentEventStore(e.db)
	names := domain.EntityNames{"aliado-banco-1": "Banco Aliado"}
	holders := consents.NewHolderService(consentStore, events, links, e.clock, names)
	revocations := consents.NewRevocationService(consents.RevocationDeps{
		Consents: consentStore, Events: events, Revocations: postgres.NewRevocationStore(e.db), Notices: postgres.NewNoticeStore(e.db),
		Receipts: pdf.NewUncompressed(), Clock: e.clock, Names: names,
	})
	verifier := tokenVerifier{
		"insured-a": {Subject: "sub-a", Roles: []security.Role{security.RoleInsured}},
		"insured-b": {Subject: "sub-b", Roles: []security.Role{security.RoleInsured}},
	}
	return httpadapter.NewRouter(httpadapter.WithHolderConsents(holders, verifier),
		httpadapter.WithHolderRevocations(holders, revocations, verifier))
}

func send(h http.Handler, method, target, token, key, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// CA-3, CA-4 and CA-6 over HTTP against PostgreSQL: the record exists when the
// 201 arrives, the consent shows revoked and not revocable, the same key
// returns the same revocation, another key is a conflict, the receipt is a
// PDF and B never reaches A's consent.
func TestTheHolderRevokesOverHTTPAgainstTheRealStores(t *testing.T) {
	e := newEnv(t)
	id := e.grantFor(t, "cust-a", "app-a", "1020304050")
	h := e.holderRouter(t)
	e.clock.set(e.clock.Now().Add(time.Hour))
	key := "0b7e3f5a-2c1d-4e8f-9a6b-1c2d3e4f5a6b"
	requested := e.clock.Now().Add(-3 * time.Second).Format(time.RFC3339)
	body := `{"requested_at":"` + requested + `","device":"Google Pixel 8","confirmation_method":"biometric"}`

	rec := send(h, http.MethodPost, "/v1/consents/"+id+"/revocations", "insured-a", key, body)

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Equal(t, 1, e.count(t, `SELECT count(*) FROM consent_revocation WHERE consent_id = $1 AND requested_at = $2
		AND channel = 'mobile_app' AND device = 'Google Pixel 8' AND actor_type = 'holder' AND actor_id = 'sub-a'
		AND request_id = $3`, id, requested, key), "recorded with every field of CA-4 before the answer")
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.Equal(t, "revoked", out["status"])
	assert.True(t, domain.ValidReceiptNumber(out["receipt_number"].(string)))
	assert.Equal(t, "in_progress", out["notice_status"])

	detail := send(h, http.MethodGet, "/v1/consents/"+id, "insured-a", "", "")
	require.Equal(t, http.StatusOK, detail.Code)
	var consent map[string]any
	require.NoError(t, json.Unmarshal(detail.Body.Bytes(), &consent))
	assert.Equal(t, "revoked", consent["status"])
	assert.Equal(t, false, consent["revocable"])
	assert.Equal(t, out["receipt_number"], consent["revocation_receipt_id"])

	again := send(h, http.MethodPost, "/v1/consents/"+id+"/revocations", "insured-a", key, body)
	assert.Equal(t, http.StatusOK, again.Code)
	assert.JSONEq(t, rec.Body.String(), again.Body.String(), "the same revocation")
	other := send(h, http.MethodPost, "/v1/consents/"+id+"/revocations", "insured-a", "1b7e3f5a-2c1d-4e8f-9a6b-1c2d3e4f5a6b", body)
	assert.Equal(t, http.StatusConflict, other.Code)
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM consent_revocation"))

	receipt := send(h, http.MethodGet, "/v1/consents/"+id+"/revocation-receipt", "insured-a", "", "")
	require.Equal(t, http.StatusOK, receipt.Code)
	assert.Equal(t, "application/pdf", receipt.Header().Get("Content-Type"))
	assert.Contains(t, receipt.Body.String(), out["receipt_number"].(string))
	assert.Contains(t, receipt.Body.String(), "******4050")

	assert.Equal(t, http.StatusNotFound, send(h, http.MethodGet, "/v1/consents/"+id+"/revocation-receipt", "insured-b", "", "").Code)
	assert.Equal(t, http.StatusNotFound, send(h, http.MethodPost, "/v1/consents/"+id+"/revocations", "insured-b",
		"2b7e3f5a-2c1d-4e8f-9a6b-1c2d3e4f5a6b", body).Code)
}

func TestAnExpiredConsentCannotBeRevokedOverHTTP(t *testing.T) {
	e := newEnv(t)
	id := e.grantFor(t, "cust-a", "app-a", "1020304050")
	h := e.holderRouter(t)
	e.clock.set(e.clock.Now().AddDate(1, 0, 0))

	rec := send(h, http.MethodPost, "/v1/consents/"+id+"/revocations", "insured-a", "0b7e3f5a-2c1d-4e8f-9a6b-1c2d3e4f5a6b",
		`{"requested_at":"`+e.clock.Now().Format(time.RFC3339)+`","device":"Pixel","confirmation_method":"password"}`)

	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Contains(t, rec.Body.String(), "consent_not_active")
	assert.Equal(t, 0, e.count(t, "SELECT count(*) FROM consent_revocation"))
}
