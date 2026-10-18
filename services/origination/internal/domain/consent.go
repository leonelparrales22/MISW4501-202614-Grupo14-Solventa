package domain

import "time"

// ConsentStatus is what the audit service answers about the explicit consent
// of a customer (HU-W1.2-01). Origination never decides it itself.
type ConsentStatus struct {
	// Active is true while the customer has a consent that covers profiling.
	Active    bool
	ConsentID string
	// ExpiresAt is when the consent stops being active; zero when there is none.
	ExpiresAt time.Time
}
