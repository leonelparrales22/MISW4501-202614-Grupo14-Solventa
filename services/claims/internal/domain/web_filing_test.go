package domain_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
)

// The rules of the web filing with supports (HU-W2.1-01 criteria 2 and 5).

const insured = "sub-carolina"

// uploads is what the insured uploaded: n files of each format, numbered so
// every id is distinct.
type uploads struct {
	files map[string]domain.SupportFile
	next  int
}

func newUploads() *uploads { return &uploads{files: map[string]domain.SupportFile{}} }

func (u *uploads) add(owner string, f domain.FileFormat, n int) []string {
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		u.next++
		id := fmt.Sprintf("sup-%032x", u.next)
		u.files[id] = domain.SupportFile{ID: id, OwnerSubject: owner, FileName: fmt.Sprintf("f%d", u.next),
			Format: f, ByteSize: 1024, SHA256: strings.Repeat("a", 64)}
		ids = append(ids, id)
	}
	return ids
}

func validWebFiling(u *uploads) domain.WebFilingRequest {
	return domain.WebFilingRequest{
		PolicyID:     "AU-2025-118204",
		IncidentType: "collision",
		OccurredAt:   "2026-09-14T23:40:00Z",
		Place:        "Calle 80 con carrera 30, Bogotá",
		Description:  "Al detenerme en el semáforo, un vehículo particular golpeó la parte trasera.",
		Supports: []domain.SupportGroup{
			{RequirementCode: "vehicle_photos", SupportIDs: u.add(insured, domain.FormatJPEG, 4)},
			{RequirementCode: "driver_document", SupportIDs: u.add(insured, domain.FormatPDF, 1)},
			{RequirementCode: "traffic_report", SupportIDs: u.add(insured, domain.FormatPNG, 1)},
		},
	}
}

func validateWeb(r domain.WebFilingRequest, u *uploads) (domain.ClaimRecord, error) {
	return r.Validate(now, domain.CatalogV1(), insured, u.files)
}

func TestAValidWebFilingBecomesAnActiveWebClaim(t *testing.T) {
	u := newUploads()
	r := validWebFiling(u)
	r.Place, r.Description = "  "+r.Place+"  ", " "+r.Description+" "

	c, err := validateWeb(r, u)

	require.NoError(t, err)
	assert.Equal(t, domain.StatusActive, c.Status)
	assert.Equal(t, domain.ChannelWeb, c.Channel)
	assert.Equal(t, "claim-catalog-v1", c.CatalogVersion)
	assert.Equal(t, domain.IncidentCollision, c.IncidentType)
	assert.Equal(t, time.Date(2026, 9, 14, 23, 40, 0, 0, time.UTC), c.OccurredAt)
	assert.Equal(t, "Calle 80 con carrera 30, Bogotá", c.Place)
	assert.Nil(t, c.Policy, "the policy is checked afterwards, with the core")
	require.Len(t, c.Supports, 6)
	for i, s := range c.Supports {
		assert.Equal(t, i, s.Position)
	}
	assert.Equal(t, "vehicle_photos", c.Supports[0].RequirementCode)
	assert.Equal(t, "traffic_report", c.Supports[5].RequirementCode)
}

func TestEveryMissingFieldAndSupportIsReportedTogether(t *testing.T) {
	u := newUploads()
	r := validWebFiling(u)
	r.Place = ""
	r.OccurredAt = ""
	r.Supports = r.Supports[:1] // only the photos
	r.Supports[0].SupportIDs = r.Supports[0].SupportIDs[:3]

	_, err := validateWeb(r, u)

	assert.Equal(t, map[string]string{
		"place":                    domain.CodeRequired,
		"occurred_at":              domain.CodeRequired,
		"supports.vehicle_photos":  domain.CodeTooFew,
		"supports.driver_document": domain.CodeMissing,
		"supports.traffic_report":  domain.CodeMissing,
	}, codes(t, err))
}

func TestTheNumberOfFilesOfASupportIsBounded(t *testing.T) {
	tests := []struct {
		photos int
		want   string
	}{
		{0, domain.CodeMissing},
		{3, domain.CodeTooFew},
		{4, ""},
		{10, ""},
		{11, domain.CodeTooMany},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%d photos", tt.photos), func(t *testing.T) {
			u := newUploads()
			r := validWebFiling(u)
			r.Supports[0].SupportIDs = u.add(insured, domain.FormatPNG, tt.photos)

			_, err := validateWeb(r, u)

			if tt.want == "" {
				assert.NoError(t, err)
				return
			}
			assert.Equal(t, map[string]string{"supports.vehicle_photos": tt.want}, codes(t, err))
		})
	}
}

