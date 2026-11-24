package kyc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
)

// DocumentCheckTimeout is the default cut of the document check of an
// endorsement (HU-W1.4-02, decision D-5 a). The analyst waits for it before
// the endorsement is applied, outside the latency budget of the profiling, so
// origination builds its own Client for it: its circuit is not the
// profiling's nor the sign-up's.
const DocumentCheckTimeout = 2 * time.Second

// Statuses of a document in the registry, as the provider names them.
const (
	DocumentValid    = "valid"
	DocumentNotFound = "not_found"
)

// Errors of the document check.
var (
	// ErrDocumentCheckUnavailable means the provider gave no usable answer:
	// timeout, connection error, non-200 status, malformed body, unknown
	// status or open circuit. The caller cannot tell whether the document is
	// valid.
	ErrDocumentCheckUnavailable = errors.New("the document check of kyc is unavailable")
	// ErrInvalidDocumentCheck is a check without the type, the number or the
	// name. It never reaches the provider nor counts against the circuit.
	ErrInvalidDocumentCheck = errors.New("the document type, the document number and the full name are required")
)

// DocumentCheckRequest names the identity document to look up in the registry.
type DocumentCheckRequest struct {
	DocumentType   string `json:"document_type"`
	DocumentNumber string `json:"document_number"`
	FullName       string `json:"full_name"`
}

// DocumentCheck is the answer of the registry.
type DocumentCheck struct {
	CheckID   string    `json:"check_id"`
	Status    string    `json:"status"`
	CheckedAt time.Time `json:"checked_at"`
}

// Valid tells whether the document is in the registry.
func (c DocumentCheck) Valid() bool { return c.Status == DocumentValid }

// CheckDocument asks the provider whether the document is valid in the
// registry. A document that is not found is an answer, not a failure: it comes
// back as a DocumentCheck that is not Valid, and only the failures of the
// provider are ErrDocumentCheckUnavailable.
func (c *Client) CheckDocument(ctx context.Context, req DocumentCheckRequest) (DocumentCheck, error) {
	req.DocumentType = strings.TrimSpace(req.DocumentType)
	req.DocumentNumber = strings.TrimSpace(req.DocumentNumber)
	req.FullName = strings.TrimSpace(req.FullName)
	if req.DocumentType == "" || req.DocumentNumber == "" || req.FullName == "" {
		return DocumentCheck{}, ErrInvalidDocumentCheck
	}
	check, err := resilience.Execute(c.breaker, func() (DocumentCheck, error) {
		check, err := post[DocumentCheck](ctx, c, "/document-checks", req)
		if err == nil && check.Status != DocumentValid && check.Status != DocumentNotFound {
			err = fmt.Errorf("unknown status %q", check.Status)
		}
		return check, err
	})
	if err != nil {
		return DocumentCheck{}, fmt.Errorf("%w: %w", ErrDocumentCheckUnavailable, err)
	}
	return check, nil
}
