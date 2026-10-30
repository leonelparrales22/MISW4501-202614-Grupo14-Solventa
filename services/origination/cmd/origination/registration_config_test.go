package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/filestore"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/memory"
)

// El registro del asegurado (HU-M1.1-01) necesita el user pool y el
// repositorio documental: la cuenta se crea en el uno y las dos caras del
// documento quedan en el otro.

func TestSinUserPoolElRegistroNoSeMonta(t *testing.T) {
	cfg, err := loadConfig(env(requiredEnv()))
	require.NoError(t, err)
	assert.False(t, cfg.registration.enabled)

	svc, err := newRegistrationService(context.Background(), cfg.registration,
		memory.NewCustomerStore(), nil, "")

	require.NoError(t, err)
	assert.Nil(t, svc, "la ruta no se monta")
}

func TestElUserPoolTraeSuGrupoPorOmision(t *testing.T) {
	m := requiredEnv()
	m["COGNITO_USER_POOL_ID"] = "us-east-2_EWwg8NL2C"
	m["DOCUMENTS_DIR"] = t.TempDir()
	m["KYC_URL"] = "http://kyc:9020"

	cfg, err := loadConfig(env(m))

	require.NoError(t, err)
	assert.True(t, cfg.registration.enabled)
	assert.Equal(t, "us-east-2_EWwg8NL2C", cfg.registration.poolID)
	assert.Equal(t, "insured", cfg.registration.group)
}

func TestElGrupoDelRegistroSePuedeFijar(t *testing.T) {
	m := requiredEnv()
	m["COGNITO_USER_POOL_ID"] = "us-east-2_EWwg8NL2C"
	m["COGNITO_INSURED_GROUP"] = "asegurados"
	m["DOCUMENTS_DIR"] = t.TempDir()
	m["KYC_URL"] = "http://kyc:9020"

	cfg, err := loadConfig(env(m))

	require.NoError(t, err)
	assert.Equal(t, "asegurados", cfg.registration.group)
}

// Sin repositorio documental el registro guardaría la cuenta y perdería el
// documento que la sustenta, así que el servicio no arranca.
func TestConUserPoolYSinRepositorioDocumentalNoArranca(t *testing.T) {
	m := requiredEnv()
	m["COGNITO_USER_POOL_ID"] = "us-east-2_EWwg8NL2C"
	m["KYC_URL"] = "http://kyc:9020"

	_, err := loadConfig(env(m))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "DOCUMENTS_BUCKET")
	assert.Contains(t, err.Error(), "DOCUMENTS_DIR")
	assert.Contains(t, err.Error(), "COGNITO_USER_POOL_ID")
}

func TestElRegistroNoSeCableaSinRepositorioDocumental(t *testing.T) {
	cfg := registrationConfig{enabled: true, poolID: "us-east-2_EWwg8NL2C", group: "insured", kycURL: "http://kyc:9020"}

	_, err := newRegistrationService(context.Background(), cfg, memory.NewCustomerStore(), nil, "")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "document repository")
}

// Sin proveedor de identidad el registro crearía cuentas sin verificar a
// nadie, que es justo lo que HU-M1.1-01 prohíbe (criterio 5): no arranca.
func TestConUserPoolYSinProveedorDeIdentidadNoArranca(t *testing.T) {
	m := requiredEnv()
	m["COGNITO_USER_POOL_ID"] = "us-east-2_EWwg8NL2C"
	m["DOCUMENTS_DIR"] = t.TempDir()
	delete(m, "KYC_URL")

	_, err := loadConfig(env(m))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "KYC_URL")
}

func TestElRegistroNoSeCableaSinProveedorDeIdentidad(t *testing.T) {
	cfg := registrationConfig{enabled: true, poolID: "us-east-2_EWwg8NL2C", group: "insured"}
	documents, err := filestore.New(t.TempDir())
	require.NoError(t, err)

	_, err = newRegistrationService(context.Background(), cfg, memory.NewCustomerStore(), documents, "disco")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "identity provider")
}
