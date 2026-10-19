package idempotency

import "errors"

// ErrEmptyKeyPart is returned by Key when the prefix or the id is empty.
var ErrEmptyKeyPart = errors.New("idempotency: the key prefix and the id are required")

// Key returns the deterministic idempotency key of the operation identified
// by id: prefix + "-" + id (origination uses Key("cobro", acceptanceID)). The
// key is never random, so every retry of the same operation carries it.
func Key(prefix, id string) (string, error) {
	if prefix == "" || id == "" {
		return "", ErrEmptyKeyPart
	}
	return prefix + "-" + id, nil
}
