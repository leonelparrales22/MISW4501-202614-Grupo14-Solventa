// Package stages tells the insured every time their claim moves on to a
// stage they wait for (HU-M3.3-01): it follows the history of the status of
// the claims, which the database writes whoever changes the status, and for
// each change that notifies writes ClaimStageChanged to the outbox. The relay
// publishes it to the claims topic and the notifications service turns it
// into the notification and the push.
package stages

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/events"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// EventTypeClaimStageChanged is the event of a change of stage
// (docs/events/README.md).
const EventTypeClaimStageChanged = "ClaimStageChanged"

// DefaultInterval is how often the history is looked at: the notice reaches
// the outbox in seconds, well inside the 60 s of the criteria.
const DefaultInterval = 2 * time.Second

// batch is how many changes one pass looks at.
const batch = 100

// Notifier writes the notices of the changes of stage.
type Notifier struct {
	store    ports.StageNoticeStore
	interval time.Duration
}

// NewNotifier builds the notifier; a non-positive interval takes
// DefaultInterval.
func NewNotifier(store ports.StageNoticeStore, interval time.Duration) *Notifier {
	if interval <= 0 {
		interval = DefaultInterval
	}
	return &Notifier{store: store, interval: interval}
}

// Run looks at the history every interval until ctx ends. A full batch is
// followed by another pass at once.
func (n *Notifier) Run(ctx context.Context) {
	ticker := time.NewTicker(n.interval)
	defer ticker.Stop()
	for {
		for {
			looked, err := n.ProcessDue(ctx)
			if err != nil {
				slog.ErrorContext(ctx, "could not write the notices of the changes of stage; retried on the next pass", "error", err)
			}
			if err != nil || looked < batch || ctx.Err() != nil {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// ProcessDue runs one pass and returns how many changes it looked at.
func (n *Notifier) ProcessDue(ctx context.Context) (int, error) {
	return n.store.NoticePending(ctx, batch, Event)
}

// payload is the payload of ClaimStageChanged v1.
type payload struct {
	ChangeID      int64      `json:"change_id"`
	ClaimID       string     `json:"claim_id"`
	FilingNumber  string     `json:"filing_number"`
	PolicyID      string     `json:"policy_id"`
	Subject       string     `json:"subject"`
	Stage         string     `json:"stage"`
	PreviousStage string     `json:"previous_stage,omitempty"`
	ChangedAt     time.Time  `json:"changed_at"`
	AmountMinor   *int64     `json:"amount_minor,omitempty"`
	Currency      string     `json:"currency,omitempty"`
	PaidAt        *time.Time `json:"paid_at,omitempty"`
}

// Event is the ClaimStageChanged of a change, or nil when the change notifies
// nothing. The amount goes with the approval and the payment (criteria 2 and
// 3), and the date of the payment with the payment.
func Event(c domain.StageChange) (*ports.OutboxEvent, error) {
	if !c.Notifies() {
		return nil, nil
	}
	p := payload{
		ChangeID: c.ChangeID, ClaimID: c.ClaimID, FilingNumber: c.FilingNumber, PolicyID: c.PolicyID, Subject: c.Subject,
		Stage: string(c.Status), PreviousStage: string(c.PreviousStatus), ChangedAt: c.ChangedAt.UTC(),
	}
	if c.Status != domain.StatusInEvaluation && c.PayoutMinor != nil {
		p.AmountMinor, p.Currency = c.PayoutMinor, c.Currency
	}
	if c.Status == domain.StatusPaid {
		paid := c.ChangedAt.UTC()
		if c.PaidAt != nil {
			paid = c.PaidAt.UTC()
		}
		p.PaidAt = &paid
	}
	id := "claim-stage-changed-" + strconv.FormatInt(c.ChangeID, 10)
	e, err := events.New(id, EventTypeClaimStageChanged, "claims", 1, c.ChangedAt, p)
	if err != nil {
		return nil, err
	}
	body, err := e.Encode()
	if err != nil {
		return nil, err
	}
	return &ports.OutboxEvent{ID: id, Type: EventTypeClaimStageChanged, Body: body, OccurredAt: c.ChangedAt}, nil
}
