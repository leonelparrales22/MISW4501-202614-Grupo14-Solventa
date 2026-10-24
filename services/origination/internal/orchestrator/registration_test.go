package orchestrator_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/orchestrator"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

var registroEn = time.Date(2026, 10, 22, 15, 0, 0, 0, time.UTC)

// proveedorFalso es el user pool: devuelve un sub, o el error que se le fije.
type proveedorFalso struct {
	mu       sync.Mutex
	sub      string
	err      error
	llamadas int
	correo   string
	clave    string
	nombre   string
}

func (p *proveedorFalso) CreateAccount(_ context.Context, email, password, fullName string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.llamadas++
	p.correo, p.clave, p.nombre = email, password, fullName
	if p.err != nil {
		return "", p.err
	}
	if p.sub == "" {
		p.sub = "sub-de-prueba"
	}
	return p.sub, nil
}

type relojFijo struct{ t time.Time }

func (r relojFijo) Now() time.Time { return r.t }

func servicioDeRegistro(p *proveedorFalso) (*memory.CustomerStore, *orchestrator.RegistrationService) {
	clientes := memory.NewCustomerStore()
	return clientes, orchestrator.NewRegistrationService(clientes, p, relojFijo{t: registroEn})
}

func solicitudValida() domain.RegistrationRequest {
	return domain.RegistrationRequest{
		DocumentID: "1020345678",
		FullName:   "Carolina Pérez Ruiz",
		Email:      "carolina.perez@example.com",
		BirthDate:  "1992-04-18",
		Password:   "Solventa2026!",
	}
}

func TestElRegistroGuardaElClienteYCreaLaCuenta(t *testing.T) {
	proveedor := &proveedorFalso{sub: "sub-carolina"}
	clientes, svc := servicioDeRegistro(proveedor)

	c, err := svc.Register(context.Background(), solicitudValida())

	require.NoError(t, err)
	assert.NotEmpty(t, c.ID)
	assert.Equal(t, "sub-carolina", c.CognitoSub)
	assert.Equal(t, registroEn, c.CreatedAt)
	assert.True(t, c.Registered())

	guardado, err := clientes.GetByDocument(context.Background(), "1020345678")
	require.NoError(t, err)
	assert.Equal(t, "sub-carolina", guardado.CognitoSub, "el sub queda persistido, no solo devuelto")

	// La cuenta se crea con lo que la persona escribió.
	assert.Equal(t, "carolina.perez@example.com", proveedor.correo)
	assert.Equal(t, "Solventa2026!", proveedor.clave)
	assert.Equal(t, "Carolina Pérez Ruiz", proveedor.nombre)
}

func TestUnaSolicitudInvalidaNoTocaElUserPool(t *testing.T) {
	proveedor := &proveedorFalso{}
	clientes, svc := servicioDeRegistro(proveedor)
	r := solicitudValida()
	r.Email = "no-es-un-correo"

	_, err := svc.Register(context.Background(), r)

	var invalid *domain.ValidationError
	assert.ErrorAs(t, err, &invalid)
	assert.Zero(t, proveedor.llamadas)
	_, err = clientes.GetByDocument(context.Background(), "1020345678")
	assert.ErrorIs(t, err, ports.ErrCustomerNotFound, "no se guarda nada")
}

func TestNoSePuedeRegistrarDosVecesElMismoDocumento(t *testing.T) {
	proveedor := &proveedorFalso{}
	_, svc := servicioDeRegistro(proveedor)
	_, err := svc.Register(context.Background(), solicitudValida())
	require.NoError(t, err)

	otra := solicitudValida()
	otra.Email = "otro.correo@example.com"
	_, err = svc.Register(context.Background(), otra)

	assert.ErrorIs(t, err, orchestrator.ErrAlreadyRegistered)
	assert.Equal(t, 1, proveedor.llamadas, "no se intenta crear una segunda cuenta")
}

func TestNoSePuedeRegistrarDosVecesElMismoCorreo(t *testing.T) {
	proveedor := &proveedorFalso{}
	_, svc := servicioDeRegistro(proveedor)
	_, err := svc.Register(context.Background(), solicitudValida())
	require.NoError(t, err)

	otra := solicitudValida()
	otra.DocumentID = "9999999999"

	_, err = svc.Register(context.Background(), otra)

	assert.ErrorIs(t, err, orchestrator.ErrAlreadyRegistered)
}

// El correo es el nombre de usuario en el user pool, así que dos personas no
// pueden compartirlo aunque lo escriban distinto.
func TestElCorreoSeComparaSinDistinguirMayusculas(t *testing.T) {
	proveedor := &proveedorFalso{}
	_, svc := servicioDeRegistro(proveedor)
	_, err := svc.Register(context.Background(), solicitudValida())
	require.NoError(t, err)

	otra := solicitudValida()
	otra.DocumentID = "9999999999"
	otra.Email = "Carolina.Perez@Example.com"

	_, err = svc.Register(context.Background(), otra)

	assert.ErrorIs(t, err, orchestrator.ErrAlreadyRegistered)
}

// Cuando el user pool ya tiene la cuenta, el registro no se presenta como una
// falla del sistema.
func TestUnaCuentaQueYaExisteSeInformaComoRegistroDuplicado(t *testing.T) {
	proveedor := &proveedorFalso{err: ports.ErrAccountExists}
	_, svc := servicioDeRegistro(proveedor)

	_, err := svc.Register(context.Background(), solicitudValida())

	assert.ErrorIs(t, err, orchestrator.ErrAlreadyRegistered)
}

// El cliente se escribe antes que la cuenta: si la cuenta falla queda un
// registro a medias, que se puede completar. Al revés quedaría una cuenta sin
// dueño en la base.
func TestSiFallaLaCuentaElClienteQuedaSinSubYSePuedeReintentar(t *testing.T) {
	proveedor := &proveedorFalso{err: errors.New("el user pool no responde")}
	clientes, svc := servicioDeRegistro(proveedor)

	_, err := svc.Register(context.Background(), solicitudValida())
	require.Error(t, err)

	aMedias, err := clientes.GetByDocument(context.Background(), "1020345678")
	require.NoError(t, err)
	assert.False(t, aMedias.Registered(), "el cliente quedó sin cuenta")

	// El reintento no choca contra el registro a medias y lo completa.
	proveedor.mu.Lock()
	proveedor.err = nil
	proveedor.mu.Unlock()

	c, err := svc.Register(context.Background(), solicitudValida())

	require.NoError(t, err)
	assert.True(t, c.Registered())
	assert.Equal(t, aMedias.ID, c.ID, "es el mismo cliente, no uno nuevo")
}
