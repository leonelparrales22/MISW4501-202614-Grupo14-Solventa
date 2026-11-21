package main

import (
	"context"
	"fmt"
	"log"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/email"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/mail"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/assistance"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// assistanceInboxStoreOf keeps the decisions of the inbox (HU-W2.3-03) where
// the requests are: in the same database, or in memory over the very store
// the phone writes its requests to.
func assistanceInboxStoreOf(requests ports.AssistanceStore,
	records ports.ClaimRecords) (ports.AssistanceInboxStore, error) {
	switch r := records.(type) {
	case *postgres.ClaimRecordStore:
		return postgres.AssistanceInboxStoreOf(r), nil
	case *memory.CaseStore:
		if mem, ok := requests.(*memory.AssistanceStore); ok {
			return memory.NewAssistanceInboxStore(mem), nil
		}
	}
	return nil, fmt.Errorf("the inbox of assistance has no store for the claims of %T", records)
}

// assistanceInboxRoutes mounts the inbox of operations of the requests for
// assistance (HU-W2.3-03) over the requests of HU-M2.3-01 and the registry of
// providers of HU-W2.3-01, and starts the worker that sends the notices of
// the dispatches, which runs until ctx ends. It needs no policy core. Without
// the e-mail provider the requests are still dispatched, and the notices wait
// until it is configured.
func assistanceInboxRoutes(ctx context.Context, requests ports.AssistanceStore, records ports.ClaimRecords,
	providers ports.ProviderDirectory, getenv func(string) string,
	verifier security.Verifier) ([]httpadapter.Option, error) {
	store, err := assistanceInboxStoreOf(requests, records)
	if err != nil {
		return nil, err
	}
	var mailer ports.Mailer
	if url := getenv(envEmailURL); url != "" {
		mailer = mail.NewMailer(email.NewClient(email.Config{BaseURL: url, Breaker: resilience.DefaultConfig()}))
	} else {
		log.Printf("%s is not set: the notices of the dispatches of assistance (HU-W2.3-03) wait until it is",
			envEmailURL)
	}
	worker := assistance.NewNoticeWorker(store, mailer, systemClock{}, assistance.NoticeConfig{})
	svc := assistance.NewInboxService(store, providers, systemClock{}, worker.Notify)
	go worker.Run(ctx)
	policy, assignment := svc.Policy()
	log.Printf("assistance inbox (HU-W2.3-03): policy %s, radii of %s", policy.Version, assignment.Version)
	return []httpadapter.Option{httpadapter.WithAssistanceInbox(svc, verifier)}, nil
}
