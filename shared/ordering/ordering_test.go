package ordering

import (
	"math/rand"
	"reflect"
	"testing"

	"digitalwallet/shared/identifiers"
)

func TestDecideExhaustiveTable(t *testing.T) {
	tests := []struct {
		name       string
		input      Input
		wantKind   DecisionKind
		wantNext   int64
		wantApply  []int64
		wantBuffer []int64
	}{
		{
			name:       "first version is in order",
			input:      Input{CurrentVersion: 0, IncomingVersion: 1},
			wantKind:   InOrder,
			wantNext:   1,
			wantApply:  []int64{1},
			wantBuffer: nil,
		},
		{
			name:       "next version is in order",
			input:      Input{CurrentVersion: 7, IncomingVersion: 8},
			wantKind:   InOrder,
			wantNext:   8,
			wantApply:  []int64{8},
			wantBuffer: nil,
		},
		{
			name:       "equal cursor is duplicate",
			input:      Input{CurrentVersion: 7, IncomingVersion: 7},
			wantKind:   DuplicateOrStale,
			wantNext:   7,
			wantApply:  nil,
			wantBuffer: nil,
		},
		{
			name:       "lower version is stale",
			input:      Input{CurrentVersion: 7, IncomingVersion: 3},
			wantKind:   DuplicateOrStale,
			wantNext:   7,
			wantApply:  nil,
			wantBuffer: nil,
		},
		{
			name:       "future version is buffered",
			input:      Input{CurrentVersion: 1, IncomingVersion: 4},
			wantKind:   FutureBuffer,
			wantNext:   1,
			wantApply:  nil,
			wantBuffer: []int64{4},
		},
		{
			name:       "future versions remain sorted",
			input:      Input{CurrentVersion: 1, IncomingVersion: 5, BufferedVersions: []int64{4, 3}},
			wantKind:   FutureBuffer,
			wantNext:   1,
			wantApply:  nil,
			wantBuffer: []int64{3, 4, 5},
		},
		{
			name:       "gap fill without contiguous drain",
			input:      Input{CurrentVersion: 1, IncomingVersion: 2, BufferedVersions: []int64{4}},
			wantKind:   GapFillDrain,
			wantNext:   2,
			wantApply:  []int64{2},
			wantBuffer: []int64{4},
		},
		{
			name:       "gap fill drains buffered versions",
			input:      Input{CurrentVersion: 1, IncomingVersion: 2, BufferedVersions: []int64{4, 3}},
			wantKind:   GapFillDrain,
			wantNext:   4,
			wantApply:  []int64{2, 3, 4},
			wantBuffer: nil,
		},
		{
			name:       "buffer duplicate version is not duplicated",
			input:      Input{CurrentVersion: 1, IncomingVersion: 4, BufferedVersions: []int64{4, 3, 3}},
			wantKind:   FutureBuffer,
			wantNext:   1,
			wantApply:  nil,
			wantBuffer: []int64{3, 4},
		},
		{
			name:       "stale buffer is discarded from canonical state",
			input:      Input{CurrentVersion: 3, IncomingVersion: 4, BufferedVersions: []int64{2}},
			wantKind:   InOrder,
			wantNext:   4,
			wantApply:  []int64{4},
			wantBuffer: nil,
		},
		{
			name:       "zero incoming version is invalid",
			input:      Input{CurrentVersion: 0, IncomingVersion: 0},
			wantKind:   InvalidInput,
			wantNext:   0,
			wantApply:  nil,
			wantBuffer: nil,
		},
		{
			name:       "negative cursor is invalid",
			input:      Input{CurrentVersion: -1, IncomingVersion: 1},
			wantKind:   InvalidInput,
			wantNext:   -1,
			wantApply:  nil,
			wantBuffer: nil,
		},
		{
			name:       "zero buffered version is invalid",
			input:      Input{CurrentVersion: 0, IncomingVersion: 1, BufferedVersions: []int64{0}},
			wantKind:   InvalidInput,
			wantNext:   0,
			wantApply:  nil,
			wantBuffer: []int64{0},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			originalBuffer := append([]int64(nil), test.input.BufferedVersions...)
			got := Decide(test.input)
			if got.Kind != test.wantKind || got.NextVersion != test.wantNext {
				t.Fatalf("Decide() = %#v, want kind %q and next %d", got, test.wantKind, test.wantNext)
			}
			if !reflect.DeepEqual(got.AppliedVersions, test.wantApply) {
				t.Fatalf("AppliedVersions = %v, want %v", got.AppliedVersions, test.wantApply)
			}
			if !reflect.DeepEqual(got.BufferedVersions, test.wantBuffer) {
				t.Fatalf("BufferedVersions = %v, want %v", got.BufferedVersions, test.wantBuffer)
			}
			if !reflect.DeepEqual(test.input.BufferedVersions, originalBuffer) {
				t.Fatalf("Decide() mutated input buffer: %v, want %v", test.input.BufferedVersions, originalBuffer)
			}
		})
	}
}

