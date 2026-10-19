package relay

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"digitalwallet/shared/events"
	"digitalwallet/shared/identifiers"
	"github.com/nats-io/nats.go"
)

type fakeStore struct {
	mu sync.Mutex

	claims       []OutboxMessage
	claimErr     error
	claimStarted chan struct{}
	claimOnce    sync.Once
	claimLimit   int32
	claimLease   time.Time
	published    []publishedCall
	failures     []failureCall
}

type publishedCall struct {
	message     OutboxMessage
	publishedAt time.Time
}

type failureCall struct {
	message       OutboxMessage
	lastError     string
	nextAttemptAt time.Time
}

func (s *fakeStore) Claim(_ context.Context, limit int32, leaseUntil time.Time) ([]OutboxMessage, error) {
	s.mu.Lock()
	s.claimLimit = limit
	s.claimLease = leaseUntil
	claims := append([]OutboxMessage(nil), s.claims...)
	s.claims = nil
	s.mu.Unlock()
	if s.claimStarted != nil {
		s.claimOnce.Do(func() { close(s.claimStarted) })
	}
	return claims, s.claimErr
}

func (s *fakeStore) MarkPublished(_ context.Context, message OutboxMessage, publishedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.published = append(s.published, publishedCall{message: message, publishedAt: publishedAt})
	return nil
}

func (s *fakeStore) MarkFailed(_ context.Context, message OutboxMessage, lastError string, nextAttemptAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures = append(s.failures, failureCall{message: message, lastError: lastError, nextAttemptAt: nextAttemptAt})
	return nil
}

type fakePublisher struct {
	mu       sync.Mutex
	messages []*nats.Msg
	err      error
}

func (p *fakePublisher) PublishMsg(message *nats.Msg, _ ...nats.PubOpt) (*nats.PubAck, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.messages = append(p.messages, message)
	if p.err != nil {
		return nil, p.err
	}
	return &nats.PubAck{}, nil
}

func testMessage(t *testing.T) OutboxMessage {
	t.Helper()
	ids := identifiers.NewDeterministicGenerator(time.UnixMilli(1000))
	eventID, err := ids.Generate()
	if err != nil {
		t.Fatal(err)
	}
	tenantID, err := ids.Generate()
	if err != nil {
		t.Fatal(err)
	}
	aggregateID, err := ids.Generate()
	if err != nil {
		t.Fatal(err)
	}
	correlationID, err := ids.Generate()
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := events.NewEnvelope(
		eventID, "wallet.created", 1, time.Unix(1000, 0).UTC(), tenantID, "wallet", aggregateID, 1,
		"wallet-source", correlationID, nil, map[string]any{"wallet_id": aggregateID.String()}, map[string]any{},
	)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := envelope.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	return OutboxMessage{
		TenantID: tenantID, ID: aggregateID, EventID: eventID, CreatedAt: time.Unix(1001, 0).UTC(),
		Attempts: 1, LeaseUntil: time.Unix(1100, 0).UTC(), AggregateType: envelope.AggregateType,
		EventType: envelope.EventType, Payload: payload,
	}
}

func TestPublishOnePublishesWithEventIDAndMarksAfterPubAck(t *testing.T) {
	message := testMessage(t)
	store := &fakeStore{}
	publisher := &fakePublisher{}
	relay := newRelay(store, publisher, Config{Jitter: func(int, identifiers.UUID, time.Duration) time.Duration { return 0 }})
	relay.now = func() time.Time { return time.Unix(1200, 0).UTC() }

	if err := relay.publishOne(context.Background(), message); err != nil {
		t.Fatalf("publishOne() error = %v", err)
	}
	if len(publisher.messages) != 1 {
		t.Fatalf("PublishMsg calls = %d, want 1", len(publisher.messages))
	}
	natsMessage := publisher.messages[0]
	wantSubject := "events." + message.TenantID.String() + ".wallet.wallet.created"
	if natsMessage.Subject != wantSubject {
		t.Fatalf("subject = %q, want %q", natsMessage.Subject, wantSubject)
	}
	if got := natsMessage.Header.Get(events.NatsMsgIDHeader); got != message.EventID.String() {
		t.Fatalf("Nats-Msg-Id = %q, want %q", got, message.EventID)
	}
	if len(store.published) != 1 || !store.published[0].publishedAt.Equal(time.Unix(1200, 0).UTC()) {
		t.Fatalf("published calls = %+v", store.published)
	}
	if len(store.failures) != 0 {
		t.Fatalf("failure calls = %+v, want none", store.failures)
	}
}

