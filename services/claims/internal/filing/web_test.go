package filing_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/filing"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// The use cases of the web filing with supports (HU-W2.1-01).

const carolina = "sub-carolina"

var (
	jpeg = append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, bytes.Repeat([]byte{1}, 2048)...)
	pdf  = append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte{2}, 2048)...)
	png  = append([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}, bytes.Repeat([]byte{3}, 2048)...)

	validFrom = time.Date(2025, 11, 1, 5, 0, 0, 0, time.UTC)
	validTo   = time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC)
)

// directory is the policy core of the tests: the policies of each holder, or
// a failure for everybody.
type directory struct {
	mu       sync.Mutex
	policies map[string][]domain.Policy
	err      error
	calls    int
}

func (d *directory) HolderPolicies(_ context.Context, subject string) ([]domain.Policy, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls++
	if d.err != nil {
		return nil, d.err
	}
	return d.policies[subject], nil
}

func auto(id string) domain.Policy {
	return domain.Policy{
		ID: id, Line: domain.LineAuto, Product: "Autos", InsuredObject: "KJT-482", Status: domain.PolicyActive,
		ValidFrom: validFrom, ValidTo: validTo,
		CoveredIncidents: []domain.IncidentType{domain.IncidentCollision, domain.IncidentTheft, domain.IncidentOther},
	}
}

// mobileOf is the mobile filing of HU-M2.2-01 over the store of the fixture,
// with the evidence of that insured already uploaded: the list, the pages and
// the filing keys are shared by the two channels.
func (f *webFixture) mobileOf(subject string, at time.Time) *filing.Service {
	evidencias := nuevoAlmacenDeEvidencia()
	evidencias.guardarTodo(requestDe(subject))
	return filing.NewService(f.store, evidencias, fixedClock{t: at})
}

type webFixture struct {
	store    *memory.CaseStore
	policies *directory
	web      *filing.WebService
	uploads  *filing.SupportService
	objects  *objectStore
}

func newWebFixture() *webFixture {
	store := memory.NewCaseStore()
	expired := auto("AU-2024-000001")
	expired.Status = "expired"
	home := domain.Policy{ID: "HG-2025-880231", Line: domain.LineHome, Product: "Hogar", InsuredObject: "Calle 1",
		Status: domain.PolicyActive, ValidFrom: validFrom, ValidTo: validTo,
		CoveredIncidents: []domain.IncidentType{domain.IncidentWaterDamage}}
	travel := domain.Policy{ID: "VJ-2025-000931", Line: domain.LineTravel, Product: "Viaje", Status: domain.PolicyActive,
		ValidFrom: validFrom, ValidTo: validTo, CoveredIncidents: []domain.IncidentType{"flight_delay"}}
	policies := &directory{policies: map[string][]domain.Policy{
		carolina:      {auto("AU-2025-118204"), home, travel, expired},
		"sub-someone": {auto("AU-2025-999999")},
	}}
	clock := fixedClock{t: filedAt}
	objects := newObjectStore()
	return &webFixture{
		store: store, policies: policies, objects: objects,
		web:     filing.NewWebService(store, store, policies, clock),
		uploads: filing.NewSupportService(store, objects, clock),
	}
}

// declaration is what the portal declares about a file it holds.
func declaration(name string, content []byte) domain.SupportUploadRequest {
	sum := sha256.Sum256(content)
	contentType := "application/pdf"
	switch {
	case bytes.HasPrefix(content, jpeg[:3]):
		contentType = "image/jpeg"
	case bytes.HasPrefix(content, png[:8]):
		contentType = "image/png"
	}
	return domain.SupportUploadRequest{
		FileName: name, ContentType: contentType, ByteSize: int64(len(content)), SHA256: hex.EncodeToString(sum[:]),
	}
}

// uploadOne declares a file, uploads it to the store as the browser would and
// confirms it.
func (f *webFixture) uploadOne(t *testing.T, subject, name string, content []byte) domain.SupportFile {
	t.Helper()
	up, err := f.uploads.Declare(context.Background(), subject, declaration(name, content))
	require.NoError(t, err)
	f.objects.put(up.File.ObjectKey(), content)
	confirmed, err := f.uploads.Confirm(context.Background(), subject, up.File.ID)
	require.NoError(t, err)
	return confirmed
}

