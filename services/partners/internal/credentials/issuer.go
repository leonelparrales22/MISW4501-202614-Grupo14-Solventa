// Package credentials generates, rotates and revokes the credentials of the
// partners (HU-W3.1-01). A credential is an API key of the API Gateway in the
// usage plan of its partner, so the gateway applies the quota and isolates
// each partner from the others. The secret is generated here, handed to the
// gateway and returned once to the caller; it is never stored.
package credentials

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/partners/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/partners/internal/ports"
)

// secretBytes is the entropy of a secret: 32 bytes, 43 characters in
// base64url (the API Gateway accepts 20 to 128 characters).
const secretBytes = 32

// Generator makes the identifiers and the secrets.
type Generator interface {
	ID(prefix string) (string, error)
	ClientID() (string, error)
	Secret() (string, error)
}

// RandomGenerator draws everything from crypto/rand.
type RandomGenerator struct{}

// ID returns prefix + 32 hexadecimal characters.
func (RandomGenerator) ID(prefix string) (string, error) {
	h, err := randomHex(16)
	return prefix + h, err
}

// ClientID returns "sol_" + 26 hexadecimal characters.
func (RandomGenerator) ClientID() (string, error) {
	h, err := randomHex(13)
	return "sol_" + h, err
}

// Secret returns 43 base64url characters.
func (RandomGenerator) Secret() (string, error) {
	b := make([]byte, secretBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate a secret: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate an identifier: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// IssuedSecret is what the caller sees once.
type IssuedSecret struct {
	ClientID     string
	ClientSecret string
}

// Issuer creates API keys in the usage plan of a partner.
type Issuer struct {
	gateway     ports.Gateway
	gen         Generator
	environment string
}

// NewIssuer builds an issuer. A nil gateway makes every issue fail with
// domain.ErrGatewayUnavailable (the gateway is not configured).
func NewIssuer(gateway ports.Gateway, gen Generator, environment string) *Issuer {
	if gen == nil {
		gen = RandomGenerator{}
	}
	return &Issuer{gateway: gateway, gen: gen, environment: environment}
}

// Available reports whether there is a gateway to issue in.
func (i *Issuer) Available() bool { return i.gateway != nil }

// Gateway returns the gateway (nil when not configured).
func (i *Issuer) Gateway() ports.Gateway { return i.gateway }

// Generator returns the generator of identifiers.
func (i *Issuer) Generator() Generator { return i.gen }

// CreatePlan creates the usage plan of a partner.
func (i *Issuer) CreatePlan(ctx context.Context, p domain.Partner) (string, error) {
	if i.gateway == nil {
		return "", fmt.Errorf("%w: not configured", domain.ErrGatewayUnavailable)
	}
	return i.gateway.CreateUsagePlan(ctx, ports.UsagePlanSpec{
		Name:        fmt.Sprintf("%s-partner-%s", i.environment, p.Code),
		Description: fmt.Sprintf("Quota and isolation of partner %s (%s), HU-W3.1-01", p.Code, p.ID),
		Quota:       p.Quota,
	})
}

// Issue creates a credential (API key) in planID and attaches it. On failure
// it removes what it created. The returned credential is not stored yet.
func (i *Issuer) Issue(ctx context.Context, p domain.Partner, planID string, actor domain.Actor, reason string,
	at time.Time) (domain.Credential, IssuedSecret, error) {
	if i.gateway == nil {
		return domain.Credential{}, IssuedSecret{}, fmt.Errorf("%w: not configured", domain.ErrGatewayUnavailable)
	}
	clientID, err := i.gen.ClientID()
	if err != nil {
		return domain.Credential{}, IssuedSecret{}, err
	}
	secret, err := i.gen.Secret()
	if err != nil {
		return domain.Credential{}, IssuedSecret{}, err
	}
	credentialID, err := i.gen.ID("crd-")
	if err != nil {
		return domain.Credential{}, IssuedSecret{}, err
	}
	keyID, err := i.gateway.CreateAPIKey(ctx, ports.APIKeySpec{
		Name: clientID, Value: secret,
		Description: fmt.Sprintf("Credential of partner %s (%s), HU-W3.1-01", p.Code, p.ID),
	})
	if err != nil {
		return domain.Credential{}, IssuedSecret{}, err
	}
	if err := i.gateway.AttachKey(ctx, planID, keyID); err != nil {
		i.Discard(ctx, "", keyID)
		return domain.Credential{}, IssuedSecret{}, err
	}
	c := domain.Credential{
		ID: credentialID, PartnerID: p.ID, ClientID: clientID, GatewayKeyID: keyID,
		SecretHint: domain.Hint(secret), Status: domain.CredentialActive, IssuedAt: at.UTC(), IssuedBy: actor, Reason: reason,
	}
	return c, IssuedSecret{ClientID: clientID, ClientSecret: secret}, nil
}

// Discard removes, best effort, an API key and a usage plan that were
// created but could not be recorded. Empty ids are skipped. It uses a fresh
// context so a cancelled request still cleans up.
func (i *Issuer) Discard(ctx context.Context, planID, keyID string) {
	if i.gateway == nil {
		return
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if keyID != "" {
		if planID != "" {
			if err := i.gateway.DetachKey(cctx, planID, keyID); err != nil {
				slog.WarnContext(ctx, "could not detach an unrecorded api key", "key_id", keyID, "error", err)
			}
		}
		if err := i.gateway.DeleteAPIKey(cctx, keyID); err != nil {
			slog.WarnContext(ctx, "could not delete an unrecorded api key", "key_id", keyID, "error", err)
		}
	}
	if planID != "" {
		if err := i.gateway.DeleteUsagePlan(cctx, planID); err != nil {
			slog.WarnContext(ctx, "could not delete an unrecorded usage plan", "plan_id", planID, "error", err)
		}
	}
}

// Retire removes a credential from the gateway: detach from the plan and
// delete the key. Missing resources are not an error.
func (i *Issuer) Retire(ctx context.Context, planID, keyID string) error {
	if i.gateway == nil {
		return fmt.Errorf("%w: not configured", domain.ErrGatewayUnavailable)
	}
	if err := i.gateway.DetachKey(ctx, planID, keyID); err != nil {
		return err
	}
	return i.gateway.DeleteAPIKey(ctx, keyID)
}

// IsGatewayError reports whether err comes from the gateway.
func IsGatewayError(err error) bool { return errors.Is(err, domain.ErrGatewayUnavailable) }
