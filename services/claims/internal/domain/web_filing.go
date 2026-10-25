package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"time"
)

// Channel is where a claim was filed.
type Channel string

const (
	ChannelMobile Channel = "mobile"
	ChannelWeb    Channel = "web"
)

// Field validation codes of the supports (HU-W2.1-01 criterion 2), besides
// the ones of HU-M2.2-01.
const (
	// CodeMissing is a required support without files.
	CodeMissing = "missing"
	// CodeTooFew and CodeTooMany are a support with fewer or more files than
	// the catalog allows (or more than MaxSupportsPerClaim in all).
	CodeTooFew  = "too_few"
	CodeTooMany = "too_many"
	// CodeNotApplicable is a support the claim type does not ask for.
	CodeNotApplicable = "not_applicable"
	// CodeNotFound is a support id that does not exist, belongs to another
	// insured or is repeated.
	CodeNotFound = "not_found"
)

var requirementCodePattern = regexp.MustCompile(`^[a-z_]{1,40}$`)

// SupportGroup is the uploaded supports that answer one requirement.
type SupportGroup struct {
	RequirementCode string
	SupportIDs      []string
}

// WebFilingRequest is what the web portal sends to file a claim with its
// supports (HU-W2.1-01).
type WebFilingRequest struct {
	PolicyID     string
	IncidentType string
	// OccurredAt is RFC 3339: the instant of the incident.
	OccurredAt  string
	Place       string
	Description string
	Supports    []SupportGroup
}

// AttachedSupport is a support attached to a claim, in the order the claim
// shows it.
type AttachedSupport struct {
	RequirementCode string
	Position        int
	File            SupportFile
}

// ClaimRecord is a filed claim as it is read back, from either channel: the
// claim of HU-M2.2-01 plus what the web filing adds. A claim filed from the
// mobile app has no Policy, no CatalogVersion and no Supports.
type ClaimRecord struct {
	Claim
	Channel Channel
	// Policy is the copy of the policy taken when the claim was filed
	// (decision D7).
	Policy         *Policy
	CatalogVersion string
	Supports       []AttachedSupport
}

