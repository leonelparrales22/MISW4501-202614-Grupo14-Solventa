package triage

import (
	"context"
	"log/slog"
	"time"
)

// Default rhythm of the sweep (decision D3): a claim that enters evaluation
// waits at most one interval, well under the 60 seconds of criterion 1.
const (
	DefaultInterval = 10 * time.Second
	DefaultBatch    = 200
)

// Worker sweeps the claims waiting for a calculation at a fixed interval. Each
// task of the service runs one; two tasks never record the same calculation
// twice (see ports.ErrTriageConflict).
type Worker struct {
	svc      *Service
	interval time.Duration
	batch    int
}

// NewWorker builds the worker. A zero interval or batch takes the default.
func NewWorker(svc *Service, interval time.Duration, batch int) *Worker {
	if interval <= 0 {
		interval = DefaultInterval
	}
	if batch <= 0 {
		batch = DefaultBatch
	}
	return &Worker{svc: svc, interval: interval, batch: batch}
}

// Run sweeps once right away and then at every interval, until ctx ends. A
// sweep that fails is logged and the next one tries again.
func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		if _, err := w.svc.Sweep(ctx, w.batch); err != nil && ctx.Err() == nil {
			slog.ErrorContext(ctx, "the sweep of the triage failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
