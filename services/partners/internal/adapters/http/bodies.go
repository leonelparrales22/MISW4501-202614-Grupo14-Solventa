package httpadapter

import (
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/partners/internal/credentials"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/partners/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/partners/internal/onboarding"
)

// Bodies of docs/contratos/HU-W3.1-01-api.md.

type errorBody struct {
	Error   string           `json:"error"`
	Message string           `json:"message"`
	Fields  []fieldErrorBody `json:"fields,omitempty"`
}

type fieldErrorBody struct {
	Field string `json:"field"`
	Code  string `json:"code"`
}

type quotaBody struct {
	Period     string `json:"period"`
	DailyLimit int    `json:"daily_limit"`
	RateLimit  int    `json:"rate_limit"`
	BurstLimit int    `json:"burst_limit"`
}

type usageBody struct {
	Date      string  `json:"date"`
	Used      int64   `json:"used"`
	Remaining int64   `json:"remaining"`
	Percent   float64 `json:"percent"`
}

type actorBody struct {
	Type  string `json:"type"`
	ID    string `json:"id"`
	Label string `json:"label"`
}

type summaryBody struct {
	PartnerID         string     `json:"partner_id"`
	Code              string     `json:"code"`
	Name              string     `json:"name"`
	Type              string     `json:"type"`
	Status            string     `json:"status"`
	CredentialsStatus string     `json:"credentials_status"`
	UsageStatus       string     `json:"usage_status"`
	RegisteredAt      time.Time  `json:"registered_at"`
	ApprovedAt        *time.Time `json:"approved_at"`
	Quota             quotaBody  `json:"quota"`
	Usage             *usageBody `json:"usage"`
}

type activeCredentialBody struct {
	CredentialID string    `json:"credential_id"`
	ClientID     string    `json:"client_id"`
	SecretHint   string    `json:"secret_hint"`
	IssuedAt     time.Time `json:"issued_at"`
	IssuedBy     actorBody `json:"issued_by"`
}

type previousCredentialBody struct {
	CredentialID string     `json:"credential_id"`
	ClientID     string     `json:"client_id"`
	SecretHint   string     `json:"secret_hint"`
	IssuedAt     time.Time  `json:"issued_at"`
	ExpiresAt    *time.Time `json:"expires_at"`
}

type credentialsBody struct {
	Authentication          string                  `json:"authentication"`
	Header                  string                  `json:"header"`
	DefaultGracePeriodHours int                     `json:"default_grace_period_hours"`
	Active                  *activeCredentialBody   `json:"active"`
	Previous                *previousCredentialBody `json:"previous"`
	FirstIssuedAt           *time.Time              `json:"first_issued_at"`
	FirstIssuedBy           *actorBody              `json:"first_issued_by"`
	LastRotatedAt           *time.Time              `json:"last_rotated_at"`
	LastRotatedBy           *actorBody              `json:"last_rotated_by"`
}

type historyBody struct {
	EventID    string         `json:"event_id"`
	Type       string         `json:"type"`
	OccurredAt time.Time      `json:"occurred_at"`
	Actor      actorBody      `json:"actor"`
	Status     string         `json:"status"`
	Details    map[string]any `json:"details"`
}

type detailBody struct {
	summaryBody
	Products              []string         `json:"products"`
	TechnicalContactEmail string           `json:"technical_contact_email"`
	Environment           string           `json:"environment"`
	RegisteredBy          actorBody        `json:"registered_by"`
	Credentials           *credentialsBody `json:"credentials"`
	History               []historyBody    `json:"history"`
}

type indicatorsBody struct {
	ActivePartners          int      `json:"active_partners"`
	AverageQuotaUsedPercent *float64 `json:"average_quota_used_percent"`
	NearLimitPartners       int      `json:"near_limit_partners"`
	NearLimitNames          []string `json:"near_limit_names"`
}

type listBody struct {
	Items      []summaryBody  `json:"items"`
	Indicators indicatorsBody `json:"indicators"`
}

type secretBody struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	Header       string `json:"header"`
}

type issuedBody struct {
	Partner detailBody `json:"partner"`
	Secret  secretBody `json:"secret"`
}

type registerBody struct {
	Name                  string   `json:"name"`
	Code                  string   `json:"code"`
	Type                  string   `json:"type"`
	Products              []string `json:"products"`
	TechnicalContactEmail string   `json:"technical_contact_email"`
	Quota                 struct {
		DailyLimit int `json:"daily_limit"`
		RateLimit  int `json:"rate_limit"`
		BurstLimit int `json:"burst_limit"`
	} `json:"quota"`
}

