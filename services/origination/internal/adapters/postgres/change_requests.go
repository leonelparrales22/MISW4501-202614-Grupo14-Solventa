package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// ChangeRequestStore is the ports.ChangeRequestRepository over the
// change_request and change_request_counter tables (HU-W1.4-01, migration
// 0017). A request is numbered and stored in one transaction; the unique
// partial index change_request_one_open decides between two requests of the
// same policy and type filed at once, without locks (pattern of EC-D02).
type ChangeRequestStore struct {
	db *sql.DB
}

var _ ports.ChangeRequestRepository = (*ChangeRequestStore)(nil)

// NewChangeRequestStore builds a ChangeRequestStore over db (already migrated).
func NewChangeRequestStore(db *sql.DB) *ChangeRequestStore { return &ChangeRequestStore{db: db} }

const changeRequestColumns = `request_id, policy_id, customer_id, change_type, previous, requested, channel,
	requested_by_sub, requested_by_email, requested_by_name, filed_at, status, status_changed_at, decision_reason`

// changeRequestOpen is the condition of an open request (adjustment A-2); it
// is the predicate of the index change_request_one_open.
const changeRequestOpen = `(status IN ('filed', 'in_review') OR (status = 'not_applied' AND decision_reason IS NULL))`

// changeRequestRejected is a request the insured sees as rejected
// (adjustment A-1): rejected, or not applied and returned with a reason.
const changeRequestRejected = `(status = 'rejected' OR (status = 'not_applied' AND decision_reason IS NOT NULL))`

// changeRequestFilter adds to the requests of the customer $1 the status the
// insured sees ($2, empty for every one), the policy ($3, empty for all of
// them) and the first instant of filing ($4, NULL for no bound).
const changeRequestFilter = ` FROM change_request
	WHERE customer_id = $1
	  AND ($2::text = ''
	       OR ($2::text = 'filed' AND status = 'filed')
	       OR ($2::text = 'in_review' AND (status = 'in_review' OR (status = 'not_applied' AND decision_reason IS NULL)))
	       OR ($2::text = 'applied' AND status = 'applied')
	       OR ($2::text = 'rejected' AND ` + changeRequestRejected + `))
	  AND ($3::text = '' OR policy_id = $3)
	  AND ($4::timestamptz IS NULL OR filed_at >= $4)`

// beneficiaryJSON is a beneficiary as previous and requested keep it.
type beneficiaryJSON struct {
	FullName         string `json:"full_name"`
	Relationship     string `json:"relationship"`
	DocumentType     string `json:"document_type"`
	DocumentNumber   string `json:"document_number"`
	ShareBasisPoints int    `json:"share_basis_points"`
}

type onerousJSON struct {
	PartnerID string `json:"partner_id"`
	Name      string `json:"name"`
	Basis     string `json:"basis"`
}

type contactJSON struct {
	Address *string `json:"address"`
	Email   *string `json:"email"`
	Phone   *string `json:"phone"`
}

// changeContentJSON is one side of a change; only the members of its type
// are written.
type changeContentJSON struct {
	Beneficiaries      *[]beneficiaryJSON `json:"beneficiaries,omitempty"`
	Onerous            *onerousJSON       `json:"onerous_beneficiary,omitempty"`
	InsuredAmountMinor *int64             `json:"insured_amount_minor,omitempty"`
	Currency           string             `json:"currency,omitempty"`
	PersonalData       *contactJSON       `json:"personal_data,omitempty"`
}

func encodeChangeContent(t domain.ChangeType, c domain.ChangeContent) ([]byte, error) {
	var out changeContentJSON
	switch t {
	case domain.ChangeBeneficiaries:
		list := make([]beneficiaryJSON, 0, len(c.Beneficiaries))
		for _, b := range c.Beneficiaries {
			list = append(list, beneficiaryJSON{FullName: b.FullName, Relationship: string(b.Relationship),
				DocumentType: string(b.DocumentType), DocumentNumber: b.DocumentNumber, ShareBasisPoints: b.ShareBasisPoints})
		}
		out.Beneficiaries = &list
		if c.Onerous != nil {
			out.Onerous = &onerousJSON{PartnerID: c.Onerous.PartnerID, Name: c.Onerous.Name, Basis: c.Onerous.Basis}
		}
	case domain.ChangeInsuredAmount:
		minor := domain.ToMinorUnits(c.InsuredAmount)
		out.InsuredAmountMinor, out.Currency = &minor, c.Currency
	case domain.ChangePersonalData:
		out.PersonalData = &contactJSON{Address: c.Contact.Address, Email: c.Contact.Email, Phone: c.Contact.Phone}
	default:
		return nil, fmt.Errorf("encode a change of unknown type %q", t)
	}
	return json.Marshal(out)
}

