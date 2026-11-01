// Package ports declares what the core of the partners service needs from
// the outside: its database and the API Gateway that applies the credentials
// and the quota of each partner (HU-W3.1-01).
package ports

import (
	"context"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/events"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/partners/internal/domain"
)

// Clock tells the time.
type Clock interface {
	Now() time.Time
}

// PartnerRecord is a partner with its credentials (all of them, newest first)
// and its history (newest first).
type PartnerRecord struct {
	Partner     domain.Partner
	Credentials []domain.Credential
	History     []domain.HistoryEntry
}

// Change is what a locked mutation writes, all in the same transaction.
type Change struct {
	// Partner, when not nil, replaces the status, the usage plan and the
	// approval instant of the partner.
	Partner *domain.Partner
	// NewCredentials are inserted.
	NewCredentials []domain.Credential
	// UpdatedCredentials replace the status, expiry and revocation of
	// existing credentials.
	UpdatedCredentials []domain.Credential
	// History entries are appended.
	History []domain.HistoryEntry
	// Events go to the outbox.
	Events []events.Envelope
}

// PartnerStore is the database of the partners.
type PartnerStore interface {
	// Create inserts a partner with its first history entry; a taken code
	// is domain.ErrCodeTaken.
	Create(ctx context.Context, p domain.Partner, entry domain.HistoryEntry) error
	// List returns every partner with its credentials (no history).
	List(ctx context.Context) ([]PartnerRecord, error)
	// Get returns a partner with its credentials and history, or
	// domain.ErrPartnerNotFound.
	Get(ctx context.Context, id string) (PartnerRecord, error)
	// Mutate locks the partner, calls fn with its current record and writes
	// the change fn returns in the same transaction. If fn fails nothing is
	// written. Concurrent mutations of the same partner run one after the
	// other.
	Mutate(ctx context.Context, id string, fn func(PartnerRecord) (Change, error)) error
	// DueRetirements returns the partners with a retiring credential whose
	// transition period ended at now.
	DueRetirements(ctx context.Context, now time.Time, limit int) ([]string, error)
}

// UsagePlanSpec is the usage plan of a partner.
type UsagePlanSpec struct {
	Name        string
	Description string
	Quota       domain.Quota
}

// APIKeySpec is an API key to create.
type APIKeySpec struct {
	Name        string
	Value       string
	Description string
}

// Gateway manages the API keys and usage plans of the partners in the API
// Gateway, where the quota and the isolation are enforced. Every error is
// wrapped in domain.ErrGatewayUnavailable; deleting something that no longer
// exists is not an error.
type Gateway interface {
	CreateUsagePlan(ctx context.Context, spec UsagePlanSpec) (string, error)
	DeleteUsagePlan(ctx context.Context, planID string) error
	CreateAPIKey(ctx context.Context, spec APIKeySpec) (string, error)
	DeleteAPIKey(ctx context.Context, keyID string) error
	AttachKey(ctx context.Context, planID, keyID string) error
	DetachKey(ctx context.Context, planID, keyID string) error
	// UsedOn returns the requests used on a day (UTC, YYYY-MM-DD) by each key
	// of the plan.
	UsedOn(ctx context.Context, planID, day string) (map[string]int64, error)
}