func (f *webFixture) upload(t *testing.T, subject string, content []byte, n int) []string {
	t.Helper()
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		ids = append(ids, f.uploadOne(t, subject, fmt.Sprintf("file-%d", i), content).ID)
	}
	return ids
}

func (f *webFixture) collision(t *testing.T) domain.WebFilingRequest {
	t.Helper()
	return domain.WebFilingRequest{
		PolicyID:     "AU-2025-118204",
		IncidentType: "collision",
		OccurredAt:   "2026-09-14T23:40:00Z",
		Place:        "Calle 80 con carrera 30, Bogotá",
		Description:  "Un vehículo particular golpeó la parte trasera.",
		Supports: []domain.SupportGroup{
			{RequirementCode: "vehicle_photos", SupportIDs: f.upload(t, carolina, jpeg, 4)},
			{RequirementCode: "driver_document", SupportIDs: f.upload(t, carolina, pdf, 1)},
			{RequirementCode: "traffic_report", SupportIDs: f.upload(t, carolina, pdf, 1)},
		},
	}
}

func TestTheOptionsAreTheActivePoliciesWithTypesThatAreFiled(t *testing.T) {
	f := newWebFixture()

	options, err := f.web.Options(context.Background(), carolina)

	require.NoError(t, err)
	assert.Equal(t, "claim-catalog-v1", options.CatalogVersion)
	require.Len(t, options.Policies, 2, "neither the expired one nor travel")
	assert.Equal(t, "AU-2025-118204", options.Policies[0].Policy.ID)
	assert.Equal(t, "HG-2025-880231", options.Policies[1].Policy.ID)
	var types []domain.IncidentType
	for _, o := range options.Policies[0].Incidents {
		types = append(types, o.Type)
	}
	assert.Equal(t, []domain.IncidentType{domain.IncidentCollision, domain.IncidentTheft, domain.IncidentOther}, types)
}

func TestAnInsuredWithoutPoliciesHasNoOptions(t *testing.T) {
	options, err := newWebFixture().web.Options(context.Background(), "sub-nobody")

	require.NoError(t, err)
	assert.NotNil(t, options.Policies)
	assert.Empty(t, options.Policies)
}

func TestThePolicyCoreDownGivesNoOptions(t *testing.T) {
	f := newWebFixture()
	f.policies.err = ports.ErrPolicyDirectoryUnavailable

	_, err := f.web.Options(context.Background(), carolina)

	assert.ErrorIs(t, err, ports.ErrPolicyDirectoryUnavailable)
}

func TestAWebFilingCreatesTheActiveClaimWithItsSupports(t *testing.T) {
	f := newWebFixture()

	c, created, err := f.web.File(context.Background(), carolina, "form-1", f.collision(t))

	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, "SIN-2026-00001", c.FilingNumber)
	assert.Equal(t, domain.StatusActive, c.Status)
	assert.Equal(t, domain.ChannelWeb, c.Channel)
	assert.Equal(t, carolina, c.Subject)
	assert.Equal(t, filedAt, c.FiledAt)
	require.NotNil(t, c.Policy)
	assert.Equal(t, "KJT-482", c.Policy.InsuredObject, "the copy of the policy is kept")
	assert.Len(t, c.Supports, 6)

	pending, err := f.store.CountPending(context.Background(), carolina)
	require.NoError(t, err)
	assert.Zero(t, pending, "the supports are attached now")
}

func TestTheSameKeyAndFilingReturnTheClaimEvenWithTheCoreDown(t *testing.T) {
	f := newWebFixture()
	r := f.collision(t)
	first, _, err := f.web.File(context.Background(), carolina, "form-1", r)
	require.NoError(t, err)

	f.policies.err = ports.ErrPolicyDirectoryUnavailable
	again, created, err := f.web.File(context.Background(), carolina, "form-1", r)

	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, first.ID, again.ID)
	assert.Equal(t, first.FilingNumber, again.FilingNumber)
}

func TestTheSameKeyWithAnotherWebFilingIsRefused(t *testing.T) {
	f := newWebFixture()
	r := f.collision(t)
	_, _, err := f.web.File(context.Background(), carolina, "form-1", r)
	require.NoError(t, err)

	r.Description = "Otra descripción completamente distinta."
	_, _, err = f.web.File(context.Background(), carolina, "form-1", r)

	assert.ErrorIs(t, err, ports.ErrFilingKeyReused)
}

