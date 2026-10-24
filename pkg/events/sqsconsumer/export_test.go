package sqsconsumer

import (
	"context"
	"time"
)

// SetSleep replaces the wait between failed receives (tests only).
func (c *Consumer) SetSleep(f func(ctx context.Context, d time.Duration) error) { c.sleep = f }
