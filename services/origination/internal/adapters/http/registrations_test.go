package httpadapter_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/orchestrator"
)

// El registro del asegurado (HU-M1.1-01). Es la única ruta de escritura que
// no lleva sesión: la cuenta todavía no existe.

type registroFalso struct {
	cliente  domain.Customer
	err      error
	recibida domain.RegistrationRequest
}

func (f *registroFalso) Register(_ context.Context, r domain.RegistrationRequest) (domain.Customer, error) {
	f.recibida = r
	return f.cliente, f.err
}

func rutaDeRegistro(svc httpadapter.RegistrationService) http.Handler {
	return httpadapter.NewRouter(&fakeResolver{}, httpadapter.WithRegistrations(svc))
}

const cuerpoDeRegistro = `{"document_id":"1020345678","full_name":"Carolina Pérez Ruiz",` +
	`"email":"carolina.perez@example.com","birth_date":"1992-04-18","password":"Solventa2026!"}`

func clienteRegistrado() domain.Customer {
	return domain.Customer{
		ID:         "cus-1",
		DocumentID: "1020345678",
		FullName:   "Carolina Pérez Ruiz",
		Email:      "carolina.perez@example.com",
		BirthDate:  time.Date(1992, 4, 18, 0, 0, 0, 0, time.UTC),
		CognitoSub: "sub-carolina",
		CreatedAt:  issuedAt,
	}
}

func TestElRegistroDevuelveElClienteCreado(t *testing.T) {
	svc := &registroFalso{cliente: clienteRegistrado()}

	rec, body := callQuotes(rutaDeRegistro(svc), http.MethodPost, "/v1/registrations", "", cuerpoDeRegistro)

	require.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, "cus-1", body["customer_id"])
	assert.Equal(t, "1020345678", body["document_id"])
	assert.Equal(t, "1992-04-18", body["birth_date"])
	assert.Equal(t, "sub-carolina", body["subject"])
	assert.Equal(t, "carolina.perez@example.com", body["email"])

	// El cuerpo llega completo al caso de uso.
	assert.Equal(t, "Solventa2026!", svc.recibida.Password)
}

// La ruta es pública: no se exige encabezado de autorización.
func TestElRegistroNoExigeSesion(t *testing.T) {
	rec, _ := callQuotes(rutaDeRegistro(&registroFalso{cliente: clienteRegistrado()}),
		http.MethodPost, "/v1/registrations", "", cuerpoDeRegistro)

	assert.Equal(t, http.StatusCreated, rec.Code)
}

// La contraseña entra, pero no vuelve a salir por ningún camino.
func TestLaContrasenaNoApareceEnLaRespuesta(t *testing.T) {
	rec, _ := callQuotes(rutaDeRegistro(&registroFalso{cliente: clienteRegistrado()}),
		http.MethodPost, "/v1/registrations", "", cuerpoDeRegistro)

	assert.NotContains(t, rec.Body.String(), "Solventa2026!")
	assert.NotContains(t, rec.Body.String(), "password")
}

func TestUnCuerpoQueNoEsJSONSeRechaza(t *testing.T) {
	rec, body := callQuotes(rutaDeRegistro(&registroFalso{}), http.MethodPost, "/v1/registrations", "", "{")

	require.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "invalid_request", body["error"])
}

func TestLosCamposInvalidosSeDevuelvenTodosJuntos(t *testing.T) {
	svc := &registroFalso{err: &domain.ValidationError{Fields: []domain.FieldError{
		{Field: "email", Code: domain.CodeInvalidFormat},
		{Field: "password", Code: domain.CodeOutOfRange},
	}}}

	rec, body := callQuotes(rutaDeRegistro(svc), http.MethodPost, "/v1/registrations", "", cuerpoDeRegistro)

	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Equal(t, "validation_failed", body["error"])
	campos, _ := body["fields"].([]any)
	require.Len(t, campos, 2)
}

func TestUnRegistroDuplicadoEsConflicto(t *testing.T) {
	svc := &registroFalso{err: orchestrator.ErrAlreadyRegistered}

	rec, body := callQuotes(rutaDeRegistro(svc), http.MethodPost, "/v1/registrations", "", cuerpoDeRegistro)

	require.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, "already_registered", body["error"])
}

// La ruta es pública: el detalle de la falla describiría el interior de la
// plataforma a cualquiera que la llame.
func TestUnaFallaInternaNoSeDetalla(t *testing.T) {
	svc := &registroFalso{err: errors.New("el user pool us-east-2_EWwg8NL2C no responde")}

	rec, body := callQuotes(rutaDeRegistro(svc), http.MethodPost, "/v1/registrations", "", cuerpoDeRegistro)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Equal(t, "registration_unavailable", body["error"])
	assert.NotContains(t, rec.Body.String(), "us-east-2_EWwg8NL2C")
}
