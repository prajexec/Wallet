package relay

import (
	"errors"
	"slices"
	"time"

	"github.com/nats-io/nats.go"
)

const WalletEventsStream = "WALLET_EVENTS"

// StreamManager is the JetStream management surface needed by bootstrap. A
// nats.JetStreamContext satisfies it; the smaller seam also keeps bootstrap
// tests independent of a running NATS server.
type StreamManager interface {
	AddStream(*nats.StreamConfig, ...nats.JSOpt) (*nats.StreamInfo, error)
	UpdateStream(*nats.StreamConfig, ...nats.JSOpt) (*nats.StreamInfo, error)
	StreamInfo(string, ...nats.JSOpt) (*nats.StreamInfo, error)
}

// WalletEventsStreamConfig returns the desired durable event stream settings.
func WalletEventsStreamConfig() nats.StreamConfig {
	return WalletEventsStreamConfigWithPrefix(WalletEventsStream, "events")
}

// WalletEventsStreamConfigWithPrefix returns the desired durable event stream settings with a custom stream and subject prefix.
func WalletEventsStreamConfigWithPrefix(streamName, subjectPrefix string) nats.StreamConfig {
	if streamName == "" {
		streamName = WalletEventsStream
	}
	if subjectPrefix == "" {
		subjectPrefix = "events"
	}
	return nats.StreamConfig{
		Name:              streamName,
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

// BootstrapWalletEvents creates or reconciles WALLET_EVENTS. Repeated calls
// are safe and a concurrent creator is handled by looking the stream up again.
func BootstrapWalletEvents(jetStream nats.JetStreamContext) (*nats.StreamInfo, error) {
	return BootstrapWalletEventsWithPrefix(jetStream, WalletEventsStream, "events")
}

// BootstrapWalletEventsWithPrefix creates or reconciles the stream with custom name and subject prefix.
func BootstrapWalletEventsWithPrefix(jetStream nats.JetStreamContext, streamName, subjectPrefix string) (*nats.StreamInfo, error) {
	return bootstrapWalletEvents(jetStream, streamName, subjectPrefix)
}

func bootstrapWalletEvents(manager StreamManager, streamName, subjectPrefix string) (*nats.StreamInfo, error) {
	if manager == nil {
		return nil, errors.New("wallet events stream manager is nil")
	}
	if streamName == "" {
		streamName = WalletEventsStream
	}
	if subjectPrefix == "" {
		subjectPrefix = "events"
	}
	desired := WalletEventsStreamConfigWithPrefix(streamName, subjectPrefix)
	info, err := manager.StreamInfo(streamName)
	if errors.Is(err, nats.ErrStreamNotFound) {
		info, err = manager.AddStream(&desired)
		if err == nil {
			return info, nil
		}
		if !errors.Is(err, nats.ErrStreamNameAlreadyInUse) {
			return nil, err
		}
		info, err = manager.StreamInfo(streamName)
	}
	if err != nil {
		return nil, err
	}
	if streamMatches(info.Config, desired) {
		return info, nil
	}
	updated := info.Config
	updated.Name = desired.Name
	updated.Subjects = append([]string(nil), desired.Subjects...)
	updated.Storage = desired.Storage
	updated.Duplicates = desired.Duplicates
	return manager.UpdateStream(&updated)
}

func streamMatches(actual, desired nats.StreamConfig) bool {
	return actual.Name == desired.Name &&
		slices.Equal(actual.Subjects, desired.Subjects) &&
		actual.Storage == desired.Storage &&
		actual.Duplicates == desired.Duplicates
}
