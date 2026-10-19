package idempotency_test

import (
	"context"
	"fmt"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
)

// The same mechanism that charges a premium in origination pays an indemnity
// in claims (HA-D02 CA-6, adopted by HU-W2.2-03): the claims service keeps its
// disbursements in its own store and calls its payment provider with a key
// derived from the claim. Here the provider is down for the first call and
// answers the second one, which carries the same key.
func ExampleRunner_disbursement() {
	now := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	key, _ := idempotency.Key("indemnizacion", "claim-42")
	store := newStore(payment{Attempt: idempotency.Attempt{
		ID: "claim-42", Key: key, Status: idempotency.StatusPending, NextAttemptAt: now,
		WindowEndsAt: resilience.DefaultRetryPolicy().WindowEnd(now),
	}, Amount: 1_200_000_00})
	runner, _ := idempotency.NewRunner[payment](store, idempotency.RunnerConfig{
		Policy: resilience.DefaultRetryPolicy(), Lease: 30 * time.Second, Now: func() time.Time { return now },
	})

	providerUp := false
	pay := func(_ context.Context, d payment) (idempotency.Outcome, error) {
		fmt.Println("disbursement sent with key", d.Key)
		if !providerUp {
			return idempotency.Outcome{}, fmt.Errorf("connection refused: %w", idempotency.ErrAmbiguous)
		}
		return idempotency.Outcome{Reference: "payout-7"}, nil
	}

	rep, _ := runner.Process(context.Background(), "claim-42", pay)
	fmt.Println(rep.Outcome, rep.Status, "next in", rep.Record.NextAttemptAt.Sub(now))

	providerUp, now = true, rep.Record.NextAttemptAt
	rep, _ = runner.Process(context.Background(), "claim-42", pay)
	fmt.Println(rep.Outcome, rep.Status, rep.Record.Reference)
	// Output:
	// disbursement sent with key indemnizacion-claim-42
	// ambiguous pending next in 500ms
	// disbursement sent with key indemnizacion-claim-42
	// confirmed confirmed payout-7
}
