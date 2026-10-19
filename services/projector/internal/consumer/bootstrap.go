// Package consumer provides JetStream consumer bootstrap, ordered delivery,
// and durable projection consumption for the projector service.
package consumer

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
)

// ConsumerManager defines the JetStream stream and consumer management methods
// required for bootstrap. A nats.JetStreamContext satisfies this interface.
type ConsumerManager interface {
	AddStream(*nats.StreamConfig, ...nats.JSOpt) (*nats.StreamInfo, error)
	StreamInfo(string, ...nats.JSOpt) (*nats.StreamInfo, error)
	AddConsumer(string, *nats.ConsumerConfig, ...nats.JSOpt) (*nats.ConsumerInfo, error)
	UpdateConsumer(string, *nats.ConsumerConfig, ...nats.JSOpt) (*nats.ConsumerInfo, error)
	ConsumerInfo(string, string, ...nats.JSOpt) (*nats.ConsumerInfo, error)
}

// DefaultStreamConfig returns the standard file-backed WALLET_EVENTS stream configuration.
func DefaultStreamConfig(name string) nats.StreamConfig {
	if strings.TrimSpace(name) == "" {
		name = "WALLET_EVENTS"
	}
	return nats.StreamConfig{
		Name:              name,
		Subjects:          []string{"events.>"},
		Storage:           nats.FileStorage,
		Duplicates:        2 * time.Minute,
		Retention:         nats.LimitsPolicy,
		Discard:           nats.DiscardOld,
		MaxMsgs:           -1,
		MaxBytes:          -1,
		MaxAge:            0,
		MaxMsgsPerSubject: -1,
	}
}

// DefaultConsumerConfig returns the durable pull consumer settings for projector.
// Every setting is explicit and documented:
// - Durable: identifies the consumer state in JetStream across restarts
// - DeliverPolicy DeliverAll: ensure all events from aggregate version 1 onward are delivered
// - AckPolicy AckExplicit: individual message ACK only after the database transaction commits
// - AckWait 30s: generous window for local database transaction while bound for dead-letter/redelivery
// - MaxDeliver 5: poison message limit before recording into dead_letter_events
// - MaxAckPending 100: bounds projector in-flight work and memory
// - FilterSubject events.>: matches all tenant domain events in the stream
func DefaultConsumerConfig(streamName, consumerName string) nats.ConsumerConfig {
	if strings.TrimSpace(consumerName) == "" {
		consumerName = "projector"
	}
	return nats.ConsumerConfig{
		Durable:       consumerName,
		Name:          consumerName,
		Description:   "Durable ordered pull consumer for projector read models",
		DeliverPolicy: nats.DeliverAllPolicy,
		AckPolicy:     nats.AckExplicitPolicy,
		AckWait:       30 * time.Second,
		MaxDeliver:    5,
		MaxAckPending: 100,
		FilterSubject: "events.>",
	}
}

// BootstrapConsumer creates or reconciles both the stream (if absent) and the durable
// pull consumer in NATS JetStream idempotently.
func BootstrapConsumer(jetStream nats.JetStreamContext, streamName, consumerName string) (*nats.ConsumerInfo, error) {
	return bootstrap(jetStream, streamName, consumerName)
}

func bootstrap(manager ConsumerManager, streamName, consumerName string) (*nats.ConsumerInfo, error) {
	if manager == nil {
		return nil, errors.New("jetstream consumer manager is nil")
	}
	if strings.TrimSpace(streamName) == "" {
		streamName = "WALLET_EVENTS"
	}
	if strings.TrimSpace(consumerName) == "" {
		consumerName = "projector"
	}

	// 1. Ensure stream exists.
	desiredStream := DefaultStreamConfig(streamName)
	_, err := manager.StreamInfo(streamName)
	if errors.Is(err, nats.ErrStreamNotFound) {
		_, err = manager.AddStream(&desiredStream)
		if err != nil && !errors.Is(err, nats.ErrStreamNameAlreadyInUse) {
			return nil, fmt.Errorf("create stream %q: %w", streamName, err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("lookup stream %q: %w", streamName, err)
	}

	// 2. Ensure consumer exists and matches desired config.
	desiredConsumer := DefaultConsumerConfig(streamName, consumerName)
	info, err := manager.ConsumerInfo(streamName, consumerName)
	if errors.Is(err, nats.ErrConsumerNotFound) {
		info, err = manager.AddConsumer(streamName, &desiredConsumer)
		if err == nil {
			return info, nil
		}
		if !errors.Is(err, nats.ErrConsumerNameAlreadyInUse) {
			return nil, fmt.Errorf("create consumer %q on %q: %w", consumerName, streamName, err)
		}
		info, err = manager.ConsumerInfo(streamName, consumerName)
	}
	if err != nil {
		return nil, fmt.Errorf("lookup consumer %q on %q: %w", consumerName, streamName, err)
	}

	if consumerMatches(info.Config, desiredConsumer) {
		return info, nil
	}

	updated := info.Config
	updated.Durable = desiredConsumer.Durable
	updated.DeliverPolicy = desiredConsumer.DeliverPolicy
	updated.AckPolicy = desiredConsumer.AckPolicy
	updated.AckWait = desiredConsumer.AckWait
	updated.MaxDeliver = desiredConsumer.MaxDeliver
	updated.MaxAckPending = desiredConsumer.MaxAckPending
	updated.FilterSubject = desiredConsumer.FilterSubject
	return manager.UpdateConsumer(streamName, &updated)
}

func consumerMatches(actual, desired nats.ConsumerConfig) bool {
	return actual.Durable == desired.Durable &&
		actual.DeliverPolicy == desired.DeliverPolicy &&
		actual.AckPolicy == desired.AckPolicy &&
		actual.AckWait == desired.AckWait &&
		actual.MaxDeliver == desired.MaxDeliver &&
		actual.MaxAckPending == desired.MaxAckPending &&
		actual.FilterSubject == desired.FilterSubject
}
