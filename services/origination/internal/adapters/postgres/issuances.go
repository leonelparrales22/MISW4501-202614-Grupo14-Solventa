package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

var _ ports.IssuanceMonitor = (*ChargeStore)(nil)

// issuanceBase is every issuance with its status and step derived: each
// charge joined with the contractual issuance of its policy, and the
// acceptances that have no charge yet (still waiting for underwriting).
//
//	charge pending / in progress ......... charging
//	charge failed ........................ exception (premium_charge)
//	charge confirmed, contract failed .... exception (contract_document or notification)
//	charge confirmed, contract completed . notified
//	charge confirmed otherwise ........... active
const issuanceBase = `SELECT c.acceptance_id, c.offer_id, c.customer_id, c.holder_full_name AS customer_name, c.channel,
		CASE WHEN c.status = 'failed' THEN 'exception'
		     WHEN c.status = 'confirmed' THEN
		         CASE k.status WHEN 'completed' THEN 'notified' WHEN 'failed' THEN 'exception' ELSE 'active' END
		     ELSE 'charging' END AS status,
		CASE WHEN c.status IN ('pending', 'in_progress') THEN
		         CASE WHEN c.payment_method_kind IS NULL THEN 'awaiting_payment_method' ELSE 'premium_charge' END
		     WHEN c.status = 'failed' THEN 'premium_charge'
		     WHEN k.status = 'completed' THEN 'completed'
		     WHEN k.document IS NULL THEN 'contract_document'
		     ELSE 'notification' END AS step,
		c.accepted_at, GREATEST(c.updated_at, k.updated_at) AS updated_at, c.amount_minor, c.currency,
		CASE WHEN c.status = 'confirmed' AND k.status IS NOT NULL THEN k.attempts ELSE c.attempts END AS attempts,
		c.policy_id,
		CASE WHEN c.status = 'failed' THEN c.failure_reason
		     WHEN k.status = 'failed' THEN
		         CASE WHEN k.document IS NULL THEN 'document_generation_failed' ELSE 'notification_failed' END END AS failure_reason,
		CASE WHEN c.status = 'failed' THEN c.last_failure_at WHEN k.status = 'failed' THEN k.last_failure_at END AS failed_at,
		c.resume_count, c.last_resumed_at, c.last_resumed_by, c.activated_at, k.notified_at
	FROM charge_attempt c
	LEFT JOIN contract_issuance k ON k.policy_id = c.policy_id
	UNION ALL
	SELECT a.acceptance_id, a.offer_id, a.customer_id, COALESCE(cu.full_name, ''), a.channel, 'accepted', 'underwriting',
		a.accepted_at, COALESCE(a.updated_at, a.recorded_at), 0::bigint, ''::text, 0, NULL::text, NULL::text,
		NULL::timestamptz, 0, NULL::timestamptz, NULL::text, NULL::timestamptz, NULL::timestamptz
	FROM acceptance a
	LEFT JOIN customer cu ON cu.cognito_sub = a.customer_id
	WHERE a.status IN ('registered', 'issuance_pending')
	  AND NOT EXISTS (SELECT 1 FROM charge_attempt c WHERE c.acceptance_id = a.acceptance_id)`

const issuanceQuery = `SELECT acceptance_id, offer_id, customer_id, customer_name, channel, status, step, accepted_at, updated_at,
		amount_minor, currency, attempts, policy_id, failure_reason, failed_at, resume_count, last_resumed_at,
		last_resumed_by, notified_at
	FROM (` + issuanceBase + `) i
	WHERE ($1 = '' OR i.status = $1)
	  AND ($2::timestamptz IS NULL OR i.accepted_at >= $2)
	  AND ($3::timestamptz IS NULL OR i.accepted_at <= $3)
	  AND ($4 = '' OR i.channel = $4)
	ORDER BY i.accepted_at DESC, i.acceptance_id
	LIMIT $5`

