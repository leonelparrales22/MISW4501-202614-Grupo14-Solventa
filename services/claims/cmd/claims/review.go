package main

import (
	"context"
	"fmt"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/triage"
)

// reviewStore is where the reviews of the advisor (HU-W2.1-03) and the
// history of the status are read and kept: the database of the claims, or
// the memory store beside them.
type reviewStore interface {
	ports.ReviewStore
	ports.StatusHistory
}

// reviewStoreOf keeps the reviews where the claims are.
func reviewStoreOf(records ports.ClaimRecords) (reviewStore, error) {
	switch r := records.(type) {
	case *postgres.ClaimRecordStore:
		return postgres.ReviewStoreOf(r), nil
	case *memory.CaseStore:
		return memory.NewReviewStore(r), nil
	}
	return nil, fmt.Errorf("the review has no store for the claims of %T", records)
}

// priorityQueueOf orders the claims in evaluation by priority (HU-W2.2-02)
// where the claims are: in the database, or in memory with the levels of the
// triage and the decisions beside them.
func priorityQueueOf(records ports.ClaimRecords, decisions ports.DecisionStore, priorities *triage.Service) (ports.PriorityQueue, error) {
	switch r := records.(type) {
	case *postgres.ClaimRecordStore:
		return postgres.PriorityQueueOf(r), nil
	case *memory.CaseStore:
		decided, _ := decisions.(*memory.DecisionStore)
		return memory.NewPriorityQueue(r, decided, levelsOf(priorities)), nil
	}
	return nil, fmt.Errorf("the queue by priority has no store for the claims of %T", records)
}

// levelsOf reads the level of the current triage of the claims.
func levelsOf(priorities *triage.Service) memory.LevelReader {
	return func(ctx context.Context, ids []string) (map[string]int, error) {
		out := map[string]int{}
		if len(ids) == 0 {
			return out, nil
		}
		summaries, err := priorities.Summaries(ctx, ids)
		if err != nil {
			return nil, err
		}
		for _, s := range summaries {
			if s.Current != nil {
				out[s.ClaimID] = int(s.Current.Assessment.Level)
			}
		}
		return out, nil
	}
}

// resubmissionStoreOf keeps the resubmissions of returned filings
// (HU-W2.1-04) where the reviews are.
func resubmissionStoreOf(records ports.ClaimRecords, reviews reviewStore) (ports.ResubmissionStore, error) {
	switch r := records.(type) {
	case *postgres.ClaimRecordStore:
		return postgres.ResubmissionStoreOf(r), nil
	case *memory.CaseStore:
		if store, ok := reviews.(*memory.ReviewStore); ok {
			return store, nil
		}
	}
	return nil, fmt.Errorf("the resubmission has no store for the claims of %T", records)
}

// claimActivityOf reads the last change and the tally of the insured's claims
// (HU-W2.1-02) where the claims are.
func claimActivityOf(records ports.ClaimRecords, decisions ports.DecisionStore) (ports.ClaimActivity, error) {
	switch r := records.(type) {
	case *postgres.ClaimRecordStore:
		return r, nil
	case *memory.CaseStore:
		return memory.NewClaimActivity(r, decisions), nil
	}
	return nil, fmt.Errorf("the reads of the insured have no store for the claims of %T", records)
}