// WebFingerprint summarizes a web filing. Two filings with the same key are
// the same filing only when their fingerprints match.
func (c ClaimRecord) WebFingerprint() string {
	var b strings.Builder
	b.WriteString(string(ChannelWeb) + "\n")
	b.WriteString(c.Fingerprint() + "\n")
	for _, s := range c.Supports {
		b.WriteString(s.RequirementCode + "=" + s.File.ID + "\n")
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// Validate checks the request at the given time against the catalog and the
// supports the insured uploaded, and returns the claim it describes, or a
// *ValidationError naming every invalid field and support at once.
//
// uploaded holds the supports the request names that exist, whoever owns them:
// one of another subject is reported as not found, the same as one that does
// not exist. The policy is not checked here: it needs the policy core (see
// Policy.EligibleFor).
func (r WebFilingRequest) Validate(now time.Time, catalog Catalog, subject string,
	uploaded map[string]SupportFile) (ClaimRecord, error) {
	var fields []FieldError
	bad := func(field, code string) { fields = append(fields, FieldError{Field: field, Code: code}) }

	policyID := strings.TrimSpace(r.PolicyID)
	switch {
	case policyID == "":
		bad("policy_id", CodeRequired)
	case !policyIDPattern.MatchString(policyID):
		bad("policy_id", CodeInvalidFormat)
	}

	incident := IncidentType(strings.TrimSpace(r.IncidentType))
	option, known := catalog.Incident(incident)
	switch {
	case incident == "":
		bad("incident_type", CodeRequired)
	case !known:
		bad("incident_type", CodeInvalidFormat)
	}

	// The same rules as the mobile filing (HU-M2.2-01).
	occurredAt := occurred(strings.TrimSpace(r.OccurredAt), now, bad)

	place := strings.TrimSpace(r.Place)
	switch {
	case place == "":
		bad("place", CodeRequired)
	case len([]rune(place)) < MinPlace, len([]rune(place)) > MaxPlace:
		bad("place", CodeOutOfRange)
	}

	description := strings.TrimSpace(r.Description)
	switch {
	case description == "":
		bad("description", CodeRequired)
	case len([]rune(description)) < MinDescription, len([]rune(description)) > MaxDescription:
		bad("description", CodeOutOfRange)
	}

	var supports []AttachedSupport
	if known {
		supports = checkSupports(r.Supports, option, subject, uploaded, bad)
	} else if countIDs(r.Supports) > MaxSupportsPerClaim {
		bad("supports", CodeTooMany)
	}

	if len(fields) > 0 {
		return ClaimRecord{}, &ValidationError{Fields: fields}
	}
	return ClaimRecord{
		Claim: Claim{
			PolicyID:     policyID,
			IncidentType: incident,
			OccurredAt:   occurredAt,
			Place:        place,
			Description:  description,
			Details:      map[string]string{},
			Status:       StatusActive,
		},
		Channel:        ChannelWeb,
		CatalogVersion: catalog.Version,
		Supports:       supports,
	}, nil
}

// checkSupports matches the groups of the request with the requirements of the
// claim type. Each group gets at most one code: an unknown or foreign file
// first, then a file of a format the requirement does not take, then the
// number of files. The supports come back in the order of the catalog.
func checkSupports(groups []SupportGroup, option IncidentOption, subject string,
	uploaded map[string]SupportFile, bad func(field, code string)) []AttachedSupport {
	if countIDs(groups) > MaxSupportsPerClaim {
		bad("supports", CodeTooMany)
	}

	// Two groups with the same code are one group.
	byCode := map[string][]string{}
	var order []string
	for _, g := range groups {
		code := strings.TrimSpace(g.RequirementCode)
		if _, seen := byCode[code]; !seen {
			order = append(order, code)
		}
		byCode[code] = append(byCode[code], g.SupportIDs...)
	}

	seen := map[string]bool{}
	files := func(code string, req Requirement, checkFormat bool) ([]SupportFile, string) {
		var out []SupportFile
		problem := ""
		for _, id := range byCode[code] {
			id = strings.TrimSpace(id)
			file, ok := uploaded[id]
			switch {
			case !ValidSupportID(id) || !ok || file.OwnerSubject != subject || seen[id]:
				problem = CodeNotFound
			case checkFormat && !req.Accepts(file.Format) && problem == "":
				problem = CodeInvalidFormat
			}
			seen[id] = true
			out = append(out, file)
		}
		return out, problem
	}

	var attached []AttachedSupport
	for _, req := range option.Requirements {
		found, problem := files(req.Code, req, true)
		n := len(found)
		switch {
		case problem != "":
		case req.Required && n == 0:
			problem = CodeMissing
		case n > 0 && n < req.MinFiles:
			problem = CodeTooFew
		case n > req.MaxFiles:
			problem = CodeTooMany
		}
		if problem != "" {
			bad("supports."+req.Code, problem)
			continue
		}
		for _, f := range found {
			attached = append(attached, AttachedSupport{RequirementCode: req.Code, Position: len(attached), File: f})
		}
	}

	// Groups the claim type does not ask for, in the order they came.
	for _, code := range order {
		if _, asked := option.Requirement(code); asked {
			continue
		}
		field := "supports." + code
		if !requirementCodePattern.MatchString(code) {
			// Never echo a code that is not one: it would be the caller's text.
			field = "supports"
		}
		if _, problem := files(code, Requirement{}, false); problem == CodeNotFound {
			bad(field, CodeNotFound)
			continue
		}
		bad(field, CodeNotApplicable)
	}
	return attached
}

func countIDs(groups []SupportGroup) int {
	n := 0
	for _, g := range groups {
		n += len(g.SupportIDs)
	}
	return n
}
