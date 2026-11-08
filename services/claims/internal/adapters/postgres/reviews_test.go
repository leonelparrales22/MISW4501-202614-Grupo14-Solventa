package postgres_test

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// HU-W2.1-03: the reviews of the advisor and the history of the status, over
// a real PostgreSQL.

const reviewedClaim = "8f14e45f-ceea-467a-9f3a-5a2d0b1c7e93"

var reviewedAt = time.Date(2026, 11, 9, 20, 0, 0, 0, time.UTC)

type reviewStores struct {
	webStores
	reviews *postgres.ReviewStore
}

func newReviewStores(t *testing.T) reviewStores {
	t.Helper()
	requireDocker(t)
	s := newWebStores(t)
	_, _, err := s.claims.CreateWeb(context.Background(), webClaim(reviewedClaim, "sub-carolina", "form-r"))
	require.NoError(t, err)
	return reviewStores{webStores: s, reviews: postgres.ReviewStoreOf(s.claims)}
}

func (s reviewStores) status(t *testing.T) domain.Status {
	t.Helper()
	c, err := s.claims.Get(context.Background(), reviewedClaim)
	require.NoError(t, err)
	return c.Status
}

var advisor = domain.Reviewer{ID: "juan@solventa.example", Name: "Juan Ríos"}

// filingOfThePhone is a collision filed from the phone, with its evidence.
func filingOfThePhone() domain.ClaimRecord {
	return domain.ClaimRecord{Claim: domain.Claim{ID: reviewedClaim, IncidentType: domain.IncidentCollision,
		Status: domain.StatusActive, Evidence: make([]domain.Evidence, 4)}, Channel: domain.ChannelMobile}
}

func returnOf(t *testing.T) domain.Review {
	t.Helper()
	r, err := domain.ReturnFiling(filingOfThePhone(), advisor, domain.ReturnRequest{
		ReasonCode: domain.ReasonIllegibleOrIncompleteSupports, Message: "Falta el croquis.",
		Supports: []domain.SupportVerdict{
			{RequirementCode: domain.SupportVehiclePhotos, Verdict: domain.VerdictIllegible, Note: "borrosas"},
			{RequirementCode: domain.SupportTrafficReport, Verdict: domain.VerdictMissing},
		},
		FactsToCorrect: []domain.Fact{domain.FactPlace}}, reviewedAt)
	require.NoError(t, err)
	return r
}

func approvalOf(t *testing.T) domain.Review {
	t.Helper()
	r, err := domain.ApproveFiling(filingOfThePhone(), advisor, reviewedAt.Add(time.Hour))
	require.NoError(t, err)
	return r
}

func TestAReturnIsRecordedAndMovesTheClaim(t *testing.T) {
	s := newReviewStores(t)
	ctx := context.Background()

	r, err := s.reviews.Review(ctx, returnOf(t))

	require.NoError(t, err)
	assert.Equal(t, 1, r.Seq)
	assert.Equal(t, domain.StatusReturned, s.status(t))

	got, err := s.reviews.Reviews(ctx, reviewedClaim)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, domain.ReviewReturned, got[0].Outcome)
	assert.NotEmpty(t, got[0].FilingNumber)
	assert.Equal(t, "juan@solventa.example", got[0].ReviewedBy)
	assert.Equal(t, "Juan Ríos", got[0].ReviewerName)
	assert.True(t, got[0].ReviewedAt.Equal(reviewedAt))
	assert.Equal(t, domain.ReasonIllegibleOrIncompleteSupports, got[0].ReasonCode)
	assert.Equal(t, "Falta el croquis.", got[0].Message)
	assert.Equal(t, []domain.SupportAssessment{
		{RequirementCode: domain.SupportVehiclePhotos, Label: "Fotos del vehículo", Verdict: domain.VerdictIllegible, Note: "borrosas"},
		{RequirementCode: domain.SupportTrafficReport, Label: "Informe de tránsito o croquis", Verdict: domain.VerdictMissing},
	}, got[0].Supports)
	assert.Equal(t, []domain.Fact{domain.FactPlace}, got[0].FactsToCorrect)

	_, err = s.reviews.Review(ctx, approvalOf(t))
	assert.ErrorIs(t, err, domain.ErrNotReviewable, "a returned filing waits for the insured")
}

