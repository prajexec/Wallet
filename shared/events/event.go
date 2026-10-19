// Package events defines the durable event envelope shared by the services.
package events

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"digitalwallet/shared/identifiers"
)

var (
	// ErrInvalidEnvelope identifies an event that cannot satisfy the durable
	// event contract.
	ErrInvalidEnvelope = errors.New("invalid event envelope")
	// ErrMissingField identifies a required envelope field that is absent.
	ErrMissingField = errors.New("missing event field")
)

// Envelope is the versioned event contract described in docs/event-model.md.
// Data is kept as raw JSON so each producer and consumer can use its own
// typed payload without changing the shared envelope.
type Envelope struct {
	EventID          identifiers.UUID  `json:"event_id"`
	EventType        string            `json:"event_type"`
	SchemaVersion    int               `json:"schema_version"`
	OccurredAt       time.Time         `json:"occurred_at"`
	TenantID         identifiers.UUID  `json:"tenant_id"`
	AggregateType    string            `json:"aggregate_type"`
	AggregateID      identifiers.UUID  `json:"aggregate_id"`
	AggregateVersion int64             `json:"aggregate_version"`
	Producer         string            `json:"producer"`
	CorrelationID    identifiers.UUID  `json:"correlation_id"`
	CausationID      *identifiers.UUID `json:"causation_id,omitempty"`
	Data             json.RawMessage   `json:"data"`
	Metadata         map[string]any    `json:"metadata"`
}

// NewEnvelope creates and validates an event envelope. data must be JSON
// encodable; it is stored in the envelope as raw JSON.
func NewEnvelope(
	eventID identifiers.UUID,
	eventType string,
	schemaVersion int,
	occurredAt time.Time,
	tenantID identifiers.UUID,
	aggregateType string,
	aggregateID identifiers.UUID,
	aggregateVersion int64,
	producer string,
	correlationID identifiers.UUID,
	causationID *identifiers.UUID,
	data any,
	metadata map[string]any,
) (Envelope, error) {
	encoded, err := json.Marshal(data)
	if err != nil {
		return Envelope{}, fmt.Errorf("encode event data: %w", err)
	}
	envelope := Envelope{
		EventID:          eventID,
		EventType:        eventType,
		SchemaVersion:    schemaVersion,
		OccurredAt:       occurredAt,
		TenantID:         tenantID,
		AggregateType:    aggregateType,
		AggregateID:      aggregateID,
		AggregateVersion: aggregateVersion,
		Producer:         producer,
		CorrelationID:    correlationID,
		CausationID:      causationID,
		Data:             encoded,
		Metadata:         metadata,
	}
	if err := envelope.Validate(); err != nil {
		return Envelope{}, err
	}
	return envelope, nil
}

// Validate checks all required envelope fields and the UUIDv7/UTC invariants
// that are part of the event contract.
func (e Envelope) Validate() error {
	fields := []struct {
		name string
		id   identifiers.UUID
	}{
		{"event_id", e.EventID},
		{"tenant_id", e.TenantID},
		{"aggregate_id", e.AggregateID},
		{"correlation_id", e.CorrelationID},
	}
	for _, field := range fields {
		if field.id.IsZero() {
			return fmt.Errorf("%w: %s is zero", ErrMissingField, field.name)
		}
		if !field.id.IsV7() {
			return fmt.Errorf("%w: %s must be UUIDv7", ErrInvalidEnvelope, field.name)
		}
	}
	if e.CausationID != nil {
		if e.CausationID.IsZero() {
			return fmt.Errorf("%w: causation_id is zero", ErrInvalidEnvelope)
		}
		if !e.CausationID.IsV7() {
			return fmt.Errorf("%w: causation_id must be UUIDv7", ErrInvalidEnvelope)
		}
	}
	if strings.TrimSpace(e.EventType) == "" {
		return fmt.Errorf("%w: event_type", ErrMissingField)
	}
	if e.SchemaVersion <= 0 {
		return fmt.Errorf("%w: schema_version must be positive", ErrInvalidEnvelope)
	}
	if e.OccurredAt.IsZero() {
		return fmt.Errorf("%w: occurred_at", ErrMissingField)
	}
	_, offset := e.OccurredAt.Zone()
	if offset != 0 {
		return fmt.Errorf("%w: occurred_at must be UTC", ErrInvalidEnvelope)
	}
	if strings.TrimSpace(e.AggregateType) == "" {
		return fmt.Errorf("%w: aggregate_type", ErrMissingField)
	}
	if e.AggregateVersion <= 0 {
		return fmt.Errorf("%w: aggregate_version must be positive", ErrInvalidEnvelope)
	}
	if strings.TrimSpace(e.Producer) == "" {
		return fmt.Errorf("%w: producer", ErrMissingField)
	}
	if len(bytes.TrimSpace(e.Data)) == 0 || bytes.Equal(bytes.TrimSpace(e.Data), []byte("null")) || !json.Valid(e.Data) {
		return fmt.Errorf("%w: data must be valid non-null JSON", ErrInvalidEnvelope)
	}
	return nil
}

// DecodeData decodes the raw payload into a consumer-owned value.
func (e Envelope) DecodeData(target any) error {
	if err := e.Validate(); err != nil {
		return err
	}
	return json.Unmarshal(e.Data, target)
}

// MarshalJSON validates before emitting the durable wire representation. A
// nil metadata map is normalized to the documented empty object.
func (e Envelope) MarshalJSON() ([]byte, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	if e.Metadata == nil {
		e.Metadata = map[string]any{}
	}
	type wire Envelope
	return json.Marshal(wire(e))
}

// UnmarshalJSON decodes and validates a durable event envelope.
func (e *Envelope) UnmarshalJSON(value []byte) error {
	type wire Envelope
	var decoded wire
	if err := json.Unmarshal(value, &decoded); err != nil {
		return err
	}
	result := Envelope(decoded)
	if err := result.Validate(); err != nil {
		return err
	}
	if result.Metadata == nil {
		result.Metadata = map[string]any{}
	}
	if e == nil {
		return errors.New("nil event envelope destination")
	}
	*e = result
	return nil
}
