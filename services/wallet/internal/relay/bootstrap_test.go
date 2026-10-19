package relay

import (
	"testing"

	"github.com/nats-io/nats.go"
)

type fakeStreamManager struct {
	info        *nats.StreamInfo
	infoErr     error
	addCalls    int
	updateCalls int
}

func (m *fakeStreamManager) AddStream(config *nats.StreamConfig, _ ...nats.JSOpt) (*nats.StreamInfo, error) {
	m.addCalls++
	info := &nats.StreamInfo{Config: *config}
	m.info = info
	return info, nil
}

func (m *fakeStreamManager) UpdateStream(config *nats.StreamConfig, _ ...nats.JSOpt) (*nats.StreamInfo, error) {
	m.updateCalls++
	m.info = &nats.StreamInfo{Config: *config}
	return m.info, nil
}

func (m *fakeStreamManager) StreamInfo(_ string, _ ...nats.JSOpt) (*nats.StreamInfo, error) {
	if m.infoErr != nil {
		return nil, m.infoErr
	}
	if m.info == nil {
		return nil, nats.ErrStreamNotFound
	}
	return m.info, nil
}

func TestBootstrapWalletEventsIsIdempotentAndReconcilesConfig(t *testing.T) {
	manager := &fakeStreamManager{}
	if _, err := bootstrapWalletEvents(manager); err != nil {
		t.Fatalf("first bootstrap error = %v", err)
	}
	if manager.addCalls != 1 || manager.updateCalls != 0 {
		t.Fatalf("first bootstrap calls add=%d update=%d", manager.addCalls, manager.updateCalls)
	}
	if _, err := bootstrapWalletEvents(manager); err != nil {
		t.Fatalf("second bootstrap error = %v", err)
	}
	if manager.addCalls != 1 || manager.updateCalls != 0 {
		t.Fatalf("second bootstrap calls add=%d update=%d", manager.addCalls, manager.updateCalls)
	}

	manager.info.Config.Storage = nats.MemoryStorage
	if _, err := bootstrapWalletEvents(manager); err != nil {
		t.Fatalf("reconcile bootstrap error = %v", err)
	}
	if manager.updateCalls != 1 || manager.info.Config.Storage != nats.FileStorage {
		t.Fatalf("reconcile calls update=%d config=%+v", manager.updateCalls, manager.info.Config)
	}
}
