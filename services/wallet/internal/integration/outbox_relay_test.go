package integration

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"digitalwallet/services/wallet/internal/relay"
	"digitalwallet/services/wallet/internal/repository"
	"digitalwallet/shared/identifiers"
	"github.com/nats-io/nats.go"
)

// tcpProxy simulates network partitions and server stops/restarts.
type tcpProxy struct {
	listener net.Listener
	addr     string
	target   string
	conns    map[net.Conn]struct{}
	mu       sync.Mutex
	closed   bool
}

func newTCPProxy(target string) (*tcpProxy, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	p := &tcpProxy{
		listener: listener,
		addr:     listener.Addr().String(),
		target:   target,
		conns:    make(map[net.Conn]struct{}),
	}
	go p.acceptLoop(listener)
	return p, nil
}

func (p *tcpProxy) Addr() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return "nats://" + p.addr
}

func (p *tcpProxy) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	if p.listener != nil {
		_ = p.listener.Close()
		p.listener = nil
	}
	for c := range p.conns {
		_ = c.Close()
		delete(p.conns, c)
	}
}

func (p *tcpProxy) Restart() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.listener != nil {
		return nil
	}
	p.closed = false
	listener, err := net.Listen("tcp", p.addr)
	if err != nil {
		return err
	}
	p.listener = listener
	go p.acceptLoop(listener)
	return nil
}

func (p *tcpProxy) acceptLoop(l net.Listener) {
	for {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			_ = conn.Close()
			return
		}
		p.conns[conn] = struct{}{}
		p.mu.Unlock()

		go p.handleConn(conn)
	}
}

func (p *tcpProxy) handleConn(src net.Conn) {
	defer func() {
		p.mu.Lock()
		delete(p.conns, src)
		p.mu.Unlock()
		_ = src.Close()
	}()

	dst, err := net.DialTimeout("tcp", p.target, time.Second)
	if err != nil {
		return
	}
	defer dst.Close()

	p.mu.Lock()
	p.conns[dst] = struct{}{}
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.conns, dst)
		p.mu.Unlock()
	}()

	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(dst, src)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(src, dst)
		done <- struct{}{}
	}()
	<-done
}

func getNATSConnection(t *testing.T) (*nats.Conn, nats.JetStreamContext) {
	t.Helper()
	natsURL := os.Getenv("NATS_TEST_URL")
	if natsURL == "" {
		natsURL = "nats://127.0.0.1:4222"
	}
	nc, err := nats.Connect(natsURL, nats.Timeout(2*time.Second))
	if err != nil {
		t.Skipf("cannot connect to NATS (%s): %v", natsURL, err)
	}
	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		t.Skipf("cannot create JetStream context: %v", err)
	}
	if _, err := relay.BootstrapWalletEvents(js); err != nil {
		nc.Close()
		t.Fatalf("bootstrap wallet events stream: %v", err)
	}
	return nc, js
}

func countMessagesForTenant(t *testing.T, js nats.JetStreamContext, tenantID identifiers.UUID) int {
	t.Helper()
	consumerName := "count_" + tenantID.String()
	cfg := &nats.ConsumerConfig{
		Durable:       consumerName,
		DeliverPolicy: nats.DeliverAllPolicy,
		AckPolicy:     nats.AckExplicitPolicy,
		FilterSubject: "events." + tenantID.String() + ".>",
	}
	_, err := js.AddConsumer(relay.WalletEventsStream, cfg)
	if err != nil {
		t.Fatalf("add count consumer: %v", err)
	}
	defer func() { _ = js.DeleteConsumer(relay.WalletEventsStream, consumerName) }()
	sub, err := js.PullSubscribe("events."+tenantID.String()+".>", consumerName, nats.Bind(relay.WalletEventsStream, consumerName))
	if err != nil {
		t.Fatalf("pull subscribe: %v", err)
	}
	msgs, _ := sub.Fetch(100, nats.MaxWait(1*time.Second))
	for _, m := range msgs {
		_ = m.Ack()
	}
	return len(msgs)
}

