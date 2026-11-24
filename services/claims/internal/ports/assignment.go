package ports

import (
	"context"
	"errors"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
)

// ErrSearchConflict means another search of the claim was recorded in the
// same place first (HU-W2.3-01): two analysts, or an analyst and the
// automatic retry, searched at the same time and this one lost.
var ErrSearchConflict = errors.New("claims: another search of the claim was recorded first")

// ErrLocationConflict means another location of the claim was recorded in the
// same place first.
var ErrLocationConflict = errors.New("claims: another location of the claim was recorded first")

// ErrProviderNotFound means there is no provider with that id.
var ErrProviderNotFound = errors.New("claims: there is no provider with that id")

// ErrProviderExists means a provider with that id is registered already.
var ErrProviderExists = errors.New("claims: a provider with that id is registered already")

// NoticeStatus is where the e-mail to the assigned provider stands.
type NoticeStatus string

const (
	NoticePending NoticeStatus = "pending"
	NoticeSent    NoticeStatus = "sent"
)

// ProviderNotice is the e-mail that gives the assigned provider the location
// of the incident (HU-W2.3-01, decision D12). It is written with the
// assignment and sent by the worker, once per key.
type ProviderNotice struct {
	ClaimID       string
	To            string
	Subject       string
	Body          string
	Status        NoticeStatus
	Attempts      int
	NextAttemptAt time.Time
	SentAt        time.Time
	LastError     string
}

// AssignmentCase is a claim with everything its assignment is decided from:
// the claim with its evidence, the last location the analyst recorded, the
// last search and the assignment with its notice (nil when there are none).
type AssignmentCase struct {
	Claim      domain.ClaimRecord
	Location   *domain.IncidentLocation
	LastSearch *domain.ProviderSearch
	Assignment *domain.Assignment
	Notice     *ProviderNotice
}

// AssignmentStore keeps the locations, the searches and the assignments of
// the claims (HU-W2.3-01). It never changes nor deletes them: it only adds
// them. Only the notice to the provider changes, as it is sent.
type AssignmentStore interface {
	// Case returns the claim with its assignment, or ErrClaimNotFound.
	Case(ctx context.Context, claimID string) (AssignmentCase, error)
	// Cases returns the claims of the ids that exist; an id that does not
	// exist is absent from the map.
	Cases(ctx context.Context, ids []string) (map[string]AssignmentCase, error)
	// RecordLocation adds the location the analyst recorded in its place. It
	// returns domain.ErrAssignmentExists when the claim was assigned
	// meanwhile, ErrLocationConflict when the place is taken.
	RecordLocation(ctx context.Context, claimID string, l domain.IncidentLocation) error
	// RecordSearch adds the search in its place. It returns
	// domain.ErrAssignmentExists when the claim was assigned meanwhile,
	// ErrSearchConflict when the place is taken.
	RecordSearch(ctx context.Context, s domain.ProviderSearch) error
	// Assign records the assignment, makes its provider busy with the claim
	// and leaves the notice due, all or nothing. It returns
	// domain.ErrAssignmentExists when the claim has its provider already,
	// domain.ErrProviderUnavailable when the provider is not available any
	// more, domain.ErrSearchOutdated when the search is not the last one of
	// the claim and domain.ErrClaimNotAssignable when the claim left
	// evaluation.
	Assign(ctx context.Context, a domain.Assignment, n ProviderNotice) error
	// PendingAssignment returns, oldest searched first, up to limit claims in
	// evaluation without a provider whose last search found none.
	PendingAssignment(ctx context.Context, limit int) ([]string, error)
	// ClaimNoticesDue returns the notices due at now and holds them for lease,
	// so another task does not send them meanwhile.
	ClaimNoticesDue(ctx context.Context, now time.Time, lease time.Duration, limit int) ([]ProviderNotice, error)
	// NoticeSent records that the notice was delivered.
	NoticeSent(ctx context.Context, claimID string, at time.Time) error
	// NoticeFailed counts a failed delivery and makes it due again at next.
	NoticeFailed(ctx context.Context, claimID string, next time.Time, cause string) error
}

// ProviderFilter narrows the list of providers; the zero value lists them all.
type ProviderFilter struct {
	Specialty    domain.Specialty
	Availability domain.Availability
}

// ProviderReader reads the providers a search measures. HU-W2.3-03 searches
// with it too (assessors.Suggest).
type ProviderReader interface {
	// WithSpecialty returns every provider with the specialty, whatever its
	// availability.
	WithSpecialty(ctx context.Context, s domain.Specialty) ([]domain.Provider, error)
}

// ProviderStore is the registry of providers (HU-W2.3-01, criterion 2).
// Operations keeps it; the assignment only makes a provider busy.
type ProviderStore interface {
	ProviderReader
	// List returns the providers of the filter by name, up to limit.
	List(ctx context.Context, f ProviderFilter, limit int) ([]domain.Provider, error)
	// Get returns the provider, or ErrProviderNotFound.
	Get(ctx context.Context, id string) (domain.Provider, error)
	// Create registers the provider, or returns ErrProviderExists.
	Create(ctx context.Context, p domain.Provider) error
	// UpdateProfile replaces the profile of the provider and returns it, or
	// ErrProviderNotFound. Its availability does not change.
	UpdateProfile(ctx context.Context, id string, p domain.ProviderProfile, by string, at time.Time) (domain.Provider, error)
	// SetAvailability sets the availability of the provider, releasing it
	// from the claim it was busy with, and returns it, or
	// ErrProviderNotFound.
	SetAvailability(ctx context.Context, id string, a domain.Availability, until *time.Time, by string,
		at time.Time) (domain.Provider, error)
}
