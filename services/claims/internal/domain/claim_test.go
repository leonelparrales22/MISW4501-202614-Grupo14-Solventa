package domain_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
)

// The rules of the filing (HU-M2.2-01). What the assistant may send and what
// the service refuses before anything is stored.

var now = time.Date(2026, 10, 23, 15, 0, 0, 0, time.UTC)

func validRequest() domain.FilingRequest {
	return domain.FilingRequest{
		PolicyID:            "AU-2025-118204",
		IncidentType:        "collision",
		OccurredAt:          "2026-10-20T23:30:00Z",
		Place:               "Calle 80 con Carrera 15, Bogotá",
		Description:         "Choque por alcance en el semáforo.",
		Details:             map[string]string{"third_party": "yes"},
		DeclarationAccepted: true,
	}
}

func codes(t *testing.T, err error) map[string]string {
	t.Helper()
	var invalid *domain.ValidationError
	require.ErrorAs(t, err, &invalid)
	out := map[string]string{}
	for _, f := range invalid.Fields {
		out[f.Field] = f.Code
	}
	return out
}

func TestAValidFilingBecomesAnActiveClaim(t *testing.T) {
	c, err := validRequest().Validate(now)

	require.NoError(t, err)
	assert.Equal(t, domain.StatusActive, c.Status)
	assert.Equal(t, domain.IncidentCollision, c.IncidentType)
	assert.Equal(t, "AU-2025-118204", c.PolicyID)
	assert.Equal(t, time.Date(2026, 10, 20, 23, 30, 0, 0, time.UTC), c.OccurredAt)
	assert.Equal(t, "yes", c.Details["third_party"])
}

// The assistant sends what the insured typed; the extra blanks are not part
// of the declaration.
func TestTheTextIsTrimmed(t *testing.T) {
	r := validRequest()
	r.Place = "  Autopista Norte, Bogotá  "
	r.Description = "  Vidrio roto en el costado derecho.  "

	c, err := r.Validate(now)

	require.NoError(t, err)
	assert.Equal(t, "Autopista Norte, Bogotá", c.Place)
	assert.Equal(t, "Vidrio roto en el costado derecho.", c.Description)
}

// The four stages collect fields that depend on each other, so the insured
// has to see everything that is wrong at once.
func TestEveryInvalidFieldIsReportedTogether(t *testing.T) {
	r := domain.FilingRequest{}

	_, err := r.Validate(now)

	got := codes(t, err)
	assert.Equal(t, domain.CodeRequired, got["policy_id"])
	assert.Equal(t, domain.CodeRequired, got["incident_type"])
	assert.Equal(t, domain.CodeRequired, got["occurred_at"])
	assert.Equal(t, domain.CodeRequired, got["place"])
	assert.Equal(t, domain.CodeRequired, got["description"])
	assert.Equal(t, domain.CodeRequired, got["declaration_accepted"])
}

// The declaration under oath is what closes the filing: without it there is
// nothing to file.
func TestWithoutTheDeclarationThereIsNoFiling(t *testing.T) {
	r := validRequest()
	r.DeclarationAccepted = false

	_, err := r.Validate(now)

	assert.Equal(t, domain.CodeRequired, codes(t, err)["declaration_accepted"])
}

func TestAnUnknownIncidentTypeIsRefused(t *testing.T) {
	r := validRequest()
	r.IncidentType = "meteorite"

	_, err := r.Validate(now)

	assert.Equal(t, domain.CodeInvalidFormat, codes(t, err)["incident_type"])
}

func TestTheFourIncidentTypesAreAccepted(t *testing.T) {
	for _, incident := range []string{"collision", "theft", "third_party_damage", "other"} {
		t.Run(incident, func(t *testing.T) {
			r := validRequest()
			r.IncidentType = incident

			c, err := r.Validate(now)

			require.NoError(t, err)
			assert.Equal(t, domain.IncidentType(incident), c.IncidentType)
		})
	}
}

func TestAnIncidentCannotHaveHappenedInTheFuture(t *testing.T) {
	r := validRequest()
	r.OccurredAt = now.Add(time.Hour).Format(time.RFC3339)

	_, err := r.Validate(now)

	assert.Equal(t, domain.CodeOutOfRange, codes(t, err)["occurred_at"])
}

