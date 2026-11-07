package main

import (
	"fmt"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
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
