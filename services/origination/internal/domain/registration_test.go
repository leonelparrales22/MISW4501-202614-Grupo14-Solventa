package domain_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

var registeredAt = time.Date(2026, 10, 22, 15, 0, 0, 0, time.UTC)

func solicitud() domain.RegistrationRequest {
	return domain.RegistrationRequest{
		DocumentID: "1020345678",
		FullName:   "Carolina Pérez Ruiz",
		Email:      "carolina.perez@example.com",
		BirthDate:  "1992-04-18",
		Password:   "Solventa2026!",
	}
}

func camposInvalidos(t *testing.T, err error) []string {
	t.Helper()
	var invalid *domain.ValidationError
	require.ErrorAs(t, err, &invalid)
	campos := make([]string, 0, len(invalid.Fields))
	for _, f := range invalid.Fields {
		campos = append(campos, f.Field)
	}
	return campos
}

func TestUnaSolicitudCompletaProduceElCliente(t *testing.T) {
	c, err := solicitud().Validate(registeredAt)

	require.NoError(t, err)
	assert.Equal(t, "1020345678", c.DocumentID)
	assert.Equal(t, "Carolina Pérez Ruiz", c.FullName)
	assert.Equal(t, "carolina.perez@example.com", c.Email)
	assert.Equal(t, 1992, c.BirthDate.Year())
	// La contraseña viaja al user pool y no forma parte del cliente.
	assert.False(t, c.Registered(), "la cuenta todavía no existe")
}

// Se informan todos los campos inválidos a la vez, no el primero.
func TestUnaSolicitudVaciaInformaTodosLosCampos(t *testing.T) {
	_, err := domain.RegistrationRequest{}.Validate(registeredAt)

	assert.ElementsMatch(t,
		[]string{"document_id", "full_name", "email", "birth_date", "password"},
		camposInvalidos(t, err))
}

func TestLosCamposSeNormalizanAntesDeValidarse(t *testing.T) {
	r := solicitud()
	r.DocumentID = "  1020345678  "
	r.FullName = "  Carolina   Pérez   Ruiz "
	r.Email = " carolina.perez@example.com "

	c, err := r.Validate(registeredAt)

	require.NoError(t, err)
	assert.Equal(t, "1020345678", c.DocumentID)
	assert.Equal(t, "Carolina Pérez Ruiz", c.FullName, "los espacios internos se colapsan")
	assert.Equal(t, "carolina.perez@example.com", c.Email)
}

func TestUnDocumentoConFormaInvalida(t *testing.T) {
	for _, documento := range []string{"123", "1020345678901234567890", "10203 45678", "abc-123"} {
		r := solicitud()
		r.DocumentID = documento

		_, err := r.Validate(registeredAt)

		assert.Contains(t, camposInvalidos(t, err), "document_id", "documento %q", documento)
	}
}

// La política del user pool exige minúscula, mayúscula, dígito y símbolo, con
// doce caracteres. Comprobarlo aquí convierte un rechazo de Cognito en un
// error de campo sobre el que la persona puede actuar.
func TestLaContrasenaSigueLaPoliticaDelUserPool(t *testing.T) {
	for _, tc := range []struct {
		nombre     string
		contrasena string
	}{
		{"muy corta", "Solv2026!"},
		{"sin mayúscula", "solventa2026!"},
		{"sin minúscula", "SOLVENTA2026!"},
		{"sin dígito", "SolventaClave!"},
		{"sin símbolo", "Solventa20264"},
	} {
		t.Run(tc.nombre, func(t *testing.T) {
			r := solicitud()
			r.Password = tc.contrasena

			_, err := r.Validate(registeredAt)

			assert.Contains(t, camposInvalidos(t, err), "password")
		})
	}
}

func TestUnaContrasenaDeDoceCaracteresConTodoLoExigidoPasa(t *testing.T) {
	r := solicitud()
	r.Password = "Solventa26!x"
	require.Len(t, []rune(r.Password), domain.MinPasswordLength)

	_, err := r.Validate(registeredAt)

	assert.NoError(t, err)
}

func TestUnaFechaDeNacimientoInvalida(t *testing.T) {
	for _, fecha := range []string{"", "18/04/1992", "2026-13-01", "2030-01-01"} {
		r := solicitud()
		r.BirthDate = fecha

		_, err := r.Validate(registeredAt)

		assert.Contains(t, camposInvalidos(t, err), "birth_date", "fecha %q", fecha)
	}
}

func TestUnCorreoConFormaInvalida(t *testing.T) {
	for _, correo := range []string{"carolina", "carolina@", "@example.com", "carolina perez@example.com"} {
		r := solicitud()
		r.Email = correo

		_, err := r.Validate(registeredAt)

		assert.Contains(t, camposInvalidos(t, err), "email", "correo %q", correo)
	}
}
