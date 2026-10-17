package links_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/links"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

const secret = "0123456789abcdef0123456789abcdef"

var now = time.Date(2026, 10, 16, 15, 0, 0, 0, time.UTC)

func signer(t *testing.T, s string) *links.Signer {
	t.Helper()
	sg, err := links.NewSigner(s)
	require.NoError(t, err)
	return sg
}

func TestSignThenVerify(t *testing.T) {
	sg := signer(t, secret)
	token := sg.Sign("pol-123", 2, now.Add(time.Hour))

	policy, version, err := sg.Verify(token, now)

	require.NoError(t, err)
	assert.Equal(t, "pol-123", policy)
	assert.Equal(t, 2, version)
	assert.NotContains(t, token, "pol-123", "the policy id is not readable in the URL")
}

func TestVerifyRejectsAnExpiredLink(t *testing.T) {
	sg := signer(t, secret)
	token := sg.Sign("pol-123", 1, now.Add(time.Hour))

	_, _, err := sg.Verify(token, now.Add(time.Hour))

	assert.ErrorIs(t, err, ports.ErrInvalidLink)
	_, _, err = sg.Verify(token, now.Add(time.Hour-time.Second))
	assert.NoError(t, err)
}

func TestVerifyRejectsATamperedOrForeignToken(t *testing.T) {
	sg := signer(t, secret)
	token := sg.Sign("pol-123", 1, now.Add(time.Hour))
	payload, mac, _ := strings.Cut(token, ".")
	other := signer(t, "ffffffffffffffffffffffffffffffff")

	for name, tok := range map[string]string{
		"empty":            "",
		"no separator":     "abc",
		"bad payload":      "!!!." + mac,
		"bad mac encoding": payload + ".!!!",
		"wrong mac":        payload + "." + mac[:len(mac)-2] + "AA",
		"mac of other key": other.Sign("pol-123", 1, now.Add(time.Hour)),
		"payload swapped":  sg.Sign("pol-999", 1, now.Add(time.Hour))[:strings.Index(sg.Sign("pol-999", 1, now.Add(time.Hour)), ".")] + "." + mac,
	} {
		_, _, err := sg.Verify(tok, now)
		assert.ErrorIs(t, err, ports.ErrInvalidLink, name)
	}
}

func TestVerifyRejectsMalformedSignedPayloads(t *testing.T) {
	sg := signer(t, secret)
	// Properly signed by the same key, but with a payload this package never issues.
	for name, token := range map[string]string{
		"missing parts": sg.SignRaw("pol-1|1"),
		"bad policy id": sg.SignRaw("pol 1|1|9999999999"),
		"bad version":   sg.SignRaw("pol-1|x|9999999999"),
		"zero version":  sg.SignRaw("pol-1|0|9999999999"),
		"bad expiry":    sg.SignRaw("pol-1|1|soon"),
	} {
		_, _, err := sg.Verify(token, now)
		assert.ErrorIs(t, err, ports.ErrInvalidLink, name)
	}
}

func TestNewSignerRequiresALongSecret(t *testing.T) {
	_, err := links.NewSigner("too short")
	assert.Error(t, err)
}
