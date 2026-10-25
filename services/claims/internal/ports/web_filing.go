package ports

import (
	"context"
	"errors"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
)

// ErrPolicyDirectoryUnavailable means the policy core did not answer: the
// claim is not filed (HU-W2.1-01, decision D1, fail-closed).
var ErrPolicyDirectoryUnavailable = errors.New("claims: the policies of the insured could not be read")

// PolicyDirectory is the policy core seen from the claims service
// (HU-W2.1-01). The holder is the subject of the token (assumption S1).
type PolicyDirectory interface {
	// HolderPolicies returns every policy of the holder, whatever its status,
	// or ErrPolicyDirectoryUnavailable.
	HolderPolicies(ctx context.Context, subject string) ([]domain.Policy, error)
}

// SupportStore keeps the uploaded supports of the web filing (HU-W2.1-01).
//
// Provisional (HU-W2.1-01 §9): the content lives in the service's database
// until the supports move to the evidence store of HU-M2.1-01 (S3 with a
// signed upload URL).
type SupportStore interface {
	// Save stores a new support with its content.
	Save(ctx context.Context, f domain.SupportFile, content []byte) error
	// Find returns the supports with the ids that exist, whoever owns them.
	// An id that does not exist is simply absent from the map.
	Find(ctx context.Context, ids []string) (map[string]domain.SupportFile, error)
	// CountPending counts the supports of the owner not attached to any claim.
	CountPending(ctx context.Context, owner string) (int, error)
}

// ErrClaimNotFound is returned when there is no claim with the id.
var ErrClaimNotFound = errors.New("claims: the claim does not exist")

// ErrSupportAlreadyAttached is returned when a support of the filing is
// already attached to another claim: a support belongs to one claim only.
var ErrSupportAlreadyAttached = errors.New("claims: a support is already attached to another claim")

// ClaimFilter narrows the list of an insured's claims. Zero values do not
// filter.
type ClaimFilter struct {
	Status    domain.Status
	PolicyID  string
	FiledFrom time.Time
}

// ClaimCursor is where a page of the list ends: the claims come newest first,
// ordered by (filed_at, id) descending.
type ClaimCursor struct {
	FiledAt time.Time
	ID      string
}

// ClaimRecords reads and writes the filed claims of both channels
// (HU-W2.1-01). It shares the claims, the filing numbers and the filing keys
// with the ClaimRepository of the mobile filing (HU-M2.2-01).
type ClaimRecords interface {
	// CreateWeb stores a web claim with its policy copy and attaches its
	// supports, all or nothing, and assigns its filing number. When the subject
	// already filed with the same key it returns that claim and created=false,
	// or ErrFilingKeyReused when it was another filing. A support already
	// attached to another claim gives ErrSupportAlreadyAttached.
	CreateWeb(ctx context.Context, c domain.ClaimRecord) (stored domain.ClaimRecord, created bool, err error)
	// FindByKey returns the claim the subject filed with the key, or
	// ErrClaimNotFound.
	FindByKey(ctx context.Context, subject, key string) (domain.ClaimRecord, error)
	// Get returns the claim with its supports, or ErrClaimNotFound.
	Get(ctx context.Context, id string) (domain.ClaimRecord, error)
	// ListByOwner returns up to limit claims of the subject, newest first,
	// after the cursor when there is one. The supports are not loaded.
	ListByOwner(ctx context.Context, subject string, f ClaimFilter, after *ClaimCursor, limit int) ([]domain.ClaimRecord, error)
}

// ReceiptRenderer draws the filing receipt the insured downloads (HU-W2.1-01
// criterion 4).
type ReceiptRenderer interface {
	Render(r domain.Receipt) ([]byte, error)
}
