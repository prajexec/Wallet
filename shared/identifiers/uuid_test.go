package identifiers

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestDeterministicGenerator(t *testing.T) {
	generator := NewDeterministicGenerator(time.UnixMilli(1234))
	seen := make(map[UUID]bool)
	var previous string
	for i := 0; i < 4; i++ {
		id, err := generator.Generate()
		if err != nil {
			t.Fatalf("Generate() error = %v", err)
		}
		if !id.IsV7() {
			t.Fatalf("generated ID %s is not UUIDv7", id)
		}
		if seen[id] {
			t.Fatalf("generated duplicate ID %s", id)
		}
		seen[id] = true
		if previous != "" && id.String() <= previous {
			t.Fatalf("IDs are not ordered: %s <= %s", id, previous)
		}
		previous = id.String()
	}
}

func TestUUIDv7GeneratorUsesMonotonicSequence(t *testing.T) {
	now := func() time.Time { return time.UnixMilli(42) }
	random := bytes.NewReader(bytes.Repeat([]byte{0x11}, 18))
	generator := NewUUIDv7GeneratorWithSource(now, random)

	first, err := generator.Generate()
	if err != nil {
		t.Fatalf("first Generate() error = %v", err)
	}
	second, err := generator.Generate()
	if err != nil {
		t.Fatalf("second Generate() error = %v", err)
	}
	if first == second || second.String() <= first.String() {
		t.Fatalf("same-clock IDs are not unique and ordered: %s, %s", first, second)
	}
}

func TestUUIDJSONAndParse(t *testing.T) {
	id := MustParse("0192f4b7-6d92-7a4d-8cb5-1b3f9d8f1111")
	encoded, err := json.Marshal(id)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if string(encoded) != `"0192f4b7-6d92-7a4d-8cb5-1b3f9d8f1111"` {
		t.Fatalf("Marshal() = %s", encoded)
	}
	var decoded UUID
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if decoded != id {
		t.Fatalf("decoded ID = %s, want %s", decoded, id)
	}

	tests := []struct {
		name  string
		value string
	}{
		{name: "empty", value: ""},
		{name: "bad hyphen", value: strings.Replace(id.String(), "-", "", 1)},
		{name: "bad hex", value: "0192f4b7-6d92-7a4d-8cb5-1b3f9d8fzzzz"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Parse(test.value); !errors.Is(err, ErrInvalidUUID) {
				t.Fatalf("Parse(%q) error = %v, want ErrInvalidUUID", test.value, err)
			}
		})
	}
}

func TestFakeGeneratorExhaustion(t *testing.T) {
	id := MustParse("0192f4b7-6d92-7a4d-8cb5-1b3f9d8f1111")
	generator := NewFakeGenerator(id)
	got, err := generator.Generate()
	if err != nil || got != id {
		t.Fatalf("first Generate() = %s, %v; want %s, nil", got, err, id)
	}
	if _, err := generator.Generate(); !errors.Is(err, ErrFakeExhausted) {
		t.Fatalf("exhausted Generate() error = %v, want ErrFakeExhausted", err)
	}
}
