package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// customerColumns is the projection of the customer table.
const customerColumns = `id, document_id, full_name, email, birth_date, cognito_sub,
	document_front_key, document_back_key, identity_status, identity_rejection,
	identity_verification_id, identity_checked_at, created_at, updated_at`

// CustomerStore is the ports.CustomerRepository over PostgreSQL (HU-M1.1-01).
// The customer the insured registers from the mobile client and the one the
// advisor looks up by document are the same row.
type CustomerStore struct {
	db *sql.DB
}

// NewCustomerStore builds a CustomerStore over an open database.
func NewCustomerStore(db *sql.DB) *CustomerStore { return &CustomerStore{db: db} }

var _ ports.CustomerRepository = (*CustomerStore)(nil)

func scanCustomer(s scanner) (domain.Customer, error) {
	var (
		c                       domain.Customer
		sub                     sql.NullString
		front, back             sql.NullString
		status                  string
		rejection, verification sql.NullString
		checkedAt               sql.NullTime
	)
	if err := s.Scan(&c.ID, &c.DocumentID, &c.FullName, &c.Email, &c.BirthDate,
		&sub, &front, &back, &status, &rejection, &verification, &checkedAt,
		&c.CreatedAt, &c.UpdatedAt); err != nil {
		return domain.Customer{}, err
	}
	c.CognitoSub = sub.String
	c.DocumentFrontKey, c.DocumentBackKey = front.String, back.String
	c.IdentityStatus = domain.IdentityStatus(status)
	c.IdentityRejection = domain.IdentityRejection(rejection.String)
	c.IdentityVerificationID = verification.String
	c.IdentityCheckedAt = timeOrZero(checkedAt)
	c.BirthDate = c.BirthDate.UTC()
	c.CreatedAt, c.UpdatedAt = c.CreatedAt.UTC(), c.UpdatedAt.UTC()
	return c, nil
}

func (s *CustomerStore) one(ctx context.Context, query string, args ...any) (domain.Customer, bool, error) {
	c, err := scanCustomer(s.db.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Customer{}, false, nil
	}
	if err != nil {
		return domain.Customer{}, false, err
	}
	return c, true, nil
}

// GetByDocument returns ports.ErrCustomerNotFound when the document is unknown.
func (s *CustomerStore) GetByDocument(ctx context.Context, documentID string) (domain.Customer, error) {
	c, found, err := s.one(ctx, `SELECT `+customerColumns+` FROM customer WHERE document_id = $1`, documentID)
	if err != nil {
		return domain.Customer{}, fmt.Errorf("read the customer with document %s: %w", documentID, err)
	}
	if !found {
		return domain.Customer{}, ports.ErrCustomerNotFound
	}
	return c, nil
}

// GetByEmail returns ports.ErrCustomerNotFound when the email is unknown. The
// comparison ignores case: the email is the username in the user pool.
func (s *CustomerStore) GetByEmail(ctx context.Context, email string) (domain.Customer, error) {
	c, found, err := s.one(ctx, `SELECT `+customerColumns+` FROM customer WHERE lower(email) = lower($1)`, email)
	if err != nil {
		return domain.Customer{}, fmt.Errorf("read the customer with email: %w", err)
	}
	if !found {
		return domain.Customer{}, ports.ErrCustomerNotFound
	}
	return c, nil
}