func TestTheKeyOfAMobileFilingCannotBeReusedOnTheWeb(t *testing.T) {
	f := newWebFixture()
	_, _, err := f.mobileOf(carolina, filedAt).File(context.Background(), carolina, "key-1", request())
	require.NoError(t, err)

	_, _, err = f.web.File(context.Background(), carolina, "key-1", f.collision(t))

	assert.ErrorIs(t, err, ports.ErrFilingKeyReused)
}

func TestAnInvalidFormNeverReachesThePolicyCore(t *testing.T) {
	f := newWebFixture()
	r := f.collision(t)
	r.Supports = r.Supports[:2]

	_, _, err := f.web.File(context.Background(), carolina, "form-1", r)

	var invalid *domain.ValidationError
	require.ErrorAs(t, err, &invalid)
	assert.Equal(t, []domain.FieldError{{Field: "supports.traffic_report", Code: domain.CodeMissing}}, invalid.Fields)
	assert.Zero(t, f.policies.calls)
}

func TestThePolicyMustTakeTheClaim(t *testing.T) {
	tests := []struct {
		name       string
		edit       func(r *domain.WebFilingRequest)
		reason     string
		withPolicy bool
	}{
		{"policy of another insured", func(r *domain.WebFilingRequest) { r.PolicyID = "AU-2025-999999" }, filing.ReasonPolicyNotFound, false},
		{"unknown policy", func(r *domain.WebFilingRequest) { r.PolicyID = "AU-2025-000000" }, filing.ReasonPolicyNotFound, false},
		{"expired policy", func(r *domain.WebFilingRequest) { r.PolicyID = "AU-2024-000001" }, filing.ReasonPolicyNotActive, true},
		{"type it does not cover", func(r *domain.WebFilingRequest) {
			r.IncidentType = "third_party_damage"
		}, filing.ReasonIncidentNotCovered, true},
		{"type of another line", func(r *domain.WebFilingRequest) {
			r.PolicyID = "HG-2025-880231"
		}, filing.ReasonIncidentNotCovered, true},
		{"incident before the policy started", func(r *domain.WebFilingRequest) {
			r.OccurredAt = validFrom.Add(-time.Second).Format(time.RFC3339)
		}, filing.ReasonOccurredOutsideValidity, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newWebFixture()
			r := f.collision(t)
			tt.edit(&r)

			_, _, err := f.web.File(context.Background(), carolina, "form-1", r)

			var notEligible *filing.PolicyNotEligibleError
			require.ErrorAs(t, err, &notEligible)
			assert.Equal(t, tt.reason, notEligible.Reason)
			assert.Equal(t, tt.withPolicy, notEligible.Policy != nil)
			if notEligible.Policy != nil {
				assert.Equal(t, validTo, notEligible.Policy.ValidTo)
			}
			_, err = f.store.FindByKey(context.Background(), carolina, "form-1")
			assert.ErrorIs(t, err, ports.ErrClaimNotFound, "nothing is stored")
		})
	}
}

func TestTheCoreDownFilesNothing(t *testing.T) {
	f := newWebFixture()
	f.policies.err = ports.ErrPolicyDirectoryUnavailable

	_, _, err := f.web.File(context.Background(), carolina, "form-1", f.collision(t))

	assert.ErrorIs(t, err, ports.ErrPolicyDirectoryUnavailable)
	_, err = f.store.FindByKey(context.Background(), carolina, "form-1")
	assert.ErrorIs(t, err, ports.ErrClaimNotFound)
}

func TestASupportCannotBeAttachedToTwoClaims(t *testing.T) {
	f := newWebFixture()
	r := f.collision(t)
	_, _, err := f.web.File(context.Background(), carolina, "form-1", r)
	require.NoError(t, err)

	r.Description = "Segunda radicación con los mismos soportes."
	_, _, err = f.web.File(context.Background(), carolina, "form-2", r)

	var invalid *domain.ValidationError
	require.ErrorAs(t, err, &invalid)
	assert.Equal(t, []domain.FieldError{{Field: "supports", Code: domain.CodeNotFound}}, invalid.Fields)
}

