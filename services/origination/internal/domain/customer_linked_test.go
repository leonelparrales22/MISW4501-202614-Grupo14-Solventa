package domain_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/events"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

var linkedAt = time.Date(2026, 10, 24, 10, 30, 0, 0, time.FixedZone("COT", -5*3600))

func registeredCustomer() domain.Customer {
	return domain.Customer{
		ID: "cus-1", DocumentID: "1020345678", FullName: "Carolina Pérez Ruiz", Email: "carolina@example.com",
		BirthDate: time.Date(1992, 4, 18, 0, 0, 0, 0, time.UTC), CognitoSub: "sub-carolina",
		DocumentFrontKey: "identity/front", DocumentBackKey: "identity/back",
	}
}

func TestNewCustomerLinkedTakesTheLinkOfARegisteredCustomer(t *testing.T) {
	e, err := domain.NewCustomerLinked(registeredCustomer(), linkedAt)

	require.NoError(t, err)
	assert.Equal(t, "customer-linked-cus-1", e.EventID, "deterministic: one link per customer")
	assert.Equal(t, "cus-1", e.CustomerID)
	assert.Equal(t, "sub-carolina", e.Subject)
	assert.Equal(t, "1020345678", e.DocumentID)
	assert.Equal(t, time.UTC, e.LinkedAt.Location())
	assert.True(t, linkedAt.Equal(e.LinkedAt))
	assert.Equal(t, domain.CustomerLinkedEventID("cus-1"), e.EventID)
}

func TestNewCustomerLinkedNeedsTheIDTheSubjectAndTheDocument(t *testing.T) {
	for name, breakIt := range map[string]func(*domain.Customer){
		"id":       func(c *domain.Customer) { c.ID = "" },
		"subject":  func(c *domain.Customer) { c.CognitoSub = "" },
		"document": func(c *domain.Customer) { c.DocumentID = "" },
	} {
		t.Run(name, func(t *testing.T) {
			c := registeredCustomer()
			breakIt(&c)
			_, err := domain.NewCustomerLinked(c, linkedAt)
			assert.ErrorIs(t, err, domain.ErrIncompleteLink)
		})
	}
}

func TestTheEnvelopeCarriesOnlyWhatAuditNeeds(t *testing.T) {
	e, err := domain.NewCustomerLinked(registeredCustomer(), linkedAt)
	require.NoError(t, err)

	env, err := e.Envelope()
	require.NoError(t, err)
	raw, err := env.Encode()
	require.NoError(t, err)
	decoded, err := events.Decode(raw)
	require.NoError(t, err)

	assert.Equal(t, "customer-linked-cus-1", decoded.EventID)
	assert.Equal(t, "CustomerLinked", decoded.EventType)
	assert.Equal(t, 1, decoded.EventVersion)
	assert.Equal(t, "origination", decoded.Source)
	assert.True(t, linkedAt.Equal(decoded.OccurredAt))
	var payload map[string]any
	require.NoError(t, json.Unmarshal(decoded.Payload, &payload))
	assert.Equal(t, map[string]any{
		"customer_id": "cus-1", "subject": "sub-carolina", "document_id": "1020345678",
		"linked_at": "2026-10-24T15:30:00Z",
	}, payload)
	for _, personal := range []string{"Carolina", "carolina@example.com", "1992-04-18", "identity/front"} {
		assert.NotContains(t, string(raw), personal, "minimization: no name, e-mail, birth date or document keys")
	}
}
