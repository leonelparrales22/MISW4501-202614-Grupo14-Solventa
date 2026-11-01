package domain

import (
	"math"
	"time"
)

// CredentialStatus is the status of one credential (one API key).
type CredentialStatus string

const (
	// CredentialActive is the current credential of the partner.
	CredentialActive CredentialStatus = "active"
	// CredentialRetiring is a rotated credential still valid until ExpiresAt
	// (the transition period of HU-W3.1-01 CA-5).
	CredentialRetiring CredentialStatus = "retiring"
	// CredentialRevoked no longer works: its API key was removed.
	CredentialRevoked CredentialStatus = "revoked"
)

// Credential is one API key of a partner. The secret (the key value) is never
// stored: only its last characters, as a hint.
type Credential struct {
	ID        string
	PartnerID string
	// ClientID is the public identifier of the credential (the key name).
	ClientID string
	// GatewayKeyID is the id of the API key in the API Gateway.
	GatewayKeyID string
	SecretHint   string
	Status       CredentialStatus
	IssuedAt     time.Time
	IssuedBy     Actor
	// Reason is ReasonApproval or ReasonRotation.
	Reason    string
	ExpiresAt *time.Time
	RevokedAt *time.Time
}

// Reasons of a credential change, as they travel in the events.
const (
	ReasonApproval         = "approval"
	ReasonRotation         = "rotation"
	ReasonGracePeriodEnded = "grace_period_ended"
)

// SecretHintLength is how many trailing characters of the secret are kept.
const SecretHintLength = 4

// Hint returns the last characters of a secret.
func Hint(secret string) string {
	if len(secret) <= SecretHintLength {
		return secret
	}
	return secret[len(secret)-SecretHintLength:]
}

// CredentialsStatus is the summary of the credentials of a partner.
type CredentialsStatus string

const (
	CredentialsNone               CredentialsStatus = "none"
	CredentialsActive             CredentialsStatus = "active"
	CredentialsRotationInProgress CredentialsStatus = "rotation_in_progress"
	CredentialsDisabled           CredentialsStatus = "disabled"
)

// Grace period of a rotation, in hours (contract §7).
const (
	MinGracePeriodHours = 1
	MaxGracePeriodHours = 168
)

// ValidGracePeriod reports whether h is an accepted transition period.
func ValidGracePeriod(h int) bool { return h >= MinGracePeriodHours && h <= MaxGracePeriodHours }

// Usage is the consumption of a partner on one day (UTC), as the API Gateway
// counts it for its usage plan.
type Usage struct {
	Date      string
	Used      int64
	Remaining int64
	Percent   float64
}

// UsageStatus is how close a partner is to its daily quota.
type UsageStatus string

const (
	UsageNormal    UsageStatus = "normal"
	UsageNearLimit UsageStatus = "near_limit"
	UsageExceeded  UsageStatus = "exceeded"
	UsageUnknown   UsageStatus = "unknown"
	UsageNone      UsageStatus = "none"
)

// NearLimitPercent is the share of the daily quota from which a partner is
// near its limit.
const NearLimitPercent = 80.0

// NewUsage builds the usage of a day from what was used and the daily limit.
func NewUsage(date string, used int64, dailyLimit int) Usage {
	remaining := int64(dailyLimit) - used
	if remaining < 0 {
		remaining = 0
	}
	var percent float64
	if dailyLimit > 0 {
		percent = math.Round(float64(used)/float64(dailyLimit)*1000) / 10
	}
	return Usage{Date: date, Used: used, Remaining: remaining, Percent: percent}
}

// StatusOf classifies a usage; nil is unknown.
func StatusOf(u *Usage) UsageStatus {
	switch {
	case u == nil:
		return UsageUnknown
	case u.Percent >= 100:
		return UsageExceeded
	case u.Percent >= NearLimitPercent:
		return UsageNearLimit
	default:
		return UsageNormal
	}
}
