// Package identifiers contains the identifiers shared by the service
// boundaries. IDs are UUIDs, and newly generated IDs use UUID version 7.
package identifiers

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

const (
	uuidTextLength = 36
	maxUUIDv7Time  = uint64(1<<48) - 1
)

var (
	// ErrInvalidUUID is returned when a value is not a canonical UUID string.
	ErrInvalidUUID = errors.New("invalid UUID")
	// ErrFakeExhausted is returned by a configured fake after its IDs are used.
	ErrFakeExhausted = errors.New("deterministic UUID generator exhausted")
)

// UUID is the 128-bit identifier used throughout the contracts.
type UUID [16]byte

// ID is an alias that makes identifier-bearing APIs read naturally.
type ID = UUID

// Generator is the seam used by commands and tests when an ID is needed.
type Generator interface {
	Generate() (UUID, error)
}

// UUIDGenerator is retained as a descriptive alias for Generator.
type UUIDGenerator = Generator

// UUIDv7Generator generates time-ordered UUID version 7 values. The clock
// and random source variants make the implementation straightforward to test
// without changing the production interface.
type UUIDv7Generator struct {
	mu          sync.Mutex
	now         func() time.Time
	random      io.Reader
	lastUnixMS  uint64
	lastRandomA uint16
	initialized bool
}

// NewUUIDv7Generator returns a generator backed by the system clock and
// crypto/rand.
func NewUUIDv7Generator() *UUIDv7Generator {
	return NewUUIDv7GeneratorWithSource(time.Now, rand.Reader)
}

// NewUUIDv7GeneratorWithSource constructs a generator with injectable time
// and entropy sources. Nil arguments use time.Now and crypto/rand.
func NewUUIDv7GeneratorWithSource(now func() time.Time, random io.Reader) *UUIDv7Generator {
	if now == nil {
		now = time.Now
	}
	if random == nil {
		random = rand.Reader
	}
	return &UUIDv7Generator{now: now, random: random}
}

// NewGenerator returns the production generator through the shared seam.
func NewGenerator() Generator {
	return NewUUIDv7Generator()
}

var defaultGenerator = NewUUIDv7Generator()

// NewUUIDv7 generates one UUID version 7 with the process-wide production
// generator.
func NewUUIDv7() (UUID, error) {
	return defaultGenerator.Generate()
}

