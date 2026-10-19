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
	return nats.StreamConfig{
		Name:              WalletEventsStream,
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

// BootstrapWalletEvents creates or reconciles WALLET_EVENTS. Repeated calls
// are safe and a concurrent creator is handled by looking the stream up again.
func BootstrapWalletEvents(jetStream nats.JetStreamContext) (*nats.StreamInfo, error) {
	return bootstrapWalletEvents(jetStream)
}

func bootstrapWalletEvents(manager StreamManager) (*nats.StreamInfo, error) {
	if manager == nil {
		return nil, errors.New("wallet events stream manager is nil")
	}
	desired := WalletEventsStreamConfig()
	info, err := manager.StreamInfo(WalletEventsStream)
	if errors.Is(err, nats.ErrStreamNotFound) {
		info, err = manager.AddStream(&desired)
		if err == nil {
			return info, nil
		}
		if !errors.Is(err, nats.ErrStreamNameAlreadyInUse) {
			return nil, err
		}
		info, err = manager.StreamInfo(WalletEventsStream)
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
