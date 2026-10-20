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
	return DefaultStreamConfigWithPrefix(name, "events")
}

// DefaultStreamConfigWithPrefix returns the stream configuration with custom subject prefix.
func DefaultStreamConfigWithPrefix(name, subjectPrefix string) nats.StreamConfig {
	if strings.TrimSpace(name) == "" {
		name = "WALLET_EVENTS"
	}
	if strings.TrimSpace(subjectPrefix) == "" {
		subjectPrefix = "events"
	}
	return nats.StreamConfig{
		Name:              name,
		Subjects:          []string{subjectPrefix + ".>"},
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
func DefaultConsumerConfig(streamName, consumerName string) nats.ConsumerConfig {
	return DefaultConsumerConfigWithPrefix(streamName, consumerName, "events")
}

// DefaultConsumerConfigWithPrefix returns durable pull consumer settings with custom filter subject prefix.
func DefaultConsumerConfigWithPrefix(streamName, consumerName, subjectPrefix string) nats.ConsumerConfig {
	if strings.TrimSpace(consumerName) == "" {
		consumerName = "projector"
	}
	if strings.TrimSpace(subjectPrefix) == "" {
		subjectPrefix = "events"
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
		FilterSubject: subjectPrefix + ".>",
	}
}

// BootstrapConsumer creates or reconciles both the stream (if absent) and the durable
// pull consumer in NATS JetStream idempotently.
func BootstrapConsumer(jetStream nats.JetStreamContext, streamName, consumerName string) (*nats.ConsumerInfo, error) {
	return BootstrapConsumerWithPrefix(jetStream, streamName, consumerName, "events")
}

// BootstrapConsumerWithPrefix creates or reconciles stream and consumer with custom subject prefix.
func BootstrapConsumerWithPrefix(jetStream nats.JetStreamContext, streamName, consumerName, subjectPrefix string) (*nats.ConsumerInfo, error) {
	return bootstrap(jetStream, streamName, consumerName, subjectPrefix)
}

func bootstrap(manager ConsumerManager, streamName, consumerName, subjectPrefix string) (*nats.ConsumerInfo, error) {
	if manager == nil {
		return nil, errors.New("jetstream consumer manager is nil")
	}
	if strings.TrimSpace(streamName) == "" {
		streamName = "WALLET_EVENTS"
	}
	if strings.TrimSpace(consumerName) == "" {
		consumerName = "projector"
	}
	if strings.TrimSpace(subjectPrefix) == "" {
		subjectPrefix = "events"
	}

	// 1. Ensure stream exists.
	desiredStream := DefaultStreamConfigWithPrefix(streamName, subjectPrefix)
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
	desiredConsumer := DefaultConsumerConfigWithPrefix(streamName, consumerName, subjectPrefix)
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
