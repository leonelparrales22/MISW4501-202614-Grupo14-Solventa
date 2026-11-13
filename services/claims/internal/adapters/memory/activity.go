package memory

import (
	"context"
	"sort"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// DecisionReader reads the decision of a claim, for what the tally adds up of
// the claims paid.
type DecisionReader interface {
	Decision(ctx context.Context, claimID string) (domain.Decision, error)
}

// ClaimActivity is the ports.ClaimActivity over the claims of a CaseStore and,
// for what the claims paid, the decisions beside them (HU-W2.1-02).
type ClaimActivity struct {
	cases     *CaseStore
	decisions DecisionReader
}

var _ ports.ClaimActivity = (*ClaimActivity)(nil)

// NewClaimActivity builds the reads over the claims of cases. decisions may be
// nil: the claims paid are then counted without their amount.
func NewClaimActivity(cases *CaseStore, decisions DecisionReader) *ClaimActivity {
	return &ClaimActivity{cases: cases, decisions: decisions}
}

// LastChanges returns when each claim of the ids reached its current status.
func (a *ClaimActivity) LastChanges(_ context.Context, ids []string) (map[string]time.Time, error) {
	wanted := make(map[string]bool, len(ids))
	for _, id := range ids {
		wanted[id] = true
	}
	a.cases.mu.Lock()
	defer a.cases.mu.Unlock()
	out := map[string]time.Time{}
	for _, c := range a.cases.changes {
		if wanted[c.ClaimID] && c.ChangedAt.After(out[c.ClaimID]) {
			out[c.ClaimID] = c.ChangedAt
		}
	}
	return out, nil
}

// Tally counts the subject's claims as PostgreSQL does.
func (a *ClaimActivity) Tally(ctx context.Context, subject string, since time.Time) (domain.ClaimTally, error) {
	t := domain.ClaimTally{ByStatus: map[domain.Status]int{}}
	var paid, returned []domain.ClaimRecord
	policies := map[string]domain.PolicyRef{}
	newest := map[string]time.Time{}
	a.cases.mu.Lock()
	for _, c := range a.cases.byID {
		if c.Subject != subject {
			continue
		}
		t.ByStatus[c.Status]++
		ref, seen := policies[c.PolicyID]
		if !seen {
			ref = domain.PolicyRef{ID: c.PolicyID}
		}
		if p := c.Policy; p != nil && !c.FiledAt.Before(newest[c.PolicyID]) {
			ref.Line, ref.Product, ref.InsuredObject = p.Line, p.Product, p.InsuredObject
			newest[c.PolicyID] = c.FiledAt
		}
		policies[c.PolicyID] = ref
		switch c.Status {
		case domain.StatusPaid:
			paid = append(paid, c)
		case domain.StatusReturned:
			returned = append(returned, c)
		}
	}
	a.cases.mu.Unlock()

	for _, c := range paid {
		if a.decisions == nil {
			continue
		}
		d, err := a.decisions.Decision(ctx, c.ID)
		if err != nil || d.Payout == nil || d.Payout.Status != idempotency.StatusConfirmed ||
			d.Payout.ConfirmedAt.Before(since) {
			continue
		}
		t.PaidSince++
		t.PaidAmountMinor += d.Payout.AmountMinor
		t.Currency = d.Payout.Currency
	}

	sort.Slice(returned, func(i, j int) bool {
		if !returned[i].FiledAt.Equal(returned[j].FiledAt) {
			return returned[i].FiledAt.After(returned[j].FiledAt)
		}
		return returned[i].ID > returned[j].ID
	})
	for _, c := range returned {
		if len(t.PendingAction) == domain.MaxPendingAction {
			break
		}
		t.PendingAction = append(t.PendingAction, domain.ClaimRef{ID: c.ID, FilingNumber: c.FilingNumber})
	}
	for _, p := range policies {
		t.Policies = append(t.Policies, p)
	}
	sort.Slice(t.Policies, func(i, j int) bool { return t.Policies[i].ID < t.Policies[j].ID })
	return t, nil
}