// ListIssuances returns the issuances that match the filter, the latest
// accepted first. It never returns nil: no match is an empty list.
func (s *ChargeStore) ListIssuances(ctx context.Context, f domain.IssuanceFilter) ([]domain.Issuance, error) {
	rows, err := s.db.QueryContext(ctx, issuanceQuery,
		string(f.Status), nullTime(f.From), nullTime(f.To), string(f.Channel), f.Limit)
	if err != nil {
		return nil, fmt.Errorf("list the issuances: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []domain.Issuance{}
	for rows.Next() {
		var (
			i                                domain.Issuance
			channel, status, step            string
			updated, failedAt, lastResumedAt sql.NullTime
			notifiedAt                       sql.NullTime
			policy, reason, resumedBy        sql.NullString
		)
		if err := rows.Scan(&i.AcceptanceID, &i.OfferID, &i.CustomerID, &i.CustomerName, &channel, &status, &step,
			&i.AcceptedAt, &updated, &i.AmountMinor, &i.Currency, &i.Attempts, &policy, &reason, &failedAt,
			&i.ResumeCount, &lastResumedAt, &resumedBy, &notifiedAt); err != nil {
			return nil, fmt.Errorf("scan an issuance: %w", err)
		}
		i.Channel, i.Status, i.Step = domain.Channel(channel), domain.IssuanceStatus(status), domain.IssuanceStep(step)
		i.AcceptedAt, i.UpdatedAt = i.AcceptedAt.UTC(), updated.Time.UTC()
		i.PolicyID, i.FailureReason = policy.String, domain.IssuanceFailureReason(reason.String)
		i.FailedAt, i.LastResumedAt, i.LastResumedBy = utc(failedAt), utc(lastResumedAt), resumedBy.String
		i.NotifiedAt = utc(notifiedAt)
		out = append(out, i)
	}
	return out, rows.Err()
}

const countsQuery = `SELECT
		count(*) FILTER (WHERE status = 'accepted'),
		count(*) FILTER (WHERE status = 'charging'),
		count(*) FILTER (WHERE status = 'charging' AND attempts > 0),
		count(*) FILTER (WHERE activated_at >= $1),
		count(*) FILTER (WHERE status = 'notified' AND notified_at >= $1),
		count(*) FILTER (WHERE status = 'exception'),
		count(*) FILTER (WHERE status = 'exception' AND failed_at <= $2)
	FROM (` + issuanceBase + `) i`

// CountIssuances summarizes every issuance, whatever the filter of the list.
func (s *ChargeStore) CountIssuances(ctx context.Context, dayFrom, now time.Time) (domain.IssuanceCounts, error) {
	var c domain.IssuanceCounts
	err := s.db.QueryRowContext(ctx, countsQuery, dayFrom.UTC(), now.UTC().Add(-domain.EscalationAfter)).Scan(
		&c.Accepted, &c.Charging, &c.ChargingWithRetries, &c.ActiveToday, &c.NotifiedToday, &c.Exceptions, &c.Unattended)
	if err != nil {
		return domain.IssuanceCounts{}, fmt.Errorf("count the issuances: %w", err)
	}
	return c, nil
}

// utc keeps the zero time for NULL and converts the rest to UTC.
func utc(t sql.NullTime) time.Time {
	if !t.Valid {
		return time.Time{}
	}
	return t.Time.UTC()
}

// ListActions returns the actions taken on an acceptance, the latest first.
func (s *ChargeStore) ListActions(ctx context.Context, acceptanceID string) ([]domain.IssuanceAction, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, acceptance_id, action, actor, note, created_at
		FROM issuance_action WHERE acceptance_id = $1 ORDER BY created_at DESC, id DESC`, acceptanceID)
	if err != nil {
		return nil, fmt.Errorf("list the actions of %s: %w", acceptanceID, err)
	}
	defer func() { _ = rows.Close() }()
	out := []domain.IssuanceAction{}
	for rows.Next() {
		var (
			a      domain.IssuanceAction
			action string
		)
		if err := rows.Scan(&a.ID, &a.AcceptanceID, &action, &a.Actor, &a.Note, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan an action: %w", err)
		}
		a.Action, a.CreatedAt = domain.IssuanceActionKind(action), a.CreatedAt.UTC()
		out = append(out, a)
	}
	return out, rows.Err()
}
