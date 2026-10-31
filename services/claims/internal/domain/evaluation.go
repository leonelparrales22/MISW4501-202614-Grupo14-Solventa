package domain

import "strings"

// Coverage is what the policy pays for one covered incident (HU-W2.2-03): an
// approved indemnity cannot exceed InsuredValueMinor, and
// DeductibleBasisPoints of it are discounted before paying (500 is 5 %).
type Coverage struct {
	Incident              IncidentType
	InsuredValueMinor     int64
	Currency              string
	DeductibleBasisPoints int
}

// PolicyTerms is the policy of a claim as the policy core describes it today,
// with the terms of every coverage. The copy taken when the claim was filed
// (ClaimRecord.Policy) says what was covered then; these are the amounts the
// evaluation works with.
type PolicyTerms struct {
	Policy    Policy
	Coverages []Coverage
}

// CoverageOf returns the coverage of the incident, or false when the policy
// does not cover it.
func (t PolicyTerms) CoverageOf(incident IncidentType) (Coverage, bool) {
	for _, c := range t.Coverages {
		if c.Incident == incident {
			return c, true
		}
	}
	return Coverage{}, false
}

// Holder is the insured as the policy core knows them: who they are, where
// they are notified and the account an indemnity is paid to (screen O6).
type Holder struct {
	FullName      string
	DocumentType  string
	DocumentID    string
	Email         string
	Phone         string
	PayoutAccount PayoutAccount
}

// PayoutAccount is the bank account registered to receive indemnities.
type PayoutAccount struct {
	BankCode      string
	BankName      string
	AccountType   string
	AccountNumber string
}

// MaskedNumber is the account number as operations sees it: only the last
// four digits, like "•••• 4512". The full number is needed to pay, not to
// evaluate, so it never leaves the service in a read.
func (a PayoutAccount) MaskedNumber() string {
	var digits []rune
	for _, r := range a.AccountNumber {
		if r >= '0' && r <= '9' {
			digits = append(digits, r)
		}
	}
	if len(digits) == 0 {
		return ""
	}
	last := digits
	if len(last) > 4 {
		last = last[len(last)-4:]
	}
	return "•••• " + strings.TrimSpace(string(last))
}
