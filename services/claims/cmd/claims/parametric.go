package main

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/events"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/events/snspublisher"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/events/sqsconsumer"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/outbox"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/messaging"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/decision"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/parametric"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// Environment of the parametric engine and the domain events (HU-M2.4-01).
const (
	// envEventsQueueURL is the SQS queue of claims, subscribed to the topic
	// of the external events. Without it no observation is consumed.
	envEventsQueueURL = "EVENTS_QUEUE_URL"
	// envEventsTopicARN is the SNS topic of claims' domain events. Without it
	// the events stay in the outbox.
	envEventsTopicARN = "EVENTS_TOPIC_ARN"
	// envOutboxPollInterval is how often the relay looks for pending events.
	envOutboxPollInterval = "OUTBOX_POLL_INTERVAL_MS"
)

const (
	defaultOutboxPollInterval = time.Second
	minOutboxPollIntervalMS   = 100
	maxOutboxPollIntervalMS   = 60_000
)

type parametricConfig struct {
	queueURL     string
	topicARN     string
	pollInterval time.Duration
}

// loadParametricConfig reads the queue, the topic and the poll of the relay.
func loadParametricConfig(getenv func(string) string) (parametricConfig, error) {
	cfg := parametricConfig{queueURL: getenv(envEventsQueueURL), topicARN: getenv(envEventsTopicARN),
		pollInterval: defaultOutboxPollInterval}
	if raw := getenv(envOutboxPollInterval); raw != "" {
		ms, err := strconv.Atoi(raw)
		if err != nil || ms < minOutboxPollIntervalMS || ms > maxOutboxPollIntervalMS {
			return parametricConfig{}, fmt.Errorf("%s must be a whole number of milliseconds between %d and %d, got %q",
				envOutboxPollInterval, minOutboxPollIntervalMS, maxOutboxPollIntervalMS, raw)
		}
		cfg.pollInterval = time.Duration(ms) * time.Millisecond
	}
	return cfg, nil
}

// startParametric starts the relay of the outbox and the consumer of the
// parametric engine, which run until ctx ends. The engine needs the database
// (the claim, its resolution and its payout are one transaction and the event
// goes to the outbox), the policy core (the policy and the account of the
// insured) and the payout worker (the gateway); without any of them it is not
// mounted, and the rest of the service keeps answering.
func startParametric(ctx context.Context, records ports.ClaimRecords, terms ports.PolicyTermsReader,
	payouts *decision.Worker, getenv func(string) string) error {
	cfg, err := loadParametricConfig(getenv)
	if err != nil {
		return err
	}
	store, ok := records.(*postgres.ClaimRecordStore)
	if !ok {
		log.Printf("%s is not set: the parametric engine (HU-M2.4-01) is not mounted", envDatabaseURL)
		return nil
	}
	if err := startRelay(ctx, cfg, postgres.OutboxStoreOf(store)); err != nil {
		return err
	}
	switch {
	case terms == nil:
		log.Printf("%s is not set: the parametric engine (HU-M2.4-01) is not mounted", envPolicyCoreURL)
		return nil
	case payouts == nil:
		log.Printf("%s is not set: the parametric engine (HU-M2.4-01) is not mounted", envPaymentGatewayURL)
		return nil
	case cfg.queueURL == "":
		log.Printf("%s is not set: the parametric engine (HU-M2.4-01) consumes no observation", envEventsQueueURL)
		return nil
	}
	engine := parametric.NewService(terms, store, domain.DefaultParametricRules(), systemClock{}, payouts.Notify)
	router := events.NewRouter()
	if err := router.Register(messaging.EventTypeParametricTriggerObserved, messaging.NewParametricTriggerHandler(engine)); err != nil {
		return err
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return fmt.Errorf("load the AWS configuration for %s: %w", envEventsQueueURL, err)
	}
	consumer, err := sqsconsumer.New(sqs.NewFromConfig(awsCfg), cfg.queueURL, router, sqsconsumer.Options{})
	if err != nil {
		return err
	}
	go consumer.Run(ctx)
	log.Printf("parametric engine (HU-M2.4-01): consuming %s from %s, rules %s",
		messaging.EventTypeParametricTriggerObserved, cfg.queueURL, domain.ParametricRulesVersion)
	return nil
}

// startRelay publishes the outbox to the topic of claims, with the
// credentials and the region of the environment (the task role and
// AWS_REGION in ECS; AWS_ENDPOINT_URL_SNS points to LocalStack in local runs).
func startRelay(ctx context.Context, cfg parametricConfig, store outbox.Store) error {
	if cfg.topicARN == "" {
		log.Printf("%s is not set: the domain events (ParametricPayoutCompleted) stay in the outbox", envEventsTopicARN)
		return nil
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return fmt.Errorf("load the AWS configuration for %s: %w", envEventsTopicARN, err)
	}
	publisher, err := snspublisher.New(sns.NewFromConfig(awsCfg), cfg.topicARN)
	if err != nil {
		return err
	}
	relayCfg := outbox.DefaultConfig()
	relayCfg.Interval = cfg.pollInterval
	relay, err := outbox.NewRelay(store, publisher, time.Now, relayCfg)
	if err != nil {
		return err
	}
	go relay.Run(ctx)
	log.Printf("domain events: outbox relay to %s every %s", cfg.topicARN, cfg.pollInterval)
	return nil
}
