package links

import "encoding/base64"

// SignRaw signs an arbitrary payload, to test how Verify treats payloads that
// Sign never produces.
func (s *Signer) SignRaw(payload string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." +
		base64.RawURLEncoding.EncodeToString(s.mac(payload))
}
