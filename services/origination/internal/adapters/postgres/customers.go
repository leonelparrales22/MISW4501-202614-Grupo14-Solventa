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
const customerColumns = `id, document_id, full_name, email, birth_date, cognito_sub, created_at, updated_at`

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
		c   domain.Customer
		sub sql.NullString
	)
	if err := s.Scan(&c.ID, &c.DocumentID, &c.FullName, &c.Email, &c.BirthDate,
		&sub, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return domain.Customer{}, err
	}
	c.CognitoSub = sub.String
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

// Save creates the customer, or updates the one with the same document id
// keeping its id and the instant it was created.
//
// cognito_sub is only written when it comes with a value, so saving the
// customer again does not erase the account it already has.
func (s *CustomerStore) Save(ctx context.Context, c domain.Customer) (domain.Customer, error) {
	now := time.Now().UTC()
	if c.CreatedAt.IsZero() {
		c.CreatedAt = now
	}
	stored, _, err := s.one(ctx, `INSERT INTO customer (`+customerColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $7)
		ON CONFLICT (document_id) DO UPDATE SET
			full_name = EXCLUDED.full_name,
			email = EXCLUDED.email,
			birth_date = EXCLUDED.birth_date,
			cognito_sub = COALESCE(EXCLUDED.cognito_sub, customer.cognito_sub),
			updated_at = EXCLUDED.updated_at
		RETURNING `+customerColumns,
		c.ID, c.DocumentID, c.FullName, c.Email, c.BirthDate.UTC(), nullString(c.CognitoSub), c.CreatedAt.UTC())
	if err != nil {
		return domain.Customer{}, fmt.Errorf("save the customer with document %s: %w", c.DocumentID, err)
	}
	return stored, nil
}