func TestPublishFailureRecordsExponentialBackoffAndClearsLease(t *testing.T) {
	message := testMessage(t)
	store := &fakeStore{}
	publisher := &fakePublisher{err: errors.New("NATS unavailable")}
	relay := newRelay(store, publisher, Config{
		BaseBackoff: 2 * time.Second,
		MaxBackoff:  10 * time.Second,
		Jitter:      func(int, identifiers.UUID, time.Duration) time.Duration { return 0 },
	})
	relay.now = func() time.Time { return time.Unix(1200, 0).UTC() }

	err := relay.publishOne(context.Background(), message)
	if err == nil || !errors.Is(err, publisher.err) {
		t.Fatalf("publishOne() error = %v, want NATS error", err)
	}
	if len(store.published) != 0 {
		t.Fatalf("published calls = %+v, want none", store.published)
	}
	if len(store.failures) != 1 {
		t.Fatalf("failure calls = %+v, want one", store.failures)
	}
	failure := store.failures[0]
	if failure.lastError != "publish event: NATS unavailable" {
		t.Fatalf("last error = %q", failure.lastError)
	}
	// Attempts starts at one, so the second attempt uses base*2.
	if !failure.nextAttemptAt.Equal(time.Unix(1200, 0).UTC().Add(4 * time.Second)) {
		t.Fatalf("next attempt = %s, want %s", failure.nextAttemptAt, time.Unix(1204, 0).UTC())
	}
	if !failure.message.LeaseUntil.Equal(message.LeaseUntil) {
		t.Fatalf("failure lease identity = %s, want %s", failure.message.LeaseUntil, message.LeaseUntil)
	}
}

func TestAfterPublishFailpointLeavesMessageUnmarked(t *testing.T) {
	message := testMessage(t)
	store := &fakeStore{}
	publisher := &fakePublisher{}
	relay := newRelay(store, publisher, Config{
		afterPublish: func(OutboxMessage) error { return errors.New("simulate process crash") },
	})

	err := relay.publishOne(context.Background(), message)
	if !errors.Is(err, errAfterPublish) {
		t.Fatalf("publishOne() error = %v, want after-publish failpoint", err)
	}
	if len(publisher.messages) != 1 {
		t.Fatalf("PublishMsg calls = %d, want 1", len(publisher.messages))
	}
	if len(store.published) != 0 || len(store.failures) != 0 {
		t.Fatalf("metadata calls published=%d failures=%d, want neither", len(store.published), len(store.failures))
	}
}

func TestRunUsesConfiguredClaimAndStopsGracefully(t *testing.T) {
	claimStarted := make(chan struct{})
	store := &fakeStore{claimStarted: claimStarted}
	relay := newRelay(store, &fakePublisher{}, Config{BatchSize: 7, PollInterval: time.Hour, LeaseDuration: time.Minute, PublishTimeout: time.Second})
	runDone := make(chan error, 1)
	go func() { runDone <- relay.Run(context.Background()) }()

	select {
	case <-claimStarted:
	case <-time.After(time.Second):
		t.Fatal("relay did not claim a batch")
	}
	stopContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := relay.Stop(stopContext); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if err := <-runDone; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if store.claimLimit != 7 {
		t.Fatalf("claim limit = %d, want 7", store.claimLimit)
	}
	if !store.claimLease.After(time.Now()) {
		t.Fatalf("claim lease = %s, want future lease", store.claimLease)
	}
}

func TestNewForTestingAppliesFailpointHook(t *testing.T) {
	hookCalled := false
	relay := NewForTesting(nil, nil, Config{}, func(OutboxMessage) error {
		hookCalled = true
		return nil
	})
	if relay.config.afterPublish == nil {
		t.Fatal("expected afterPublish hook to be set")
	}
	_ = relay.config.afterPublish(OutboxMessage{})
	if !hookCalled {
		t.Fatal("expected afterPublish hook to be called")
	}
}
