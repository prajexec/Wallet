package events

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"digitalwallet/shared/identifiers"
)

func validEnvelope(t *testing.T) Envelope {
	t.Helper()
	generator := identifiers.NewDeterministicGenerator(time.UnixMilli(1000))
	ids := make([]identifiers.UUID, 5)
	for i := range ids {
		var err error
		ids[i], err = generator.Generate()
		if err != nil {
			t.Fatalf("Generate() error = %v", err)
		}
	}
	envelope, err := NewEnvelope(
		ids[0], "wallet.deposit_posted", 1, time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC),
		ids[1], "wallet", ids[2], 7, "wallet-source", ids[3], &ids[4],
		map[string]any{"amount_minor": int64(1250), "currency": "USD"},
		map[string]any{"request_id": "req_01"},
	)
	if err != nil {
		t.Fatalf("NewEnvelope() error = %v", err)
	}
	return envelope
}

func TestEnvelopeJSONRoundTrip(t *testing.T) {
	want := validEnvelope(t)
	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var got Envelope
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got.EventID != want.EventID || got.TenantID != want.TenantID || got.AggregateVersion != want.AggregateVersion {
		t.Fatalf("decoded envelope identity/version = %+v, want %+v", got, want)
	}
	if string(got.Data) != string(want.Data) || got.Metadata["request_id"] != "req_01" {
		t.Fatalf("decoded payload = %s / %#v, want %s / request_id", got.Data, got.Metadata, want.Data)
	}
	var payload struct {
		AmountMinor int64  `json:"amount_minor"`
		Currency    string `json:"currency"`
	}
	if err := got.DecodeData(&payload); err != nil {
		t.Fatalf("DecodeData() error = %v", err)
	}
	if payload.AmountMinor != 1250 || payload.Currency != "USD" {
		t.Fatalf("decoded data = %+v", payload)
	}
}

func TestEnvelopeValidation(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Envelope)
		want   error
	}{
		{name: "missing event type", change: func(e *Envelope) { e.EventType = " " }, want: ErrMissingField},
		{name: "invalid schema version", change: func(e *Envelope) { e.SchemaVersion = 0 }, want: ErrInvalidEnvelope},
		{name: "non UTC timestamp", change: func(e *Envelope) {
			e.OccurredAt = time.Date(2026, 10, 19, 12, 0, 0, 0, time.FixedZone("PDT", -7*60*60))
		}, want: ErrInvalidEnvelope},
		{name: "missing aggregate version", change: func(e *Envelope) { e.AggregateVersion = 0 }, want: ErrInvalidEnvelope},
		{name: "nil data", change: func(e *Envelope) { e.Data = nil }, want: ErrInvalidEnvelope},
		{name: "non UUIDv7 event ID", change: func(e *Envelope) { e.EventID = identifiers.UUID{} }, want: ErrMissingField},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			envelope := validEnvelope(t)
			test.change(&envelope)
			if err := envelope.Validate(); !errors.Is(err, test.want) {
				t.Fatalf("Validate() error = %v, want %v", err, test.want)
			}
		})
	}
}
