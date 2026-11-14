package domain

import (
	"errors"
	"fmt"
	"strconv"
	"time"
)

// HU-M3.3-01: the notification of every change of stage of a claim the insured
// follows.

// KindClaimStageChanged is the notification of a change of stage of a claim.
const KindClaimStageChanged = "claim_stage_changed"

// GroupClaimStages is the thread the device stacks these pushes in, apart
// from the automatic payments.
const GroupClaimStages = "claim_stages"

// The stages the insured is told about.
const (
	StageInEvaluation = "in_evaluation"
	StageApproved     = "approved"
	StagePaid         = "paid"
)

// ErrInvalidStageChange is returned for a change of stage that misses data the
// notification must carry.
var ErrInvalidStageChange = errors.New("invalid claim stage change")

// ClaimStageChange is a change of stage of a claim, as the claims service
// reports it (event ClaimStageChanged).
type ClaimStageChange struct {
	// ChangeID identifies the change in the history of the claims.
	ChangeID int64
	// ClaimID is the id the app opens the timeline of the claim with;
	// FilingNumber, the number the insured sees (SIN-…).
	ClaimID      string
	FilingNumber string
	PolicyID     string
	Subject      string
	Stage        string
	// ChangedAt is when the claim reached the stage: the 60 s of the criteria
	// count from here, and the notification shows it (criterion 5).
	ChangedAt time.Time
	// AmountMinor and Currency are the amount approved or paid (criteria 2
	// and 3); nil for the evaluation.
	AmountMinor *int64
	Currency    string
	// PaidAt is the date of the disbursement (criterion 3).
	PaidAt *time.Time
}

// Validate reports why the change cannot become a notification.
func (c ClaimStageChange) Validate() error {
	switch {
	case c.ChangeID <= 0:
		return fmt.Errorf("%w: change_id is required", ErrInvalidStageChange)
	case !validID(c.ClaimID) || !validID(c.Subject) || c.FilingNumber == "":
		return fmt.Errorf("%w: claim_id, filing_number and subject are required", ErrInvalidStageChange)
	case c.Stage != StageInEvaluation && c.Stage != StageApproved && c.Stage != StagePaid:
		return fmt.Errorf("%w: unknown stage %q", ErrInvalidStageChange, c.Stage)
	case c.ChangedAt.IsZero():
		return fmt.Errorf("%w: changed_at is required", ErrInvalidStageChange)
	case c.AmountMinor != nil && (*c.AmountMinor <= 0 || len(c.Currency) != 3):
		return fmt.Errorf("%w: an amount must be positive and have a three-letter currency", ErrInvalidStageChange)
	}
	return nil
}

// StageNotificationIDFor is the id of the notification of a change: one per
// change, so a redelivered event finds the one it already created.
func StageNotificationIDFor(changeID int64) string {
	return "ntf-stage-" + strconv.FormatInt(changeID, 10)
}

// NewStageNotification builds the notification of a change of stage recorded
// at now.
func NewStageNotification(c ClaimStageChange, now time.Time) Notification {
	change := c
	return Notification{
		ID: StageNotificationIDFor(c.ChangeID), Subject: c.Subject, Kind: KindClaimStageChanged,
		OccurredAt: c.ChangedAt.UTC(), CreatedAt: now.UTC(), Stage: &change,
	}
}
