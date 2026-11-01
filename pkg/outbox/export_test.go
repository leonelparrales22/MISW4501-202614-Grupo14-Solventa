package outbox

import (
	"context"
	"time"
)

// SetSleep replaces the wait between passes (tests only).
func (r *Relay) SetSleep(f func(ctx context.Context, d time.Duration) error) { r.sleep = f }
