package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// El cliente en PostgreSQL (HU-M1.1-01). Es lo que hace que el registro
// sobreviva a un redespliegue del servicio.

func cliente(id, documento, correo string) domain.Customer {
	return domain.Customer{
		ID:         id,
		DocumentID: documento,
		FullName:   "Carolina Pérez Ruiz",
		Email:      correo,
		BirthDate:  time.Date(1992, 4, 18, 0, 0, 0, 0, time.UTC),
		CreatedAt:  t0,
		UpdatedAt:  t0,
	}
}

func TestElClienteSeGuardaYSeLeePorDocumento(t *testing.T) {
	s := postgres.NewCustomerStore(newDB(t))
	ctx := context.Background()

	guardado, err := s.Save(ctx, cliente("cus-1", "1020345678", "carolina@example.com"))
	require.NoError(t, err)
	assert.Equal(t, "cus-1", guardado.ID)

	leido, err := s.GetByDocument(ctx, "1020345678")
	require.NoError(t, err)
	assert.Equal(t, "Carolina Pérez Ruiz", leido.FullName)
	assert.Equal(t, "carolina@example.com", leido.Email)
	assert.Equal(t, 1992, leido.BirthDate.Year())
	assert.Equal(t, 4, int(leido.BirthDate.Month()))
	assert.Equal(t, 18, leido.BirthDate.Day())
	assert.False(t, leido.Registered(), "sin cuenta todavía")
}

func TestUnDocumentoDesconocidoNoEsUnError(t *testing.T) {
	s := postgres.NewCustomerStore(newDB(t))

	_, err := s.GetByDocument(context.Background(), "0000000000")

	assert.ErrorIs(t, err, ports.ErrCustomerNotFound)
}

// El correo es el nombre de usuario en el user pool, así que la búsqueda no
// puede depender de cómo se escriba.
func TestElClienteSeLeePorCorreoSinDistinguirMayusculas(t *testing.T) {
	s := postgres.NewCustomerStore(newDB(t))
	ctx := context.Background()
	_, err := s.Save(ctx, cliente("cus-1", "1020345678", "Carolina.Perez@Example.com"))
	require.NoError(t, err)

	leido, err := s.GetByEmail(ctx, "carolina.perez@example.com")

	require.NoError(t, err)
	assert.Equal(t, "cus-1", leido.ID)
}

// El documento identifica a la persona: un reintento del registro actualiza
// al mismo cliente en lugar de crear otro.
func TestGuardarDosVecesElMismoDocumentoActualizaAlMismoCliente(t *testing.T) {
	s := postgres.NewCustomerStore(newDB(t))
	ctx := context.Background()
	primero, err := s.Save(ctx, cliente("cus-1", "1020345678", "carolina@example.com"))
	require.NoError(t, err)

	otro := cliente("cus-2", "1020345678", "carolina.nueva@example.com")
	otro.FullName = "Carolina Pérez R."
	segundo, err := s.Save(ctx, otro)

	require.NoError(t, err)
	assert.Equal(t, primero.ID, segundo.ID, "conserva el identificador original")
	assert.Equal(t, primero.CreatedAt, segundo.CreatedAt, "conserva cuándo se creó")
	assert.Equal(t, "Carolina Pérez R.", segundo.FullName)
	assert.Equal(t, "carolina.nueva@example.com", segundo.Email)
}

// Volver a guardar el cliente no puede borrar la cuenta que ya tiene.
func TestGuardarDeNuevoConservaLaCuenta(t *testing.T) {
	s := postgres.NewCustomerStore(newDB(t))
	ctx := context.Background()
	c := cliente("cus-1", "1020345678", "carolina@example.com")
	c.CognitoSub = "sub-carolina"
	_, err := s.Save(ctx, c)
	require.NoError(t, err)

	sinCuenta := cliente("cus-1", "1020345678", "carolina@example.com")
	sinCuenta.FullName = "Carolina P."
	guardado, err := s.Save(ctx, sinCuenta)

	require.NoError(t, err)
	assert.Equal(t, "sub-carolina", guardado.CognitoSub)
	assert.True(t, guardado.Registered())
}

