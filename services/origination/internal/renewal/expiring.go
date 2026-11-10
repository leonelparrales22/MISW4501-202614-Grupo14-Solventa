package renewal

import (
	"context"
	"errors"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// ExpiringService lists to the advisor the policies about to expire
// (HU-W1.5-01, screen S6), the entry point of the assisted renewal
// (HU-W1.5-03). It reuses the renewal window of HU-W1.5-02: the default range
// is the window and a policy can be renewed only inside it (decision D-5 a).
// Every advisor sees every policy (P-1): there is no portfolio per advisor.
type ExpiringService struct {
	query ports.ExpiringPolicyQuery
	rules domain.RenewalPolicy
	clock ports.Clock
}

// NewExpiringService builds an ExpiringService.
func NewExpiringService(query ports.ExpiringPolicyQuery, rules domain.RenewalPolicy, clock ports.Clock) (*ExpiringService, error) {
	if query == nil || clock == nil {
		return nil, errors.New("renewal: the expiring policies query and the clock are required")
	}
	if err := rules.Validate(); err != nil {
		return nil, err
	}
	return &ExpiringService{query: query, rules: rules, clock: clock}, nil
}

// List returns the page of the policies about to expire that the query asks
// for, with the defaults applied (today to today plus the renewal window, the
// first page of DefaultExpiringPageSize), and how many end within the window.
// Each row says whether the advisor can renew it now and when its window
// opens and closes. Only an advisor can list them; an invalid query returns
// domain.ErrExpiringRangeInverted or domain.ErrInvalidExpiringQuery.
func (s *ExpiringService) List(ctx context.Context, q domain.ExpiringQuery) (domain.ExpiringPage, error) {
	if err := security.RequireAnyRole(ctx, security.RoleAdvisor); err != nil {
		return domain.ExpiringPage{}, err
	}
	now := s.clock.Now().UTC()
	q = q.WithDefaults(now, s.rules)
	if err := q.Validate(); err != nil {
		return domain.ExpiringPage{}, err
	}
	start, end := q.Bounds()
	items, total, err := s.query.ListExpiring(ctx, start, end, now, q.Status, q.Offset(), q.PageSize)
	if err != nil {
		return domain.ExpiringPage{}, err
	}
	due, err := s.query.CountDueWithinWindow(ctx, now, now.AddDate(0, 0, s.rules.WindowDays))
	if err != nil {
		return domain.ExpiringPage{}, err
	}
	for i := range items {
		p := items[i].Policy
		items[i].CanRenew = items[i].Renewal == nil && s.rules.InWindow(p, now)
		items[i].WindowOpensAt, items[i].WindowClosesAt = s.rules.WindowOpensAt(p), p.CoverageEnd
	}
	if items == nil {
		items = []domain.ExpiringPolicy{}
	}
	return domain.ExpiringPage{
		Items: items, Total: total, DueWithinWindow: due,
		From: domain.CivilDayOf(q.From), To: domain.CivilDayOf(q.To), WindowDays: s.rules.WindowDays,
		Page: q.Page, PageSize: q.PageSize,
	}, nil
}
