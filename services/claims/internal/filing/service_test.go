package filing_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/filing"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// The use case of the filing (HU-M2.2-01).

var filedAt = time.Date(2026, 10, 23, 15, 0, 0, 0, time.UTC)

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

func service() (*memory.CaseStore, *filing.Service) {
	claims, _, svc := serviceWithEvidence()
	return claims, svc
}

func serviceWithEvidence() (*memory.CaseStore, *evidenceFalsa, *filing.Service) {
	claims := memory.NewCaseStore()
	store := nuevoAlmacenDeEvidencia()
	for _, e := range request().Evidence {
		store.guardar(e.ObjectKey, e.ByteSize, e.SHA256)
	}
	return claims, store, filing.NewService(claims, store, fixedClock{t: filedAt})
}

const sujeto = "sub-carolina"

func request() domain.FilingRequest { return requestDe(sujeto) }

func requestDe(subject string) domain.FilingRequest {
	return domain.FilingRequest{
		PolicyID:            "AU-2025-118204",
		IncidentType:        "collision",
		OccurredAt:          "2026-10-20T23:30:00Z",
		Place:               "Calle 80 con Carrera 15, Bogotá",
		Description:         "Choque por alcance en el semáforo.",
		Evidence:            evidencia(subject),
		DeclarationAccepted: true,
	}
}

// evidencia is the minimum a filing needs: four photographs of the place.
func evidencia(subject string) []domain.EvidenceRequest {
	out := make([]domain.EvidenceRequest, 0, domain.MinPhotos)
	for i := 0; i < domain.MinPhotos; i++ {
		digest := strings.Repeat(string(byte('a'+i)), 64)
		out = append(out, domain.EvidenceRequest{
			ObjectKey:       "evidence/" + subject + "/" + digest + ".jpg",
			Kind:            "photo",
			CapturedAt:      "2026-10-20T23:32:14Z",
			Latitude:        4.7712,
			Longitude:       -74.0452,
			AccuracyM:       8,
			LocationTrusted: true,
			SHA256:          digest,
			ByteSize:        312044,
		})
	}
	return out
}

// evidenceFalsa is the evidence store: it knows the size and the digest of
// what was uploaded.
type evidenceFalsa struct {
	objetos map[string]ports.StoredEvidence
	err     error
}

func nuevoAlmacenDeEvidencia() *evidenceFalsa {
	return &evidenceFalsa{objetos: map[string]ports.StoredEvidence{}}
}

func (a *evidenceFalsa) guardar(key string, size int64, digest string) {
	a.objetos[key] = ports.StoredEvidence{ByteSize: size, SHA256: digest}
}

// guardarTodo registers everything a filing declares, as if the phone had
// already uploaded it.
func (a *evidenceFalsa) guardarTodo(r domain.FilingRequest) {
	for _, e := range r.Evidence {
		a.guardar(e.ObjectKey, e.ByteSize, e.SHA256)
	}
}

func (a *evidenceFalsa) SignUpload(context.Context, string, domain.Upload,
	time.Duration) (ports.SignedUpload, error) {
	return ports.SignedUpload{}, nil
}

func (a *evidenceFalsa) Stat(_ context.Context, key string) (ports.StoredEvidence, error) {
	if a.err != nil {
		return ports.StoredEvidence{}, a.err
	}
	stored, ok := a.objetos[key]
	if !ok {
		return ports.StoredEvidence{}, ports.ErrEvidenceNotFound
	}
	return stored, nil
}

func TestFilingCreatesTheCaseWithItsNumber(t *testing.T) {
	_, svc := service()

	c, created, err := svc.File(context.Background(), sujeto, "draft-1", request())

	require.NoError(t, err)
	assert.True(t, created)
	assert.NotEmpty(t, c.ID)
	assert.Equal(t, "SIN-2026-00001", c.FilingNumber)
	assert.Equal(t, domain.StatusActive, c.Status)
	assert.Equal(t, filedAt, c.FiledAt)
	assert.Equal(t, "sub-carolina", c.Subject, "the case belongs to whoever filed it")
}

// The filing identifies its subject by the token, never by what the body says.
func TestTheCaseIsFiledForTheSubjectOfTheSession(t *testing.T) {
	_, store, svc := serviceWithEvidence()
	ana, luis := requestDe("sub-ana"), requestDe("sub-luis")
	store.guardarTodo(ana)
	store.guardarTodo(luis)

	first, _, err := svc.File(context.Background(), "sub-ana", "draft-1", ana)
	require.NoError(t, err)
	second, _, err := svc.File(context.Background(), "sub-luis", "draft-1", luis)
	require.NoError(t, err)

	assert.NotEqual(t, first.ID, second.ID, "the same draft key of two people is two cases")
	assert.NotEqual(t, first.FilingNumber, second.FilingNumber)
}

// A retry after a lost response must not file the claim twice.
func TestTheSameKeyReturnsTheCaseThatAlreadyExists(t *testing.T) {
	_, svc := service()
	first, created, err := svc.File(context.Background(), sujeto, "draft-1", request())
	require.NoError(t, err)
	require.True(t, created)

	second, created, err := svc.File(context.Background(), sujeto, "draft-1", request())

	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, first.ID, second.ID)
	assert.Equal(t, first.FilingNumber, second.FilingNumber)
	assert.Equal(t, first.FiledAt, second.FiledAt, "it keeps the instant of the original filing")
}

// The same key with another filing is a mistake of the client, not a retry.
func TestTheSameKeyWithAnotherFilingIsRefused(t *testing.T) {
	_, svc := service()
	_, _, err := svc.File(context.Background(), sujeto, "draft-1", request())
	require.NoError(t, err)

	other := request()
	other.IncidentType = "theft"
	_, _, err = svc.File(context.Background(), sujeto, "draft-1", other)

	assert.ErrorIs(t, err, ports.ErrFilingKeyReused)
}

