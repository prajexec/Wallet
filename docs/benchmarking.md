# Benchmarking

The comparable 1,000 TPS / 142 ms p99 result is diagnostic context only. It is not a v1 acceptance target. Benchmarks begin with contention and correctness signals, then measure latency and throughput.

## Workloads

| Workload | Shape | Purpose |
| --- | --- | --- |
| Hot-wallet withdrawals | Many clients, one wallet, sufficient/insufficient funds mix | Measure lock wait, serialization, retries, and negative-balance safety. |
| Even wallet deposits | Many wallets and tenants | Establish journal append and outbox throughput without a hot aggregate. |
| Transfer saga | Source/destination pairs with both hot and distributed accounts | Measure step latency, retries, saga age, and cross-service effects. |
| Outbox relay | Burst committed rows with multiple relays | Measure claim contention, publish lag, duplicate publish rate. |
| Projection catch-up | Replay retained event range and live tail | Measure projection lag, buffer size, and rebuild duration. |
| Reconciliation | Inject missing/ambiguous/mismatched facts | Verify detection time and that no automatic adjustment occurs. |

## Measurements

Capture per run:

- throughput and accepted/error counts;
- p50, p95, p99, and max latency per endpoint and saga step;
- PostgreSQL lock wait, transaction retry, connection pool saturation, and WAL volume;
- hot-account serialization time;
- outbox oldest age, publish attempts, and duplicate publish rate;
- NATS consumer lag and redelivery count;
- projector lag, reorder-buffer depth, gap alerts, and rebuild checksum duration;
- reconciliation exception count and time to classification.

## Method

1. Pin a commit, configuration, dataset size, tenant mix, and hardware/container limits.
2. Warm up without including startup or migration time.
3. Run a low-contention control, then increase concurrency on the hot-wallet workload.
4. Record database and broker metrics alongside client results.
5. Repeat each point at least three times; report median run and variance.
6. Verify invariants and duplicate safety after every run.
7. Never call a result a pass if money conservation or journal checks fail, even if throughput improves.

## Results

Phase 1 result: **not run**. No executable service or benchmark harness exists yet. Phase 8 will append dated results, environment, exact commands, and observed contention findings here.
