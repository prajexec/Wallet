package main

import (
	"errors"
	"sort"
	"time"
)

var errRejected = errors.New("expected rejected request")

type Summary struct {
	Count      int     `json:"count"`
	Accepted   int     `json:"accepted"`
	Rejected   int     `json:"rejected"`
	Errors     int     `json:"errors"`
	P50        float64 `json:"p50_ms"`
	P95        float64 `json:"p95_ms"`
	P99        float64 `json:"p99_ms"`
	Max        float64 `json:"max_ms"`
	Throughput float64 `json:"throughput_per_sec"`
}

type Result struct {
	Workload string             `json:"workload"`
	Run      int                `json:"run"`
	Metrics  map[string]Summary `json:"metrics"`
	Checks   map[string]any     `json:"checks"`
}

func runConcurrent(count, concurrency int, fn func(int) error) Summary {
	if concurrency < 1 {
		concurrency = 1
	}
	if concurrency > count {
		concurrency = count
	}
	latencies := make([]time.Duration, count)
	results := make(chan error, count)
	jobs := make(chan int)
	started := time.Now()

	for worker := 0; worker < concurrency; worker++ {
		go func() {
			for index := range jobs {
				begin := time.Now()
				err := fn(index)
				latencies[index] = time.Since(begin)
				results <- err
			}
		}()
	}
	go func() {
		for index := 0; index < count; index++ {
			jobs <- index
		}
		close(jobs)
	}()

	accepted, rejected, failed := 0, 0, 0
	for index := 0; index < count; index++ {
		switch err := <-results; {
		case err == nil:
			accepted++
		case errors.Is(err, errRejected):
			rejected++
		default:
			failed++
		}
	}

	values := make([]float64, 0, count)
	for _, latency := range latencies {
		values = append(values, float64(latency)/float64(time.Millisecond))
	}
	sort.Float64s(values)
	percentile := func(p float64) float64 {
		if len(values) == 0 {
			return 0
		}
		index := int(float64(len(values)-1) * p)
		return values[index]
	}
	max := 0.0
	if len(values) > 0 {
		max = values[len(values)-1]
	}
	return Summary{
		Count:      count,
		Accepted:   accepted,
		Rejected:   rejected,
		Errors:     failed,
		P50:        percentile(.50),
		P95:        percentile(.95),
		P99:        percentile(.99),
		Max:        max,
		Throughput: float64(count) / time.Since(started).Seconds(),
	}
}

func oneSample(start, end time.Time) Summary {
	ms := float64(end.Sub(start)) / float64(time.Millisecond)
	throughput := 0.0
	if ms > 0 {
		throughput = 1000 / ms
	}
	return Summary{Count: 1, Accepted: 1, P50: ms, P95: ms, P99: ms, Max: ms, Throughput: throughput}
}
