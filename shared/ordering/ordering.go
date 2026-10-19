// Package ordering contains the pure aggregate-version ordering decisions
// shared by event consumers. Persistence, buffering storage, and ACK timing
// remain owned by the consuming service.
package ordering

import (
	"math"
	"sort"

	"digitalwallet/shared/events"
	"digitalwallet/shared/identifiers"
)

// DecisionKind is the action a consumer should take for an incoming version.
type DecisionKind string

const (
	// InOrder means the incoming version is exactly the next version.
	InOrder DecisionKind = "in_order"
	// DuplicateOrStale means the incoming version has already been applied or
	// is older than the current cursor.
	DuplicateOrStale DecisionKind = "duplicate_or_stale"
	// FutureBuffer means the incoming version has a predecessor gap and must be
	// durably buffered.
	FutureBuffer DecisionKind = "future_buffer"
	// GapFillDrain means the incoming version fills the next gap and any
	// consecutive buffered versions can be applied after it.
	GapFillDrain DecisionKind = "gap_fill_drain"
	// InvalidInput means the version state violates the ordering value rules.
	InvalidInput DecisionKind = "invalid_input"

	// Descriptive aliases keep decision names explicit at call sites.
	DecisionInOrder        = InOrder
	DecisionDuplicateStale = DuplicateOrStale
	DecisionFutureBuffer   = FutureBuffer
	DecisionGapFillDrain   = GapFillDrain
	DecisionInvalid        = InvalidInput
)

// AggregateKey identifies the ordering stream within one consumer. A State
// value represents one consumer, so consumer_name is intentionally outside
// this key as described by docs/event-model.md.
type AggregateKey struct {
	TenantID    identifiers.UUID
	AggregateID identifiers.UUID
}

// Event is the ordering portion of an event envelope.
type Event struct {
	Key              AggregateKey
	AggregateVersion int64
}

// EventFromEnvelope extracts the ordering key and version from a shared event
// envelope without introducing any persistence or transport behavior.
func EventFromEnvelope(envelope events.Envelope) Event {
	return Event{
		Key: AggregateKey{
			TenantID:    envelope.TenantID,
			AggregateID: envelope.AggregateID,
		},
		AggregateVersion: envelope.AggregateVersion,
	}
}

// Cursor is the pure ordering state for one aggregate. BufferedVersions is
// kept sorted and contains versions that have not yet been applied.
type Cursor struct {
	CurrentVersion   int64
	BufferedVersions []int64
}

// Input contains the current cursor and the incoming aggregate version. It is
// useful when a service stores cursor and buffer rows separately.
type Input struct {
	CurrentVersion   int64
	IncomingVersion  int64
	BufferedVersions []int64
}

// Decision describes the pure outcome of comparing an event with a cursor.
// AppliedVersions is ordered and includes the incoming version first when it
// is applicable. BufferedVersions is the canonical buffer after this decision.
type Decision struct {
	Key              AggregateKey
	Kind             DecisionKind
	CurrentVersion   int64
	IncomingVersion  int64
	NextVersion      int64
	AppliedVersions  []int64
	BufferedVersions []int64
}

// Decide returns the ordering decision for one aggregate. It never mutates
// the supplied buffer. Versions start at one; a new aggregate uses
// CurrentVersion 0.
func Decide(input Input) Decision {
	buffered, valid := canonicalBuffer(input.CurrentVersion, input.BufferedVersions)
	decision := Decision{
		CurrentVersion:   input.CurrentVersion,
		IncomingVersion:  input.IncomingVersion,
		NextVersion:      input.CurrentVersion,
		BufferedVersions: buffered,
	}
	if input.CurrentVersion < 0 || !valid || input.IncomingVersion <= 0 {
		decision.Kind = InvalidInput
		return decision
	}

	if input.IncomingVersion <= input.CurrentVersion {
		decision.Kind = DuplicateOrStale
		return decision
	}

	if input.CurrentVersion == math.MaxInt64 || input.IncomingVersion > input.CurrentVersion+1 {
		decision.Kind = FutureBuffer
		decision.BufferedVersions = insertVersion(buffered, input.IncomingVersion)
		return decision
	}

	decision.AppliedVersions = []int64{input.IncomingVersion}
	decision.NextVersion = input.IncomingVersion
	wasBuffered := len(buffered) > 0
	for containsVersion(buffered, decision.NextVersion+1) {
		decision.NextVersion++
		decision.AppliedVersions = append(decision.AppliedVersions, decision.NextVersion)
	}
	decision.BufferedVersions = removeThrough(buffered, decision.NextVersion)
	if wasBuffered {
		decision.Kind = GapFillDrain
	} else {
		decision.Kind = InOrder
	}
	return decision
}

