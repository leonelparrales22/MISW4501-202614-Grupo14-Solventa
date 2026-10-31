package mail_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/email"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/mail"
)

type proveedor struct{ got email.Message }

func (p *proveedor) Send(_ context.Context, m email.Message) error {
	p.got = m
	return nil
}

func TestTheMessageCarriesItsKey(t *testing.T) {
	p := &proveedor{}
	require.NoError(t, mail.NewMailer(p).Send(context.Background(), "c@x", "asunto", "cuerpo", "rechazo-cl-1"))
	assert.Equal(t, email.Message{To: "c@x", Subject: "asunto", Body: "cuerpo", IdempotencyKey: "rechazo-cl-1"}, p.got)
}
