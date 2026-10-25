package events

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ErrInvalidEnvelope is returned when an envelope misses a required field or
// its payload is not a JSON object.
var ErrInvalidEnvelope = errors.New("events: invalid envelope")

// Envelope is the JSON every domain event travels in (docs/events/README.md).
//
// The reader is tolerant: unknown fields are ignored, so a contract can grow
// (a new optional field) without breaking the consumers that do not know it
// yet. A breaking change raises EventVersion instead.
type Envelope struct {
	// EventID identifies the event; it is deterministic for the fact it
	// describes, so publishing the same fact again yields the same id and the
	// consumers can deduplicate it.
	EventID string `json:"event_id"`
	// EventType is the name of the event in the catalog (for example
	// CustomerLinked).
	EventType string `json:"event_type"`
	// EventVersion is the version of the payload contract, starting at 1.
	EventVersion int `json:"event_version"`
	// Source is the service that emits the event.
	Source string `json:"source"`
	// OccurredAt is when the fact happened, in UTC (RFC 3339).
	OccurredAt time.Time `json:"occurred_at"`
	// CorrelationID ties the event to the request that caused it, when known.
	CorrelationID string `json:"correlation_id,omitempty"`
	// Payload is the JSON object with the data of the event.
	Payload json.RawMessage `json:"payload"`
}

// New builds a valid envelope, encoding payload as its JSON object. The
// instant is kept in UTC.
func New(id, eventType, source string, version int, occurredAt time.Time, payload any) (Envelope, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return Envelope{}, fmt.Errorf("%w: encode the payload of %s: %v", ErrInvalidEnvelope, eventType, err)
	}
	e := Envelope{
		EventID:      id,
		EventType:    eventType,
		EventVersion: version,
		Source:       source,
		OccurredAt:   occurredAt.UTC(),
		Payload:      raw,
	}
	if err := e.Validate(); err != nil {
		return Envelope{}, err
	}
	return e, nil
}

// Validate reports the first required field that is missing, a version below
// 1 or a payload that is not a JSON object.
func (e Envelope) Validate() error {
	switch {
	case e.EventID == "":
		return fmt.Errorf("%w: event_id is required", ErrInvalidEnvelope)
	case e.EventType == "":
		return fmt.Errorf("%w: event_type is required (event %s)", ErrInvalidEnvelope, e.EventID)
	case e.EventVersion < 1:
		return fmt.Errorf("%w: event_version must be at least 1 (event %s)", ErrInvalidEnvelope, e.EventID)
	case e.Source == "":
		return fmt.Errorf("%w: source is required (event %s)", ErrInvalidEnvelope, e.EventID)
	case e.OccurredAt.IsZero():
		return fmt.Errorf("%w: occurred_at is required (event %s)", ErrInvalidEnvelope, e.EventID)
	case !isJSONObject(e.Payload):
		return fmt.Errorf("%w: payload must be a JSON object (event %s)", ErrInvalidEnvelope, e.EventID)
	}
	return nil
}

// Encode validates the envelope and returns its JSON.
func (e Envelope) Encode() ([]byte, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	e.OccurredAt = e.OccurredAt.UTC()
	b, err := json.Marshal(e)
	if err != nil {
		return nil, fmt.Errorf("%w: encode event %s: %v", ErrInvalidEnvelope, e.EventID, err)
	}
	return b, nil
}

// Decode parses and validates an envelope. Unknown fields are ignored.
func Decode(b []byte) (Envelope, error) {
	var e Envelope
	if err := json.Unmarshal(b, &e); err != nil {
		return Envelope{}, fmt.Errorf("%w: %v", ErrInvalidEnvelope, err)
	}
	if err := e.Validate(); err != nil {
		return Envelope{}, err
	}
	e.OccurredAt = e.OccurredAt.UTC()
	return e, nil
}

// DecodePayload decodes the payload into v. Unknown fields are ignored, so a
// consumer keeps working when the contract gains an optional field.
func (e Envelope) DecodePayload(v any) error {
	if !isJSONObject(e.Payload) {
		return fmt.Errorf("%w: payload must be a JSON object (event %s)", ErrInvalidEnvelope, e.EventID)
	}
	if err := json.Unmarshal(e.Payload, v); err != nil {
		return fmt.Errorf("%w: decode the payload of event %s: %v", ErrInvalidEnvelope, e.EventID, err)
	}
	return nil
}

// isJSONObject tells whether raw is a valid JSON object.
func isJSONObject(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return false
	}
	return json.Valid(trimmed)
}
