package orchestrator_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/orchestrator"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// La verificación de identidad del registro (HU-M1.1-01). La cuenta solo se
// crea si el proveedor aprueba a la persona (criterio 5), y el motivo de un
// rechazo nunca sale del servicio (criterio 3).

// verificadorFalso es el proveedor de identidad: aprueba, salvo que se le fije
// un rechazo o un error.
type verificadorFalso struct {
	mu        sync.Mutex
	rechazo   domain.IdentityRejection
	err       error
	lectura   domain.DocumentData
	llamadas  int
	revisados []domain.IdentityCheck
	leidas    int
}

func (v *verificadorFalso) ReadDocument(_ context.Context, _, _ domain.IdentityDocument) (domain.DocumentData, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.leidas++
	if v.err != nil {
		return domain.DocumentData{}, v.err
	}
	return v.lectura, nil
}

func (v *verificadorFalso) Verify(_ context.Context, check domain.IdentityCheck) (domain.IdentityVerdict, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.llamadas++
	v.revisados = append(v.revisados, check)
	if v.err != nil {
		return domain.IdentityVerdict{}, v.err
	}
	if v.rechazo != "" {
		return domain.IdentityVerdict{VerificationID: "kyc-rechazo", Rejection: v.rechazo, CheckedAt: registroEn}, nil
	}
	return domain.IdentityVerdict{VerificationID: "kyc-ok", Approved: true, CheckedAt: registroEn}, nil
}

func servicioConVerificador(v *verificadorFalso, p *proveedorFalso) (*memory.CustomerStore, *orchestrator.RegistrationService) {
	clientes := memory.NewCustomerStore()
	return clientes, orchestrator.NewRegistrationService(clientes, nuevoAlmacen(), v, p, relojFijo{t: registroEn})
}

func TestUnaPersonaVerificadaQuedaConCuentaYConLaVerificacionRegistrada(t *testing.T) {
	verificador := &verificadorFalso{}
	clientes, svc := servicioConVerificador(verificador, &proveedorFalso{sub: "sub-carolina"})

	c, err := svc.Register(context.Background(), solicitudValida())

	require.NoError(t, err)
	assert.Equal(t, domain.IdentityVerified, c.IdentityStatus)
	assert.Equal(t, "kyc-ok", c.IdentityVerificationID)
	assert.Equal(t, registroEn, c.IdentityCheckedAt)
	guardado, err := clientes.GetByDocument(context.Background(), "1020345678")
	require.NoError(t, err)
	assert.Equal(t, domain.IdentityVerified, guardado.IdentityStatus)
	assert.Equal(t, "sub-carolina", guardado.CognitoSub)
}

func TestElProveedorRevisaLosDatosConfirmadosYLasCarasGuardadas(t *testing.T) {
	verificador := &verificadorFalso{}
	_, svc := servicioConVerificador(verificador, &proveedorFalso{})

	_, err := svc.Register(context.Background(), solicitudValida())

	require.NoError(t, err)
	require.Len(t, verificador.revisados, 1)
	revisado := verificador.revisados[0]
	assert.Equal(t, "1020345678", revisado.DocumentID)
	assert.Equal(t, "Carolina Pérez Ruiz", revisado.FullName)
	assert.Equal(t, "1992-04-18", revisado.BirthDate.Format("2006-01-02"))
	assert.Len(t, revisado.FrontSHA256, 64)
	assert.Len(t, revisado.BackSHA256, 64)
	assert.NotEqual(t, revisado.FrontSHA256, revisado.BackSHA256)
}

// Criterio 5: mientras la verificación no sea exitosa, la cuenta no existe.
func TestUnRechazoNoCreaLaCuentaYNoDiceElMotivo(t *testing.T) {
	for _, rechazo := range []domain.IdentityRejection{
		domain.RejectionRestrictedList, domain.RejectionUnreadable, domain.RejectionUnverifiable,
	} {
		t.Run(string(rechazo), func(t *testing.T) {
			proveedor := &proveedorFalso{}
			clientes, svc := servicioConVerificador(&verificadorFalso{rechazo: rechazo}, proveedor)

			_, err := svc.Register(context.Background(), solicitudValida())

			require.ErrorIs(t, err, orchestrator.ErrIdentityNotVerified)
			assert.NotContains(t, err.Error(), string(rechazo), "el motivo no sale del servicio")
			assert.Zero(t, proveedor.llamadas, "no se crea ninguna cuenta")
			guardado, err := clientes.GetByDocument(context.Background(), "1020345678")
			require.NoError(t, err)
			assert.Equal(t, domain.IdentityRejected, guardado.IdentityStatus)
			assert.Equal(t, rechazo, guardado.IdentityRejection, "el motivo queda para la plataforma")
			assert.False(t, guardado.Registered())
		})
	}
}

