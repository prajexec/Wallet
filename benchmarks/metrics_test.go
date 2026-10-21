package main

import (
	"errors"
	"testing"
)

func TestRunConcurrentRecordsPercentilesAndOutcomes(t *testing.T) {
	result := runConcurrent(20, 4, func(index int) error {
		if index%5 == 0 {
			return errRejected
		}
		if index == 1 {
			return errors.New("failed")
		}
		return nil
	})
	if result.Count != 20 || result.Accepted != 15 || result.Rejected != 4 || result.Errors != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.P50 <= 0 || result.P95 <= 0 || result.P99 <= 0 || result.Throughput <= 0 {
		t.Fatalf("missing latency metrics: %+v", result)
	}
}
