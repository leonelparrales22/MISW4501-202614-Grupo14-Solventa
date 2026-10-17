// Package links issues the expiring, tamper-proof links the issuance
// notification carries to the contractual document (HU-W1.3-03).
package links

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// MinSecretLength is the shortest accepted signing secret, in bytes.
const MinSecretLength = 32

// Signer implements ports.LinkSigner with HMAC-SHA256. The token is
// base64url(policy|version|expiry).base64url(mac): it proves who issued it and
// that it was not altered, and expires by itself.
type Signer struct {
	secret []byte
}

var _ ports.LinkSigner = (*Signer)(nil)

// NewSigner builds a Signer. The secret must have at least MinSecretLength
// bytes and comes from the environment or Secrets Manager, never from code.
func NewSigner(secret string) (*Signer, error) {
	if len(secret) < MinSecretLength {
		return nil, errors.New("links: the signing secret must have at least 32 bytes")
	}
	return &Signer{secret: []byte(secret)}, nil
}

// Sign returns the token for the version of the policy document, valid until
// expiresAt.
func (s *Signer) Sign(policyID string, version int, expiresAt time.Time) string {
	payload := policyID + "|" + strconv.Itoa(version) + "|" + strconv.FormatInt(expiresAt.Unix(), 10)
	return b64(payload) + "." + b64(string(s.mac(payload)))
}

// Verify returns the policy and version a token was issued for, or
// ports.ErrInvalidLink when it is malformed, altered or expired.
func (s *Signer) Verify(token string, now time.Time) (string, int, error) {
	encPayload, encMAC, ok := strings.Cut(token, ".")
	if !ok {
		return "", 0, ports.ErrInvalidLink
	}
	payload, err := base64.RawURLEncoding.DecodeString(encPayload)
	if err != nil {
		return "", 0, ports.ErrInvalidLink
	}
	got, err := base64.RawURLEncoding.DecodeString(encMAC)
	if err != nil || !hmac.Equal(got, s.mac(string(payload))) {
		return "", 0, ports.ErrInvalidLink
	}
	parts := strings.Split(string(payload), "|")
	if len(parts) != 3 || !domain.ValidPolicyID(parts[0]) {
		return "", 0, ports.ErrInvalidLink
	}
	version, err := strconv.Atoi(parts[1])
	if err != nil || version < 1 {
		return "", 0, ports.ErrInvalidLink
	}
	expiry, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || !now.Before(time.Unix(expiry, 0)) {
		return "", 0, ports.ErrInvalidLink
	}
	return parts[0], version, nil
}

func (s *Signer) mac(payload string) []byte {
	m := hmac.New(sha256.New, s.secret)
	_, _ = m.Write([]byte(payload))
	return m.Sum(nil)
}

func b64(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