// Generate returns a UUID version 7. Values generated during the same clock
// millisecond use a monotonic 12-bit sequence, and a clock moving backwards
// is held at the last emitted timestamp.
func (g *UUIDv7Generator) Generate() (UUID, error) {
	if g == nil {
		return UUID{}, errors.New("nil UUID generator")
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	unixMS := g.now().UnixMilli()
	if unixMS < 0 || uint64(unixMS) > maxUUIDv7Time {
		return UUID{}, fmt.Errorf("UUIDv7 timestamp out of range: %d", unixMS)
	}

	var randomA uint16
	if !g.initialized || uint64(unixMS) > g.lastUnixMS {
		var bytes [2]byte
		if _, err := io.ReadFull(g.random, bytes[:]); err != nil {
			return UUID{}, fmt.Errorf("generate UUIDv7 random component: %w", err)
		}
		randomA = uint16(bytes[0])<<4 | uint16(bytes[1]>>4)
		g.lastUnixMS = uint64(unixMS)
		g.lastRandomA = randomA
		g.initialized = true
	} else {
		g.lastUnixMS = max(g.lastUnixMS, uint64(unixMS))
		if g.lastRandomA == 0x0fff {
			// More than 4096 IDs in one millisecond is unusual, but advancing
			// the logical timestamp preserves ordering and uniqueness.
			if g.lastUnixMS == maxUUIDv7Time {
				return UUID{}, errors.New("UUIDv7 timestamp exhausted")
			}
			g.lastUnixMS++
			g.lastRandomA = 0
		} else {
			g.lastRandomA++
		}
		randomA = g.lastRandomA
	}

	var randomB [8]byte
	if _, err := io.ReadFull(g.random, randomB[:]); err != nil {
		return UUID{}, fmt.Errorf("generate UUIDv7 random component: %w", err)
	}
	var tail uint64
	for _, b := range randomB {
		tail = tail<<8 | uint64(b)
	}
	tail &= (uint64(1) << 62) - 1

	return makeUUIDv7(g.lastUnixMS, randomA, tail), nil
}

// New is a short alias useful at call sites that only know they need an ID.
func (g *UUIDv7Generator) New() (UUID, error) {
	return g.Generate()
}

// FakeGenerator returns a fixed sequence of IDs. It is useful when a test
// needs to control IDs independently of their UUIDv7 bit layout.
type FakeGenerator struct {
	mu   sync.Mutex
	ids  []UUID
	next int
}

// NewFakeGenerator creates a generator that returns ids in order.
func NewFakeGenerator(ids ...UUID) *FakeGenerator {
	return &FakeGenerator{ids: append([]UUID(nil), ids...)}
}

// Generate returns the next configured ID.
func (g *FakeGenerator) Generate() (UUID, error) {
	if g == nil {
		return UUID{}, errors.New("nil fake UUID generator")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.next >= len(g.ids) {
		return UUID{}, ErrFakeExhausted
	}
	id := g.ids[g.next]
	g.next++
	return id, nil
}

// New is the fake-generator alias for Generate.
func (g *FakeGenerator) New() (UUID, error) {
	return g.Generate()
}

// DeterministicGenerator creates valid, repeatable UUIDv7 values without
// reading the system clock or entropy source.
type DeterministicGenerator struct {
	mu      sync.Mutex
	startMS uint64
	next    uint64
}

// NewDeterministicGenerator creates a deterministic generator. If start is
// omitted, the sequence starts at Unix millisecond 1.
func NewDeterministicGenerator(start ...time.Time) *DeterministicGenerator {
	startMS := uint64(1)
	if len(start) > 0 {
		ms := start[0].UnixMilli()
		if ms >= 0 && uint64(ms) <= maxUUIDv7Time {
			startMS = uint64(ms)
		}
	}
	return &DeterministicGenerator{startMS: startMS}
}

// Generate returns the next repeatable UUIDv7.
func (g *DeterministicGenerator) Generate() (UUID, error) {
	if g == nil {
		return UUID{}, errors.New("nil deterministic UUID generator")
	}
	g.mu.Lock()
	defer g.mu.Unlock()

	sequence := g.next
	g.next++
	timestamp := g.startMS + sequence/4096
	if timestamp > maxUUIDv7Time {
		return UUID{}, errors.New("deterministic UUIDv7 timestamp exhausted")
	}
	randomA := uint16(sequence % 4096)
	// A fixed mixing constant gives the tail useful bit variation while
	// remaining completely deterministic.
	tail := (sequence*0x9e3779b97f4a7c15 + 0xd1b54a32d192ed03) & ((uint64(1) << 62) - 1)
	return makeUUIDv7(timestamp, randomA, tail), nil
}

// New is the deterministic-generator alias for Generate.
func (g *DeterministicGenerator) New() (UUID, error) {
	return g.Generate()
}

func makeUUIDv7(timestamp uint64, randomA uint16, randomB uint64) UUID {
	var id UUID
	id[0] = byte(timestamp >> 40)
	id[1] = byte(timestamp >> 32)
	id[2] = byte(timestamp >> 24)
	id[3] = byte(timestamp >> 16)
	id[4] = byte(timestamp >> 8)
	id[5] = byte(timestamp)
	id[6] = 0x70 | byte(randomA>>8)
	id[7] = byte(randomA)
	id[8] = 0x80 | byte(randomB>>56)
	id[9] = byte(randomB >> 48)
	id[10] = byte(randomB >> 40)
	id[11] = byte(randomB >> 32)
	id[12] = byte(randomB >> 24)
	id[13] = byte(randomB >> 16)
	id[14] = byte(randomB >> 8)
	id[15] = byte(randomB)
	return id
}

// Parse parses a canonical, hyphenated UUID string.
func Parse(value string) (UUID, error) {
	if len(value) != uuidTextLength || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return UUID{}, fmt.Errorf("%w: %q", ErrInvalidUUID, value)
	}
	var id UUID
	decoded, err := hex.DecodeString(value[:8] + value[9:13] + value[14:18] + value[19:23] + value[24:])
	if err != nil {
		return UUID{}, fmt.Errorf("%w: %q", ErrInvalidUUID, value)
	}
	copy(id[:], decoded)
	return id, nil
}

// ParseUUID is a descriptive alias for Parse.
func ParseUUID(value string) (UUID, error) {
	return Parse(value)
}

// MustParse parses value and panics if it is invalid. It is intended for
// package-level test fixtures and constants only.
func MustParse(value string) UUID {
	id, err := Parse(value)
	if err != nil {
		panic(err)
	}
	return id
}

// String returns the canonical lower-case UUID representation.
func (id UUID) String() string {
	var encoded [36]byte
	hex.Encode(encoded[0:8], id[0:4])
	encoded[8] = '-'
	hex.Encode(encoded[9:13], id[4:6])
	encoded[13] = '-'
	hex.Encode(encoded[14:18], id[6:8])
	encoded[18] = '-'
	hex.Encode(encoded[19:23], id[8:10])
	encoded[23] = '-'
	hex.Encode(encoded[24:36], id[10:16])
	return string(encoded[:])
}

// IsZero reports whether id is the all-zero UUID.
func (id UUID) IsZero() bool {
	return id == UUID{}
}

// IsV7 reports whether id has the UUIDv7 version and RFC 9562 variant bits.
func (id UUID) IsV7() bool {
	return id[6]&0xf0 == 0x70 && id[8]&0xc0 == 0x80
}

// Bytes returns a copy of the binary UUID representation.
func (id UUID) Bytes() []byte {
	bytes := make([]byte, len(id))
	copy(bytes, id[:])
	return bytes
}

// MarshalText implements encoding.TextMarshaler.
func (id UUID) MarshalText() ([]byte, error) {
	return []byte(id.String()), nil
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (id *UUID) UnmarshalText(value []byte) error {
	parsed, err := Parse(string(value))
	if err != nil {
		return err
	}
	if id == nil {
		return errors.New("nil UUID destination")
	}
	*id = parsed
	return nil
}

// MarshalJSON encodes IDs as the strings used by the HTTP and event
// contracts.
func (id UUID) MarshalJSON() ([]byte, error) {
	return json.Marshal(id.String())
}

// UnmarshalJSON decodes a UUID string.
func (id *UUID) UnmarshalJSON(value []byte) error {
	var text string
	if err := json.Unmarshal(value, &text); err != nil {
		return fmt.Errorf("UUID must be a string: %w", err)
	}
	return id.UnmarshalText([]byte(text))
}

func max(a, b uint64) uint64 {
	if a > b {
		return a
	}
	return b
}