func TestStateKeepsInterleavedAggregatesIndependent(t *testing.T) {
	ids := identifiers.NewDeterministicGenerator()
	tenant, _ := ids.Generate()
	aggregateA, _ := ids.Generate()
	aggregateB, _ := ids.Generate()
	keyA := AggregateKey{TenantID: tenant, AggregateID: aggregateA}
	keyB := AggregateKey{TenantID: tenant, AggregateID: aggregateB}

	state := State{}
	sequence := []Event{
		{Key: keyA, AggregateVersion: 3},
		{Key: keyB, AggregateVersion: 2},
		{Key: keyA, AggregateVersion: 1},
		{Key: keyB, AggregateVersion: 1},
		{Key: keyA, AggregateVersion: 2},
		{Key: keyA, AggregateVersion: 2},
	}
	wantKinds := []DecisionKind{FutureBuffer, FutureBuffer, GapFillDrain, GapFillDrain, GapFillDrain, DuplicateOrStale}
	for index, event := range sequence {
		decision := state.Decide(event)
		if decision.Kind != wantKinds[index] {
			t.Fatalf("event %d decision kind = %q, want %q", index, decision.Kind, wantKinds[index])
		}
		if decision.Key != event.Key {
			t.Fatalf("event %d decision key = %+v, want %+v", index, decision.Key, event.Key)
		}
		state = state.Apply(event)
	}

	if got := state.Cursors[keyA]; got.CurrentVersion != 3 || len(got.BufferedVersions) != 0 {
		t.Fatalf("aggregate A cursor = %+v, want version 3 and empty buffer", got)
	}
	if got := state.Cursors[keyB]; got.CurrentVersion != 2 || len(got.BufferedVersions) != 0 {
		t.Fatalf("aggregate B cursor = %+v, want version 2 and empty buffer", got)
	}
}

func TestStateApplyDoesNotMutatePriorState(t *testing.T) {
	key := AggregateKey{}
	original := State{Cursors: map[AggregateKey]Cursor{
		key: {CurrentVersion: 1, BufferedVersions: []int64{4}},
	}}
	next := original.Apply(Event{Key: key, AggregateVersion: 2})

	if got := original.Cursors[key]; got.CurrentVersion != 1 || !reflect.DeepEqual(got.BufferedVersions, []int64{4}) {
		t.Fatalf("original state mutated to %+v", got)
	}
	if got := next.Cursors[key]; got.CurrentVersion != 2 || !reflect.DeepEqual(got.BufferedVersions, []int64{4}) {
		t.Fatalf("next state = %+v, want version 2 and buffer [4]", got)
	}
}

func TestRandomizedShuffledVersionsConverge(t *testing.T) {
	for seed := int64(0); seed < 100; seed++ {
		random := rand.New(rand.NewSource(seed))
		state := State{}
		keys := make([]AggregateKey, 3)
		for index := range keys {
			keys[index] = AggregateKey{TenantID: deterministicID(index), AggregateID: deterministicID(index + 10)}
		}

		var events []Event
		for _, key := range keys {
			count := int64(1 + random.Intn(20))
			for version := int64(1); version <= count; version++ {
				events = append(events, Event{Key: key, AggregateVersion: version})
				if random.Intn(4) == 0 {
					events = append(events, Event{Key: key, AggregateVersion: version})
				}
			}
		}
		random.Shuffle(len(events), func(i, j int) { events[i], events[j] = events[j], events[i] })

		for _, event := range events {
			state = state.Apply(event)
		}
		for _, key := range keys {
			cursor := state.Cursors[key]
			if cursor.CurrentVersion == 0 || len(cursor.BufferedVersions) != 0 {
				t.Fatalf("seed %d key %+v cursor = %+v, want applied contiguous versions", seed, key, cursor)
			}
		}
	}
}

func deterministicID(value int) identifiers.UUID {
	generator := identifiers.NewDeterministicGenerator()
	for index := 0; index < value; index++ {
		_, _ = generator.Generate()
	}
	id, _ := generator.Generate()
	return id
}
