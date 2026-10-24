package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrInvalidHolderLink is returned when a holder link misses a field or has an
// invalid one.
var ErrInvalidHolderLink = errors.New("invalid holder link")

// HolderLink ties the account of an insured (the subject of the user pool) to
// their identity document, so audit recognizes them as the holder of the
// consents granted with that document (HU-M1.3-01). It comes from the
// CustomerLinked event of origination and is append-only.
type HolderLink struct {
	Subject    string
	DocumentID string
	CustomerID string
	LinkedAt   time.Time
	// EventID is the CustomerLinked event that carried the link.
	EventID string
}

// NormalizeDocument trims the document and turns it to upper case. The
// registration and the consent both accept letters and digits with spaces
// around, but they may differ in case; normalizing both sides makes them
// match.
func NormalizeDocument(s string) string { return strings.ToUpper(strings.TrimSpace(s)) }

// Validate reports the first missing or invalid field. The document must be
// normalized already.
func (l HolderLink) Validate() error {
	identifier := func(field, value string) error {
		switch {
		case value == "":
			return fmt.Errorf("%w: %s is required", ErrInvalidHolderLink, field)
		case len(value) > MaxIdentifierLength:
			return fmt.Errorf("%w: %s is too long", ErrInvalidHolderLink, field)
		}
		return nil
	}
	for _, f := range []struct{ name, value string }{
		{"subject", l.Subject}, {"customer_id", l.CustomerID}, {"event_id", l.EventID},
	} {
		if err := identifier(f.name, f.value); err != nil {
			return err
		}
	}
	switch {
	case l.DocumentID == "":
		return fmt.Errorf("%w: document_id is required", ErrInvalidHolderLink)
	case !documentPattern.MatchString(l.DocumentID) || l.DocumentID != NormalizeDocument(l.DocumentID):
		return fmt.Errorf("%w: document_id is not a normalized document", ErrInvalidHolderLink)
	case l.LinkedAt.IsZero():
		return fmt.Errorf("%w: linked_at is required", ErrInvalidHolderLink)
	}
	return nil
}
