package orchestrator_test

import (
	"bytes"
	"context"
	"encoding/base64"
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

// almacenFalso es el repositorio documental: guarda lo que recibe, o falla
// con el error que se le fije.
type almacenFalso struct {
	mu         sync.Mutex
	objetos    map[string][]byte
	tipos      map[string]string
	escrituras int
	err        error
}

func nuevoAlmacen() *almacenFalso {
	return &almacenFalso{objetos: map[string][]byte{}, tipos: map[string]string{}}
}

func (a *almacenFalso) Put(_ context.Context, key string, content []byte, contentType string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.escrituras++
	if a.err != nil {
		return a.err
	}
	a.objetos[key] = content
	a.tipos[key] = contentType
	return nil
}

func (a *almacenFalso) Get(_ context.Context, key string) ([]byte, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	contenido, ok := a.objetos[key]
	if !ok {
		return nil, ports.ErrDocumentNotFound
	}
	return contenido, nil
}

func (a *almacenFalso) guardados() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.objetos)
}

type relojFijo struct{ t time.Time }

func (r relojFijo) Now() time.Time { return r.t }

func servicioDeRegistro(p *proveedorFalso) (*memory.CustomerStore, *orchestrator.RegistrationService) {
	clientes, _, svc := servicioConAlmacen(p, nuevoAlmacen())
	return clientes, svc
}

func servicioConAlmacen(p *proveedorFalso, almacen *almacenFalso) (*memory.CustomerStore, *almacenFalso,
	*orchestrator.RegistrationService) {
	clientes := memory.NewCustomerStore()
	return clientes, almacen,
		orchestrator.NewRegistrationService(clientes, almacen, p, relojFijo{t: registroEn})
}

// jpegEnBase64 arma una cara del documento: la firma de un JPEG y el tamaño
// mínimo que el registro acepta.
func jpegEnBase64(relleno byte) string {
	contenido := append([]byte{0xFF, 0xD8, 0xFF, 0xE0},
		bytes.Repeat([]byte{relleno}, domain.MinIdentityDocumentBytes)...)
	return base64.StdEncoding.EncodeToString(contenido)
}

func solicitudValida() domain.RegistrationRequest {
	return domain.RegistrationRequest{
		DocumentID:    "1020345678",
		FullName:      "Carolina Pérez Ruiz",
		Email:         "carolina.perez@example.com",
		BirthDate:     "1992-04-18",
		Password:      "Solventa2026!",
		DocumentFront: jpegEnBase64(0x11),
		DocumentBack:  jpegEnBase64(0x22),
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

// El documento de identidad es la razón de ser de la verificación: las dos
// caras quedan en el repositorio documental y el cliente guarda su llave.
func TestElRegistroGuardaLasDosCarasDelDocumento(t *testing.T) {
	proveedor := &proveedorFalso{}
	clientes, almacen, svc := servicioConAlmacen(proveedor, nuevoAlmacen())

	c, err := svc.Register(context.Background(), solicitudValida())

	require.NoError(t, err)
	assert.Equal(t, 2, almacen.guardados(), "el frente y el reverso")
	assert.NotEmpty(t, c.DocumentFrontKey)
	assert.NotEmpty(t, c.DocumentBackKey)
	for _, llave := range []string{c.DocumentFrontKey, c.DocumentBackKey} {
		contenido, err := almacen.Get(context.Background(), llave)
		require.NoError(t, err, "la llave %s apunta a un objeto escrito", llave)
		assert.Equal(t, []byte{0xFF, 0xD8, 0xFF}, contenido[:3], "se guardó el JPEG tal cual")
		assert.Equal(t, "image/jpeg", almacen.tipos[llave])
	}

	guardado, err := clientes.GetByDocument(context.Background(), "1020345678")
	require.NoError(t, err)
	assert.Equal(t, c.DocumentFrontKey, guardado.DocumentFrontKey, "las llaves quedan persistidas")
	assert.Equal(t, c.DocumentBackKey, guardado.DocumentBackKey)
}

// Sin el documento guardado no hay registro: ni cliente, ni cuenta. Un cliente
// que apunte a un objeto que nunca se escribió es una verificación que nadie
// puede revisar después.
func TestSiFallaElRepositorioDocumentalNoHayClienteNiCuenta(t *testing.T) {
	proveedor := &proveedorFalso{}
	almacen := nuevoAlmacen()
	almacen.err = errors.New("el bucket no responde")
	clientes, _, svc := servicioConAlmacen(proveedor, almacen)

	_, err := svc.Register(context.Background(), solicitudValida())

	require.Error(t, err)
	assert.Zero(t, proveedor.llamadas, "no se crea la cuenta")
	_, err = clientes.GetByDocument(context.Background(), "1020345678")
	assert.ErrorIs(t, err, ports.ErrCustomerNotFound, "no se guarda el cliente")
}

// La llave sale del contenido de la imagen, así que reintentar el registro con
// las mismas fotos no deja una segunda copia.
func TestUnReintentoNoDuplicaLasImagenes(t *testing.T) {
	proveedor := &proveedorFalso{err: errors.New("el user pool no responde")}
	_, almacen, svc := servicioConAlmacen(proveedor, nuevoAlmacen())
	_, err := svc.Register(context.Background(), solicitudValida())
	require.Error(t, err)

	proveedor.mu.Lock()
	proveedor.err = nil
	proveedor.mu.Unlock()

	_, err = svc.Register(context.Background(), solicitudValida())

	require.NoError(t, err)
	assert.Equal(t, 2, almacen.guardados(), "las mismas dos caras, no cuatro")
}

// Una solicitud sin las caras del documento se rechaza antes de escribir nada.
func TestSinElDocumentoNoSeEscribeNada(t *testing.T) {
	proveedor := &proveedorFalso{}
	_, almacen, svc := servicioConAlmacen(proveedor, nuevoAlmacen())
	r := solicitudValida()
	r.DocumentFront = ""

	_, err := svc.Register(context.Background(), r)

	var invalid *domain.ValidationError
	assert.ErrorAs(t, err, &invalid)
	assert.Zero(t, almacen.escrituras)
	assert.Zero(t, proveedor.llamadas)
}
