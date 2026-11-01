// Package onboarding registers the commercial partners, approves them and
// shows them to the operations analysts with their quota and today's
// consumption (HU-W3.1-01). Approving a partner creates its usage plan and its
// first credential through the credentials module.
package onboarding

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/events"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/partners/internal/credentials"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/partners/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/partners/internal/ports"
)

// Bounds of the reads of today's consumption from the gateway.
const (
	usageTimeout     = 2 * time.Second
	usageConcurrency = 8
)

// View is a partner as the portal shows it.
type View struct {
	ports.PartnerRecord
	// Usage is today's consumption; nil without credentials or when the
	// gateway did not answer.
	Usage *domain.Usage
}

// CredentialsStatus summarizes the credentials of the partner.
func (v View) CredentialsStatus() domain.CredentialsStatus {
	if v.Partner.Status == domain.StatusOffboarded {
		return domain.CredentialsDisabled
	}
	status := domain.CredentialsNone
	for _, c := range v.Credentials {
		switch c.Status {
		case domain.CredentialRetiring:
			return domain.CredentialsRotationInProgress
		case domain.CredentialActive:
			status = domain.CredentialsActive
		}
	}
	return status
}

// UsageStatus classifies today's consumption.
func (v View) UsageStatus() domain.UsageStatus {
	if v.CredentialsStatus() == domain.CredentialsNone {
		return domain.UsageNone
	}
	return domain.StatusOf(v.Usage)
}

// Indicators are the cards above the list.
type Indicators struct {
	ActivePartners          int
	AverageQuotaUsedPercent *float64
	NearLimitNames          []string
}

// Approval is the result of approving a partner: its view and the secret,
// shown once.
type Approval struct {
	View   View
	Secret credentials.IssuedSecret
}

// Service is the onboarding use case.
type Service struct {
	store  ports.PartnerStore
	issuer *credentials.Issuer
	clock  ports.Clock
}

// NewService builds the service.
func NewService(store ports.PartnerStore, issuer *credentials.Issuer, clock ports.Clock) *Service {
	return &Service{store: store, issuer: issuer, clock: clock}
}

// Register records a partner pending approval, without credentials.
func (s *Service) Register(ctx context.Context, req domain.RegisterRequest, actor domain.Actor) (View, error) {
	req = req.Normalize()
	if err := req.Validate(); err != nil {
		return View{}, err
	}
	gen := s.issuer.Generator()
	id, err := gen.ID("ptn-")
	if err != nil {
		return View{}, err
	}
	entryID, err := gen.ID("pev-")
	if err != nil {
		return View{}, err
	}
	now := s.clock.Now().UTC()
	p := domain.Partner{
		ID: id, Code: req.Code, Name: req.Name, Type: req.Type, Products: req.Products,
		TechnicalContactEmail: req.TechnicalContactEmail, Status: domain.StatusPendingApproval, Quota: req.Quota,
		RegisteredAt: now, RegisteredBy: actor,
	}
	entry := domain.HistoryEntry{ID: entryID, PartnerID: id, Type: domain.HistoryRegistered, OccurredAt: now, Actor: actor,
		Details: map[string]any{}}
	if err := s.store.Create(ctx, p, entry); err != nil {
		return View{}, err
	}
	slog.InfoContext(ctx, "partner registered", "partner_id", id, "code", p.Code)
	return s.Get(ctx, id)
}