func TestAnIncidentOlderThanFiveYearsIsRefused(t *testing.T) {
	r := validRequest()
	r.OccurredAt = now.AddDate(-6, 0, 0).Format(time.RFC3339)

	_, err := r.Validate(now)

	assert.Equal(t, domain.CodeOutOfRange, codes(t, err)["occurred_at"])
}

func TestADateThatIsNotRFC3339IsRefused(t *testing.T) {
	r := validRequest()
	r.OccurredAt = "20/10/2026 18:30"

	_, err := r.Validate(now)

	assert.Equal(t, domain.CodeInvalidFormat, codes(t, err)["occurred_at"])
}

func TestTheDescriptionHasAMinimumAndAMaximum(t *testing.T) {
	short := validRequest()
	short.Description = "choque"
	assert.Equal(t, domain.CodeOutOfRange, codes(t, mustFail(t, short))["description"])

	long := validRequest()
	long.Description = strings.Repeat("a", domain.MaxDescription+1)
	assert.Equal(t, domain.CodeOutOfRange, codes(t, mustFail(t, long))["description"])
}

func TestThePlaceHasAMinimumAndAMaximum(t *testing.T) {
	short := validRequest()
	short.Place = "ab"
	assert.Equal(t, domain.CodeOutOfRange, codes(t, mustFail(t, short))["place"])

	long := validRequest()
	long.Place = strings.Repeat("a", domain.MaxPlace+1)
	assert.Equal(t, domain.CodeOutOfRange, codes(t, mustFail(t, long))["place"])
}

// The answers of the adaptive questions are bounded: they are answers, not a
// place to upload anything.
func TestTheDetailsAreBounded(t *testing.T) {
	many := validRequest()
	many.Details = map[string]string{}
	for i := 0; i <= domain.MaxDetails; i++ {
		many.Details["q"+string(rune('a'+i%26))+string(rune('a'+i/26))] = "yes"
	}
	assert.Equal(t, domain.CodeOutOfRange, codes(t, mustFail(t, many))["details"])

	long := validRequest()
	long.Details = map[string]string{"third_party": strings.Repeat("a", domain.MaxDetailValue+1)}
	assert.Equal(t, domain.CodeOutOfRange, codes(t, mustFail(t, long))["details"])

	badKey := validRequest()
	badKey.Details = map[string]string{"tercero involucrado": "yes"}
	assert.Equal(t, domain.CodeInvalidFormat, codes(t, mustFail(t, badKey))["details"])
}

func TestAFilingWithoutDetailsIsValid(t *testing.T) {
	r := validRequest()
	r.Details = nil

	c, err := r.Validate(now)

	require.NoError(t, err)
	assert.Empty(t, c.Details)
}

// Two filings are the same filing only when what they declare is the same.
func TestTheFingerprintChangesWithWhatWasDeclared(t *testing.T) {
	first, err := validRequest().Validate(now)
	require.NoError(t, err)
	same, err := validRequest().Validate(now)
	require.NoError(t, err)
	assert.Equal(t, first.Fingerprint(), same.Fingerprint())

	other := validRequest()
	other.Description = "Hurto del espejo retrovisor izquierdo."
	changed, err := other.Validate(now)
	require.NoError(t, err)
	assert.NotEqual(t, first.Fingerprint(), changed.Fingerprint())
}

// The order of the answers is not part of what was declared.
func TestTheFingerprintDoesNotDependOnTheOrderOfTheDetails(t *testing.T) {
	first := validRequest()
	first.Details = map[string]string{"third_party": "yes", "police_report": "no"}
	second := validRequest()
	second.Details = map[string]string{"police_report": "no", "third_party": "yes"}

	a, err := first.Validate(now)
	require.NoError(t, err)
	b, err := second.Validate(now)
	require.NoError(t, err)

	assert.Equal(t, a.Fingerprint(), b.Fingerprint())
}

// The number is what the insured quotes to support, so its shape is fixed.
func TestTheFilingNumberIsTheYearAndFiveDigits(t *testing.T) {
	assert.Equal(t, "SIN-2026-00001", domain.FilingNumber(now, 1))
	assert.Equal(t, "SIN-2026-01877", domain.FilingNumber(now, 1877))
	assert.Equal(t, "SIN-2026-123456", domain.FilingNumber(now, 123456))
}

func mustFail(t *testing.T, r domain.FilingRequest) error {
	t.Helper()
	_, err := r.Validate(now)
	require.Error(t, err)
	return err
}
