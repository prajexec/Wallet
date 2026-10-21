package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func main() {
	ops := flag.Int("ops", envInt("PHASE8_OPS", 50), "operations per workload")
	concurrency := flag.Int("concurrency", envInt("PHASE8_CONCURRENCY", 16), "concurrent clients")
	repeats := flag.Int("repeats", envInt("PHASE8_REPEATS", 1), "runs per workload")
	workloadFlag := flag.String("workloads", envOr("PHASE8_WORKLOADS", "all"), "comma-separated workload names")
	out := flag.String("out", "../docs/benchmarking.md", "benchmark result markdown path")
	jsonOut := flag.String("json-out", "../.ecc/benchmarks/phase8.json", "benchmark result JSON path")
	flag.Parse()

	c := &client{
		http:             &http.Client{Timeout: 15 * time.Second},
		wallet:           envOr("PHASE8_WALLET_URL", "http://127.0.0.1:8081"),
		walletStats:      envOr("PHASE8_WALLET_INTERNAL_URL", "http://127.0.0.1:8181"),
		destination:      envOr("PHASE8_DESTINATION_URL", "http://127.0.0.1:8082"),
		transfer:         envOr("PHASE8_TRANSFER_URL", "http://127.0.0.1:8083"),
		transferInternal: envOr("PHASE8_TRANSFER_INTERNAL_URL", "http://127.0.0.1:8183"),
		projector:        envOr("PHASE8_PROJECTOR_URL", "http://127.0.0.1:8084"),
		internal:         envOr("PHASE8_INTERNAL_TOKEN", "internal-secret-token"),
		operator:         envOr("PHASE8_OPERATOR_ID", "phase8-benchmark"),
	}
	cfg := config{Ops: *ops, Concurrency: *concurrency, PollTimeout: 30 * time.Second}
	if cfg.Ops < 1 || cfg.Concurrency < 1 || *repeats < 1 {
		fatalf("ops, concurrency, and repeats must be positive")
	}

	selected := make(map[string]bool)
	for _, name := range strings.Split(*workloadFlag, ",") {
		selected[strings.TrimSpace(name)] = true
	}
	ctx := context.Background()
	var results []Result
	for run := 1; run <= *repeats; run++ {
		for _, workload := range []struct {
			name string
			fn   func(context.Context, *client, config) Result
		}{
			{"hot-wallet-withdrawals", runHotWallet},
			{"even-wallet-deposits", runEvenDeposits},
			{"transfer-saga", runTransferSaga},
			{"outbox-relay", runOutboxRelay},
			{"projection-catch-up", runProjectionCatchup},
			{"reconciliation", runReconciliation},
		} {
			if !selected["all"] && !selected[workload.name] {
				continue
			}
			result := workload.fn(ctx, c, cfg)
			result.Run = run
			results = append(results, result)
			fmt.Printf("%s run=%d metrics=%v checks=%v\n", result.Workload, run, result.Metrics, result.Checks)
		}
	}

	encoded, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		fatalf("encode results: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(*jsonOut), 0o755); err != nil {
		fatalf("create JSON output directory: %v", err)
	}
	if err := os.WriteFile(*jsonOut, encoded, 0o644); err != nil {
		fatalf("write JSON results: %v", err)
	}
	if err := updateDocs(*out, cfg, results); err != nil {
		fatalf("write benchmark docs: %v", err)
	}
}

func updateDocs(path string, cfg config, results []Result) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	const marker = "\n## Phase 8 results\n"
	base := string(content)
	if index := strings.Index(base, marker); index >= 0 {
		base = base[:index]
	}
	var builder strings.Builder
	builder.WriteString(base)
	builder.WriteString(marker)
	fmt.Fprintf(&builder, "\nGenerated %s with ops=%d, concurrency=%d.\n\n", time.Now().UTC().Format(time.RFC3339), cfg.Ops, cfg.Concurrency)
	builder.WriteString("| Workload | Run | Metric | Count | Accepted | Rejected | Errors | p50 ms | p95 ms | p99 ms | Throughput/s |\n| --- | ---: | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |\n")
	for _, result := range results {
		for name, metric := range result.Metrics {
			fmt.Fprintf(&builder, "| %s | %d | %s | %d | %d | %d | %d | %.3f | %.3f | %.3f | %.2f |\n", result.Workload, result.Run, name, metric.Count, metric.Accepted, metric.Rejected, metric.Errors, metric.P50, metric.P95, metric.P99, metric.Throughput)
		}
	}
	builder.WriteString("\nChecks are recorded in `.ecc/benchmarks/phase8.json`; a run with non-zero errors or failed correctness checks is diagnostic only.\n")
	return os.WriteFile(path, []byte(builder.String()), 0o644)
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func envInt(name string, fallback int) int {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