func TestTheSupportsOfAnotherInsuredAreNotFound(t *testing.T) {
	f := newWebFixture()
	r := f.collision(t)
	r.Supports[1].SupportIDs = f.upload(t, "sub-someone", pdf, 1)

	_, _, err := f.web.File(context.Background(), carolina, "form-1", r)

	var invalid *domain.ValidationError
	require.ErrorAs(t, err, &invalid)
	assert.Equal(t, []domain.FieldError{{Field: "supports.driver_document", Code: domain.CodeNotFound}}, invalid.Fields)
}

// failingStore fails the operation it is told to; the rest goes to the store.
type failingStore struct {
	*memory.CaseStore
	failFind, failFindByKey, failCreate, failCount, failSave, failConfirm bool
}

var errBroken = errors.New("database is down")

func (s failingStore) Find(ctx context.Context, ids []string) (map[string]domain.SupportFile, error) {
	if s.failFind {
		return nil, errBroken
	}
	return s.CaseStore.Find(ctx, ids)
}

func (s failingStore) FindByKey(ctx context.Context, subject, key string) (domain.ClaimRecord, error) {
	if s.failFindByKey {
		return domain.ClaimRecord{}, errBroken
	}
	return s.CaseStore.FindByKey(ctx, subject, key)
}

func (s failingStore) CreateWeb(ctx context.Context, c domain.ClaimRecord) (domain.ClaimRecord, bool, error) {
	if s.failCreate {
		return domain.ClaimRecord{}, false, errBroken
	}
	return s.CaseStore.CreateWeb(ctx, c)
}

func (s failingStore) CountPending(ctx context.Context, owner string) (int, error) {
	if s.failCount {
		return 0, errBroken
	}
	return s.CaseStore.CountPending(ctx, owner)
}

func (s failingStore) Save(ctx context.Context, f domain.SupportFile) error {
	if s.failSave {
		return errBroken
	}
	return s.CaseStore.Save(ctx, f)
}

func (s failingStore) Confirm(ctx context.Context, id string, at time.Time) (domain.SupportFile, error) {
	if s.failConfirm {
		return domain.SupportFile{}, errBroken
	}
	return s.CaseStore.Confirm(ctx, id, at)
}

func TestAStoreFailureIsReported(t *testing.T) {
	for name, broken := range map[string]func(*failingStore){
		"reading the supports":      func(s *failingStore) { s.failFind = true },
		"looking up the filing key": func(s *failingStore) { s.failFindByKey = true },
		"creating the claim":        func(s *failingStore) { s.failCreate = true },
	} {
		t.Run(name, func(t *testing.T) {
			f := newWebFixture()
			r := f.collision(t)
			store := failingStore{CaseStore: f.store}
			broken(&store)
			web := filing.NewWebService(store, store, f.policies, fixedClock{t: filedAt})

			_, _, err := web.File(context.Background(), carolina, "form-1", r)

			assert.ErrorIs(t, err, errBroken)
		})
	}
}

// --- Supports of the web filing (criterion 5) --------------------------------

func TestADeclaredSupportIsSignedUnderTheDirectoryOfItsOwner(t *testing.T) {
	f := newWebFixture()

	up, err := f.uploads.Declare(context.Background(), carolina, declaration("../renamed.pdf", png))

	require.NoError(t, err)
	assert.Regexp(t, `^sup-[0-9a-f]{32}$`, up.File.ID)
	assert.Equal(t, "renamed.pdf", up.File.FileName)
	assert.Equal(t, domain.FormatPNG, up.File.Format)
	assert.Equal(t, int64(len(png)), up.File.ByteSize)
	assert.Equal(t, filedAt, up.File.UploadedAt)
	assert.False(t, up.File.Confirmed(), "nothing is attachable before the upload is confirmed")
	assert.Equal(t, "supports/"+carolina+"/"+up.File.ID, up.File.ObjectKey())
	assert.Equal(t, []string{up.File.ObjectKey()}, f.objects.signed)
	assert.NotEmpty(t, up.URL)
	assert.Equal(t, filedAt.Add(filing.SupportUploadTTL), up.ExpiresAt)
}

