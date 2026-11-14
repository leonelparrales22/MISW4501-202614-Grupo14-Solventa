package main

import (
	"context"
	"log"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/stages"
)

// startStageNotices starts the notifier of the changes of stage (HU-M3.3-01),
// which runs until ctx ends. It needs the database: it follows the history of
// the status of the claims and writes to the outbox, which the relay
// publishes when EVENTS_TOPIC_ARN is set. In memory nothing is notified.
func startStageNotices(ctx context.Context, records ports.ClaimRecords) {
	store, ok := records.(*postgres.ClaimRecordStore)
	if !ok {
		log.Printf("%s is not set: the notices of the changes of stage (HU-M3.3-01) are not written", envDatabaseURL)
		return
	}
	go stages.NewNotifier(postgres.StageNoticeStoreOf(store), stages.DefaultInterval).Run(ctx)
	log.Printf("stage notices (HU-M3.3-01): every %s, to the outbox", stages.DefaultInterval)
}
