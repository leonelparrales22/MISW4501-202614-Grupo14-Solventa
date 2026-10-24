package domain_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

// jpeg arma una imagen con la firma de un JPEG y el tamaño mínimo que el
// registro acepta. El relleno distingue una cara de la otra.
func jpeg(relleno byte) []byte {
	return append([]byte{0xFF, 0xD8, 0xFF, 0xE0},
		bytes.Repeat([]byte{relleno}, domain.MinIdentityDocumentBytes)...)
}

func jpegEnBase64(relleno byte) string {
	return base64.StdEncoding.EncodeToString(jpeg(relleno))
}

// codigoDe devuelve el código con el que se rechazó un campo.
func codigoDe(t *testing.T, err error, campo string) string {
	t.Helper()
	var invalid *domain.ValidationError
	require.ErrorAs(t, err, &invalid)
	for _, f := range invalid.Fields {
		if f.Field == campo {
			return f.Code
		}
	}
	t.Fatalf("el campo %s no fue rechazado; campos: %v", campo, invalid.Fields)
	return ""
}

// La llave sale del contenido de la imagen: así una reintentada del registro
// con la misma foto apunta al mismo objeto.
func TestLaLlaveDeUnaCaraSaleDeSuContenido(t *testing.T) {
	r := solicitud()

	registro, err := r.Validate(registeredAt)

	require.NoError(t, err)
	suma := sha256.Sum256(jpeg(0x11))
	assert.Equal(t, hex.EncodeToString(suma[:]), registro.Front.SHA256)
	assert.Equal(t, "identity-documents/"+hex.EncodeToString(suma[:])+".jpg", registro.Front.Key())
	assert.Equal(t, "image/jpeg", registro.Front.ContentType())
	assert.Equal(t, domain.FaceFront, registro.Front.Face)
	assert.Equal(t, domain.FaceBack, registro.Back.Face)
}

func TestElClienteConservaLaLlaveDeCadaCara(t *testing.T) {
	registro, err := solicitud().Validate(registeredAt)

	require.NoError(t, err)
	assert.Equal(t, registro.Front.Key(), registro.Customer.DocumentFrontKey)
	assert.Equal(t, registro.Back.Key(), registro.Customer.DocumentBackKey)
	assert.NotEqual(t, registro.Customer.DocumentFrontKey, registro.Customer.DocumentBackKey)
}

func TestLasDosCarasSeDevuelvenConElFrenteAdelante(t *testing.T) {
	registro, err := solicitud().Validate(registeredAt)

	require.NoError(t, err)
	caras := registro.Documents()
	require.Len(t, caras, 2)
	assert.Equal(t, domain.FaceFront, caras[0].Face)
	assert.Equal(t, domain.FaceBack, caras[1].Face)
}

func TestLaMismaImagenNoSirveParaLasDosCaras(t *testing.T) {
	r := solicitud()
	r.DocumentBack = r.DocumentFront

	_, err := r.Validate(registeredAt)

	assert.Equal(t, "invalid_format", codigoDe(t, err, "document_back"))
}

func TestSinLasCarasDelDocumentoNoHayRegistro(t *testing.T) {
	r := solicitud()
	r.DocumentFront, r.DocumentBack = "", ""

	_, err := r.Validate(registeredAt)

	assert.Equal(t, "required", codigoDe(t, err, "document_front"))
	assert.Equal(t, "required", codigoDe(t, err, "document_back"))
}

func TestUnaCaraQueNoEsBase64(t *testing.T) {
	r := solicitud()
	r.DocumentFront = "esto no es base64 %%%"

	_, err := r.Validate(registeredAt)

	assert.Equal(t, "invalid_format", codigoDe(t, err, "document_front"))
}

// Lo único que produce la captura es un JPEG: otro formato no es la
// fotografía que el registro pide.
func TestUnaCaraQueNoEsJpeg(t *testing.T) {
	png := append([]byte{0x89, 0x50, 0x4E, 0x47}, bytes.Repeat([]byte{0x22}, domain.MinIdentityDocumentBytes)...)
	r := solicitud()
	r.DocumentBack = base64.StdEncoding.EncodeToString(png)

	_, err := r.Validate(registeredAt)

	assert.Equal(t, "invalid_format", codigoDe(t, err, "document_back"))
}

func TestUnaCaraFueraDeTamano(t *testing.T) {
	for nombre, contenido := range map[string][]byte{
		"demasiado pequeña": {0xFF, 0xD8, 0xFF, 0xE0},
		"demasiado grande": append([]byte{0xFF, 0xD8, 0xFF, 0xE0},
			bytes.Repeat([]byte{0x33}, domain.MaxIdentityDocumentBytes)...),
	} {
		t.Run(nombre, func(t *testing.T) {
			r := solicitud()
			r.DocumentFront = base64.StdEncoding.EncodeToString(contenido)

			_, err := r.Validate(registeredAt)

			assert.Equal(t, "out_of_range", codigoDe(t, err, "document_front"))
		})
	}
}

// La llave no lleva el documento, el nombre ni el correo de la persona.
func TestLaLlaveNoLlevaDatosPersonales(t *testing.T) {
	registro, err := solicitud().Validate(registeredAt)

	require.NoError(t, err)
	for _, dato := range []string{"1020345678", "Carolina", "carolina.perez@example.com"} {
		assert.NotContains(t, registro.Front.Key(), dato)
		assert.NotContains(t, registro.Back.Key(), dato)
	}
	assert.True(t, strings.HasSuffix(registro.Front.Key(), ".jpg"))
}