// upsertCustomer is the write of Save and Link: it creates the customer, or
// updates the one with the same document id keeping its id and the instant it
// was created. cognito_sub and the keys of the identity document are only
// written when they come with a value, so saving the customer again does not
// erase the account it already has nor the faces it already captured.
//
// The identity check (HU-M1.1-01) is written as a whole when it comes with a
// status, so a new answer of the provider replaces the previous one; a save
// without a status (the advisor's) leaves the check as it was.
const upsertCustomer = `INSERT INTO customer (` + customerColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $13)
		ON CONFLICT (document_id) DO UPDATE SET
			full_name = EXCLUDED.full_name,
			email = EXCLUDED.email,
			birth_date = EXCLUDED.birth_date,
			cognito_sub = COALESCE(EXCLUDED.cognito_sub, customer.cognito_sub),
			document_front_key = COALESCE(EXCLUDED.document_front_key, customer.document_front_key),
			document_back_key = COALESCE(EXCLUDED.document_back_key, customer.document_back_key),
			identity_status = CASE WHEN EXCLUDED.identity_status = '' THEN customer.identity_status
				ELSE EXCLUDED.identity_status END,
			identity_rejection = CASE WHEN EXCLUDED.identity_status = '' THEN customer.identity_rejection
				ELSE EXCLUDED.identity_rejection END,
			identity_verification_id = CASE WHEN EXCLUDED.identity_status = '' THEN customer.identity_verification_id
				ELSE EXCLUDED.identity_verification_id END,
			identity_checked_at = CASE WHEN EXCLUDED.identity_status = '' THEN customer.identity_checked_at
				ELSE EXCLUDED.identity_checked_at END,
			updated_at = EXCLUDED.updated_at
		RETURNING ` + customerColumns

// queryRower is a *sql.DB or a *sql.Tx.
type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func upsert(ctx context.Context, q queryRower, c domain.Customer) (domain.Customer, error) {
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now().UTC()
	}
	stored, err := scanCustomer(q.QueryRowContext(ctx, upsertCustomer,
		c.ID, c.DocumentID, c.FullName, c.Email, c.BirthDate.UTC(), nullString(c.CognitoSub),
		nullString(c.DocumentFrontKey), nullString(c.DocumentBackKey), string(c.IdentityStatus),
		nullString(string(c.IdentityRejection)), nullString(c.IdentityVerificationID), nullTime(c.IdentityCheckedAt),
		c.CreatedAt.UTC()))
	if err != nil {
		return domain.Customer{}, fmt.Errorf("save the customer with document %s: %w", c.DocumentID, err)
	}
	return stored, nil
}

// Save creates the customer, or updates the one with the same document id
// keeping its id and the instant it was created (see upsertCustomer).
func (s *CustomerStore) Save(ctx context.Context, c domain.Customer) (domain.Customer, error) {
	return upsert(ctx, s.db, c)
}

// Link saves the customer with its subject and writes the CustomerLinked event
// to outbox_event in the same transaction (HU-M1.3-01): if either write fails
// neither stays. The event id is the primary key of the outbox, so linking the
// same customer again writes no second event.
func (s *CustomerStore) Link(ctx context.Context, c domain.Customer, e domain.CustomerLinked) (domain.Customer, error) {
	env, err := e.Envelope()
	if err != nil {
		return domain.Customer{}, fmt.Errorf("build the CustomerLinked event: %w", err)
	}
	body, err := env.Encode()
	if err != nil {
		return domain.Customer{}, fmt.Errorf("encode the CustomerLinked event: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Customer{}, fmt.Errorf("link the customer with document %s: %w", c.DocumentID, err)
	}
	defer func() { _ = tx.Rollback() }()
	stored, err := upsert(ctx, tx, c)
	if err != nil {
		return domain.Customer{}, err
	}
	if stored.ID != e.CustomerID {
		return domain.Customer{}, fmt.Errorf("link the customer with document %s: the event is for customer %s, not %s",
			c.DocumentID, e.CustomerID, stored.ID)
	}
	if err := insertOutboxEvent(ctx, tx, env.EventID, env.EventType, body, env.OccurredAt); err != nil {
		return domain.Customer{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Customer{}, fmt.Errorf("link the customer with document %s: %w", c.DocumentID, err)
	}
	return stored, nil
}

var _ ports.CustomerBySubject = (*CustomerStore)(nil)

// GetBySubject returns ports.ErrCustomerNotFound when no customer has the
// account subject.
func (s *CustomerStore) GetBySubject(ctx context.Context, subject string) (domain.Customer, error) {
	if subject == "" {
		return domain.Customer{}, ports.ErrCustomerNotFound
	}
	c, found, err := s.one(ctx, `SELECT `+customerColumns+` FROM customer WHERE cognito_sub = $1`, subject)
	if err != nil {
		return domain.Customer{}, fmt.Errorf("read the customer by subject: %w", err)
	}
	if !found {
		return domain.Customer{}, ports.ErrCustomerNotFound
	}
	return c, nil
}
