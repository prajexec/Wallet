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

## Phase 3 ledger read measurement

Measured locally on 2026-10-19 against PostgreSQL 17 in the Compose container using the wallet's authoritative balance query and the existing `(tenant_id, account_id, currency)` index:

```sql
SELECT COALESCE(SUM(CASE WHEN side = 'CREDIT' THEN amount_minor ELSE -amount_minor END), 0)::bigint
FROM journal_postings
WHERE tenant_id = $1 AND account_id = $2 AND currency = 'USD';
```

Dataset: one user-wallet account with **100,000 postings** and a matching 100,000 postings on its clearing account, warm local PostgreSQL cache, `EXPLAIN (ANALYZE, BUFFERS)` after `ANALYZE journal_postings`.

Observed result:

| Metric | Result |
| --- | --- |
| Plan | Bitmap index scan on `journal_postings_account_balance_idx` followed by bitmap heap scan and aggregate |
| Rows aggregated | 100,000 |
| Shared buffers | 1,808 hits |
| Planning time | 0.560 ms |
| Execution time | 18.098 ms |

This is a local diagnostic, not a performance acceptance target. It shows that a journal-derived balance is currently practical at this scale with the composite index, but the cost is linear in the account's postings. A derived snapshot remains a Phase 5 read-model concern and must not replace the journal authority.

### Phase 4 outbox relay throughput measurement

Measured locally on 2026-10-20 using the transactional leased outbox relay worker in `services/wallet/internal/relay` against PostgreSQL 17 and NATS JetStream 2.10:

- **Workload:** 1,000 outbox rows claimed with `FOR UPDATE SKIP LOCKED`, published sequentially to JetStream stream `WALLET_EVENTS`, and marked published upon PubAck receipt.
- **Measured throughput:** ~100 events/second.
- **Assessment:** Acceptable for Phase 4's sequential, strictly ordered outbox relay requirement and local Compose stack verification.
- **Phase 8 optimization:** Async batched publishing with pipelined JetStream publish futures and batch status updates is scheduled as a performance enhancement in Phase 8 to scale outbox throughput beyond 1,000 events/sec without compromising at-least-once delivery semantics.

## Phase 8 harness

The executable harness lives in `benchmarks/` and exercises the running Compose
services through their public and internal HTTP contracts. It records per-request
accepted/rejected/error counts, p50/p95/p99/max latency, throughput, and workload
correctness checks. Results are written to `.ecc/benchmarks/phase8.json` and this
document by the Make target:

```text
make benchmark ARGS="-ops=50 -concurrency=16 -repeats=3"
```

The six workload names are `hot-wallet-withdrawals`, `even-wallet-deposits`,
`transfer-saga`, `outbox-relay`, `projection-catch-up`, and `reconciliation`.
Use `-workloads=<comma-separated names>` to run a subset. A benchmark run requires
the local stack and migrations (`make up && make migrate`). It is diagnostic only
when a service is unavailable or a correctness check fails.

Phase 8 results are generated below by the harness; no numbers are invented when
the local stack cannot be built or reached.

## Phase 8 results

Generated 2026-10-21T16:42:47Z with ops=50, concurrency=16.

| Workload | Run | Metric | Count | Accepted | Rejected | Errors | p50 ms | p95 ms | p99 ms | Throughput/s |
| --- | ---: | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| hot-wallet-withdrawals | 1 | withdrawal | 50 | 25 | 25 | 0 | 52.738 | 179.134 | 188.739 | 187.13 |
| even-wallet-deposits | 1 | deposit | 50 | 50 | 0 | 0 | 19.591 | 26.626 | 28.104 | 757.90 |
| transfer-saga | 1 | end_to_end | 50 | 50 | 0 | 0 | 480.831 | 719.471 | 724.179 | 32.39 |
| outbox-relay | 1 | publish_lag | 1 | 1 | 0 | 0 | 172.797 | 172.797 | 172.797 | 5.79 |
| projection-catch-up | 1 | write_to_read_model | 50 | 50 | 0 | 0 | 303.571 | 383.402 | 404.502 | 43.96 |
| reconciliation | 1 | scan_and_invariant_check | 1 | 1 | 0 | 0 | 55.171 | 55.171 | 55.171 | 18.13 |
| hot-wallet-withdrawals | 2 | withdrawal | 50 | 25 | 25 | 0 | 21.974 | 41.915 | 44.185 | 674.99 |
| even-wallet-deposits | 2 | deposit | 50 | 50 | 0 | 0 | 16.470 | 21.523 | 21.844 | 871.76 |
| transfer-saga | 2 | end_to_end | 50 | 50 | 0 | 0 | 370.585 | 502.254 | 567.357 | 41.02 |
| outbox-relay | 2 | publish_lag | 1 | 1 | 0 | 0 | 220.778 | 220.778 | 220.778 | 4.53 |
| projection-catch-up | 2 | write_to_read_model | 50 | 50 | 0 | 0 | 264.195 | 398.576 | 418.750 | 44.25 |
| reconciliation | 2 | scan_and_invariant_check | 1 | 1 | 0 | 0 | 36.041 | 36.041 | 36.041 | 27.75 |
| hot-wallet-withdrawals | 3 | withdrawal | 50 | 25 | 25 | 0 | 19.891 | 35.883 | 36.056 | 737.70 |
| even-wallet-deposits | 3 | deposit | 50 | 50 | 0 | 0 | 13.638 | 29.148 | 37.779 | 722.76 |
| transfer-saga | 3 | end_to_end | 50 | 50 | 0 | 0 | 343.333 | 487.568 | 511.416 | 43.46 |
| outbox-relay | 3 | publish_lag | 1 | 1 | 0 | 0 | 29.032 | 29.032 | 29.032 | 34.44 |
| projection-catch-up | 3 | write_to_read_model | 50 | 50 | 0 | 0 | 277.647 | 593.987 | 600.940 | 43.71 |
| reconciliation | 3 | scan_and_invariant_check | 1 | 1 | 0 | 0 | 39.252 | 39.252 | 39.252 | 25.48 |

Checks are recorded in `.ecc/benchmarks/phase8.json`; a run with non-zero errors or failed correctness checks is diagnostic only.