func decodeChangeContent(t domain.ChangeType, raw []byte) (domain.ChangeContent, error) {
	var in changeContentJSON
	if err := json.Unmarshal(raw, &in); err != nil {
		return domain.ChangeContent{}, err
	}
	var out domain.ChangeContent
	switch t {
	case domain.ChangeBeneficiaries:
		out.Beneficiaries = []domain.Beneficiary{}
		if in.Beneficiaries != nil {
			for _, b := range *in.Beneficiaries {
				out.Beneficiaries = append(out.Beneficiaries, domain.Beneficiary{FullName: b.FullName,
					Relationship: domain.Relationship(b.Relationship), DocumentType: domain.DocumentType(b.DocumentType),
					DocumentNumber: b.DocumentNumber, ShareBasisPoints: b.ShareBasisPoints})
			}
		}
		if in.Onerous != nil {
			out.Onerous = &domain.OnerousBeneficiary{PartnerID: in.Onerous.PartnerID, Name: in.Onerous.Name, Basis: in.Onerous.Basis}
		}
	case domain.ChangeInsuredAmount:
		if in.InsuredAmountMinor != nil {
			out.InsuredAmount = domain.FromMinorUnits(*in.InsuredAmountMinor)
		}
		out.Currency = in.Currency
	case domain.ChangePersonalData:
		if in.PersonalData != nil {
			out.Contact = domain.ContactData{Address: in.PersonalData.Address, Email: in.PersonalData.Email, Phone: in.PersonalData.Phone}
		}
	}
	return out, nil
}

func scanChangeRequest(s scanner) (domain.ChangeRequest, error) {
	var (
		r                       domain.ChangeRequest
		changeType, channel, st string
		previous, requested     []byte
		reason                  sql.NullString
	)
	if err := s.Scan(&r.ID, &r.PolicyID, &r.CustomerID, &changeType, &previous, &requested, &channel,
		&r.RequestedBy.Subject, &r.RequestedBy.Email, &r.RequestedBy.Name, &r.FiledAt, &st, &r.StatusChangedAt, &reason); err != nil {
		return domain.ChangeRequest{}, err
	}
	r.Type, r.Channel, r.Status, r.DecisionReason = domain.ChangeType(changeType), domain.Channel(channel),
		domain.ChangeRequestStatus(st), reason.String
	var err error
	if r.Previous, err = decodeChangeContent(r.Type, previous); err != nil {
		return domain.ChangeRequest{}, fmt.Errorf("decode the previous value of %s: %w", r.ID, err)
	}
	if r.Requested, err = decodeChangeContent(r.Type, requested); err != nil {
		return domain.ChangeRequest{}, fmt.Errorf("decode the requested value of %s: %w", r.ID, err)
	}
	r.FiledAt, r.StatusChangedAt = r.FiledAt.UTC(), r.StatusChangedAt.UTC()
	return r, nil
}

