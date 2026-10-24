// Package ports declares what the audit core needs from the outside. The
// adapters implement it (AGENTS.md §2).
package ports

import (
	"context"
	"errors"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
)

// ErrConsentNotFound is returned by a ConsentStore that has no consent with
// the requested id.
var ErrConsentNotFound = errors.New("consent not found")

// ConsentStore keeps the consent records. It is append-only (HU-W1.2-01
// criterion 4): it has no operation to update or delete a record, and the
// PostgreSQL adapter also rejects them in the database. A future revocation
// will be a new record, never an update.
type ConsentStore interface {
	// Create stores the consent. It is idempotent by (customer_id,
	// application_id, terms_version): when a consent for that key exists it
	// returns the stored one with created=false and writes nothing.
	Create(ctx context.Context, c domain.Consent) (stored domain.Consent, created bool, err error)
	// Get returns the consent or ErrConsentNotFound.
	Get(ctx context.Context, id string) (domain.Consent, error)
	// LatestByCustomer returns the most recently granted consent of the
	// customer, or ErrConsentNotFound.
	LatestByCustomer(ctx context.Context, customerID string) (domain.Consent, error)
	// ListByHolder returns every consent granted with the document, most
	// recently granted first (HU-M1.3-01). The document is normalized
	// (domain.NormalizeDocument) and compared without case.
	ListByHolder(ctx context.Context, document string) ([]domain.Consent, error)
}

// ConsentEventReader reads the append-only consent_event table: the changes
// of state of the consents (HU-M1.3-01). Writing events is HU-M1.3-02's.
type ConsentEventReader interface {
	// ListByConsents returns the events of each consent, keyed by consent id.
	// A consent without events has no key.
	ListByConsents(ctx context.Context, consentIDs []string) (map[string][]domain.ConsentEvent, error)
}

// ErrHolderNotLinked is returned when the account (subject) has no holder
// link yet: origination has not sent its CustomerLinked event, or the account
// never went through the registration (decision D-13 a).
var ErrHolderNotLinked = errors.New("the account is not linked to a holder")

// HolderLinkStore keeps the links between accounts and documents
// (HU-M1.3-01). It is append-only: a link is never updated nor deleted.
type HolderLinkStore interface {
	// Link inserts the link unless the subject already has one: then it
	// returns the stored link with created=false and writes nothing.
	Link(ctx context.Context, l domain.HolderLink) (stored domain.HolderLink, created bool, err error)
	// DocumentOf returns the normalized document linked to the subject, or
	// ErrHolderNotLinked.
	DocumentOf(ctx context.Context, subject string) (string, error)
}

// Clock gives the server time, the only source of granted_at.
type Clock interface {
	Now() time.Time
}