func TestAnInvalidFilingStoresNothing(t *testing.T) {
	_, svc := service()
	r := request()
	r.DeclarationAccepted = false

	_, created, err := svc.File(context.Background(), sujeto, "draft-1", r)

	var invalid *domain.ValidationError
	assert.ErrorAs(t, err, &invalid)
	assert.False(t, created)

	// The same key is still free: the filing never happened.
	c, created, err := svc.File(context.Background(), sujeto, "draft-1", request())
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, "SIN-2026-00001", c.FilingNumber, "no number was burned by the invalid filing")
}

func TestEachFilingTakesItsOwnNumber(t *testing.T) {
	_, svc := service()

	first, _, err := svc.File(context.Background(), sujeto, "draft-1", request())
	require.NoError(t, err)
	second, _, err := svc.File(context.Background(), sujeto, "draft-2", request())
	require.NoError(t, err)

	assert.Equal(t, "SIN-2026-00001", first.FilingNumber)
	assert.Equal(t, "SIN-2026-00002", second.FilingNumber)
}

// The evidence of the case is confronted with the store: a key that nobody
// can open would be a verification impossible to review afterwards.

func TestTheEvidenceOfTheCaseIsKept(t *testing.T) {
	_, _, svc := serviceWithEvidence()

	c, _, err := svc.File(context.Background(), sujeto, "draft-1", request())

	require.NoError(t, err)
	require.Len(t, c.Evidence, domain.MinPhotos)
	assert.Equal(t, domain.KindPhoto, c.Evidence[0].Kind)
	assert.True(t, c.Evidence[0].LocationTrusted)
}

func TestAnEvidenceThatIsNotInTheStore(t *testing.T) {
	_, store, svc := serviceWithEvidence()
	r := request()
	delete(store.objetos, r.Evidence[2].ObjectKey)

	_, _, err := svc.File(context.Background(), sujeto, "draft-1", r)

	var invalid *domain.ValidationError
	require.ErrorAs(t, err, &invalid)
	require.Len(t, invalid.Fields, 1)
	assert.Equal(t, "evidence.2.object_key", invalid.Fields[0].Field)
	assert.Equal(t, domain.CodeNotStored, invalid.Fields[0].Code)
}

// One insured cannot file with the evidence of another, even knowing its key.
func TestTheEvidenceOfSomebodyElseIsNotAccepted(t *testing.T) {
	_, _, svc := serviceWithEvidence()

	_, _, err := svc.File(context.Background(), "sub-ana", "draft-1", request())

	var invalid *domain.ValidationError
	require.ErrorAs(t, err, &invalid)
	assert.Len(t, invalid.Fields, domain.MinPhotos, "none of them is hers")
	assert.Equal(t, domain.CodeNotStored, invalid.Fields[0].Code)
}

// What was uploaded has to be what was declared: the size and the digest of
// the object settle it, not the body of the filing.
func TestAnEvidenceThatDoesNotMatchWhatWasUploaded(t *testing.T) {
	for name, change := range map[string]func(*evidenceFalsa, string){
		"another size":   func(a *evidenceFalsa, k string) { a.guardar(k, 999, a.objetos[k].SHA256) },
		"another digest": func(a *evidenceFalsa, k string) { a.guardar(k, a.objetos[k].ByteSize, strings.Repeat("f", 64)) },
	} {
		t.Run(name, func(t *testing.T) {
			_, store, svc := serviceWithEvidence()
			r := request()
			change(store, r.Evidence[0].ObjectKey)

			_, _, err := svc.File(context.Background(), sujeto, "draft-1", r)

			var invalid *domain.ValidationError
			require.ErrorAs(t, err, &invalid)
			assert.Equal(t, "evidence.0.object_key", invalid.Fields[0].Field)
		})
	}
}

// A store that does not answer is not an invalid filing: it is a failure that
// the insured retries with the same key.
func TestAStoreThatDoesNotAnswerIsNotAValidationError(t *testing.T) {
	_, store, svc := serviceWithEvidence()
	store.err = errors.New("the bucket is not reachable")

	_, _, err := svc.File(context.Background(), sujeto, "draft-1", request())

	require.Error(t, err)
	var invalid *domain.ValidationError
	assert.False(t, errors.As(err, &invalid))
}

// Un emulador de S3 puede guardar el objeto sin calcular su SHA-256. El
// tamaño basta para saber que es el que se declaró, y el sello de integridad
// viaja igual con el caso.
func TestAStorageWithoutDigestSettlesWithTheSize(t *testing.T) {
	_, store, svc := serviceWithEvidence()
	r := request()
	store.objetos[r.Evidence[0].ObjectKey] = ports.StoredEvidence{
		ByteSize: r.Evidence[0].ByteSize,
		SHA256:   "",
	}

	c, _, err := svc.File(context.Background(), sujeto, "draft-1", r)

	require.NoError(t, err)
	assert.Len(t, c.Evidence, domain.MinPhotos)
}

func TestAStorageWithoutDigestStillChecksTheSize(t *testing.T) {
	_, store, svc := serviceWithEvidence()
	r := request()
	store.objetos[r.Evidence[0].ObjectKey] = ports.StoredEvidence{ByteSize: 7, SHA256: ""}

	_, _, err := svc.File(context.Background(), sujeto, "draft-1", r)

	var invalid *domain.ValidationError
	require.ErrorAs(t, err, &invalid)
	assert.Equal(t, "evidence.0.object_key", invalid.Fields[0].Field)
}
