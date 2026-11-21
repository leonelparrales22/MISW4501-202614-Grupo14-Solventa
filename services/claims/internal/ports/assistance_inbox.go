package ports

import (
	"context"
	"errors"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
)

// ErrAssistanceChanged means another analyst decided on the request after it
// was read: the write did nothing, and the caller reads it again and decides
// once more (HU-W2.3-03, decision D6).
var ErrAssistanceChanged = errors.New("claims: the request for assistance changed since it was read")

// AssistanceNotice is the e-mail that sends the dispatched provider the
// request and its location (HU-W2.3-03, decision D4). It is written with the
// dispatch and sent by the worker, once per key.
type AssistanceNotice struct {
	RequestID     string
	To            string
	Subject       string
	Body          string
	Status        NoticeStatus
	Attempts      int
	NextAttemptAt time.Time
	SentAt        time.Time
	LastError     string
}

// AssistanceRecord is a request with everything the inbox decided on it and
// the notice of its dispatch (nil when it was not dispatched).
type AssistanceRecord struct {
	Case   domain.AssistanceCase
	Notice *AssistanceNotice
}

// Version is how many decisions the request had when it was read. Every
// write of the inbox names it: a write on a request that changed meanwhile
// returns ErrAssistanceChanged.
func (r AssistanceRecord) Version() int { return len(r.Case.History) }

// InboxFilter narrows the inbox: the statuses (none is every status) and how
// many requests, the latest first.
type InboxFilter struct {
	Statuses []domain.AssistanceStatus
	Limit    int
}

// AssistanceInboxStore keeps the decisions of the inbox of operations on the
// requests for assistance (HU-W2.3-03). The requests are those of
// AssistanceStore (HU-M2.3-01); only their status changes. Every write is
// one transaction that appends the action numbered version+1, so the history
// keeps every decision (criterion 5, decision D7).
type AssistanceInboxStore interface {
	// List returns the requests of the filter, the latest first.
	List(ctx context.Context, f InboxFilter) ([]AssistanceRecord, error)
	// Counts returns how many requests are new and in call, and how long
	// each dispatch made since dayStart took from its request.
	Counts(ctx context.Context, dayStart time.Time) (newCount, inCall int, dispatches []time.Duration, err error)
	// Case returns the request, or ErrAssistanceRequestNotFound.
	Case(ctx context.Context, id string) (AssistanceRecord, error)
	// Attend makes the request in_call and records who attended it.
	Attend(ctx context.Context, id string, version int, a domain.AssistanceAction) error
	// RecordNeed records the need in force.
	RecordNeed(ctx context.Context, id string, version int, n domain.RecordedNeed, a domain.AssistanceAction) error
	// RecordVoiceLocation records the location given by voice in force.
	RecordVoiceLocation(ctx context.Context, id string, version int, v domain.VoiceLocation,
		a domain.AssistanceAction) error
	// Dispatch makes the request dispatched, records the dispatch and leaves
	// its notice due.
	Dispatch(ctx context.Context, id string, version int, d domain.AssistanceDispatch, a domain.AssistanceAction,
		n AssistanceNotice) error
	// Close makes the request closed and records why.
	Close(ctx context.Context, id string, version int, c domain.AssistanceClosure, a domain.AssistanceAction) error
	// DispatchesSince counts the dispatches of each provider since dayStart.
	DispatchesSince(ctx context.Context, providerIDs []string, dayStart time.Time) (map[string]int, error)
	// ClaimNoticesDue returns the notices due at now and holds them for
	// lease, so another task does not send them meanwhile.
	ClaimNoticesDue(ctx context.Context, now time.Time, lease time.Duration, limit int) ([]AssistanceNotice, error)
	// NoticeSent records that the notice was delivered.
	NoticeSent(ctx context.Context, requestID string, at time.Time) error
	// NoticeFailed counts a failed delivery and makes it due again at next.
	NoticeFailed(ctx context.Context, requestID string, next time.Time, cause string) error
}

// ProviderDirectory reads the registry of providers for the inbox: the
// providers a suggestion measures and the one a dispatch sends.
type ProviderDirectory interface {
	ProviderReader
	// Get returns the provider, or ErrProviderNotFound.
	Get(ctx context.Context, id string) (domain.Provider, error)
}
