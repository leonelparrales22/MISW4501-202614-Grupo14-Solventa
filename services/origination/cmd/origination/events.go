package main

import (
	"context"
	"fmt"
	"log"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sns"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/events/snspublisher"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/outbox"
)

// Environment of the domain events (HU-M1.3-01).
const (
	// envEventsTopicARN is the SNS topic of origination's domain events
	// (modules/messaging). Without it the events stay in the outbox.
	envEventsTopicARN = "EVENTS_TOPIC_ARN"
	// envOutboxPollInterval is how often the relay looks for pending events.
	envOutboxPollInterval = "OUTBOX_POLL_INTERVAL_MS"
)

// defaultOutboxPollInterval is the poll of the relay when nothing is set.
const defaultOutboxPollInterval = time.Second

// noTopicNotice is logged when EVENTS_TOPIC_ARN is empty.
const noTopicNotice = envEventsTopicARN + " is not set: domain events (CustomerLinked) stay in the outbox and are not published"

type eventsConfig struct {
	topicARN     string
	pollInterval time.Duration
}

// loadEventsConfig reads the topic and the poll interval of the relay. An
// invalid interval stops the service.
func loadEventsConfig(getenv func(string) string, cfg *eventsConfig) error {
	cfg.topicARN = getenv(envEventsTopicARN)
	ms, err := intVar(getenv, envOutboxPollInterval, int(defaultOutboxPollInterval/time.Millisecond))
	if err != nil {
		return err
	}
	cfg.pollInterval = time.Duration(ms) * time.Millisecond
	return nil
}

// newEventRelay builds the relay that publishes the outbox to the topic, with
// the credentials and the region of the environment (the task role and
// AWS_REGION in ECS; AWS_ENDPOINT_URL_SNS points to LocalStack in local runs).
// It returns nil, and logs a notice, when there is no topic.
func newEventRelay(ctx context.Context, cfg eventsConfig, store outbox.Store) (*outbox.Relay, error) {
	if cfg.topicARN == "" {
		log.Print(noTopicNotice)
		return nil, nil
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("load the AWS configuration for %s: %w", envEventsTopicARN, err)
	}
	publisher, err := snspublisher.New(sns.NewFromConfig(awsCfg), cfg.topicARN)
	if err != nil {
		return nil, err
	}
	relayCfg := outbox.DefaultConfig()
	relayCfg.Interval = cfg.pollInterval
	relay, err := outbox.NewRelay(store, publisher, time.Now, relayCfg)
	if err != nil {
		return nil, err
	}
	log.Printf("domain events: outbox relay to %s every %s", cfg.topicARN, cfg.pollInterval)
	return relay, nil
}
