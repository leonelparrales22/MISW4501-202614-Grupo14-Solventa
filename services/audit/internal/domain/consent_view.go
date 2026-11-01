package domain

import (
	"sort"
	"time"
)

// ConsentStatus is the state of a consent as the holder sees it (HU-M1.3-01).
// It is derived from the record and its append-only events, never stored as a
// mutable column (decision D-4 a).
type ConsentStatus string

const (
	// StatusActive covers the holder: granted, not expired, not revoked.
	StatusActive ConsentStatus = "active"
	// StatusExpired reached its expires_at.
	StatusExpired ConsentStatus = "expired"
	// StatusRevoked was revoked before it expired (HU-M1.3-02).
	StatusRevoked ConsentStatus = "revoked"
	// StatusRevocationPending is reserved: the server does not use it with
	// decision D-1 a of HU-M1.3-02 ("pending upload" is a state of the app's
	// offline queue).
	StatusRevocationPending ConsentStatus = "revocation_pending"
	// StatusPartiallyRevoked is a revoked consent (Solventa no longer queries
	// the sources) that some entity did not confirm in its window
	// (HU-M1.3-02, decision D-1 a; HU-W4.2-02 shows it and retries).
	StatusPartiallyRevoked ConsentStatus = "partially_revoked"
)

// Current tells whether the status still covers the holder. Only an active
// consent does; the reserved statuses are not current.
func (s ConsentStatus) Current() bool { return s == StatusActive }

// Revocable tells whether a consent with this status can be revoked
// (HU-M1.3-02 criterion 6): only an active one.
func (s ConsentStatus) Revocable() bool { return s == StatusActive }

// known tells whether s is one of the statuses above.
func (s ConsentStatus) known() bool {
	switch s {
	case StatusActive, StatusExpired, StatusRevoked, StatusRevocationPending, StatusPartiallyRevoked:
		return true
	}
	return false
}

// Statuses of the history of a consent. granted and expired are synthetic:
// they come from the record itself (granted_at, expires_at), not from an
// event. revocation_requested and revoked are written by HU-M1.3-02, with the
// events of each entity (revocation.go).
const (
	HistoryGranted             = "granted"
	HistoryExpired             = "expired"
	HistoryRevocationRequested = "revocation_requested"
	HistoryRevoked             = "revoked"
)

// ConsentEvent is one row of the append-only consent_event table: a change of
// state of a consent, or of one entity of it when EntityCode is set.
type ConsentEvent struct {
	EventID   string
	ConsentID string
	// EntityCode is empty for an event of the whole consent.
	EntityCode string
	Status     string
	OccurredAt time.Time
	RecordedAt time.Time
	ActorType  string
	ActorID    string
	Channel    string
	Device     string
	ReceiptID  string
	Reason     string
	Reference  string
	Details    []byte
}

// Entity is the financial entity a consent authorizes, as the holder sees it.
// Today it is the partner the consent was granted through (decision D-2 a).
type Entity struct {
	Code string
	Name string
}

// EntityNames is the catalog of display names by entity code
// (PARTNER_DISPLAY_NAMES).
type EntityNames map[string]string

// Entity returns the entity with its display name; a code without a name is
// shown as it is.
func (n EntityNames) Entity(code string) Entity {
	if name, ok := n[code]; ok && name != "" {
		return Entity{Code: code, Name: name}
	}
	return Entity{Code: code, Name: code}
}

// HistoryEntry is one change of state shown to the holder (criterion 2).
type HistoryEntry struct {
	Status     string
	OccurredAt time.Time
	ReceiptID  string
	Channel    string
	EntityCode string
}

// ConsentView is a consent as the holder sees it: the record, its derived
// status and its history (criteria 1 to 3).
type ConsentView struct {
	Consent Consent
	Status  ConsentStatus
	// Current is Status.Current(): the consent still covers the holder.
	Current  bool
	Entities []Entity
	// EndedAt is when the consent stopped covering the holder; zero while it
	// is current.
	EndedAt time.Time
	// RevocationReceiptID is the receipt of the revocation, when revoked.
	RevocationReceiptID string
	// Revocable is Status.Revocable(): the holder can still revoke it.
	Revocable bool
	// Revocation is the revocation that ended the consent, with the state of
	// the notice to each entity (HU-M1.3-02); nil when it was not revoked.
	Revocation *RevocationView
	History    []HistoryEntry
}

// sortEvents orders the events by occurred_at, then recorded_at, then id, so
// the order is deterministic even when two events share an instant.
func sortEvents(events []ConsentEvent) []ConsentEvent {
	out := append([]ConsentEvent(nil), events...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if !a.OccurredAt.Equal(b.OccurredAt) {
			return a.OccurredAt.Before(b.OccurredAt)
		}
		if !a.RecordedAt.Equal(b.RecordedAt) {
			return a.RecordedAt.Before(b.RecordedAt)
		}
		return a.EventID < b.EventID
	})
	return out
}