// Scenario 1: Commit succeeds while NATS is stopped. After NATS restarts,
// the event is published exactly once into the stream (verify stream message count).
func TestRelay_CommitSucceedsWhileNATSStoppedAndPublishesOnceAfterRestart(t *testing.T) {
	if testPool == nil || testRepo == nil {
		t.Skip("integration database not configured")
	}

	directNC, directJS := getNATSConnection(t)
	defer directNC.Close()

	// Clean any pre-existing unpublished outbox rows so test runs against a clean state
	_, _ = testPool.Exec(context.Background(), "UPDATE outbox_messages SET published_at = now() WHERE published_at IS NULL")

	// Start proxy
	proxy, err := newTCPProxy("127.0.0.1:4222")
	if err != nil {
		t.Fatalf("create proxy: %v", err)
	}
	defer proxy.Stop()

	// Connect relay to proxy with reconnect options
	proxyNC, err := nats.Connect(proxy.Addr(), nats.MaxReconnects(-1), nats.ReconnectWait(100*time.Millisecond))
	if err != nil {
		t.Fatalf("connect through proxy: %v", err)
	}
	defer proxyNC.Close()
	proxyJS, err := proxyNC.JetStream()
	if err != nil {
		t.Fatalf("jetstream through proxy: %v", err)
	}

	// 1. Stop proxy so NATS is stopped for the relay
	proxy.Stop()

	// 2. Commit deposit in database while NATS is stopped
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tenantID, _ := identifiers.NewGenerator().Generate()
	walletReq := struct {
		ExternalReference string `json:"external_reference"`
	}{ExternalReference: "key-1"}
	wHash, _ := repository.HashRequest(walletReq)
	res, err := testRepo.CreateWallet(ctx, tenantID, "ops-1", wHash, "key-1")
	if err != nil || res.StatusCode != 201 {
		t.Fatalf("CreateWallet failed while NATS stopped: err=%v status=%d", err, res.StatusCode)
	}
	var wallet repository.Wallet
	_ = json.Unmarshal(res.Body, &wallet)
	walletID := wallet.WalletID

	depReq := struct {
		AmountMinor int64  `json:"amount_minor"`
		Currency    string `json:"currency"`
	}{AmountMinor: 5000, Currency: "USD"}
	dHash, _ := repository.HashRequest(depReq)
	depRes, err := testRepo.Deposit(ctx, tenantID, walletID, "dep-1", dHash, 5000, "USD")
	if err != nil || (depRes.StatusCode != 200 && depRes.StatusCode != 201) {
		t.Fatalf("Deposit failed while NATS stopped: err=%v status=%d", err, depRes.StatusCode)
	}

	// Verify outbox has unpublished row
	var unpublishedCount int
	err = testPool.QueryRow(ctx, "SELECT count(*) FROM outbox_messages WHERE published_at IS NULL").Scan(&unpublishedCount)
	if err != nil || unpublishedCount < 1 {
		t.Fatalf("expected >= 1 unpublished outbox row, got %d, err=%v", unpublishedCount, err)
	}

	// 3. Start relay in background
	relayCtx, relayCancel := context.WithCancel(context.Background())
	defer relayCancel()

	r := relay.New(testPool, proxyJS, relay.Config{
		BatchSize:      10,
		PollInterval:   50 * time.Millisecond,
		LeaseDuration:  2 * time.Second,
		PublishTimeout: 200 * time.Millisecond,
		BaseBackoff:    50 * time.Millisecond,
		MaxBackoff:     200 * time.Millisecond,
	})
	go func() { _ = r.Run(relayCtx) }()

	// Wait briefly to confirm relay encounters failure while proxy is stopped
	time.Sleep(250 * time.Millisecond)

	// 4. Restart proxy (NATS becomes available again at the same address)
	if err := proxy.Restart(); err != nil {
		t.Fatalf("restart proxy: %v", err)
	}

	// Wait for outbox to drain
	deadline := time.Now().Add(5 * time.Second)
	var remaining int
	for time.Now().Before(deadline) {
		_ = testPool.QueryRow(context.Background(), "SELECT count(*) FROM outbox_messages WHERE published_at IS NULL").Scan(&remaining)
		if remaining == 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if remaining != 0 {
		t.Fatalf("outbox failed to drain: %d remaining", remaining)
	}

	// Verify stream received exactly the newly created messages for this tenant
	msgsForTenant := countMessagesForTenant(t, directJS, tenantID)
	if msgsForTenant != 2 {
		t.Fatalf("stream msgs for tenant = %d, want 2", msgsForTenant)
	}
}

// Scenario 2: Relay crashes after publish and before mark: redelivery happens,
// and the stream still holds one message (Nats-Msg-Id dedupe).
func TestRelay_CrashAfterPublishDeduplicatedInStream(t *testing.T) {
	if testPool == nil || testRepo == nil {
		t.Skip("integration database not configured")
	}

	nc, js := getNATSConnection(t)
	defer nc.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Clean any pre-existing unpublished outbox rows so test runs against a clean state
	_, _ = testPool.Exec(ctx, "UPDATE outbox_messages SET published_at = now() WHERE published_at IS NULL")

	tenantID, _ := identifiers.NewGenerator().Generate()
	walletReq := struct {
		ExternalReference string `json:"external_reference"`
	}{ExternalReference: "key-crash"}
	wHash, _ := repository.HashRequest(walletReq)
	res, err := testRepo.CreateWallet(ctx, tenantID, "ops-crash", wHash, "key-crash")
	if err != nil || res.StatusCode != 201 {
		t.Fatalf("CreateWallet error: %v", err)
	}

	// 1. Run relay with failpoint that stops immediately after PubAck but before MarkPublished
	var crashed atomic.Bool
	crashRelay := relay.NewForTesting(testPool, js, relay.Config{
		BatchSize:      1,
		PollInterval:   50 * time.Millisecond,
		LeaseDuration:  1 * time.Second, // short lease so it expires quickly
		PublishTimeout: time.Second,
	}, func(msg relay.OutboxMessage) error {
		crashed.Store(true)
		return errors.New("simulated crash after publish")
	})

	runCtx, runCancel := context.WithCancel(context.Background())
	go func() {
		_ = crashRelay.Run(runCtx)
	}()

	// Wait for failpoint to be hit
	for i := 0; i < 30; i++ {
		if crashed.Load() {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	runCancel()
	_ = crashRelay.Stop(context.Background())

	if !crashed.Load() {
		t.Fatal("failpoint was not hit")
	}

	// At this point, the event was published to JetStream, but DB row remains unpublished
	var pubAt *time.Time
	err = testPool.QueryRow(ctx, "SELECT published_at FROM outbox_messages WHERE published_at IS NULL LIMIT 1").Scan(&pubAt)
	if err != nil {
		t.Fatalf("outbox row should be unpublished: %v", err)
	}

	// Stream should have 1 message for this tenant
	firstCount := countMessagesForTenant(t, js, tenantID)
	if firstCount != 1 {
		t.Fatalf("stream msgs after first publish = %d, want 1", firstCount)
	}

	// 2. Wait for lease to expire, then run normal relay to redeliver
	time.Sleep(1200 * time.Millisecond)

	normalRelay := relay.New(testPool, js, relay.Config{
		BatchSize:      10,
		PollInterval:   50 * time.Millisecond,
		LeaseDuration:  2 * time.Second,
		PublishTimeout: time.Second,
	})

	normCtx, normCancel := context.WithCancel(context.Background())
	defer normCancel()
	go func() { _ = normalRelay.Run(normCtx) }()

	// Wait for outbox to drain
	for i := 0; i < 40; i++ {
		var unpub int
		_ = testPool.QueryRow(ctx, "SELECT count(*) FROM outbox_messages WHERE published_at IS NULL").Scan(&unpub)
		if unpub == 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Check stream message count STILL holds only 1 message (Nats-Msg-Id dedupe)!
	redeliveredCount := countMessagesForTenant(t, js, tenantID)
	if redeliveredCount != 1 {
		t.Fatalf("stream msgs after redelivery = %d, want 1 (duplicate suppressed by JetStream)", redeliveredCount)
	}
}

// Scenario 3: Two relays running concurrently on the same outbox publish every row,
// and none is claimed by both at the same time.
func TestRelay_ConcurrentRelaysPublishAllRowsWithoutDoubleClaim(t *testing.T) {
	if testPool == nil || testRepo == nil {
		t.Skip("integration database not configured")
	}

	nc, js := getNATSConnection(t)
	defer nc.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Clean any pre-existing unpublished outbox rows
	_, _ = testPool.Exec(ctx, "UPDATE outbox_messages SET published_at = now() WHERE published_at IS NULL")

	var tenantIDs []identifiers.UUID
	// Create 10 wallets, producing 10 outbox messages
	for i := 0; i < 10; i++ {
		tenantID, _ := identifiers.NewGenerator().Generate()
		tenantIDs = append(tenantIDs, tenantID)
		randID, _ := identifiers.NewGenerator().Generate()
		wReq := struct {
			ExternalReference string `json:"external_reference"`
		}{ExternalReference: "key-" + randID.String()}
		wHash, _ := repository.HashRequest(wReq)
		_, err := testRepo.CreateWallet(ctx, tenantID, "conc-"+randID.String(), wHash, "key-"+randID.String())
		if err != nil {
			t.Fatalf("create wallet: %v", err)
		}
	}

	// Start Relay 1 and Relay 2 concurrently
	relay1 := relay.New(testPool, js, relay.Config{
		BatchSize:      3,
		PollInterval:   20 * time.Millisecond,
		LeaseDuration:  2 * time.Second,
		PublishTimeout: time.Second,
	})
	relay2 := relay.New(testPool, js, relay.Config{
		BatchSize:      3,
		PollInterval:   20 * time.Millisecond,
		LeaseDuration:  2 * time.Second,
		PublishTimeout: time.Second,
	})

	rCtx, rCancel := context.WithCancel(context.Background())
	defer rCancel()

	go func() { _ = relay1.Run(rCtx) }()
	go func() { _ = relay2.Run(rCtx) }()

	// Wait for all outbox messages to be published
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var unpub int
		_ = testPool.QueryRow(ctx, "SELECT count(*) FROM outbox_messages WHERE published_at IS NULL").Scan(&unpub)
		if unpub == 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	var remainingUnpublished int
	err := testPool.QueryRow(ctx, "SELECT count(*) FROM outbox_messages WHERE published_at IS NULL").Scan(&remainingUnpublished)
	if err != nil || remainingUnpublished != 0 {
		t.Fatalf("expected 0 unpublished rows, got %d, err=%v", remainingUnpublished, err)
	}

	// Verify no messages experienced lease lost / failed attempts
	var failedAttempts int
	err = testPool.QueryRow(ctx, "SELECT count(*) FROM outbox_messages WHERE tenant_id = ANY($1) AND attempts > 1", tenantIDs).Scan(&failedAttempts)
	if err != nil {
		t.Fatalf("check failed attempts: %v", err)
	}
	if failedAttempts != 0 {
		t.Fatalf("expected 0 messages with attempts > 1 under concurrent relays, got %d", failedAttempts)
	}
}