func TestTheDeclarationOfASupportIsBounded(t *testing.T) {
	good := declaration("a.pdf", pdf)
	tests := []struct {
		name   string
		change func(*domain.SupportUploadRequest)
		want   error
	}{
		{"empty", func(r *domain.SupportUploadRequest) { r.ByteSize = 0 }, domain.ErrEmptyFile},
		{"exactly 10 MiB", func(r *domain.SupportUploadRequest) { r.ByteSize = domain.MaxSupportBytes }, nil},
		{"one byte over 10 MiB", func(r *domain.SupportUploadRequest) { r.ByteSize = domain.MaxSupportBytes + 1 }, domain.ErrFileTooLarge},
		{"an executable", func(r *domain.SupportUploadRequest) { r.ContentType = "application/x-msdownload" }, domain.ErrUnsupportedFileType},
		{"a GIF", func(r *domain.SupportUploadRequest) { r.ContentType = "image/gif" }, domain.ErrUnsupportedFileType},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := good
			tt.change(&r)

			_, err := newWebFixture().uploads.Declare(context.Background(), carolina, r)

			if tt.want == nil {
				assert.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, tt.want)
		})
	}
}

func TestADigestThatIsNotSHA256IsRefused(t *testing.T) {
	for _, digest := range []string{"", "abc", strings.Repeat("G", 64), strings.ToUpper(strings.Repeat("a", 64))} {
		r := declaration("a.pdf", pdf)
		r.SHA256 = digest

		_, err := newWebFixture().uploads.Declare(context.Background(), carolina, r)

		var invalid *domain.ValidationError
		require.ErrorAs(t, err, &invalid, digest)
		assert.Equal(t, "sha256", invalid.Fields[0].Field)
	}
}

func TestAConfirmedSupportIsTheOneThatWasUploaded(t *testing.T) {
	f := newWebFixture()

	s := f.uploadOne(t, carolina, "a.pdf", pdf)

	assert.True(t, s.Confirmed())
	assert.Equal(t, filedAt, s.ConfirmedAt)
	again, err := f.uploads.Confirm(context.Background(), carolina, s.ID)
	require.NoError(t, err, "confirming twice is harmless")
	assert.Equal(t, s.ConfirmedAt, again.ConfirmedAt)
}

func TestTheContentDecidesNotTheDeclaredType(t *testing.T) {
	for name, content := range map[string][]byte{
		"an executable":           append([]byte("MZ\x90\x00"), pdf...),
		"html":                    []byte("<html>" + strings.Repeat("x", 100)),
		"a PNG declared as a PDF": png,
	} {
		t.Run(name, func(t *testing.T) {
			f := newWebFixture()
			r := declaration("x.pdf", content)
			r.ContentType = "application/pdf"
			up, err := f.uploads.Declare(context.Background(), carolina, r)
			require.NoError(t, err)
			f.objects.put(up.File.ObjectKey(), content)

			_, err = f.uploads.Confirm(context.Background(), carolina, up.File.ID)

			assert.ErrorIs(t, err, domain.ErrUnsupportedFileType)
		})
	}
}

func TestASupportThatWasNotUploadedCannotBeConfirmed(t *testing.T) {
	f := newWebFixture()
	up, err := f.uploads.Declare(context.Background(), carolina, declaration("a.pdf", pdf))
	require.NoError(t, err)

	_, err = f.uploads.Confirm(context.Background(), carolina, up.File.ID)

	assert.ErrorIs(t, err, domain.ErrSupportNotUploaded)
	// It can be confirmed once the file arrives: nothing is lost.
	f.objects.put(up.File.ObjectKey(), pdf)
	_, err = f.uploads.Confirm(context.Background(), carolina, up.File.ID)
	assert.NoError(t, err)
}

func TestAStoredFileOfAnotherSizeOrDigestIsNotTheDeclaredOne(t *testing.T) {
	f := newWebFixture()
	up, err := f.uploads.Declare(context.Background(), carolina, declaration("a.pdf", pdf))
	require.NoError(t, err)

	f.objects.put(up.File.ObjectKey(), append(pdf, 1))
	_, err = f.uploads.Confirm(context.Background(), carolina, up.File.ID)
	assert.ErrorIs(t, err, domain.ErrSupportMismatch, "another size")

	same := append([]byte(nil), pdf...)
	same[100] ^= 0xFF
	f.objects.put(up.File.ObjectKey(), same)
	f.objects.digest[up.File.ObjectKey()] = strings.Repeat("0", 64)
	_, err = f.uploads.Confirm(context.Background(), carolina, up.File.ID)
	assert.ErrorIs(t, err, domain.ErrSupportMismatch, "the same size and another digest")
}