// DeriveStatus returns the status of the consent at now, when it stopped
// being current (zero if it is) and the receipt of its revocation. It is
// deterministic (decision D-4 a):
//
//  1. Revoked, when an event of the whole consent says revoked and happened
//     before expires_at; it ended at that event and its receipt is the
//     event's. It is partially revoked when some entity of the revocation
//     did not confirm it: a revocation_not_confirmed of the entity without a
//     later revocation_confirmed, or no confirmation once its window ended
//     (HU-M1.3-02, decision D-1 a).
//  2. Otherwise expired, when now is at or after expires_at; it ended at
//     expires_at, computed, with no event.
//  3. Otherwise the status of the last event of the whole consent, when it is
//     a known status.
//  4. Otherwise active.
//
// A revocation after the consent expired does not change the status, but it
// still shows in the history.
func DeriveStatus(c Consent, events []ConsentEvent, now time.Time) (status ConsentStatus, endedAt time.Time, receiptID string) {
	status, endedAt, receiptID, _ = derive(c, events, now, nil)
	return status, endedAt, receiptID
}

// derive is DeriveStatus plus the revocation that ended the consent, if any.
func derive(c Consent, events []ConsentEvent, now time.Time, names EntityNames) (
	status ConsentStatus, endedAt time.Time, receiptID string, revocation *RevocationView) {
	expires := c.ExpiresAt()
	sorted := sortEvents(events)
	own := make([]ConsentEvent, 0, len(sorted))
	for _, e := range sorted {
		if e.ConsentID == c.ID && e.EntityCode == "" {
			own = append(own, e)
		}
	}
	for _, e := range own {
		if e.Status == HistoryRevoked && e.OccurredAt.Before(expires) {
			rev := revocationView(c, e, own, sorted, now, names)
			return rev.Status, e.OccurredAt, e.ReceiptID, rev
		}
	}
	if !now.Before(expires) {
		return StatusExpired, expires, "", nil
	}
	if n := len(own); n > 0 {
		if last := ConsentStatus(own[n-1].Status); last.known() && last != StatusRevoked && last != StatusExpired &&
			last != StatusPartiallyRevoked {
			return last, time.Time{}, "", nil
		}
	}
	return StatusActive, time.Time{}, "", nil
}

// revocationView derives the revocation that the revoked event ended: its
// receipt, the time it was requested (the revocation_requested event of the
// whole consent before it, if any), the time it took effect, the channel and
// device, and the state of the notice to each entity at now.
func revocationView(c Consent, revoked ConsentEvent, own, sorted []ConsentEvent, now time.Time, names EntityNames) *RevocationView {
	requestedAt := revoked.OccurredAt
	for _, e := range own {
		if e.Status == HistoryRevocationRequested && !e.OccurredAt.After(revoked.OccurredAt) {
			requestedAt = e.OccurredAt
		}
	}
	entityEvents := make([]ConsentEvent, 0, len(sorted))
	for _, e := range sorted {
		if e.ConsentID == c.ID && e.EntityCode != "" && !e.OccurredAt.Before(revoked.OccurredAt) {
			entityEvents = append(entityEvents, e)
		}
	}
	notices, partial := deriveNotices(revoked, entityEvents, now, names)
	status := StatusRevoked
	if partial {
		status = StatusPartiallyRevoked
	}
	return &RevocationView{
		ReceiptNumber: revoked.ReceiptID, Status: status,
		RequestedAt: requestedAt.UTC(), EffectiveAt: revoked.OccurredAt.UTC(),
		Channel: revoked.Channel, Device: revoked.Device,
		Summary: summarize(notices, partial), Entities: notices,
		OutOfDeadline: revocationOutOfDeadline(revoked, entityEvents, now),
	}
}

// View builds what the holder sees of a consent at now. The history starts
// with the grant (granted_at, with its channel), follows with the events of
// the consent in order and adds the expiry when it happened, all in
// chronological order.
func View(c Consent, events []ConsentEvent, now time.Time, names EntityNames) ConsentView {
	status, endedAt, receipt, revocation := derive(c, events, now, names)
	history := []HistoryEntry{{Status: HistoryGranted, OccurredAt: c.GrantedAt, Channel: string(c.Channel)}}
	for _, e := range sortEvents(events) {
		if e.ConsentID != c.ID {
			continue
		}
		history = append(history, HistoryEntry{
			Status: e.Status, OccurredAt: e.OccurredAt, ReceiptID: e.ReceiptID, Channel: e.Channel, EntityCode: e.EntityCode,
		})
	}
	if expires := c.ExpiresAt(); !now.Before(expires) {
		history = append(history, HistoryEntry{Status: HistoryExpired, OccurredAt: expires})
		// Keep it in time order; an event at the same instant goes first.
		sort.SliceStable(history, func(i, j int) bool { return history[i].OccurredAt.Before(history[j].OccurredAt) })
	}
	return ConsentView{
		Consent:             c,
		Status:              status,
		Current:             status.Current(),
		Entities:            []Entity{names.Entity(c.PartnerID)},
		EndedAt:             endedAt,
		RevocationReceiptID: receipt,
		Revocable:           status.Revocable(),
		Revocation:          revocation,
		History:             history,
	}
}

// SortForHolder orders the views as the holder reads them: the current ones
// first, most recently granted first; then the history, most recently ended
// first.
func SortForHolder(views []ConsentView) {
	sort.SliceStable(views, func(i, j int) bool {
		a, b := views[i], views[j]
		if a.Current != b.Current {
			return a.Current
		}
		if a.Current {
			if !a.Consent.GrantedAt.Equal(b.Consent.GrantedAt) {
				return a.Consent.GrantedAt.After(b.Consent.GrantedAt)
			}
			return a.Consent.ID < b.Consent.ID
		}
		if !a.EndedAt.Equal(b.EndedAt) {
			return a.EndedAt.After(b.EndedAt)
		}
		return a.Consent.ID < b.Consent.ID
	})
}