func TestAnOptionalSupportMayBeLeftOutButNotOverfilled(t *testing.T) {
	u := newUploads()
	r := domain.WebFilingRequest{
		PolicyID: "AU-2025-118204", IncidentType: "glass", OccurredAt: "2026-10-20T10:00:00Z",
		Place: "Autopista Norte", Description: "Una piedra rompió el parabrisas.",
		Supports: []domain.SupportGroup{{RequirementCode: "glass_photos", SupportIDs: u.add(insured, domain.FormatJPEG, 2)}},
	}
	_, err := validateWeb(r, u)
	require.NoError(t, err, "the driver document is optional for glass")

	r.Supports = append(r.Supports, domain.SupportGroup{
		RequirementCode: "driver_document", SupportIDs: u.add(insured, domain.FormatPDF, 3)})
	_, err = validateWeb(r, u)
	assert.Equal(t, map[string]string{"supports.driver_document": domain.CodeTooMany}, codes(t, err))
}

func TestAFileOfAFormatTheSupportDoesNotTakeIsRefused(t *testing.T) {
	u := newUploads()
	r := validWebFiling(u)
	r.Supports[0].SupportIDs[2] = u.add(insured, domain.FormatPDF, 1)[0]

	_, err := validateWeb(r, u)

	assert.Equal(t, map[string]string{"supports.vehicle_photos": domain.CodeInvalidFormat}, codes(t, err))
}

func TestUnknownForeignAndRepeatedSupportsAreNotFound(t *testing.T) {
	tests := map[string]func(u *uploads, r *domain.WebFilingRequest){
		"unknown id": func(_ *uploads, r *domain.WebFilingRequest) {
			r.Supports[1].SupportIDs = []string{"sup-" + strings.Repeat("f", 32)}
		},
		"malformed id": func(_ *uploads, r *domain.WebFilingRequest) {
			r.Supports[1].SupportIDs = []string{"../../etc/passwd"}
		},
		"of another insured": func(u *uploads, r *domain.WebFilingRequest) {
			r.Supports[1].SupportIDs = u.add("sub-someone-else", domain.FormatPDF, 1)
		},
		"repeated in another support": func(_ *uploads, r *domain.WebFilingRequest) {
			r.Supports[1].SupportIDs = []string{r.Supports[2].SupportIDs[0]}
		},
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			u := newUploads()
			r := validWebFiling(u)
			change(u, &r)

			_, err := validateWeb(r, u)

			got := codes(t, err)
			assert.Contains(t, []string{got["supports.driver_document"], got["supports.traffic_report"]}, domain.CodeNotFound)
		})
	}
}

func TestASupportTheTypeDoesNotAskForIsNotApplicable(t *testing.T) {
	u := newUploads()
	r := validWebFiling(u)
	r.Supports = append(r.Supports, domain.SupportGroup{
		RequirementCode: "police_report", SupportIDs: u.add(insured, domain.FormatPDF, 1)})

	_, err := validateWeb(r, u)

	assert.Equal(t, map[string]string{"supports.police_report": domain.CodeNotApplicable}, codes(t, err))
}

func TestACodeThatIsNotOneIsNeverEchoed(t *testing.T) {
	u := newUploads()
	r := validWebFiling(u)
	r.Supports = append(r.Supports, domain.SupportGroup{RequirementCode: "<script>", SupportIDs: u.add(insured, domain.FormatPDF, 1)})

	_, err := validateWeb(r, u)

	assert.Equal(t, map[string]string{"supports": domain.CodeNotApplicable}, codes(t, err))
}

func TestTwoGroupsWithTheSameCodeAreOne(t *testing.T) {
	u := newUploads()
	r := validWebFiling(u)
	photos := r.Supports[0].SupportIDs
	r.Supports[0].SupportIDs = photos[:2]
	r.Supports = append(r.Supports, domain.SupportGroup{RequirementCode: "vehicle_photos", SupportIDs: photos[2:]})

	c, err := validateWeb(r, u)

	require.NoError(t, err)
	assert.Len(t, c.Supports, 6)
}