// Una lista restrictiva cierra el documento: reintentar no vuelve a consultar
// al proveedor, para que nadie pruebe hasta pasar.
func TestUnaListaRestrictivaCierraElDocumentoParaSiempre(t *testing.T) {
	verificador := &verificadorFalso{rechazo: domain.RejectionRestrictedList}
	_, svc := servicioConVerificador(verificador, &proveedorFalso{})
	_, err := svc.Register(context.Background(), solicitudValida())
	require.ErrorIs(t, err, orchestrator.ErrIdentityNotVerified)

	verificador.rechazo = ""
	otraVez := solicitudValida()
	otraVez.DocumentFront, otraVez.DocumentBack = jpegEnBase64(0x33), jpegEnBase64(0x44)
	_, err = svc.Register(context.Background(), otraVez)

	assert.ErrorIs(t, err, orchestrator.ErrIdentityNotVerified)
	assert.Equal(t, 1, verificador.llamadas, "el proveedor no se vuelve a consultar")
}

// Una foto ilegible o una identidad no verificable se reintenta con otras
// fotos o con los datos corregidos.
func TestUnRechazoQueNoEsDefinitivoSePuedeReintentar(t *testing.T) {
	for _, rechazo := range []domain.IdentityRejection{domain.RejectionUnreadable, domain.RejectionUnverifiable} {
		t.Run(string(rechazo), func(t *testing.T) {
			verificador := &verificadorFalso{rechazo: rechazo}
			proveedor := &proveedorFalso{sub: "sub-carolina"}
			clientes, svc := servicioConVerificador(verificador, proveedor)
			_, err := svc.Register(context.Background(), solicitudValida())
			require.ErrorIs(t, err, orchestrator.ErrIdentityNotVerified)

			verificador.rechazo = ""
			c, err := svc.Register(context.Background(), solicitudValida())

			require.NoError(t, err)
			assert.Equal(t, domain.IdentityVerified, c.IdentityStatus)
			assert.Equal(t, 1, proveedor.llamadas)
			guardado, _ := clientes.GetByDocument(context.Background(), "1020345678")
			assert.Empty(t, guardado.IdentityRejection, "la aprobación reemplaza el rechazo anterior")
		})
	}
}

func TestSiElProveedorNoRespondeNoHayCuentaYElRegistroSePuedeReintentar(t *testing.T) {
	verificador := &verificadorFalso{err: ports.ErrIdentityCheckUnavailable}
	proveedor := &proveedorFalso{}
	clientes, svc := servicioConVerificador(verificador, proveedor)

	_, err := svc.Register(context.Background(), solicitudValida())

	require.ErrorIs(t, err, ports.ErrIdentityCheckUnavailable)
	assert.NotErrorIs(t, err, orchestrator.ErrIdentityNotVerified, "no responder no es rechazar")
	assert.Zero(t, proveedor.llamadas)
	guardado, err := clientes.GetByDocument(context.Background(), "1020345678")
	require.NoError(t, err)
	assert.Equal(t, domain.IdentityPending, guardado.IdentityStatus)

	verificador.err = nil
	_, err = svc.Register(context.Background(), solicitudValida())
	assert.NoError(t, err)
}

func TestLaLecturaDevuelveLosDatosDelProveedor(t *testing.T) {
	leidos := domain.DocumentData{DocumentType: "CC", DocumentID: "1020345678", GivenNames: "Carolina",
		Surnames: "Pérez Ruiz", BirthDate: "1992-04-18"}
	verificador := &verificadorFalso{lectura: leidos}
	_, svc := servicioConVerificador(verificador, &proveedorFalso{})

	got, err := svc.ReadDocument(context.Background(), jpegEnBase64(0x11), jpegEnBase64(0x22))

	require.NoError(t, err)
	assert.Equal(t, leidos, got)
}

func TestLaLecturaValidaLasCarasAntesDeLlamarAlProveedor(t *testing.T) {
	verificador := &verificadorFalso{}
	_, svc := servicioConVerificador(verificador, &proveedorFalso{})

	for nombre, caras := range map[string][2]string{
		"sin frente":      {"", jpegEnBase64(0x22)},
		"no es base64":    {"%%%", jpegEnBase64(0x22)},
		"no es JPEG":      {"aG9sYQ==", jpegEnBase64(0x22)},
		"la misma imagen": {jpegEnBase64(0x11), jpegEnBase64(0x11)},
	} {
		_, err := svc.ReadDocument(context.Background(), caras[0], caras[1])
		var invalido *domain.ValidationError
		assert.True(t, errors.As(err, &invalido), nombre)
	}
	assert.Zero(t, verificador.leidas)
}

func TestSiElProveedorNoLeeLaLecturaFalla(t *testing.T) {
	_, svc := servicioConVerificador(&verificadorFalso{err: ports.ErrIdentityCheckUnavailable}, &proveedorFalso{})

	_, err := svc.ReadDocument(context.Background(), jpegEnBase64(0x11), jpegEnBase64(0x22))

	assert.ErrorIs(t, err, ports.ErrIdentityCheckUnavailable)
}
