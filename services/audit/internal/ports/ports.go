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
}

// Clock gives the server time, the only source of granted_at.
type Clock interface {
	Now() time.Time
}
