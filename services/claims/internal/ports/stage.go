package ports

import (
	"context"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
)

// OutboxEvent is an encoded domain event to write to the outbox.
type OutboxEvent struct {
	ID         string
	Type       string
	Body       []byte
	OccurredAt time.Time
}

// StageNoticeStore follows the history of the status of the claims for the
// notice of each change of stage (HU-M3.3-01).
type StageNoticeStore interface {
	// NoticePending hands up to limit changes not looked at yet, oldest
	// first, to toEvent, and in one transaction marks them looked at and
	// writes to the outbox the event of each one toEvent returns (nil: the
	// change notifies nothing). Several tasks may call it at once: a change
	// is handed to one of them. It returns how many changes it looked at.
	NoticePending(ctx context.Context, limit int, toEvent func(domain.StageChange) (*OutboxEvent, error)) (int, error)
}
