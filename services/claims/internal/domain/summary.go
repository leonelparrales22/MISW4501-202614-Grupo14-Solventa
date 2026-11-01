package domain

import "time"

// ColombiaTime is the time zone the insured reads dates in. Colombia keeps
// UTC-5 all year, so a fixed zone needs no time zone database in the image.
var ColombiaTime = time.FixedZone("UTC-5", -5*60*60)

// CatalogFor returns the catalog a claim was filed with. A claim filed from
// the mobile app has no catalog version: its labels come from the current one.
func CatalogFor(version string) Catalog {
	// claim-catalog-v1 is the only version so far.
	return CatalogV1()
}

// SupportItem is one requirement of the claim type with the files attached to
// it.
type SupportItem struct {
	RequirementCode string
	Label           string
	Files           []SupportFile
}

// SupportSummary is what the confirmation shows of the supports: "3 of 3
// required, 8 files" and the files of each requirement.
type SupportSummary struct {
	RequiredTotal    int
	RequiredAttached int
	Files            int
	Items            []SupportItem
}

// IncidentLabel is the name the insured knows the claim type by.
func (c ClaimRecord) IncidentLabel() string {
	if o, ok := CatalogFor(c.CatalogVersion).Incident(c.IncidentType); ok {
		return o.Label
	}
	return string(c.IncidentType)
}

// SupportSummary groups the supports by requirement, in the order of the
// catalog. A claim filed from the mobile app has no supports: everything is
// zero.
func (c ClaimRecord) SupportSummary() SupportSummary {
	var s SupportSummary
	if c.Channel != ChannelWeb {
		return s
	}
	option, _ := CatalogFor(c.CatalogVersion).Incident(c.IncidentType)
	for _, req := range option.Requirements {
		item := SupportItem{RequirementCode: req.Code, Label: req.Label}
		for _, a := range c.Supports {
			if a.RequirementCode == req.Code {
				item.Files = append(item.Files, a.File)
			}
		}
		if req.Required {
			s.RequiredTotal++
			if len(item.Files) > 0 {
				s.RequiredAttached++
			}
		}
		if len(item.Files) > 0 {
			s.Files += len(item.Files)
			s.Items = append(s.Items, item)
		}
	}
	return s
}

// Receipt is what the filing receipt prints (HU-W2.1-01 criterion 4). It never
// carries the content of the files.
type Receipt struct {
	FilingNumber  string
	Status        Status
	FiledAt       time.Time
	PolicyID      string
	Product       string
	InsuredObject string
	IncidentLabel string
	OccurredAt    time.Time
	Place         string
	Supports      SupportSummary
}

// Receipt builds the receipt of the claim.
func (c ClaimRecord) Receipt() Receipt {
	r := Receipt{
		FilingNumber: c.FilingNumber, Status: c.Status, FiledAt: c.FiledAt, PolicyID: c.PolicyID,
		IncidentLabel: c.IncidentLabel(), OccurredAt: c.OccurredAt, Place: c.Place, Supports: c.SupportSummary(),
	}
	if c.Policy != nil {
		r.Product, r.InsuredObject = c.Policy.Product, c.Policy.InsuredObject
	}
	return r
}
