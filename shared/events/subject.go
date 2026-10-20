package events

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"digitalwallet/shared/identifiers"
)

const (
	// SubjectPrefix is the stable NATS subject namespace for durable events.
	SubjectPrefix = "events"
	// NatsMsgIDHeader is the JetStream duplicate-detection header populated
	// from Envelope.EventID.
	NatsMsgIDHeader = "Nats-Msg-Id"
)

var (
	// ErrInvalidSubject identifies subject components that cannot be used by
	// the event subject contract.
	ErrInvalidSubject = errors.New("invalid event subject")
	// ErrInvalidMessage identifies a message whose transport fields and
	// envelope payload do not agree.
	ErrInvalidMessage = errors.New("invalid event message")
)

// SubjectWithPrefix returns the subject for an event with a configurable prefix:
// <prefix>.<tenant_id>.<aggregate_type>.<event_type>.
func SubjectWithPrefix(prefix string, tenantID identifiers.UUID, aggregateType, eventType string) (string, error) {
	if prefix == "" {
		prefix = SubjectPrefix
	}
	if err := validateSubjectComponent("subject_prefix", prefix, false); err != nil {
		return "", err
	}
	if tenantID.IsZero() {
		return "", fmt.Errorf("%w: tenant_id is zero", ErrInvalidSubject)
	}
	if !tenantID.IsV7() {
		return "", fmt.Errorf("%w: tenant_id must be UUIDv7", ErrInvalidSubject)
	}
	if err := validateSubjectComponent("aggregate_type", aggregateType, false); err != nil {
		return "", err
	}
	if err := validateSubjectComponent("event_type", eventType, true); err != nil {
		return "", err
	}
	return strings.Join([]string{prefix, tenantID.String(), aggregateType, eventType}, "."), nil
}

// SubjectFor returns the stable subject for an event:
// events.<tenant_id>.<aggregate_type>.<event_type>.
//
// Event types may contain dots because the event catalog uses names such as
// wallet.deposit_posted. Aggregate types remain one subject token.
func SubjectFor(tenantID identifiers.UUID, aggregateType, eventType string) (string, error) {
	return SubjectWithPrefix(SubjectPrefix, tenantID, aggregateType, eventType)
}

// SubjectForEnvelope returns the subject for a validated durable envelope.
func SubjectForEnvelope(envelope Envelope) (string, error) {
	if err := envelope.Validate(); err != nil {
		return "", err
	}
	return SubjectFor(envelope.TenantID, envelope.AggregateType, envelope.EventType)
}

// Subject returns the subject for a validated durable envelope.
func (e Envelope) Subject() (string, error) {
	return SubjectForEnvelope(e)
}

// Message is the infrastructure-neutral representation of a publishable
// event. Payload is the JSON-encoded Envelope; a NATS adapter can publish it
// without importing NATS into the shared module.
type Message struct {
	Subject   string          `json:"subject"`
	NatsMsgID string          `json:"nats_msg_id"`
	Payload   json.RawMessage `json:"payload"`
}

// EventMessage is a descriptive alias for Message.
type EventMessage = Message

// NewMessage maps a validated envelope to its stable subject, duplicate
// detection ID, and JSON payload.
func NewMessage(envelope Envelope) (Message, error) {
	subject, err := SubjectForEnvelope(envelope)
	if err != nil {
		return Message{}, err
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return Message{}, fmt.Errorf("encode event message payload: %w", err)
	}
	return Message{
		Subject:   subject,
		NatsMsgID: envelope.EventID.String(),
		Payload:   payload,
	}, nil
}

// MessageFromEnvelope is an explicit alias for NewMessage at call sites that
// describe the direction of the mapping.
func MessageFromEnvelope(envelope Envelope) (Message, error) {
	return NewMessage(envelope)
}

// DecodeEnvelope decodes and validates the envelope carried by a message.
func (m Message) DecodeEnvelope() (Envelope, error) {
	var envelope Envelope
	if len(m.Payload) == 0 {
		return Envelope{}, fmt.Errorf("%w: empty payload", ErrInvalidMessage)
	}
	if err := json.Unmarshal(m.Payload, &envelope); err != nil {
		return Envelope{}, fmt.Errorf("%w: decode payload: %v", ErrInvalidMessage, err)
	}
	return envelope, nil
}

// Validate checks that a message's subject, NATS message ID, and payload all
// identify the same durable envelope.
func (m Message) Validate() error {
	if strings.TrimSpace(m.Subject) == "" {
		return fmt.Errorf("%w: subject is empty", ErrInvalidMessage)
	}
	if strings.TrimSpace(m.NatsMsgID) == "" {
		return fmt.Errorf("%w: Nats-Msg-Id is empty", ErrInvalidMessage)
	}
	envelope, err := m.DecodeEnvelope()
	if err != nil {
		return err
	}
	parts := strings.SplitN(m.Subject, ".", 2)
	prefix := parts[0]
	subject, err := SubjectWithPrefix(prefix, envelope.TenantID, envelope.AggregateType, envelope.EventType)
	if err != nil {
		return fmt.Errorf("%w: subject from payload: %v", ErrInvalidMessage, err)
	}
	if m.Subject != subject {
		return fmt.Errorf("%w: subject %q does not match payload subject %q", ErrInvalidMessage, m.Subject, subject)
	}
	if m.NatsMsgID != envelope.EventID.String() {
		return fmt.Errorf("%w: Nats-Msg-Id %q does not match event_id %q", ErrInvalidMessage, m.NatsMsgID, envelope.EventID)
	}
	return nil
}

func validateSubjectComponent(name, value string, allowDots bool) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%w: %s is empty", ErrInvalidSubject, name)
	}
	if value != strings.TrimSpace(value) || strings.ContainsAny(value, " \t\r\n\x00*>") {
		return fmt.Errorf("%w: %s contains invalid NATS subject characters", ErrInvalidSubject, name)
	}
	if !allowDots && strings.Contains(value, ".") {
		return fmt.Errorf("%w: %s must be one subject token", ErrInvalidSubject, name)
	}
	if allowDots && (strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") || strings.Contains(value, "..")) {
		return fmt.Errorf("%w: %s contains an empty subject token", ErrInvalidSubject, name)
	}
	return nil
}
