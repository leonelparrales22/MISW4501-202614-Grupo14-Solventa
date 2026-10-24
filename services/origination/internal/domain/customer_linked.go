package domain

import (
	"errors"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/events"
)

// Contract of the CustomerLinked event (docs/events/README.md).
const (
	EventTypeCustomerLinked    = "CustomerLinked"
	EventVersionCustomerLinked = 1
	// EventSource is the service that emits origination's events.
	EventSource = "origination"
	// customerLinkedIDPrefix starts the deterministic id of the event: one
	// link per customer, so the same fact always has the same id.
	customerLinkedIDPrefix = "customer-linked-"
)

// ErrIncompleteLink is returned when a customer misses what the link needs:
// its id, its account subject or its document.
var ErrIncompleteLink = errors.New("the customer cannot be linked: id, account subject and document are required")

// CustomerLinked records that the account of the insured (the subject of the
// user pool) was linked to their identity document when the registration
// finished (HU-M1.1-01). The audit service uses it to recognize the holder of
// the consents (HU-M1.3-01).
//
// It carries only what audit needs (data minimization): never the name, the
// e-mail, the birth date nor the keys of the document images.
type CustomerLinked struct {
	EventID    string
	CustomerID string
	Subject    string
	DocumentID string
	LinkedAt   time.Time
}

// NewCustomerLinked builds the event for a registered customer at the given
// instant.
func NewCustomerLinked(c Customer, at time.Time) (CustomerLinked, error) {
	if c.ID == "" || c.CognitoSub == "" || c.DocumentID == "" {
		return CustomerLinked{}, ErrIncompleteLink
	}
	return CustomerLinked{
		EventID:    CustomerLinkedEventID(c.ID),
		CustomerID: c.ID,
		Subject:    c.CognitoSub,
		DocumentID: c.DocumentID,
		LinkedAt:   at.UTC(),
	}, nil
}

// CustomerLinkedEventID is the deterministic id of the link of a customer.
func CustomerLinkedEventID(customerID string) string { return customerLinkedIDPrefix + customerID }

// customerLinkedPayload is the JSON payload of the event.
type customerLinkedPayload struct {
	CustomerID string    `json:"customer_id"`
	Subject    string    `json:"subject"`
	DocumentID string    `json:"document_id"`
	LinkedAt   time.Time `json:"linked_at"`
}

// Envelope wraps the event in the envelope of pkg/events.
func (e CustomerLinked) Envelope() (events.Envelope, error) {
	return events.New(e.EventID, EventTypeCustomerLinked, EventSource, EventVersionCustomerLinked, e.LinkedAt,
		customerLinkedPayload{CustomerID: e.CustomerID, Subject: e.Subject, DocumentID: e.DocumentID, LinkedAt: e.LinkedAt.UTC()})
}
