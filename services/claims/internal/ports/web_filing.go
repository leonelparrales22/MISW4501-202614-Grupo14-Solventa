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

// SupportStore keeps the metadata of the supports of the web filing
// (HU-W2.1-01). The file itself lives in the object store, under
// domain.SupportFile.ObjectKey.
type SupportStore interface {
	// Save stores a declared support, not confirmed yet.
	Save(ctx context.Context, f domain.SupportFile) error
	// Find returns the supports with the ids that exist, whoever owns them.
	// An id that does not exist is simply absent from the map.
	Find(ctx context.Context, ids []string) (map[string]domain.SupportFile, error)
	// Confirm marks the support as checked and returns it, or
	// domain.ErrSupportNotFound.
	Confirm(ctx context.Context, id string, at time.Time) (domain.SupportFile, error)
	// CountPending counts the supports of the owner not attached to any claim.
	CountPending(ctx context.Context, owner string) (int, error)
}

// SignedDownload is the signed instruction to read one stored object.
type SignedDownload struct {
	URL       string
	ExpiresAt time.Time
}

// ObjectStore is the object storage of the claims service: the evidence of
// the phone (HU-M2.1-01) and the supports of the portal (HU-W2.1-01). The
// files go from the client to the store with a signed URL; the service only
// signs, checks what arrived and signs the reads.
type ObjectStore interface {
	EvidenceStore
	// Head returns the first n bytes of the object, or ErrEvidenceNotFound.
	Head(ctx context.Context, key string, n int64) ([]byte, error)
	// SignDownload returns a URL that reads the object until ttl passes.
	SignDownload(ctx context.Context, key string, ttl time.Duration) (SignedDownload, error)
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
// (HU-W2.1-01). It is also the ClaimRepository of the mobile filing
// (HU-M2.2-01): one numbering, one set of filing keys and one writer.
type ClaimRecords interface {
	ClaimRepository
	// CreateWeb stores a web claim with its policy copy and attaches its
	// supports, all or nothing, and assigns its filing number. When the subject
	// already filed with the same key it returns that claim and created=false,
	// or ErrFilingKeyReused when it was another filing. A support already
	// attached to another claim gives ErrSupportAlreadyAttached.
	CreateWeb(ctx context.Context, c domain.ClaimRecord) (stored domain.ClaimRecord, created bool, err error)
	// FindByKey returns the claim the subject filed with the key, or
	// ErrClaimNotFound.
	FindByKey(ctx context.Context, subject, key string) (domain.ClaimRecord, error)
	// Get returns the claim with its supports and its evidence, or
	// ErrClaimNotFound.
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
