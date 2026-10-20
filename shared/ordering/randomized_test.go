package ordering_test

import (
	"math/rand"
	"testing"
	"time"

	"digitalwallet/shared/identifiers"
	"digitalwallet/shared/ordering"
)

func TestRandomizedOrderingState(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	ids := identifiers.NewGenerator()

	const numAggregates = 10
	const eventsPerAggregate = 15

	aggregates := make([]ordering.AggregateKey, numAggregates)
	for i := 0; i < numAggregates; i++ {
		tID, _ := ids.Generate()
		aID, _ := ids.Generate()
		aggregates[i] = ordering.AggregateKey{TenantID: tID, AggregateID: aID}
	}

	// Reference in-order application:
	refState := ordering.State{}
	for _, agg := range aggregates {
		for v := int64(1); v <= eventsPerAggregate; v++ {
			refState = refState.Apply(ordering.Event{Key: agg, AggregateVersion: v})
		}
	}

	// Verify reference state has all cursors at eventsPerAggregate with empty buffers
	for _, agg := range aggregates {
		cursor := refState.Cursors[agg]
		if cursor.CurrentVersion != eventsPerAggregate {
			t.Fatalf("ref cursor = %d, want %d", cursor.CurrentVersion, eventsPerAggregate)
		}
		if len(cursor.BufferedVersions) != 0 {
			t.Fatalf("ref buffered = %v, want empty", cursor.BufferedVersions)
		}
	}

	// Run 20 randomized trials with shuffling, duplicates, and delays
	for trial := 0; trial < 20; trial++ {
		var eventPool []ordering.Event
		for _, agg := range aggregates {
			for v := int64(1); v <= eventsPerAggregate; v++ {
				ev := ordering.Event{Key: agg, AggregateVersion: v}
				eventPool = append(eventPool, ev)
				// 30% chance of inserting duplicates
				if rng.Float64() < 0.3 {
					eventPool = append(eventPool, ev)
				}
				// 10% chance of double duplicate
				if rng.Float64() < 0.1 {
					eventPool = append(eventPool, ev)
				}
			}
		}

		// Shuffle the pool randomly
		rng.Shuffle(len(eventPool), func(i, j int) {
			eventPool[i], eventPool[j] = eventPool[j], eventPool[i]
		})

		// Apply all events to state
		testState := ordering.State{}
		for _, ev := range eventPool {
			testState = testState.Apply(ev)
		}

		// Assert that the final state matches reference state exactly
		for _, agg := range aggregates {
			refCursor := refState.Cursors[agg]
			testCursor := testState.Cursors[agg]
			if testCursor.CurrentVersion != refCursor.CurrentVersion {
				t.Fatalf("trial %d: agg %v current version = %d, want %d", trial, agg.AggregateID, testCursor.CurrentVersion, refCursor.CurrentVersion)
			}
			if len(testCursor.BufferedVersions) != 0 {
				t.Fatalf("trial %d: agg %v buffer not drained: %v", trial, agg.AggregateID, testCursor.BufferedVersions)
			}
		}
	}
}

func TestRandomizedOrderingDecisionTransitions(t *testing.T) {
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	for trial := 0; trial < 500; trial++ {
		current := int64(rng.Intn(50))
		incoming := int64(rng.Intn(60) + 1)
		bufLen := rng.Intn(10)
		buf := make([]int64, 0, bufLen)
		seen := make(map[int64]bool)
		for len(buf) < bufLen {
			v := int64(rng.Intn(70) + 1)
			if v > current && !seen[v] {
				seen[v] = true
				buf = append(buf, v)
			}
		}

		decision := ordering.Decide(ordering.Input{
			CurrentVersion:   current,
			IncomingVersion:  incoming,
			BufferedVersions: buf,
		})

		if incoming <= current {
			if decision.Kind != ordering.DuplicateOrStale {
				t.Fatalf("expected DuplicateOrStale for incoming %d <= current %d, got %s", incoming, current, decision.Kind)
			}
			if decision.NextVersion != current {
				t.Fatalf("stale event changed version: %d != %d", decision.NextVersion, current)
			}
		} else if incoming > current+1 {
			if decision.Kind != ordering.FutureBuffer {
				t.Fatalf("expected FutureBuffer for incoming %d > current+1 %d, got %s", incoming, current+1, decision.Kind)
			}
			if decision.NextVersion != current {
				t.Fatalf("future event changed version: %d != %d", decision.NextVersion, current)
			}
		} else if incoming == current+1 {
			if len(buf) > 0 && decision.Kind != ordering.GapFillDrain && decision.Kind != ordering.InOrder {
				t.Fatalf("expected GapFillDrain or InOrder for incoming %d == current+1 %d, got %s", incoming, current+1, decision.Kind)
			}
			if decision.NextVersion < incoming {
				t.Fatalf("in-order event failed to advance: %d < %d", decision.NextVersion, incoming)
			}
		}
	}
}