func TestOnlyTheOwnerConfirmsASupport(t *testing.T) {
	f := newWebFixture()
	up, err := f.uploads.Declare(context.Background(), carolina, declaration("a.pdf", pdf))
	require.NoError(t, err)
	f.objects.put(up.File.ObjectKey(), pdf)

	_, err = f.uploads.Confirm(context.Background(), "sub-someone", up.File.ID)
	assert.ErrorIs(t, err, domain.ErrSupportNotFound)
	_, err = f.uploads.Confirm(context.Background(), carolina, "sup-"+strings.Repeat("0", 32))
	assert.ErrorIs(t, err, domain.ErrSupportNotFound)
}

func TestAnUnconfirmedSupportCannotBeAttached(t *testing.T) {
	f := newWebFixture()
	r := f.collision(t)
	up, err := f.uploads.Declare(context.Background(), carolina, declaration("croquis.pdf", pdf))
	require.NoError(t, err)
	r.Supports[2].SupportIDs = []string{up.File.ID}

	_, _, err = f.web.File(context.Background(), carolina, "form-1", r)

	var invalid *domain.ValidationError
	require.ErrorAs(t, err, &invalid)
	assert.Equal(t, []domain.FieldError{{Field: "supports.traffic_report", Code: domain.CodeNotFound}}, invalid.Fields)
}

func TestAnInsuredCannotPileUpPendingSupports(t *testing.T) {
	f := newWebFixture()
	f.upload(t, carolina, pdf, domain.MaxPendingSupports)

	_, err := f.uploads.Declare(context.Background(), carolina, declaration("one-more.pdf", pdf))
	assert.ErrorIs(t, err, domain.ErrTooManyPendingSupports)

	_, err = f.uploads.Declare(context.Background(), "sub-someone", declaration("theirs.pdf", pdf))
	assert.NoError(t, err, "the limit is per insured")
}

func TestTheSupportFailuresAreReported(t *testing.T) {
	for name, broken := range map[string]func(*failingStore, *objectStore){
		"counting": func(s *failingStore, _ *objectStore) { s.failCount = true },
		"saving":   func(s *failingStore, _ *objectStore) { s.failSave = true },
		"signing":  func(_ *failingStore, o *objectStore) { o.signErr = errBroken },
	} {
		t.Run(name, func(t *testing.T) {
			store, objects := failingStore{CaseStore: memory.NewCaseStore()}, newObjectStore()
			broken(&store, objects)

			_, err := filing.NewSupportService(store, objects, fixedClock{t: filedAt}).
				Declare(context.Background(), carolina, declaration("a.pdf", pdf))

			assert.ErrorIs(t, err, errBroken)
		})
	}
}

func TestTheFailuresOfTheStorageAreReportedWhenConfirming(t *testing.T) {
	for name, broken := range map[string]func(*failingStore, *objectStore){
		"reading the support": func(s *failingStore, _ *objectStore) { s.failFind = true },
		"reading the object":  func(_ *failingStore, o *objectStore) { o.statErr = errBroken },
		"reading its start":   func(_ *failingStore, o *objectStore) { o.headErr = errBroken },
		"confirming":          func(s *failingStore, _ *objectStore) { s.failConfirm = true },
	} {
		t.Run(name, func(t *testing.T) {
			store, objects := failingStore{CaseStore: memory.NewCaseStore()}, newObjectStore()
			svc := filing.NewSupportService(store, objects, fixedClock{t: filedAt})
			up, err := svc.Declare(context.Background(), carolina, declaration("a.pdf", pdf))
			require.NoError(t, err)
			objects.put(up.File.ObjectKey(), pdf)
			broken(&store, objects)
			svc = filing.NewSupportService(store, objects, fixedClock{t: filedAt})

			_, err = svc.Confirm(context.Background(), carolina, up.File.ID)

			assert.ErrorIs(t, err, errBroken)
		})
	}
}