// Create numbers the request and stores it in one transaction. When the
// policy already has an open request of the same type, the insert does
// nothing, the transaction is rolled back (so is the number) and the open
// request is named.
func (s *ChangeRequestStore) Create(ctx context.Context, r domain.ChangeRequest) (domain.ChangeRequest, error) {
	previous, err := encodeChangeContent(r.Type, r.Previous)
	if err != nil {
		return domain.ChangeRequest{}, err
	}
	requested, err := encodeChangeContent(r.Type, r.Requested)
	if err != nil {
		return domain.ChangeRequest{}, err
	}
	// PostgreSQL keeps microseconds: the stored request is the one returned.
	r.FiledAt, r.StatusChangedAt = r.FiledAt.UTC().Truncate(time.Microsecond), r.StatusChangedAt.UTC().Truncate(time.Microsecond)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ChangeRequest{}, fmt.Errorf("file the change request: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	year := domain.ChangeRequestYear(r.FiledAt)
	var n int
	if err := tx.QueryRowContext(ctx, `INSERT INTO change_request_counter (year, last_number) VALUES ($1, 1)
		ON CONFLICT (year) DO UPDATE SET last_number = change_request_counter.last_number + 1
		RETURNING last_number`, year).Scan(&n); err != nil {
		return domain.ChangeRequest{}, fmt.Errorf("number the change request: %w", err)
	}
	r.ID = domain.FormatChangeRequestID(year, n)

	res, err := tx.ExecContext(ctx, `INSERT INTO change_request (`+changeRequestColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		ON CONFLICT DO NOTHING`,
		r.ID, r.PolicyID, r.CustomerID, string(r.Type), string(previous), string(requested), string(r.Channel),
		r.RequestedBy.Subject, r.RequestedBy.Email, r.RequestedBy.Name, r.FiledAt, string(r.Status), r.StatusChangedAt,
		nullString(r.DecisionReason))
	if err != nil {
		return domain.ChangeRequest{}, fmt.Errorf("store change request %s: %w", r.ID, err)
	}
	stored, err := res.RowsAffected()
	if err != nil {
		return domain.ChangeRequest{}, fmt.Errorf("store change request %s: %w", r.ID, err)
	}
	if stored == 0 {
		_ = tx.Rollback()
		return domain.ChangeRequest{}, s.openConflict(ctx, r.PolicyID, r.Type)
	}
	if err := tx.Commit(); err != nil {
		return domain.ChangeRequest{}, fmt.Errorf("store change request %s: %w", r.ID, err)
	}
	return r, nil
}

// openConflict reads the open request that won, after the rollback.
func (s *ChangeRequestStore) openConflict(ctx context.Context, policyID string, t domain.ChangeType) error {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT request_id FROM change_request
		WHERE policy_id = $1 AND change_type = $2 AND `+changeRequestOpen+` LIMIT 1`, policyID, string(t)).Scan(&id)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read the open change request of %s: %w", policyID, err)
	}
	return &ports.ChangeRequestOpenError{RequestID: id}
}

// Get returns ports.ErrChangeRequestNotFound when the id is unknown.
func (s *ChangeRequestStore) Get(ctx context.Context, requestID string) (domain.ChangeRequest, error) {
	r, err := scanChangeRequest(s.db.QueryRowContext(ctx,
		`SELECT `+changeRequestColumns+` FROM change_request WHERE request_id = $1`, requestID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ChangeRequest{}, ports.ErrChangeRequestNotFound
	}
	if err != nil {
		return domain.ChangeRequest{}, fmt.Errorf("read change request %s: %w", requestID, err)
	}
	return r, nil
}

// ListOpen returns the open requests of the policy, by type.
func (s *ChangeRequestStore) ListOpen(ctx context.Context, policyID string) ([]domain.ChangeRequest, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+changeRequestColumns+` FROM change_request
		WHERE policy_id = $1 AND `+changeRequestOpen+` ORDER BY change_type`, policyID)
	if err != nil {
		return nil, fmt.Errorf("list the open change requests of %s: %w", policyID, err)
	}
	return scanChangeRequests(rows)
}

