package cognito_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	cip "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
	"github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	cognitoadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/cognito"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// La creación de la cuenta en el user pool (HU-M1.1-01).

type apiFalsa struct {
	crear      *cip.AdminCreateUserInput
	contrasena *cip.AdminSetUserPasswordInput
	grupo      *cip.AdminAddUserToGroupInput

	errCrear      error
	errContrasena error
	errGrupo      error
	sub           string
}

func (a *apiFalsa) AdminCreateUser(_ context.Context, in *cip.AdminCreateUserInput,
	_ ...func(*cip.Options)) (*cip.AdminCreateUserOutput, error) {
	a.crear = in
	if a.errCrear != nil {
		return nil, a.errCrear
	}
	sub := a.sub
	if sub == "" {
		sub = "sub-de-prueba"
	}
	return &cip.AdminCreateUserOutput{User: &types.UserType{Attributes: []types.AttributeType{
		{Name: aws.String("email"), Value: in.Username},
		{Name: aws.String("sub"), Value: aws.String(sub)},
	}}}, nil
}

func (a *apiFalsa) AdminSetUserPassword(_ context.Context, in *cip.AdminSetUserPasswordInput,
	_ ...func(*cip.Options)) (*cip.AdminSetUserPasswordOutput, error) {
	a.contrasena = in
	return &cip.AdminSetUserPasswordOutput{}, a.errContrasena
}

func (a *apiFalsa) AdminAddUserToGroup(_ context.Context, in *cip.AdminAddUserToGroupInput,
	_ ...func(*cip.Options)) (*cip.AdminAddUserToGroupOutput, error) {
	a.grupo = in
	return &cip.AdminAddUserToGroupOutput{}, a.errGrupo
}

func proveedor(api *apiFalsa) *cognitoadapter.Provider {
	return cognitoadapter.NewProvider(api, "us-east-2_Ejemplo", "insured")
}

func TestLaCuentaQuedaUsableDeInmediato(t *testing.T) {
	api := &apiFalsa{sub: "sub-carolina"}

	sub, err := proveedor(api).CreateAccount(context.Background(),
		"carolina.perez@example.com", "Solventa2026!", "Carolina Pérez Ruiz")

	require.NoError(t, err)
	assert.Equal(t, "sub-carolina", sub)

	// La contraseña es la que la persona eligió y queda permanente, así que
	// la cuenta no exige cambiarla en el primer ingreso.
	require.NotNil(t, api.contrasena)
	assert.Equal(t, "Solventa2026!", aws.ToString(api.contrasena.Password))
	assert.True(t, api.contrasena.Permanent)

	// Sin correo de bienvenida: Cognito enviaría una contraseña temporal que
	// nadie va a usar.
	require.NotNil(t, api.crear)
	assert.Equal(t, types.MessageActionTypeSuppress, api.crear.MessageAction)
	assert.Equal(t, "carolina.perez@example.com", aws.ToString(api.crear.Username))
}

// El grupo es el rol que el token anuncia en cognito:groups, que es contra lo
// que autorizan los servicios.
func TestLaCuentaEntraAlGrupoDelAsegurado(t *testing.T) {
	api := &apiFalsa{}

	_, err := proveedor(api).CreateAccount(context.Background(), "a@example.com", "Solventa2026!", "Ana")

	require.NoError(t, err)
	require.NotNil(t, api.grupo)
	assert.Equal(t, "insured", aws.ToString(api.grupo.GroupName))
	assert.Equal(t, "us-east-2_Ejemplo", aws.ToString(api.grupo.UserPoolId))
}

func TestElCorreoQuedaMarcadoComoVerificado(t *testing.T) {
	api := &apiFalsa{}

	_, err := proveedor(api).CreateAccount(context.Background(), "a@example.com", "Solventa2026!", "Ana")

	require.NoError(t, err)
	atributos := map[string]string{}
	for _, a := range api.crear.UserAttributes {
		atributos[aws.ToString(a.Name)] = aws.ToString(a.Value)
	}
	assert.Equal(t, "true", atributos["email_verified"])
	assert.Equal(t, "Ana", atributos["name"], "el nombre viaja para que el token lo traiga")
}

func TestUnCorreoYaTomadoSeDistingueDeUnaFalla(t *testing.T) {
	api := &apiFalsa{errCrear: &types.UsernameExistsException{}}

	_, err := proveedor(api).CreateAccount(context.Background(), "a@example.com", "Solventa2026!", "Ana")

	assert.ErrorIs(t, err, ports.ErrAccountExists)
}

func TestUnaFallaDelPoolSePropaga(t *testing.T) {
	fallo := errors.New("el user pool no responde")

	for _, tc := range []struct {
		nombre string
		api    *apiFalsa
	}{
		{"al crear", &apiFalsa{errCrear: fallo}},
		{"al fijar la contraseña", &apiFalsa{errContrasena: fallo}},
		{"al agregar al grupo", &apiFalsa{errGrupo: fallo}},
	} {
		t.Run(tc.nombre, func(t *testing.T) {
			_, err := proveedor(tc.api).CreateAccount(context.Background(), "a@example.com", "Solventa2026!", "Ana")

			assert.ErrorIs(t, err, fallo)
			assert.NotErrorIs(t, err, ports.ErrAccountExists)
		})
	}
}
