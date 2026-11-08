package domain

// AssignmentPolicyVersionV1 names the assignment policy of HU-W2.3-01
// (decision D6). It is not defined by the business yet: if it changes, it
// changes its version, and every search and assignment keeps the version it
// was made with.
const AssignmentPolicyVersionV1 = "assignment-policy-v1"

// SpecialtyRule is the specialty a claim type of a line needs. An empty
// Specialty is a claim type with no on-site visit.
type SpecialtyRule struct {
	Line      PolicyLine
	Incident  IncidentType
	Specialty Specialty
}

// AssignmentPolicy is a version of the specialty each claim type needs and of
// the radii the search widens through (criteria 2 and 4).
type AssignmentPolicy struct {
	Version     string
	Specialties []SpecialtyRule
	// RadiiKm are the radii of the search, in order: the first is the
	// initial one and the last the maximum.
	RadiiKm []float64
}

// AssignmentPolicyV1 returns the provisional policy (assumption S2 and the
// radii of the mockup). Each call returns a fresh copy, so no caller can
// change the policy of the others.
func AssignmentPolicyV1() AssignmentPolicy {
	return AssignmentPolicy{
		Version: AssignmentPolicyVersionV1,
		// Theft has no site to assess; water damage needs "plomería y
		// humedades", as the mockup shows.
		Specialties: []SpecialtyRule{
			{Line: LineAuto, Incident: IncidentCollision, Specialty: SpecialtyVehicleAssessment},
			{Line: LineAuto, Incident: IncidentTheft},
			{Line: LineAuto, Incident: IncidentThirdPartyDamage, Specialty: SpecialtyVehicleAssessment},
			{Line: LineAuto, Incident: IncidentGlass, Specialty: SpecialtyAutoGlass},
			{Line: LineAuto, Incident: IncidentOther, Specialty: SpecialtyVehicleAssessment},
			{Line: LineHome, Incident: IncidentWaterDamage, Specialty: SpecialtyPlumbing},
		},
		RadiiKm: []float64{5, 10, 20},
	}
}

// SpecialtyOf returns the specialty the claim type of the line needs, or ""
// when it needs no on-site visit. A type the table does not know needs no
// visit, and known is false so the caller can log it.
func (p AssignmentPolicy) SpecialtyOf(line PolicyLine, incident IncidentType) (s Specialty, known bool) {
	for _, r := range p.Specialties {
		if r.Line == line && r.Incident == incident {
			return r.Specialty, true
		}
	}
	return "", false
}

// SpecialtyFor is the specialty the claim needs. The line comes from the
// catalog the claim was filed with, as in the triage.
func (p AssignmentPolicy) SpecialtyFor(c ClaimRecord) (PolicyLine, Specialty, bool) {
	option, ok := CatalogFor(c.CatalogVersion).Incident(c.IncidentType)
	if !ok {
		return "", "", false
	}
	s, known := p.SpecialtyOf(option.Line, c.IncidentType)
	return option.Line, s, known
}

// InitialRadiusKm is the radius a search starts from.
func (p AssignmentPolicy) InitialRadiusKm() float64 { return p.RadiiKm[0] }

// MaxRadiusKm is the radius beyond which no provider is eligible.
func (p AssignmentPolicy) MaxRadiusKm() float64 { return p.RadiiKm[len(p.RadiiKm)-1] }

// Ladder is the radii a search from the given radius tries, in order; false
// when from is not a radius of the policy.
func (p AssignmentPolicy) Ladder(fromKm float64) ([]float64, bool) {
	for i, r := range p.RadiiKm {
		if r == fromKm {
			return append([]float64(nil), p.RadiiKm[i:]...), true
		}
	}
	return nil, false
}