func scanChangeRequests(rows *sql.Rows) ([]domain.ChangeRequest, error) {
	defer func() { _ = rows.Close() }()
	out := []domain.ChangeRequest{}
	for rows.Next() {
		r, err := scanChangeRequest(rows)
		if err != nil {
			return nil, fmt.Errorf("read a change request: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListByCustomer reads the page, the total, the indicators and the policies
// in one read-only snapshot, so they agree.
func (s *ChangeRequestStore) ListByCustomer(ctx context.Context, customerID string,
	f domain.ChangeRequestFilter) (domain.ChangeRequestPage, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return domain.ChangeRequestPage{}, fmt.Errorf("list the change requests: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var from sql.NullTime
	if !f.FiledFrom.IsZero() {
		from = sql.NullTime{Time: domain.CivilDayOf(f.FiledFrom).UTC(), Valid: true}
	}
	args := []any{customerID, string(f.Status), f.PolicyID, from}
	rows, err := tx.QueryContext(ctx, `SELECT `+changeRequestColumns+changeRequestFilter+`
		ORDER BY filed_at DESC, request_id DESC LIMIT $5 OFFSET $6`, append(args, f.PageSize, f.Offset())...)
	if err != nil {
		return domain.ChangeRequestPage{}, fmt.Errorf("list the change requests: %w", err)
	}
	page := domain.ChangeRequestPage{Page: f.Page, PageSize: f.PageSize}
	if page.Items, err = scanChangeRequests(rows); err != nil {
		return domain.ChangeRequestPage{}, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*)`+changeRequestFilter, args...).Scan(&page.Total); err != nil {
		return domain.ChangeRequestPage{}, fmt.Errorf("count the change requests: %w", err)
	}
	if page.Indicators, err = changeRequestIndicators(ctx, tx, customerID, f); err != nil {
		return domain.ChangeRequestPage{}, err
	}
	if page.Policies, err = changeRequestPolicies(ctx, tx, customerID); err != nil {
		return domain.ChangeRequestPage{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.ChangeRequestPage{}, fmt.Errorf("list the change requests: %w", err)
	}
	return page, nil
}

// changeRequestIndicators counts, whatever the filter, the open requests, the
// ones applied in the civil year of the query and the rejected ones, each with
// its latest.
func changeRequestIndicators(ctx context.Context, tx *sql.Tx, customerID string,
	f domain.ChangeRequestFilter) (domain.ChangeRequestIndicators, error) {
	yearStart, yearEnd := f.YearBounds()
	var out domain.ChangeRequestIndicators
	if err := tx.QueryRowContext(ctx, `SELECT
			count(*) FILTER (WHERE `+changeRequestOpen+`),
			count(*) FILTER (WHERE status = 'applied' AND status_changed_at >= $2 AND status_changed_at < $3),
			count(*) FILTER (WHERE `+changeRequestRejected+`)
		FROM change_request WHERE customer_id = $1`, customerID, yearStart, yearEnd).
		Scan(&out.Open, &out.AppliedThisYear, &out.Rejected); err != nil {
		return domain.ChangeRequestIndicators{}, fmt.Errorf("count the change requests by status: %w", err)
	}
	for _, q := range []struct {
		dest  **domain.ChangeRequestRef
		where string
		order string
		args  []any
	}{
		{&out.LatestOpen, changeRequestOpen, "filed_at DESC", nil},
		{&out.LastApplied, `status = 'applied' AND status_changed_at >= $2 AND status_changed_at < $3`, "status_changed_at DESC",
			[]any{yearStart, yearEnd}},
		{&out.LastRejected, changeRequestRejected, "status_changed_at DESC", nil},
	} {
		var ref domain.ChangeRequestRef
		var changeType string
		err := tx.QueryRowContext(ctx, `SELECT request_id, change_type, filed_at, status_changed_at FROM change_request
			WHERE customer_id = $1 AND `+q.where+` ORDER BY `+q.order+`, request_id DESC LIMIT 1`,
			append([]any{customerID}, q.args...)...).Scan(&ref.ID, &changeType, &ref.FiledAt, &ref.StatusChangedAt)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			continue
		case err != nil:
			return domain.ChangeRequestIndicators{}, fmt.Errorf("read the latest change request: %w", err)
		}
		ref.Type, ref.FiledAt, ref.StatusChangedAt = domain.ChangeType(changeType), ref.FiledAt.UTC(), ref.StatusChangedAt.UTC()
		*q.dest = &ref
	}
	return out, nil
}

// changeRequestPolicies are the policies of the customer that have requests,
// the options of the filter by policy.
func changeRequestPolicies(ctx context.Context, tx *sql.Tx, customerID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT policy_id FROM change_request WHERE customer_id = $1 ORDER BY policy_id`,
		customerID)
	if err != nil {
		return nil, fmt.Errorf("list the policies with change requests: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("read a policy with change requests: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
