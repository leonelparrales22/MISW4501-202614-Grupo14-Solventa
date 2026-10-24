package events_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/events"
)

var occurredAt = time.Date(2026, 10, 24, 15, 4, 5, 123456000, time.FixedZone("COT", -5*3600))

type linkedPayload struct {
	CustomerID string `json:"customer_id"`
	Subject    string `json:"subject"`
}

func validEnvelope(t *testing.T) events.Envelope {
	t.Helper()
	e, err := events.New("customer-linked-c1", "CustomerLinked", "origination", 1, occurredAt,
		linkedPayload{CustomerID: "c1", Subject: "sub-1"})
	require.NoError(t, err)
	return e
}

func TestNewBuildsAValidEnvelopeInUTC(t *testing.T) {
	e := validEnvelope(t)

	assert.Equal(t, "customer-linked-c1", e.EventID)
	assert.Equal(t, "CustomerLinked", e.EventType)
	assert.Equal(t, 1, e.EventVersion)
	assert.Equal(t, "origination", e.Source)
	assert.Equal(t, time.UTC, e.OccurredAt.Location())
	assert.True(t, occurredAt.Equal(e.OccurredAt))
	assert.JSONEq(t, `{"customer_id":"c1","subject":"sub-1"}`, string(e.Payload))
	assert.NoError(t, e.Validate())
}

func TestNewRefusesAPayloadThatIsNotAnObject(t *testing.T) {
	_, err := events.New("id", "T", "src", 1, occurredAt, []string{"a"})
	assert.ErrorIs(t, err, events.ErrInvalidEnvelope)

	_, err = events.New("id", "T", "src", 1, occurredAt, func() {})
	assert.ErrorIs(t, err, events.ErrInvalidEnvelope, "a payload that cannot be encoded")
}

func TestValidateNamesEachMissingField(t *testing.T) {
	cases := map[string]func(*events.Envelope){
		"event_id":       func(e *events.Envelope) { e.EventID = "" },
		"event_type":     func(e *events.Envelope) { e.EventType = "" },
		"event_version":  func(e *events.Envelope) { e.EventVersion = 0 },
		"source":         func(e *events.Envelope) { e.Source = "" },
		"occurred_at":    func(e *events.Envelope) { e.OccurredAt = time.Time{} },
		"payload":        func(e *events.Envelope) { e.Payload = nil },
		"payload object": func(e *events.Envelope) { e.Payload = json.RawMessage(`"text"`) },
		"payload json":   func(e *events.Envelope) { e.Payload = json.RawMessage(`{"a":`) },
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			e := validEnvelope(t)
			breakIt(&e)

			err := e.Validate()

			assert.ErrorIs(t, err, events.ErrInvalidEnvelope)
			_, err = e.Encode()
			assert.ErrorIs(t, err, events.ErrInvalidEnvelope, "an invalid envelope is never encoded")
		})
	}
}

func TestEncodeAndDecodeRoundTrip(t *testing.T) {
	e := validEnvelope(t)
	e.CorrelationID = "corr-1"

	raw, err := e.Encode()
	require.NoError(t, err)
	got, err := events.Decode(raw)

	require.NoError(t, err)
	assert.Equal(t, e.EventID, got.EventID)
	assert.Equal(t, e.EventType, got.EventType)
	assert.Equal(t, e.EventVersion, got.EventVersion)
	assert.Equal(t, e.Source, got.Source)
	assert.Equal(t, "corr-1", got.CorrelationID)
	assert.True(t, e.OccurredAt.Equal(got.OccurredAt))
	assert.Equal(t, time.UTC, got.OccurredAt.Location())
	var p linkedPayload
	require.NoError(t, got.DecodePayload(&p))
	assert.Equal(t, linkedPayload{CustomerID: "c1", Subject: "sub-1"}, p)
}

func TestEncodeUsesTheContractFieldNames(t *testing.T) {
	raw, err := validEnvelope(t).Encode()
	require.NoError(t, err)

	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	for _, field := range []string{"event_id", "event_type", "event_version", "source", "occurred_at", "payload"} {
		assert.Contains(t, m, field)
	}
	assert.NotContains(t, m, "correlation_id", "an empty correlation id is left out")
	assert.Equal(t, "2026-10-24T20:04:05.123456Z", m["occurred_at"], "RFC 3339 in UTC")
}

func TestDecodeToleratesUnknownFields(t *testing.T) {
	raw := `{"event_id":"e1","event_type":"CustomerLinked","event_version":1,"source":"origination",
		"occurred_at":"2026-10-24T20:04:05Z","new_envelope_field":true,
		"payload":{"customer_id":"c1","subject":"sub-1","new_payload_field":"x"}}`

	e, err := events.Decode([]byte(raw))
	require.NoError(t, err)
	var p linkedPayload
	require.NoError(t, e.DecodePayload(&p))

	assert.Equal(t, "c1", p.CustomerID)
	assert.Equal(t, "sub-1", p.Subject)
}

func TestDecodeRefusesInvalidInput(t *testing.T) {
	for name, raw := range map[string]string{
		"not json":         `not json`,
		"missing type":     `{"event_id":"e1","event_version":1,"source":"s","occurred_at":"2026-10-24T20:04:05Z","payload":{}}`,
		"payload as array": `{"event_id":"e1","event_type":"T","event_version":1,"source":"s","occurred_at":"2026-10-24T20:04:05Z","payload":[]}`,
		"bad instant":      `{"event_id":"e1","event_type":"T","event_version":1,"source":"s","occurred_at":"yesterday","payload":{}}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := events.Decode([]byte(raw))
			assert.ErrorIs(t, err, events.ErrInvalidEnvelope)
		})
	}
}

func TestDecodePayloadRefusesAMismatchedPayload(t *testing.T) {
	e := validEnvelope(t)
	e.Payload = json.RawMessage(`{"customer_id":42}`)

	var p linkedPayload
	assert.ErrorIs(t, e.DecodePayload(&p), events.ErrInvalidEnvelope)

	e.Payload = json.RawMessage(`[]`)
	assert.ErrorIs(t, e.DecodePayload(&p), events.ErrInvalidEnvelope)
}