func TestTheEvidenceOfAMobileClaimComesWithSignedReads(t *testing.T) {
	f := newWebFixture()
	req := requestDe(carolina)
	ev := nuevoAlmacenDeEvidencia()
	ev.guardarTodo(req)
	mobile, _, err := filing.NewService(f.store, ev, fixedClock{t: filedAt}).File(context.Background(), carolina, "draft-1", req)
	require.NoError(t, err)
	record, err := f.store.Get(context.Background(), mobile.ID)
	require.NoError(t, err)
	require.NotEmpty(t, record.Evidence)

	links := filing.NewQueryService(f.store, &receipts{}).WithObjects(f.objects).EvidenceLinks(context.Background(), record)
	require.Len(t, links, len(record.Evidence))
	assert.Contains(t, links[0].URL, record.Evidence[0].ObjectKey)
	assert.Equal(t, filedAt.Add(filing.EvidenceLinkTTL), links[0].ExpiresAt)

	f.objects.signErr = errBroken
	unsigned := filing.NewQueryService(f.store, &receipts{}).WithObjects(f.objects).EvidenceLinks(context.Background(), record)
	require.Len(t, unsigned, len(record.Evidence), "the evidence is listed even when it cannot be signed")
	assert.Empty(t, unsigned[0].URL)

	withoutStore := filing.NewQueryService(f.store, &receipts{}).EvidenceLinks(context.Background(), record)
	assert.Len(t, withoutStore, len(record.Evidence))
	assert.Empty(t, withoutStore[0].URL)
}

// --- List, detail and receipt (criteria 4 and 6) ----------------------------

type receipts struct{ got domain.Receipt }

func (r *receipts) Render(rc domain.Receipt) ([]byte, error) {
	r.got = rc
	return []byte("%PDF-receipt " + rc.FilingNumber), nil
}

func as(subject string, roles ...security.Role) context.Context {
	return security.WithClaims(context.Background(), security.Claims{Subject: subject, Roles: roles})
}

func TestTheOwnerReadsTheClaimAndItsReceipt(t *testing.T) {
	f := newWebFixture()
	c, _, err := f.web.File(context.Background(), carolina, "form-1", f.collision(t))
	require.NoError(t, err)
	r := &receipts{}
	q := filing.NewQueryService(f.store, r)

	got, err := q.Get(as(carolina, security.RoleInsured), c.ID)
	require.NoError(t, err)
	assert.Equal(t, c.FilingNumber, got.FilingNumber)
	assert.Len(t, got.Supports, 6)

	_, content, err := q.Receipt(as(carolina, security.RoleInsured), c.ID)
	require.NoError(t, err)
	assert.Equal(t, "%PDF-receipt "+c.FilingNumber, string(content))
	assert.Equal(t, "Colisión con daños al vehículo", r.got.IncidentLabel)
	assert.Equal(t, 3, r.got.Supports.RequiredAttached)
	assert.Equal(t, 6, r.got.Supports.Files)
}

func TestAnotherInsuredCannotReadTheClaim(t *testing.T) {
	f := newWebFixture()
	c, _, err := f.web.File(context.Background(), carolina, "form-1", f.collision(t))
	require.NoError(t, err)
	q := filing.NewQueryService(f.store, &receipts{})

	for _, ctx := range []context.Context{
		as("sub-someone", security.RoleInsured),
		as("sub-advisor", security.RoleAdvisor),
		as("sub-ops", security.RoleOperations),
	} {
		_, err = q.Get(ctx, c.ID)
		assert.ErrorIs(t, err, security.ErrForbidden)
		_, _, err = q.Receipt(ctx, c.ID)
		assert.ErrorIs(t, err, security.ErrForbidden)
	}

	_, err = q.Get(as(carolina, security.RoleInsured), "8f14e45f-ceea-467a-9f3a-5a2d0b1c7e90")
	assert.ErrorIs(t, err, ports.ErrClaimNotFound)
}