func TestAFilingSentAgainIsReviewedInANewRound(t *testing.T) {
	s := newReviewStores(t)
	ctx := context.Background()
	_, err := s.reviews.Review(ctx, returnOf(t))
	require.NoError(t, err)
	// HU-W2.1-04: the insured sends it again.
	_, err = s.db.Exec(`UPDATE claim SET status = 'active' WHERE id = $1`, reviewedClaim)
	require.NoError(t, err)

	r, err := s.reviews.Review(ctx, approvalOf(t))

	require.NoError(t, err)
	assert.Equal(t, 2, r.Seq)
	assert.Equal(t, domain.StatusInEvaluation, s.status(t))
	got, err := s.reviews.Reviews(ctx, reviewedClaim)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, domain.ReviewApproved, got[1].Outcome)
	assert.Empty(t, got[1].ReasonCode)
	assert.Empty(t, got[1].Message)
	assert.Equal(t, []domain.SupportAssessment{{RequirementCode: domain.SupportVehiclePhotos, Label: "Fotos del vehículo",
		Verdict: domain.VerdictValid}}, got[1].Supports)
}

func TestTwoAdvisorsReviewingAtOnceRecordOneReview(t *testing.T) {
	s := newReviewStores(t)
	ctx := context.Background()

	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := approvalOf(t)
			if i%2 == 1 {
				r = returnOf(t)
			}
			_, errs[i] = s.reviews.Review(ctx, r)
		}(i)
	}
	wg.Wait()

	won := 0
	for _, err := range errs {
		if err == nil {
			won++
			continue
		}
		assert.ErrorIs(t, err, domain.ErrNotReviewable)
	}
	assert.Equal(t, 1, won)
	got, err := s.reviews.Reviews(ctx, reviewedClaim)
	require.NoError(t, err)
	assert.Len(t, got, 1)
}

func TestAReviewOfAClaimThatDoesNotExistIsNotFound(t *testing.T) {
	s := newReviewStores(t)
	r := approvalOf(t)
	r.ClaimID = "8f14e45f-ceea-467a-9f3a-000000000000"

	_, err := s.reviews.Review(context.Background(), r)

	assert.ErrorIs(t, err, ports.ErrClaimNotFound)
	got, err := s.reviews.Reviews(context.Background(), r.ClaimID)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestTheDatabaseRecordsEveryChangeOfStatus(t *testing.T) {
	s := newReviewStores(t)
	ctx := context.Background()
	_, err := s.reviews.Review(ctx, returnOf(t))
	require.NoError(t, err)
	for _, st := range []string{"active", "active", "in_evaluation", "approved", "payment_exception", "approved", "paid"} {
		_, err := s.db.Exec(`UPDATE claim SET status = $1 WHERE id = $2`, st, reviewedClaim)
		require.NoError(t, err)
	}
	_, err = s.db.Exec(`UPDATE claim SET place = 'Otra calle' WHERE id = $1`, reviewedClaim)
	require.NoError(t, err)

	got, err := s.reviews.History(ctx, reviewedClaim)

	require.NoError(t, err)
	var statuses, previous []domain.Status
	for _, c := range got {
		statuses, previous = append(statuses, c.Status), append(previous, c.PreviousStatus)
		assert.Equal(t, reviewedClaim, c.ClaimID)
	}
	assert.Equal(t, []domain.Status{"active", "returned", "active", "in_evaluation", "approved", "payment_exception",
		"approved", "paid"}, statuses, "a write that does not change the status is not a change")
	assert.Equal(t, []domain.Status{"", "active", "returned", "active", "in_evaluation", "approved",
		"payment_exception", "approved"}, previous)
	assert.True(t, got[0].ChangedAt.Equal(filedAt), "the filing is at the time it was filed")
	for i := 1; i < len(got); i++ {
		assert.Greater(t, got[i].ID, got[i-1].ID)
	}

	other, err := s.reviews.History(ctx, "no-such-claim")
	require.NoError(t, err)
	assert.Empty(t, other)
}

func TestTheReviewsAndTheHistoryCannotBeChanged(t *testing.T) {
	s := newReviewStores(t)
	_, err := s.reviews.Review(context.Background(), returnOf(t))
	require.NoError(t, err)

	for _, stmt := range []string{
		`UPDATE claim_review SET message = 'otra'`, `DELETE FROM claim_review`, `TRUNCATE claim_review`,
		`UPDATE claim_status_change SET status = 'paid'`, `DELETE FROM claim_status_change`, `TRUNCATE claim_status_change`,
	} {
		_, err := s.db.Exec(stmt)
		assert.ErrorContains(t, err, "append-only", stmt)
	}
}

func TestTheShapeOfAReviewIsCheckedByTheDatabase(t *testing.T) {
	s := newReviewStores(t)
	for name, stmt := range map[string]string{
		"approval with a reason": `INSERT INTO claim_review (claim_id, seq, outcome, reviewed_by, reviewed_at, reason_code, message)
			VALUES ($1, 1, 'approved', 'a', now(), 'other', 'x')`,
		"return without reason": `INSERT INTO claim_review (claim_id, seq, outcome, reviewed_by, reviewed_at, message)
			VALUES ($1, 1, 'returned', 'a', now(), 'x')`,
		"return without message": `INSERT INTO claim_review (claim_id, seq, outcome, reviewed_by, reviewed_at, reason_code)
			VALUES ($1, 1, 'returned', 'a', now(), 'other')`,
		"a reason of no list": `INSERT INTO claim_review (claim_id, seq, outcome, reviewed_by, reviewed_at, reason_code, message)
			VALUES ($1, 1, 'returned', 'a', now(), 'porque', 'x')`,
		"an outcome of no review": `INSERT INTO claim_review (claim_id, seq, outcome, reviewed_by, reviewed_at)
			VALUES ($1, 1, 'rejected', 'a', now())`,
	} {
		_, err := s.db.Exec(stmt, reviewedClaim)
		assert.Error(t, err, name)
	}
}

// rewindTo0006 leaves the database as it was before 0007: no reviews, no
// history and no trigger on the claim.
func rewindTo0006(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`DROP TRIGGER claim_status_change_on_insert ON claim;
		DROP TRIGGER claim_status_change_on_update ON claim;
		DROP TABLE claim_status_change, claim_review;
		DELETE FROM schema_migrations WHERE version = '0007_claim_review'`)
	require.NoError(t, err)
}