func (b registerBody) request() domain.RegisterRequest {
	return domain.RegisterRequest{
		Name: b.Name, Code: b.Code, Type: domain.Type(b.Type), Products: b.Products,
		TechnicalContactEmail: b.TechnicalContactEmail,
		Quota:                 domain.Quota{DailyLimit: b.Quota.DailyLimit, RateLimit: b.Quota.RateLimit, BurstLimit: b.Quota.BurstLimit},
	}
}

type rotationBody struct {
	GracePeriodHours *int `json:"grace_period_hours"`
}

// apiKeyHeader is where a partner sends its secret.
const apiKeyHeader = "x-api-key"

func actorOfDomain(a domain.Actor) actorBody {
	return actorBody{Type: a.Type, ID: a.ID, Label: a.Label}
}

func secretOf(s credentials.IssuedSecret) secretBody {
	return secretBody{ClientID: s.ClientID, ClientSecret: s.ClientSecret, Header: apiKeyHeader}
}

func summaryOf(v onboarding.View) summaryBody {
	p := v.Partner
	b := summaryBody{
		PartnerID: p.ID, Code: p.Code, Name: p.Name, Type: string(p.Type), Status: string(p.Status),
		CredentialsStatus: string(v.CredentialsStatus()), UsageStatus: string(v.UsageStatus()),
		RegisteredAt: p.RegisteredAt.UTC(), ApprovedAt: utc(p.ApprovedAt),
		Quota: quotaBody{Period: "day", DailyLimit: p.Quota.DailyLimit, RateLimit: p.Quota.RateLimit, BurstLimit: p.Quota.BurstLimit},
	}
	if u := v.Usage; u != nil {
		b.Usage = &usageBody{Date: u.Date, Used: u.Used, Remaining: u.Remaining, Percent: u.Percent}
	}
	return b
}

func (h handlers) detailOf(v onboarding.View) detailBody {
	p := v.Partner
	b := detailBody{
		summaryBody: summaryOf(v), Products: p.Products, TechnicalContactEmail: p.TechnicalContactEmail,
		Environment: h.environment, RegisteredBy: actorOfDomain(p.RegisteredBy), History: []historyBody{},
	}
	if b.Products == nil {
		b.Products = []string{}
	}
	retiring := map[string]bool{}
	if p.Status != domain.StatusPendingApproval {
		cb := &credentialsBody{Authentication: "api_key", Header: apiKeyHeader, DefaultGracePeriodHours: h.rot.DefaultGracePeriodHours()}
		for _, c := range v.Credentials {
			switch c.Status {
			case domain.CredentialActive:
				cb.Active = &activeCredentialBody{CredentialID: c.ID, ClientID: c.ClientID, SecretHint: c.SecretHint,
					IssuedAt: c.IssuedAt.UTC(), IssuedBy: actorOfDomain(c.IssuedBy)}
			case domain.CredentialRetiring:
				retiring[c.ID] = true
				cb.Previous = &previousCredentialBody{CredentialID: c.ID, ClientID: c.ClientID, SecretHint: c.SecretHint,
					IssuedAt: c.IssuedAt.UTC(), ExpiresAt: utc(c.ExpiresAt)}
			}
		}
		// Credentials come newest first: the first issued is the last one
		// and the last rotation is the newest one issued by a rotation.
		for i := len(v.Credentials) - 1; i >= 0; i-- {
			c := v.Credentials[i]
			if c.Reason == domain.ReasonApproval && cb.FirstIssuedAt == nil {
				at, by := c.IssuedAt.UTC(), actorOfDomain(c.IssuedBy)
				cb.FirstIssuedAt, cb.FirstIssuedBy = &at, &by
			}
		}
		for _, c := range v.Credentials {
			if c.Reason == domain.ReasonRotation {
				at, by := c.IssuedAt.UTC(), actorOfDomain(c.IssuedBy)
				cb.LastRotatedAt, cb.LastRotatedBy = &at, &by
				break
			}
		}
		b.Credentials = cb
	}
	for _, e := range v.History {
		status := "applied"
		if e.Type == domain.HistoryCredentialsRotated {
			if prev, ok := e.Details["previous_credential_id"].(string); ok && retiring[prev] {
				status = "in_progress"
			}
		}
		details := e.Details
		if details == nil {
			details = map[string]any{}
		}
		b.History = append(b.History, historyBody{EventID: e.ID, Type: string(e.Type), OccurredAt: e.OccurredAt.UTC(),
			Actor: actorOfDomain(e.Actor), Status: status, Details: details})
	}
	return b
}