// Approve creates the usage plan of a pending partner and its first
// credential, and records everything in one transaction (CA-1, CA-3). If
// anything fails the gateway is left as it was and nothing is recorded.
func (s *Service) Approve(ctx context.Context, partnerID string, actor domain.Actor) (Approval, error) {
	var (
		secret  credentials.IssuedSecret
		planID  string
		created domain.Credential
	)
	err := s.store.Mutate(ctx, partnerID, func(rec ports.PartnerRecord) (ports.Change, error) {
		p := rec.Partner
		if p.Status != domain.StatusPendingApproval {
			return ports.Change{}, domain.ErrNotPending
		}
		now := s.clock.Now().UTC()
		plan, err := s.issuer.CreatePlan(ctx, p)
		if err != nil {
			return ports.Change{}, err
		}
		planID = plan
		c, sec, err := s.issuer.Issue(ctx, p, plan, actor, domain.ReasonApproval, now)
		if err != nil {
			return ports.Change{}, err
		}
		created, secret = c, sec
		approved := p
		approved.Status, approved.UsagePlanID, approved.ApprovedAt = domain.StatusActive, plan, &now
		entryID, err := s.issuer.Generator().ID("pev-")
		if err != nil {
			return ports.Change{}, err
		}
		ev, err := domain.CredentialEvent(domain.EventCredentialsIssued, approved, c, actor, now, nil, 0)
		if err != nil {
			return ports.Change{}, err
		}
		return ports.Change{
			Partner:        &approved,
			NewCredentials: []domain.Credential{c},
			History: []domain.HistoryEntry{{
				ID: entryID, PartnerID: p.ID, Type: domain.HistoryCredentialsIssued, OccurredAt: now, Actor: actor,
				Details: map[string]any{
					"client_id": c.ClientID, "daily_limit": p.Quota.DailyLimit, "rate_limit": p.Quota.RateLimit,
					"burst_limit": p.Quota.BurstLimit,
				},
			}},
			Events: []events.Envelope{ev},
		}, nil
	})
	if err != nil {
		if planID != "" {
			s.issuer.Discard(ctx, planID, created.GatewayKeyID)
		}
		return Approval{}, err
	}
	slog.InfoContext(ctx, "partner approved and credentials issued", "partner_id", partnerID, "credential_id", created.ID)
	v, err := s.Get(ctx, partnerID)
	if err != nil {
		return Approval{}, err
	}
	return Approval{View: v, Secret: secret}, nil
}

// Get returns a partner with its credentials, history and today's usage.
func (s *Service) Get(ctx context.Context, id string) (View, error) {
	if id == "" || len(id) > domain.MaxPartnerIDLen {
		return View{}, domain.ErrPartnerNotFound
	}
	rec, err := s.store.Get(ctx, id)
	if err != nil {
		return View{}, err
	}
	views := []View{{PartnerRecord: rec}}
	s.fillUsage(ctx, views)
	return views[0], nil
}

// List returns every partner (active first, then by name) with today's
// usage, and the indicators.
func (s *Service) List(ctx context.Context) ([]View, Indicators, error) {
	recs, err := s.store.List(ctx)
	if err != nil {
		return nil, Indicators{}, err
	}
	views := make([]View, len(recs))
	for i, r := range recs {
		views[i] = View{PartnerRecord: r}
	}
	sort.SliceStable(views, func(i, j int) bool {
		ai, aj := views[i].Partner.Status == domain.StatusActive, views[j].Partner.Status == domain.StatusActive
		if ai != aj {
			return ai
		}
		return views[i].Partner.Name < views[j].Partner.Name
	})
	s.fillUsage(ctx, views)
	return views, indicators(views), nil
}

func indicators(views []View) Indicators {
	var (
		ind   Indicators
		sum   float64
		count int
	)
	ind.NearLimitNames = []string{}
	for _, v := range views {
		if v.Partner.Status != domain.StatusActive {
			continue
		}
		ind.ActivePartners++
		if v.Usage != nil {
			sum += v.Usage.Percent
			count++
		}
		if st := v.UsageStatus(); st == domain.UsageNearLimit || st == domain.UsageExceeded {
			ind.NearLimitNames = append(ind.NearLimitNames, v.Partner.Name)
		}
	}
	if count > 0 {
		avg := math.Round(sum/float64(count)*10) / 10
		ind.AverageQuotaUsedPercent = &avg
	}
	return ind
}

// fillUsage reads today's consumption of each partner with credentials from
// the gateway, in parallel and within usageTimeout. A partner whose usage
// cannot be read keeps a nil usage; the rest of the answer is unaffected.
func (s *Service) fillUsage(ctx context.Context, views []View) {
	gw := s.issuer.Gateway()
	if gw == nil {
		return
	}
	day := s.clock.Now().UTC().Format(time.DateOnly)
	uctx, cancel := context.WithTimeout(ctx, usageTimeout)
	defer cancel()
	sem := make(chan struct{}, usageConcurrency)
	var wg sync.WaitGroup
	for i := range views {
		v := &views[i]
		if v.Partner.UsagePlanID == "" || v.CredentialsStatus() == domain.CredentialsNone {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			used, err := gw.UsedOn(uctx, v.Partner.UsagePlanID, day)
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					slog.WarnContext(ctx, "could not read the usage of a partner", "partner_id", v.Partner.ID, "error", err)
				}
				return
			}
			// Every key of the plan is this partner's (the active one and the
			// one in transition), so the day's consumption is their sum.
			var total int64
			for _, n := range used {
				total += n
			}
			u := domain.NewUsage(day, total, v.Partner.Quota.DailyLimit)
			v.Usage = &u
		}()
	}
	wg.Wait()
}
