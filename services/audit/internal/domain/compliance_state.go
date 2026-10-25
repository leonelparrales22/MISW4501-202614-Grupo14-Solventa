package domain

import "strings"

// ComplianceState is a consent as the compliance portal sees it (HU-W4.2-02):
// the view derived from the record and its events, the immutable revocation
// record (who requested it, why and with which reference) and the events in
// order. Record is nil while the consent is not revoked.
type ComplianceState struct {
	View    ConsentView
	Record  *Revocation
	History []ConsentEvent
}

// MaskDocument hides every character of the document but the last four. With
// four characters or fewer, all of them are hidden.
func MaskDocument(doc string) string {
	runes := []rune(strings.TrimSpace(doc))
	if len(runes) <= 4 {
		return strings.Repeat("*", len(runes))
	}
	return strings.Repeat("*", len(runes)-4) + string(runes[len(runes)-4:])
}
