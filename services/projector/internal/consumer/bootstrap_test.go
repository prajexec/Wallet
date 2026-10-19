package consumer

import (
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

type fakeConsumerManager struct {
	streamInfo      *nats.StreamInfo
	streamErr       error
	consumerInfo    *nats.ConsumerInfo
	consumerErr     error
	addStreamCalls  int
	addConsCalls    int
	updateConsCalls int
}

func (f *fakeConsumerManager) AddStream(config *nats.StreamConfig, _ ...nats.JSOpt) (*nats.StreamInfo, error) {
	f.addStreamCalls++
	f.streamInfo = &nats.StreamInfo{Config: *config}
	return f.streamInfo, nil
}

func (f *fakeConsumerManager) StreamInfo(name string, _ ...nats.JSOpt) (*nats.StreamInfo, error) {
	if f.streamErr != nil {
		return nil, f.streamErr
	}
	if f.streamInfo == nil {
		return nil, nats.ErrStreamNotFound
	}
	return f.streamInfo, nil
}

func (f *fakeConsumerManager) AddConsumer(stream string, config *nats.ConsumerConfig, _ ...nats.JSOpt) (*nats.ConsumerInfo, error) {
	f.addConsCalls++
	f.consumerInfo = &nats.ConsumerInfo{Config: *config}
	return f.consumerInfo, nil
}

func (f *fakeConsumerManager) UpdateConsumer(stream string, config *nats.ConsumerConfig, _ ...nats.JSOpt) (*nats.ConsumerInfo, error) {
	f.updateConsCalls++
	f.consumerInfo = &nats.ConsumerInfo{Config: *config}
	return f.consumerInfo, nil
}

func (f *fakeConsumerManager) ConsumerInfo(stream, name string, _ ...nats.JSOpt) (*nats.ConsumerInfo, error) {
	if f.consumerErr != nil {
		return nil, f.consumerErr
	}
	if f.consumerInfo == nil {
		return nil, nats.ErrConsumerNotFound
	}
	return f.consumerInfo, nil
}

func TestBootstrapConsumerIsIdempotentAndReconciles(t *testing.T) {
	mgr := &fakeConsumerManager{}
	info, err := bootstrap(mgr, "WALLET_EVENTS", "projector")
	if err != nil {
		t.Fatalf("first bootstrap error = %v", err)
	}
	if info == nil || info.Config.Durable != "projector" {
		t.Fatalf("unexpected info: %+v", info)
	}
	if mgr.addStreamCalls != 1 || mgr.addConsCalls != 1 || mgr.updateConsCalls != 0 {
		t.Fatalf("call counts stream=%d addCons=%d updateCons=%d", mgr.addStreamCalls, mgr.addConsCalls, mgr.updateConsCalls)
	}

	// Second run: no extra creates or updates
	_, err = bootstrap(mgr, "WALLET_EVENTS", "projector")
	if err != nil {
		t.Fatalf("second bootstrap error = %v", err)
	}
	if mgr.addStreamCalls != 1 || mgr.addConsCalls != 1 || mgr.updateConsCalls != 0 {
		t.Fatalf("second run call counts stream=%d addCons=%d updateCons=%d", mgr.addStreamCalls, mgr.addConsCalls, mgr.updateConsCalls)
	}

	// Configuration drift trigger update
	mgr.consumerInfo.Config.AckWait = 5 * time.Second
	_, err = bootstrap(mgr, "WALLET_EVENTS", "projector")
	if err != nil {
		t.Fatalf("reconcile bootstrap error = %v", err)
	}
	if mgr.updateConsCalls != 1 || mgr.consumerInfo.Config.AckWait != 30*time.Second {
		t.Fatalf("update calls = %d, ack wait = %v", mgr.updateConsCalls, mgr.consumerInfo.Config.AckWait)
	}
}