func TestNoMoreThanTwentyFilesInAll(t *testing.T) {
	u := newUploads()
	r := validWebFiling(u)
	r.Supports[0].SupportIDs = u.add(insured, domain.FormatJPEG, 10)
	r.Supports[1].SupportIDs = u.add(insured, domain.FormatPDF, 2)
	r.Supports[2].SupportIDs = u.add(insured, domain.FormatPDF, 3)
	_, err := validateWeb(r, u)
	require.NoError(t, err, "15 files")

	r.Supports = append(r.Supports, domain.SupportGroup{RequirementCode: "unused", SupportIDs: u.add(insured, domain.FormatPDF, 6)})
	_, err = validateWeb(r, u)
	assert.Equal(t, domain.CodeTooMany, codes(t, err)["supports"], "21 files")
}

func TestAnUnknownTypeIsRefusedWithoutJudgingTheSupports(t *testing.T) {
	u := newUploads()
	r := validWebFiling(u)
	r.IncidentType = "flight_delay"

	_, err := validateWeb(r, u)

	assert.Equal(t, map[string]string{"incident_type": domain.CodeInvalidFormat}, codes(t, err))
}

func TestTheWebTypesOfTheCatalogAreAccepted(t *testing.T) {
	for _, o := range domain.CatalogV1().Incidents {
		t.Run(string(o.Type), func(t *testing.T) {
			u := newUploads()
			r := validWebFiling(u)
			r.IncidentType = string(o.Type)
			r.Supports = nil
			for _, req := range o.Requirements {
				format := req.Formats[0]
				r.Supports = append(r.Supports, domain.SupportGroup{
					RequirementCode: req.Code, SupportIDs: u.add(insured, format, req.MinFiles)})
			}

			_, err := validateWeb(r, u)

			assert.NoError(t, err)
		})
	}
}

func TestTheTextRulesAreTheOnesOfTheMobileFiling(t *testing.T) {
	tests := []struct {
		name  string
		edit  func(r *domain.WebFilingRequest)
		field string
		code  string
	}{
		{"description of 9", func(r *domain.WebFilingRequest) { r.Description = strings.Repeat("a", 9) }, "description", domain.CodeOutOfRange},
		{"description of 1001", func(r *domain.WebFilingRequest) { r.Description = strings.Repeat("a", 1001) }, "description", domain.CodeOutOfRange},
		{"place of 161", func(r *domain.WebFilingRequest) { r.Place = strings.Repeat("a", 161) }, "place", domain.CodeOutOfRange},
		{"incident in the future", func(r *domain.WebFilingRequest) { r.OccurredAt = now.Add(time.Second).Format(time.RFC3339) }, "occurred_at", domain.CodeOutOfRange},
		{"date that is not RFC 3339", func(r *domain.WebFilingRequest) { r.OccurredAt = "14/09/2026 18:40" }, "occurred_at", domain.CodeInvalidFormat},
		{"policy id with spaces", func(r *domain.WebFilingRequest) { r.PolicyID = "AU 2025" }, "policy_id", domain.CodeInvalidFormat},
		{"no policy", func(r *domain.WebFilingRequest) { r.PolicyID = "" }, "policy_id", domain.CodeRequired},
		{"no type", func(r *domain.WebFilingRequest) { r.IncidentType = "" }, "incident_type", domain.CodeRequired},
		{"no description", func(r *domain.WebFilingRequest) { r.Description = " " }, "description", domain.CodeRequired},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := newUploads()
			r := validWebFiling(u)
			tt.edit(&r)

			_, err := validateWeb(r, u)

			assert.Equal(t, tt.code, codes(t, err)[tt.field])
		})
	}

	u := newUploads()
	r := validWebFiling(u)
	r.Description = strings.Repeat("a", 10)
	r.Place = strings.Repeat("a", 3)
	_, err := validateWeb(r, u)
	assert.NoError(t, err, "10 and 3 characters are the minimums")
}

func TestTheWebFingerprintChangesWithTheSupports(t *testing.T) {
	u := newUploads()
	a, err := validateWeb(validWebFiling(u), u)
	require.NoError(t, err)
	b := a
	b.Supports = append([]domain.AttachedSupport(nil), a.Supports...)
	b.Supports[0].File.ID = "sup-" + strings.Repeat("b", 32)

	assert.Equal(t, a.WebFingerprint(), a.WebFingerprint())
	assert.NotEqual(t, a.WebFingerprint(), b.WebFingerprint())
	assert.NotEqual(t, a.Fingerprint(), a.WebFingerprint(), "a web filing never matches a mobile one")
}
