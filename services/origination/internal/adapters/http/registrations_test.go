package httpadapter_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/orchestrator"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// El registro del asegurado (HU-M1.1-01). Es la única ruta de escritura que
// no lleva sesión: la cuenta todavía no existe.

type registroFalso struct {
	cliente  domain.Customer
	err      error
	recibida domain.RegistrationRequest

	lectura     domain.DocumentData
	errLectura  error
	carasLeidas [2]string
}

func (f *registroFalso) ReadDocument(_ context.Context, front, back string) (domain.DocumentData, error) {
	f.carasLeidas = [2]string{front, back}
	return f.lectura, f.errLectura
}

func (f *registroFalso) Register(_ context.Context, r domain.RegistrationRequest) (domain.Customer, error) {
	f.recibida = r
	return f.cliente, f.err
}

func rutaDeRegistro(svc httpadapter.RegistrationService) http.Handler {
	return httpadapter.NewRouter(&fakeResolver{}, httpadapter.WithRegistrations(svc))
}

// Las caras del documento viajan en base64. Quien juzga la imagen es el
// dominio, así que aquí basta con que el campo llegue.
const (
	frenteEnBase64  = "ZnJlbnRlLWRlbC1kb2N1bWVudG8="
	reversoEnBase64 = "cmV2ZXJzby1kZWwtZG9jdW1lbnRv"
)

const cuerpoDeRegistro = `{"document_id":"1020345678","full_name":"Carolina Pérez Ruiz",` +
	`"email":"carolina.perez@example.com","birth_date":"1992-04-18","password":"Solventa2026!",` +
	`"document_front":"` + frenteEnBase64 + `","document_back":"` + reversoEnBase64 + `"}`

func clienteRegistrado() domain.Customer {
	return domain.Customer{
		ID:               "cus-1",
		DocumentID:       "1020345678",
		FullName:         "Carolina Pérez Ruiz",
		Email:            "carolina.perez@example.com",
		BirthDate:        time.Date(1992, 4, 18, 0, 0, 0, 0, time.UTC),
		CognitoSub:       "sub-carolina",
		DocumentFrontKey: "identity-documents/aaaa.jpg",
		DocumentBackKey:  "identity-documents/bbbb.jpg",
		CreatedAt:        issuedAt,
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

	// El cuerpo llega completo al caso de uso, las dos caras incluidas.
	assert.Equal(t, "Solventa2026!", svc.recibida.Password)
	assert.Equal(t, frenteEnBase64, svc.recibida.DocumentFront)
	assert.Equal(t, reversoEnBase64, svc.recibida.DocumentBack)
}

// La respuesta nombra el objeto de cada cara: con eso se ubica la imagen en el
// repositorio documental sin tener que leerla de vuelta.
func TestLaRespuestaNombraElObjetoDeCadaCara(t *testing.T) {
	rec, body := callQuotes(rutaDeRegistro(&registroFalso{cliente: clienteRegistrado()}),
		http.MethodPost, "/v1/registrations", "", cuerpoDeRegistro)

	require.Equal(t, http.StatusCreated, rec.Code)
	caras, _ := body["identity_documents"].([]any)
	require.Len(t, caras, 2)
	frente, _ := caras[0].(map[string]any)
	reverso, _ := caras[1].(map[string]any)
	assert.Equal(t, "front", frente["face"])
	assert.Equal(t, "identity-documents/aaaa.jpg", frente["object_key"])
	assert.Equal(t, "back", reverso["face"])
	assert.Equal(t, "identity-documents/bbbb.jpg", reverso["object_key"])

	// La imagen entra, pero no vuelve a salir: lo que se devuelve es su llave.
	assert.NotContains(t, rec.Body.String(), frenteEnBase64)
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

// El tope del cuerpo deja holgura para las dos caras y nada más.
func TestUnCuerpoMasGrandeQueElTopeSeRechaza(t *testing.T) {
	svc := &registroFalso{cliente: clienteRegistrado()}
	enorme := `{"document_front":"` + strings.Repeat("A", 5<<20) + `"}`

	rec, body := callQuotes(rutaDeRegistro(svc), http.MethodPost, "/v1/registrations", "", enorme)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "invalid_request", body["error"])
	assert.Empty(t, svc.recibida.DocumentFront, "no llega al caso de uso")
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

// Criterio 3: un rechazo del proveedor se responde igual sea cual sea el
// motivo, y sin decirlo.
func TestUnRechazoDeIdentidadSeRespondeSinElMotivo(t *testing.T) {
	svc := &registroFalso{err: orchestrator.ErrIdentityNotVerified}

	rec, body := callQuotes(rutaDeRegistro(svc), http.MethodPost, "/v1/registrations", "", cuerpoDeRegistro)

	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Equal(t, "identity_not_verified", body["error"])
	for _, motivo := range []string{"restricted", "list", "unreadable", "unverifiable", "lista"} {
		assert.NotContains(t, strings.ToLower(rec.Body.String()), motivo)
	}
}

func TestSiLaVerificacionNoRespondeElRegistroEsReintentable(t *testing.T) {
	svc := &registroFalso{err: ports.ErrIdentityCheckUnavailable}

	rec, body := callQuotes(rutaDeRegistro(svc), http.MethodPost, "/v1/registrations", "", cuerpoDeRegistro)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Equal(t, "registration_unavailable", body["error"])
}

const cuerpoDeLectura = `{"document_front":"` + frenteEnBase64 + `","document_back":"` + reversoEnBase64 + `"}`

// Criterio 1: el proveedor lee el documento y la persona confirma los datos.
func TestLaLecturaDelDocumentoDevuelveLosDatosLeidos(t *testing.T) {
	svc := &registroFalso{lectura: domain.DocumentData{DocumentType: "CC", DocumentID: "1020345678",
		GivenNames: "Carolina", Surnames: "Pérez Ruiz", BirthDate: "1992-04-18"}}

	rec, body := callQuotes(rutaDeRegistro(svc), http.MethodPost, "/v1/registrations/document-reads", "", cuerpoDeLectura)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, "CC", body["document_type"])
	assert.Equal(t, "1020345678", body["document_id"])
	assert.Equal(t, "Carolina", body["given_names"])
	assert.Equal(t, "Pérez Ruiz", body["surnames"])
	assert.Equal(t, "1992-04-18", body["birth_date"])
	assert.Equal(t, [2]string{frenteEnBase64, reversoEnBase64}, svc.carasLeidas)
}

func TestLaLecturaRespondeLosErroresDeCadaCaso(t *testing.T) {
	for nombre, caso := range map[string]struct {
		cuerpo string
		err    error
		codigo int
		error  string
	}{
		"cuerpo invalido": {`{no es json`, nil, http.StatusBadRequest, "invalid_request"},
		"cara invalida": {cuerpoDeLectura, &domain.ValidationError{Fields: []domain.FieldError{
			{Field: "document_back", Code: domain.CodeInvalidFormat}}}, http.StatusUnprocessableEntity, "validation_failed"},
		"proveedor caido": {cuerpoDeLectura, ports.ErrIdentityCheckUnavailable, http.StatusServiceUnavailable,
			"document_read_unavailable"},
	} {
		svc := &registroFalso{errLectura: caso.err}

		rec, body := callQuotes(rutaDeRegistro(svc), http.MethodPost, "/v1/registrations/document-reads", "", caso.cuerpo)

		assert.Equal(t, caso.codigo, rec.Code, nombre)
		assert.Equal(t, caso.error, body["error"], nombre)
	}
}
