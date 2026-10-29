package kyc_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/kyc"
	kycadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/kyc"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

type identidadFalsa struct {
	lectura      kyc.DocumentRead
	verificacion kyc.Verification
	err          error
	pedida       kyc.VerificationRequest
	caras        [2][]byte
}

func (f *identidadFalsa) ReadDocument(_ context.Context, front, back []byte) (kyc.DocumentRead, error) {
	f.caras = [2][]byte{front, back}
	return f.lectura, f.err
}

func (f *identidadFalsa) Verify(_ context.Context, req kyc.VerificationRequest) (kyc.Verification, error) {
	f.pedida = req
	return f.verificacion, f.err
}

var revisadoEn = time.Date(2026, 10, 29, 15, 0, 0, 0, time.UTC)

func chequeo() domain.IdentityCheck {
	return domain.IdentityCheck{DocumentID: "1020345678", FullName: "Carolina Pérez Ruiz",
		BirthDate: time.Date(1992, 4, 18, 0, 0, 0, 0, time.UTC), FrontSHA256: "aa", BackSHA256: "bb"}
}

func TestVerifyEnviaLosDatosConfirmadosYTraduceLaAprobacion(t *testing.T) {
	falso := &identidadFalsa{verificacion: kyc.Verification{VerificationID: "kyc-1", Outcome: kyc.OutcomeApproved,
		CheckedAt: revisadoEn}}

	got, err := kycadapter.NewVerifier(falso).Verify(context.Background(), chequeo())

	require.NoError(t, err)
	assert.Equal(t, domain.IdentityVerdict{VerificationID: "kyc-1", Approved: true, CheckedAt: revisadoEn}, got)
	assert.Equal(t, kyc.VerificationRequest{DocumentID: "1020345678", FullName: "Carolina Pérez Ruiz",
		BirthDate: "1992-04-18", DocumentFrontSHA256: "aa", DocumentBackSHA256: "bb"}, falso.pedida)
}

func TestVerifyTraduceCadaMotivoDeRechazo(t *testing.T) {
	for motivo, esperado := range map[string]domain.IdentityRejection{
		kyc.ReasonRestrictedList: domain.RejectionRestrictedList,
		kyc.ReasonUnreadable:     domain.RejectionUnreadable,
		kyc.ReasonUnverifiable:   domain.RejectionUnverifiable,
		"motivo-nuevo":           domain.RejectionUnverifiable,
	} {
		falso := &identidadFalsa{verificacion: kyc.Verification{Outcome: kyc.OutcomeRejected, Reason: motivo,
			CheckedAt: revisadoEn}}

		got, err := kycadapter.NewVerifier(falso).Verify(context.Background(), chequeo())

		require.NoError(t, err, motivo)
		assert.False(t, got.Approved, motivo)
		assert.Equal(t, esperado, got.Rejection, motivo)
	}
}

func TestLasFallasDelProveedorSonVerificacionNoDisponible(t *testing.T) {
	falso := &identidadFalsa{err: errors.New("se cayó")}
	v := kycadapter.NewVerifier(falso)

	_, err := v.Verify(context.Background(), chequeo())
	assert.ErrorIs(t, err, ports.ErrIdentityCheckUnavailable)

	_, err = v.ReadDocument(context.Background(), domain.IdentityDocument{}, domain.IdentityDocument{})
	assert.ErrorIs(t, err, ports.ErrIdentityCheckUnavailable)
}

func TestReadDocumentEnviaLasCarasYTraduceLaLectura(t *testing.T) {
	falso := &identidadFalsa{lectura: kyc.DocumentRead{ReadID: "read-1", DocumentType: "CC", DocumentID: "1020345678",
		GivenNames: "Carolina", Surnames: "Pérez Ruiz", BirthDate: "1992-04-18"}}

	got, err := kycadapter.NewVerifier(falso).ReadDocument(context.Background(),
		domain.IdentityDocument{Content: []byte("frente")}, domain.IdentityDocument{Content: []byte("reverso")})

	require.NoError(t, err)
	assert.Equal(t, domain.DocumentData{DocumentType: "CC", DocumentID: "1020345678", GivenNames: "Carolina",
		Surnames: "Pérez Ruiz", BirthDate: "1992-04-18"}, got)
	assert.Equal(t, [2][]byte{[]byte("frente"), []byte("reverso")}, falso.caras)
}
