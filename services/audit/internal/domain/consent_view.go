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
	// StatusRevocationPending is reserved for HU-M1.3-02: the revocation was
	// requested and is being applied.
	StatusRevocationPending ConsentStatus = "revocation_pending"
	// StatusPartiallyRevoked is reserved for HU-W4.2-02: some entities were
	// revoked and others were not.
	StatusPartiallyRevoked ConsentStatus = "partially_revoked"
)

// Current tells whether the status still covers the holder. Only an active
// consent does; the reserved statuses are not current.
func (s ConsentStatus) Current() bool { return s == StatusActive }

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
// event. revocation_requested and revoked are written by HU-M1.3-02.
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
	History             []HistoryEntry
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
//     event's.
//  2. Otherwise expired, when now is at or after expires_at; it ended at
//     expires_at, computed, with no event.
//  3. Otherwise the status of the last event of the whole consent, when it is
//     a known status.
//  4. Otherwise active.
//
// A revocation after the consent expired does not change the status, but it
// still shows in the history.
func DeriveStatus(c Consent, events []ConsentEvent, now time.Time) (status ConsentStatus, endedAt time.Time, receiptID string) {
	expires := c.ExpiresAt()
	own := make([]ConsentEvent, 0, len(events))
	for _, e := range sortEvents(events) {
		if e.ConsentID == c.ID && e.EntityCode == "" {
			own = append(own, e)
		}
	}
	for _, e := range own {
		if e.Status == HistoryRevoked && e.OccurredAt.Before(expires) {
			return StatusRevoked, e.OccurredAt, e.ReceiptID
		}
	}
	if !now.Before(expires) {
		return StatusExpired, expires, ""
	}
	if n := len(own); n > 0 {
		if last := ConsentStatus(own[n-1].Status); last.known() && last != StatusRevoked && last != StatusExpired {
			return last, time.Time{}, ""
		}
	}
	return StatusActive, time.Time{}, ""
}

// View builds what the holder sees of a consent at now. The history starts
// with the grant (granted_at, with its channel), follows with the events of
// the consent in order and adds the expiry when it happened, all in
// chronological order.
func View(c Consent, events []ConsentEvent, now time.Time, names EntityNames) ConsentView {
	status, endedAt, receipt := DeriveStatus(c, events, now)
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