func TestLaCuentaSeEscribeCuandoLlega(t *testing.T) {
	s := postgres.NewCustomerStore(newDB(t))
	ctx := context.Background()
	aMedias, err := s.Save(ctx, cliente("cus-1", "1020345678", "carolina@example.com"))
	require.NoError(t, err)
	require.False(t, aMedias.Registered())

	aMedias.CognitoSub = "sub-carolina"
	completo, err := s.Save(ctx, aMedias)

	require.NoError(t, err)
	assert.Equal(t, "sub-carolina", completo.CognitoSub)

	leido, err := s.GetByDocument(ctx, "1020345678")
	require.NoError(t, err)
	assert.Equal(t, "sub-carolina", leido.CognitoSub)
}

// Dos personas no pueden compartir el correo: es el nombre de usuario.
func TestDosClientesNoPuedenCompartirElCorreo(t *testing.T) {
	s := postgres.NewCustomerStore(newDB(t))
	ctx := context.Background()
	_, err := s.Save(ctx, cliente("cus-1", "1020345678", "carolina@example.com"))
	require.NoError(t, err)

	_, err = s.Save(ctx, cliente("cus-2", "9999999999", "CAROLINA@example.com"))

	assert.Error(t, err, "el índice único del correo lo impide")
}

// Las llaves de las dos caras del documento viajan con el cliente: la imagen
// vive en el repositorio documental y aquí queda con qué recuperarla.
func TestLasLlavesDelDocumentoSeGuardanYSeLeen(t *testing.T) {
	s := postgres.NewCustomerStore(newDB(t))
	ctx := context.Background()
	c := cliente("cus-1", "1020345678", "carolina@example.com")
	c.DocumentFrontKey = "identity-documents/aaaa1111.jpg"
	c.DocumentBackKey = "identity-documents/bbbb2222.jpg"

	_, err := s.Save(ctx, c)
	require.NoError(t, err)

	leido, err := s.GetByDocument(ctx, "1020345678")
	require.NoError(t, err)
	assert.Equal(t, "identity-documents/aaaa1111.jpg", leido.DocumentFrontKey)
	assert.Equal(t, "identity-documents/bbbb2222.jpg", leido.DocumentBackKey)
}

// El cliente que cotizó un asesor (HU-W1.1-01) nunca capturó un documento.
func TestUnClienteSinDocumentoCapturadoNoTieneLlaves(t *testing.T) {
	s := postgres.NewCustomerStore(newDB(t))
	ctx := context.Background()

	_, err := s.Save(ctx, cliente("cus-1", "1020345678", "carolina@example.com"))
	require.NoError(t, err)

	leido, err := s.GetByDocument(ctx, "1020345678")
	require.NoError(t, err)
	assert.Empty(t, leido.DocumentFrontKey)
	assert.Empty(t, leido.DocumentBackKey)
}

// Volver a guardar el cliente no puede borrar el documento que ya capturó: el
// asesor que lo actualiza por documento no envía ninguna imagen.
func TestGuardarDeNuevoConservaLasLlavesDelDocumento(t *testing.T) {
	s := postgres.NewCustomerStore(newDB(t))
	ctx := context.Background()
	c := cliente("cus-1", "1020345678", "carolina@example.com")
	c.DocumentFrontKey = "identity-documents/aaaa1111.jpg"
	c.DocumentBackKey = "identity-documents/bbbb2222.jpg"
	_, err := s.Save(ctx, c)
	require.NoError(t, err)

	guardado, err := s.Save(ctx, cliente("cus-1", "1020345678", "carolina@example.com"))

	require.NoError(t, err)
	assert.Equal(t, "identity-documents/aaaa1111.jpg", guardado.DocumentFrontKey)
	assert.Equal(t, "identity-documents/bbbb2222.jpg", guardado.DocumentBackKey)
}
