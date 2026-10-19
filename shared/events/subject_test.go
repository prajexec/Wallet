package events

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"digitalwallet/shared/identifiers"
)

func TestSubjectForEnvelope(t *testing.T) {
	envelope := validEnvelope(t)
	want := "events." + envelope.TenantID.String() + ".wallet.wallet.deposit_posted"

	got, err := SubjectForEnvelope(envelope)
	if err != nil {
		t.Fatalf("SubjectForEnvelope() error = %v", err)
	}
	if got != want {
		t.Fatalf("SubjectForEnvelope() = %q, want %q", got, want)
	}
	if methodGot, err := envelope.Subject(); err != nil || methodGot != want {
		t.Fatalf("Envelope.Subject() = %q, %v, want %q", methodGot, err, want)
	}
}

func TestSubjectForRejectsUnstableComponents(t *testing.T) {
	envelope := validEnvelope(t)
	tests := []struct {
		name          string
		aggregateType string
		eventType     string
	}{
		{name: "empty aggregate", aggregateType: "", eventType: envelope.EventType},
		{name: "aggregate has subject separator", aggregateType: "wallet.v2", eventType: envelope.EventType},
		{name: "event has wildcard", aggregateType: envelope.AggregateType, eventType: "wallet.*"},
		{name: "event has empty token", aggregateType: envelope.AggregateType, eventType: "wallet..created"},
		{name: "event has whitespace", aggregateType: envelope.AggregateType, eventType: "wallet.created now"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := SubjectFor(envelope.TenantID, test.aggregateType, test.eventType)
			if !errors.Is(err, ErrInvalidSubject) {
				t.Fatalf("SubjectFor() error = %v, want ErrInvalidSubject", err)
			}
		})
	}

	var zero identifiers.UUID
	if _, err := SubjectFor(zero, envelope.AggregateType, envelope.EventType); !errors.Is(err, ErrInvalidSubject) {
		t.Fatalf("SubjectFor(zero tenant) error = %v, want ErrInvalidSubject", err)
	}
}

func TestNewMessageRoundTrip(t *testing.T) {
	envelope := validEnvelope(t)
	message, err := NewMessage(envelope)
	if err != nil {
		t.Fatalf("NewMessage() error = %v", err)
	}
	if message.Subject == "" || message.NatsMsgID != envelope.EventID.String() {
		t.Fatalf("message metadata = %+v, want subject and event ID", message)
	}
	if err := message.Validate(); err != nil {
		t.Fatalf("Message.Validate() error = %v", err)
	}
	decoded, err := message.DecodeEnvelope()
	if err != nil {
		t.Fatalf("DecodeEnvelope() error = %v", err)
	}
	if decoded.EventID != envelope.EventID || decoded.AggregateVersion != envelope.AggregateVersion {
		t.Fatalf("decoded envelope = %+v, want %+v", decoded, envelope)
	}
	if !json.Valid(message.Payload) {
		t.Fatalf("message payload is not valid JSON: %s", message.Payload)
	}

	alias, err := MessageFromEnvelope(envelope)
	if err != nil || alias.Subject != message.Subject || string(alias.Payload) != string(message.Payload) {
		t.Fatalf("MessageFromEnvelope() = %+v, %v, want %+v", alias, err, message)
	}
}

func TestMessageValidationRejectsMismatches(t *testing.T) {
	envelope := validEnvelope(t)
	message, err := NewMessage(envelope)
	if err != nil {
		t.Fatalf("NewMessage() error = %v", err)
	}
	tests := []struct {
		name   string
		change func(*Message)
	}{
		{name: "subject", change: func(m *Message) { m.Subject = "events.other" }},
		{name: "message ID", change: func(m *Message) { m.NatsMsgID = "0192f4b7-6d92-7a4d-8cb5-1b3f9d8f9999" }},
		{name: "payload", change: func(m *Message) { m.Payload = []byte(`{"event_id":"not-an-envelope"}`) }},
		{name: "empty payload", change: func(m *Message) { m.Payload = nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := message
			test.change(&candidate)
			if err := candidate.Validate(); !errors.Is(err, ErrInvalidMessage) {
				t.Fatalf("Message.Validate() error = %v, want ErrInvalidMessage", err)
			}
		})
	}

	if !strings.HasPrefix(NatsMsgIDHeader, "Nats-") {
		t.Fatalf("NatsMsgIDHeader = %q, want NATS header name", NatsMsgIDHeader)
	}
}
