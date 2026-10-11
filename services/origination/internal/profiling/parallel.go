package profiling

import (
	"context"
	"errors"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// Default latency limits of the parallel profiling (HA-L01 CA-2, EXP-L01).
const (
	// DefaultSourceBudget is the latency budget of each external source.
	DefaultSourceBudget = 120 * time.Millisecond
	// DefaultProfilingDeadline is the total cut over the whole fan-out.
	DefaultProfilingDeadline = 700 * time.Millisecond
)

// NamedSource is one external source of the parallel profiling.
type NamedSource struct {
	Name   string
	Source ports.RiskProfileSource
}

// CombineFunc builds one risk profile from the signals that arrived, given in
// the priority order of the sources. arrived is never empty.
type CombineFunc func(customerID string, arrived []domain.RiskProfile) domain.RiskProfile

// ParallelConfig configures a ParallelSource.
type ParallelConfig struct {
	// Budget is the latency budget of each source; a slower source is canceled.
	// Zero uses DefaultSourceBudget.
	Budget time.Duration
	// Deadline is the total cut over the fan-out. Zero uses DefaultProfilingDeadline.
	Deadline time.Duration
	// Required names the source without which no profile is calculated online
	// (decision D-2: Open Finance when OPEN_FINANCE_REQUIRED=true). When it
	// misses its budget, fails or is isolated, the caller degrades. The other
	// calls are not canceled early: each one ends within its own budget, so
	// its circuit breaker records only its real result and its outcome is
	// reported. Empty means the sources are equivalent: any signal that
	// arrives is enough. It must be the name of one of the sources.
	Required string
	// Combine builds the profile from the signals that arrived. Nil uses
	// CombineProvisional.
	Combine CombineFunc
}

// CombineProvisional is the provisional rule of HA-L01 (decision D-4): the
// average of the scores that arrived, with the category and calculation date
// of the highest-priority source present (Open Finance whenever it arrived).
// It is isolated so HU-W1.2-02 can replace it with the actuarial rule.
func CombineProvisional(customerID string, arrived []domain.RiskProfile) domain.RiskProfile {
	var sum float64
	for _, p := range arrived {
		sum += p.Score
	}
	first := arrived[0]
	return domain.RiskProfile{
		CustomerID:   customerID,
		Score:        sum / float64(len(arrived)),
		Category:     first.Category,
		CalculatedAt: first.CalculatedAt,
	}
}

// ParallelSource implements ports.RiskProfileSource by consulting several
// external sources at the same time (scatter-gather). Each source has its own
// latency budget inside a total deadline; a source that misses it is
// canceled and the profile is calculated with the signals that arrived,
// marked as partial. When no profile can be calculated it returns a
// *ports.SourcesUnavailableError, and the Resolver degrades to the cached or
// base profile (HA-D01).
type ParallelSource struct {
	sources []NamedSource
	cfg     ParallelConfig
}

// NewParallelSource builds the fan-out over sources, given in priority order
// (Open Finance, Open Data, KYC).
func NewParallelSource(sources []NamedSource, cfg ParallelConfig) *ParallelSource {
	if cfg.Budget <= 0 {
		cfg.Budget = DefaultSourceBudget
	}
	if cfg.Deadline <= 0 {
		cfg.Deadline = DefaultProfilingDeadline
	}
	if cfg.Combine == nil {
		cfg.Combine = CombineProvisional
	}
	return &ParallelSource{sources: sources, cfg: cfg}
}

type sourceResult struct {
	index   int
	profile domain.RiskProfile
	outcome domain.SourceOutcome
}

// FetchProfile consults every source concurrently and returns as soon as all
// of them answered, each one within its own budget, or the deadline expired.
// A failed required source does not cut the others short: the answer is
// never later than the common budget, and no healthy source is canceled
// (and counted as a failure by its circuit breaker) because another one
// failed. The calls still in flight are canceled on return; their goroutines
// finish on their own and never block, because the results channel has room
// for all.
func (p *ParallelSource) FetchProfile(ctx context.Context, customerID string) (domain.RiskProfile, error) {
	fanCtx, cancel := context.WithTimeout(ctx, p.cfg.Deadline)
	defer cancel()

	start := time.Now()
	results := make(chan sourceResult, len(p.sources))
	for i, s := range p.sources {
		go func(i int, s NamedSource) {
			sourceCtx, cancelSource := context.WithTimeout(fanCtx, p.cfg.Budget)
			defer cancelSource()
			profile, err := s.Source.FetchProfile(sourceCtx, customerID)
			results <- sourceResult{
				index:   i,
				profile: profile,
				outcome: domain.SourceOutcome{Source: s.Name, Status: classify(sourceCtx, err), Latency: time.Since(start)},
			}
		}(i, s)
	}

	outcomes := make([]domain.SourceOutcome, len(p.sources))
	answered := make([]bool, len(p.sources))
	signals := make([]domain.RiskProfile, len(p.sources))
	p.collect(fanCtx, results, outcomes, answered, signals)

	// A source still in flight did not answer in the time it was given.
	elapsed := time.Since(start)
	arrived := make([]domain.RiskProfile, 0, len(p.sources))
	requiredArrived := p.cfg.Required == ""
	for i, s := range p.sources {
		if !answered[i] {
			outcomes[i] = domain.SourceOutcome{Source: s.Name, Status: domain.SourceStatusTimeout, Latency: elapsed}
			continue
		}
		if outcomes[i].Status == domain.SourceStatusOK {
			arrived = append(arrived, signals[i])
			if s.Name == p.cfg.Required {
				requiredArrived = true
			}
		}
	}
	if len(arrived) == 0 || !requiredArrived {
		return domain.RiskProfile{}, &ports.SourcesUnavailableError{Outcomes: outcomes}
	}

	profile := p.cfg.Combine(customerID, arrived)
	profile.Partial = len(arrived) < len(p.sources)
	profile.Sources = outcomes
	return profile, nil
}

// collect gathers results until every source answered or the fan-out context
// ended.
func (p *ParallelSource) collect(ctx context.Context, results <-chan sourceResult, outcomes []domain.SourceOutcome, answered []bool, signals []domain.RiskProfile) {
	for pending := len(p.sources); pending > 0; pending-- {
		select {
		case r := <-results:
			answered[r.index] = true
			outcomes[r.index] = r.outcome
			if r.outcome.Status == domain.SourceStatusOK {
				signals[r.index] = r.profile
			}
		case <-ctx.Done():
			return
		}
	}
}

// classify maps the result of one source call to its reported status.
func classify(ctx context.Context, err error) domain.SourceStatus {
	switch {
	case err == nil:
		return domain.SourceStatusOK
	case errors.Is(err, ports.ErrCircuitOpen):
		return domain.SourceStatusCircuitOpen
	case ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded):
		return domain.SourceStatusTimeout
	default:
		return domain.SourceStatusError
	}
}
