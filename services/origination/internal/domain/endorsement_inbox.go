package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// HU-W1.4-02: the inbox of the operations analyst (screen O2), the history of
// the endorsements of a policy (screen O4) and the e-mail of each decision to
// the insured (decision D-12 a).

// InboxStatus is the status filter of the inbox of the analyst.
type InboxStatus string

const (
	// InboxPending is the default: filed, in review, and not applied and not
	// returned yet (it still waits for the analyst).
	InboxPending InboxStatus = "pending"
	// InboxAll is every status.
	InboxAll InboxStatus = "all"
)

// Limits of the inbox.
const (
	DefaultInboxPageSize = 20
	MaxInboxPageSize     = 50
)

// ErrInvalidInboxQuery: a type, status, page or page size the inbox does not
// understand.
var ErrInvalidInboxQuery = errors.New("the query of the endorsement inbox is not valid")

// ParseInboxStatus reads the status filter: pending (also when empty), filed,
// in_review, applied, rejected, not_applied or all.
func ParseInboxStatus(raw string) (InboxStatus, error) {
	switch s := InboxStatus(strings.TrimSpace(raw)); s {
	case "":
		return InboxPending, nil
	case InboxPending, InboxAll, InboxStatus(ChangeRequestFiled), InboxStatus(ChangeRequestInReview),
		InboxStatus(ChangeRequestApplied), InboxStatus(ChangeRequestRejected), InboxStatus(ChangeRequestNotApplied):
		return s, nil
	default:
		return "", fmt.Errorf("%w: status must be pending, filed, in_review, applied, rejected, not_applied or all", ErrInvalidInboxQuery)
	}
}

// EndorsementInboxFilter is the query of the inbox.
type EndorsementInboxFilter struct {
	// ChangeType narrows the inbox to one type; empty for all of them.
	ChangeType ChangeType
	Status     InboxStatus
	// FiledFrom is the first instant of the civil day of Colombia from which
	// the requests were filed; zero for no bound.
	FiledFrom time.Time
	Page      int
	PageSize  int
	// Now is the instant of the query, for the indicators.
	Now time.Time
}

// WithDefaults fills the status, the first page and the default page size.
func (f EndorsementInboxFilter) WithDefaults() EndorsementInboxFilter {
	if f.Status == "" {
		f.Status = InboxPending
	}
	if f.Page == 0 {
		f.Page = 1
	}
	if f.PageSize == 0 {
		f.PageSize = DefaultInboxPageSize
	}
	return f
}

// Validate checks a filter with its defaults applied.
func (f EndorsementInboxFilter) Validate() error {
	if f.ChangeType != "" {
		if _, ok := ParseChangeType(string(f.ChangeType)); !ok {
			return fmt.Errorf("%w: change_type must be beneficiaries, insured_amount or personal_data", ErrInvalidInboxQuery)
		}
	}
	switch {
	case f.Page < 1 || f.Page > maxExpiringPage:
		return fmt.Errorf("%w: page must be 1 or more", ErrInvalidInboxQuery)
	case f.PageSize < 1 || f.PageSize > MaxInboxPageSize:
		return fmt.Errorf("%w: page_size must be between 1 and %d", ErrInvalidInboxQuery, MaxInboxPageSize)
	}
	_, err := ParseInboxStatus(string(f.Status))
	return err
}

// Offset is the number of matching requests before the page.
func (f EndorsementInboxFilter) Offset() int { return (f.Page - 1) * f.PageSize }

// Matches tells whether a request passes the filter. It is the reference in Go
// of the condition of the SQL query.
func (f EndorsementInboxFilter) Matches(r ChangeRequest) bool {
	switch {
	case f.ChangeType != "" && r.Type != f.ChangeType:
		return false
	case !f.FiledFrom.IsZero() && r.FiledAt.Before(CivilDayOf(f.FiledFrom)):
		return false
	}
	switch f.Status {
	case InboxAll:
		return true
	case InboxPending, "":
		return r.Open()
	default:
		return r.Status == ChangeRequestStatus(f.Status)
	}
}

// PendingOverdue is how long a pending request waits before the inbox counts
// it as overdue (screen O2: "N llevan más de 24 h").
const PendingOverdue = 24 * time.Hour

// RecentDecisionWindow is the window of the indicator of the rejected and not
// applied requests (screen O2: "Rechazados (7 días)").
const RecentDecisionWindow = 7 * 24 * time.Hour

// TopReasonAnalystRejection is the most frequent reason when the analysts
// rejected more requests than any validation failed.
const TopReasonAnalystRejection = "analyst_rejection"

// EndorsementIndicators are the three counters of the inbox, whatever the
// filter.
type EndorsementIndicators struct {
	Pending        int
	PendingOverdue int
	// AppliedToday counts the endorsements of the civil day of Colombia, and
	// MeanTimeToApply is the mean time from filing to applying them (nil
	// without any).
	AppliedToday    int
	MeanTimeToApply *time.Duration
	// RejectedRecently counts the requests rejected or not applied in the last
	// seven days, and TopReason is the validation that failed most often
	// (or TopReasonAnalystRejection); empty without any.
	RejectedRecently int
	TopReason        string
}

// EndorsementInboxItem is one row of the inbox: the request with the holder
// of its policy.
type EndorsementInboxItem struct {
	Request        ChangeRequest
	HolderName     string
	HolderDocument string
}

// EndorsementInboxPage is a page of the inbox.
type EndorsementInboxPage struct {
	Items      []EndorsementInboxItem
	Total      int
	Page       int
	PageSize   int
	Indicators EndorsementIndicators
}

// TotalPages is how many pages the matching requests take (0 when none match).
func (p EndorsementInboxPage) TotalPages() int {
	if p.PageSize <= 0 {
		return 0
	}
	return (p.Total + p.PageSize - 1) / p.PageSize
}

// EndorsementHistoryEntry is one entry of the history of a policy (screen O4):
// an applied endorsement or an attempt that was not applied.
type EndorsementHistoryEntry struct {
	Outcome EndorsementOutcome
	At      time.Time
	// EndorsementID is set for an applied endorsement.
	EndorsementID string
	// Request is the request the entry came from: its type and its changes.
	Request ChangeRequest
	// PreviousPremium and NewPremium are set for an applied change of
	// insured value.
	PreviousPremium float64
	NewPremium      float64
	Currency        string
	// Failed are the validations that did not hold (not applied).
	Failed []ValidationCode
	By     Actor
}

// NoticeOutcome is the decision the insured is told by e-mail (decision D-12 a).
type NoticeOutcome string

const (
	NoticeApplied  NoticeOutcome = "applied"
	NoticeRejected NoticeOutcome = "rejected"
	NoticeReturned NoticeOutcome = "returned"
)

// NoticeDelivery is where the e-mail of a decision stands.
type NoticeDelivery struct {
	Status        NotificationStatus
	Attempts      int
	NextAttemptAt time.Time
	SentAt        time.Time
	LastFailureAt time.Time
	LastFailure   string
}

// EndorsementNotice is the e-mail of a decision on a request, with what it says.
type EndorsementNotice struct {
	RequestID     string
	PolicyID      string
	ChangeType    ChangeType
	Outcome       NoticeOutcome
	To            string
	Name          string
	DecidedAt     time.Time
	Observations  string
	EndorsementID string
	Delivery      NoticeDelivery
}

// EndorsementOutcomeIdempotencyKey is the key of the e-mail of the decision on
// a request: a retry never duplicates it.
func EndorsementOutcomeIdempotencyKey(requestID string) string {
	return "endorsement-outcome:" + requestID
}