// DecideCursor is the cursor-oriented form of Decide.
func DecideCursor(cursor Cursor, incomingVersion int64) Decision {
	return Decide(Input{
		CurrentVersion:   cursor.CurrentVersion,
		IncomingVersion:  incomingVersion,
		BufferedVersions: cursor.BufferedVersions,
	})
}

// State is an immutable-by-convention collection of per-aggregate cursors.
// Its Apply method returns a cloned state, making interleaved aggregate
// decisions independent and straightforward to test.
type State struct {
	Cursors map[AggregateKey]Cursor
}

// Decide returns the decision for an event using the cursor for its key. A
// missing key starts at version zero with an empty buffer.
func (s State) Decide(event Event) Decision {
	cursor := s.cursor(event.Key)
	decision := DecideCursor(cursor, event.AggregateVersion)
	decision.Key = event.Key
	return decision
}

// Apply returns a new state with the event decision reflected. Future events
// are buffered, in-order/gap-fill events advance the cursor, and duplicates
// leave the applied cursor unchanged. Invalid input leaves the state intact.
func (s State) Apply(event Event) State {
	decision := s.Decide(event)
	if decision.Kind == InvalidInput {
		return s.Clone()
	}

	next := s.Clone()
	if next.Cursors == nil {
		next.Cursors = make(map[AggregateKey]Cursor)
	}
	cursor := next.cursor(event.Key)
	cursor.CurrentVersion = decision.NextVersion
	cursor.BufferedVersions = append([]int64(nil), decision.BufferedVersions...)
	if decision.Kind == DuplicateOrStale && len(cursor.BufferedVersions) == 0 {
		// Keep the normal zero-value cursor representation stable.
		cursor.BufferedVersions = nil
	}
	next.Cursors[event.Key] = cursor
	return next
}

// Clone returns a deep copy of the pure ordering state.
func (s State) Clone() State {
	if s.Cursors == nil {
		return State{}
	}
	clone := State{Cursors: make(map[AggregateKey]Cursor, len(s.Cursors))}
	for key, cursor := range s.Cursors {
		clone.Cursors[key] = Cursor{
			CurrentVersion:   cursor.CurrentVersion,
			BufferedVersions: append([]int64(nil), cursor.BufferedVersions...),
		}
	}
	return clone
}

func (s State) cursor(key AggregateKey) Cursor {
	if cursor, ok := s.Cursors[key]; ok {
		cursor.BufferedVersions = append([]int64(nil), cursor.BufferedVersions...)
		return cursor
	}
	return Cursor{}
}

func canonicalBuffer(current int64, values []int64) ([]int64, bool) {
	if current < 0 {
		return append([]int64(nil), values...), false
	}
	if len(values) == 0 {
		return nil, true
	}
	result := make([]int64, 0, len(values))
	seen := make(map[int64]struct{}, len(values))
	for _, value := range values {
		if value <= 0 {
			return append([]int64(nil), values...), false
		}
		if value <= current {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result, true
}

func insertVersion(values []int64, version int64) []int64 {
	result := append([]int64(nil), values...)
	if !containsVersion(result, version) {
		result = append(result, version)
		sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	}
	return result
}

func containsVersion(values []int64, version int64) bool {
	for _, value := range values {
		if value == version {
			return true
		}
	}
	return false
}

func removeThrough(values []int64, version int64) []int64 {
	result := make([]int64, 0, len(values))
	for _, value := range values {
		if value > version {
			result = append(result, value)
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}