func TestMigration0007RebuildsTheHistoryOfTheClaimsFiledBefore(t *testing.T) {
	requireDocker(t)
	ctx := context.Background()
	s := newWebStores(t)
	rewindTo0006(t, s.db)
	const (
		filed   = "8f14e45f-ceea-467a-9f3a-5a2d0b1c7e94"
		decided = "8f14e45f-ceea-467a-9f3a-5a2d0b1c7e95"
	)
	for i, id := range []string{filed, decided} {
		_, _, err := s.claims.CreateWeb(ctx, webClaim(id, "sub-carolina", "form-"+string(rune('a'+i))))
		require.NoError(t, err)
	}
	decidedAt := filedAt.Add(48 * time.Hour)
	_, err := s.db.Exec(`UPDATE claim SET status = 'rejected' WHERE id = $1`, decided)
	require.NoError(t, err)
	_, err = s.db.Exec(`INSERT INTO claim_decision (claim_id, outcome, decided_by, decided_at, rejection_reason, notice_to)
		VALUES ($1, 'rejected', 'analista', $2, 'x', 'c@correo.example')`, decided, decidedAt)
	require.NoError(t, err)

	require.NoError(t, postgres.Migrate(ctx, s.db))
	require.NoError(t, postgres.Migrate(ctx, s.db), "applying it again changes nothing")

	store := postgres.ReviewStoreOf(s.claims)
	h, err := store.History(ctx, filed)
	require.NoError(t, err)
	require.Len(t, h, 1)
	assert.Equal(t, domain.StatusActive, h[0].Status)
	assert.True(t, h[0].ChangedAt.Equal(filedAt))

	h, err = store.History(ctx, decided)
	require.NoError(t, err)
	require.Len(t, h, 2)
	assert.Equal(t, domain.StatusActive, h[0].Status)
	assert.Equal(t, domain.StatusRejected, h[1].Status, "the current status, at the time of the decision")
	assert.Equal(t, domain.StatusActive, h[1].PreviousStatus)
	assert.True(t, h[1].ChangedAt.Equal(decidedAt))
}