func TestTheListHasTheClaimsOfBothChannelsNewestFirst(t *testing.T) {
	f := newWebFixture()
	mobile := f.mobileOf(carolina, filedAt.Add(-time.Hour))
	m, _, err := mobile.File(context.Background(), carolina, "draft-1", request())
	require.NoError(t, err)
	w, _, err := f.web.File(context.Background(), carolina, "form-1", f.collision(t))
	require.NoError(t, err)
	_, _, err = f.mobileOf("sub-someone", filedAt).
		File(context.Background(), "sub-someone", "draft-9", requestDe("sub-someone"))
	require.NoError(t, err)

	page, err := filing.NewQueryService(f.store, &receipts{}).List(context.Background(), carolina, filing.ClaimQuery{})

	require.NoError(t, err)
	require.Len(t, page.Claims, 2, "only the insured's own")
	assert.Equal(t, w.ID, page.Claims[0].ID)
	assert.Equal(t, m.ID, page.Claims[1].ID)
	assert.Equal(t, domain.ChannelMobile, page.Claims[1].Channel)
	assert.Nil(t, page.Claims[1].Policy)
	assert.Empty(t, page.NextPageToken)
}

func TestTheListIsPagedAndKeepsItsFilters(t *testing.T) {
	f := newWebFixture()
	for i := 0; i < 5; i++ {
		svc := f.mobileOf(carolina, filedAt.Add(time.Duration(i)*time.Minute))
		r := request()
		if i%2 == 1 {
			r.PolicyID = "HG-2025-880231"
		}
		_, _, err := svc.File(context.Background(), carolina, fmt.Sprintf("draft-%d", i), r)
		require.NoError(t, err)
	}
	q := filing.NewQueryService(f.store, &receipts{})

	first, err := q.List(context.Background(), carolina, filing.ClaimQuery{PolicyID: "AU-2025-118204", PageSize: 2})
	require.NoError(t, err)
	require.Len(t, first.Claims, 2)
	require.NotEmpty(t, first.NextPageToken)

	second, err := q.List(context.Background(), carolina,
		filing.ClaimQuery{PolicyID: "AU-2025-118204", PageSize: 2, PageToken: first.NextPageToken})
	require.NoError(t, err)
	require.Len(t, second.Claims, 1)
	assert.Empty(t, second.NextPageToken)
	for _, c := range append(first.Claims, second.Claims...) {
		assert.Equal(t, "AU-2025-118204", c.PolicyID)
	}
	assert.True(t, first.Claims[1].FiledAt.After(second.Claims[0].FiledAt))
}

func TestTheListFiltersByTheDayInColombia(t *testing.T) {
	f := newWebFixture()
	// 2026-10-23 04:59 UTC is still the 22nd in Colombia; 05:00 UTC is the 23rd.
	for i, at := range []time.Time{
		time.Date(2026, 10, 23, 4, 59, 0, 0, time.UTC),
		time.Date(2026, 10, 23, 5, 0, 0, 0, time.UTC),
	} {
		_, _, err := f.mobileOf(carolina, at).
			File(context.Background(), carolina, fmt.Sprintf("draft-%d", i), request())
		require.NoError(t, err)
	}

	page, err := filing.NewQueryService(f.store, &receipts{}).
		List(context.Background(), carolina, filing.ClaimQuery{From: "2026-10-23", Status: "active"})

	require.NoError(t, err)
	require.Len(t, page.Claims, 1)
	assert.Equal(t, time.Date(2026, 10, 23, 5, 0, 0, 0, time.UTC), page.Claims[0].FiledAt)
}

func TestAnInvalidQueryIsRefused(t *testing.T) {
	q := filing.NewQueryService(memory.NewCaseStore(), &receipts{})
	for name, query := range map[string]filing.ClaimQuery{
		"unknown status":     {Status: "closed"},
		"from not a date":    {From: "23/10/2026"},
		"page size of 0 - 1": {PageSize: -1},
		"page size of 51":    {PageSize: 51},
		"token not base64":   {PageToken: "%%%"},
		"token not json":     {PageToken: "bm90IGpzb24"},
		"token incomplete":   {PageToken: "e30"},
	} {
		_, err := q.List(context.Background(), carolina, query)
		assert.ErrorIs(t, err, filing.ErrInvalidQuery, name)
	}

	_, err := q.List(context.Background(), carolina, filing.ClaimQuery{PageSize: 50})
	assert.NoError(t, err, "50 is the maximum")
}

func TestThePolicyNotEligibleErrorNamesItsReason(t *testing.T) {
	err := &filing.PolicyNotEligibleError{Reason: filing.ReasonPolicyNotActive}
	assert.True(t, strings.HasSuffix(err.Error(), "policy_not_active"))
}
