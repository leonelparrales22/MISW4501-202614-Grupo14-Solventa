package kyc

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
)

// IdentityTimeout is the cut of the identity check of the sign-up
// (HU-M1.1-01). It is not the 700 ms of the profiling: the sign-up is not in
// the latency budget of HA-L01, a real provider takes seconds to read a
// document, and the mobile client stops waiting at 30 s. The sign-up builds its
// own Client with this timeout, so its circuit is not the profiling's either.
const IdentityTimeout = 10 * time.Second

// Outcomes and reasons of a verification, as the provider names them.
const (
	OutcomeApproved = "approved"
	OutcomeRejected = "rejected"

	ReasonRestrictedList = "restricted_list"
	ReasonUnreadable     = "unreadable"
	ReasonUnverifiable   = "unverifiable"
)

// ErrInvalidIdentityRequest is a read without both faces or a verification
// without its data. It never reaches the provider.
var ErrInvalidIdentityRequest = errors.New("both faces, or the document id, full name, birth date and the digests of both faces, are required")

// DocumentRead is the data the provider read on the document.
type DocumentRead struct {
	ReadID       string `json:"read_id"`
	DocumentType string `json:"document_type"`
	DocumentID   string `json:"document_id"`
	GivenNames   string `json:"given_names"`
	Surnames     string `json:"surnames"`
	// BirthDate is YYYY-MM-DD.
	BirthDate string `json:"birth_date"`
}

// VerificationRequest is the data the person confirmed, with the digest of
// each face so the provider checks the same images that were read.
type VerificationRequest struct {
	DocumentID          string `json:"document_id"`
	FullName            string `json:"full_name"`
	BirthDate           string `json:"birth_date"`
	DocumentFrontSHA256 string `json:"document_front_sha256"`
	DocumentBackSHA256  string `json:"document_back_sha256"`
}

// Verification is the provider's verdict. Reason is set when Outcome is
// OutcomeRejected, and it never reaches the person (criterion 3).
type Verification struct {
	VerificationID string    `json:"verification_id"`
	Outcome        string    `json:"outcome"`
	Reason         string    `json:"reason"`
	CheckedAt      time.Time `json:"checked_at"`
}

// Approved tells whether the person was verified.
func (v Verification) Approved() bool { return v.Outcome == OutcomeApproved }

// ReadDocument asks the provider to read the two faces (JPEG bytes). Every
// failure of the provider is ErrSourceUnavailable.
func (c *Client) ReadDocument(ctx context.Context, front, back []byte) (DocumentRead, error) {
	if len(front) == 0 || len(back) == 0 {
		return DocumentRead{}, ErrInvalidIdentityRequest
	}
	body := map[string]string{
		"document_front": base64.StdEncoding.EncodeToString(front),
		"document_back":  base64.StdEncoding.EncodeToString(back),
	}
	read, err := resilience.Execute(c.breaker, func() (DocumentRead, error) {
		return post[DocumentRead](ctx, c, "/documents/reads", body)
	})
	if err != nil {
		return DocumentRead{}, fmt.Errorf("%w: %w", ErrSourceUnavailable, err)
	}
	if read.DocumentID == "" {
		return DocumentRead{}, fmt.Errorf("%w: the read has no document id", ErrSourceUnavailable)
	}
	return read, nil
}

// Verify asks the provider to verify the person. A rejection is an answer,
// not a failure: it comes back as a Verification that is not Approved, and
// only the failures of the provider are ErrSourceUnavailable.
func (c *Client) Verify(ctx context.Context, req VerificationRequest) (Verification, error) {
	if req.DocumentID == "" || req.FullName == "" || req.BirthDate == "" ||
		req.DocumentFrontSHA256 == "" || req.DocumentBackSHA256 == "" {
		return Verification{}, ErrInvalidIdentityRequest
	}
	v, err := resilience.Execute(c.breaker, func() (Verification, error) {
		return post[Verification](ctx, c, "/verifications", req)
	})
	if err != nil {
		return Verification{}, fmt.Errorf("%w: %w", ErrSourceUnavailable, err)
	}
	if v.Outcome != OutcomeApproved && v.Outcome != OutcomeRejected {
		return Verification{}, fmt.Errorf("%w: unknown outcome %q", ErrSourceUnavailable, v.Outcome)
	}
	return v, nil
}

func post[T any](ctx context.Context, c *Client, path string, payload any) (T, error) {
	var zero T
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	raw, err := json.Marshal(payload)
	if err != nil {
		return zero, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(raw))
	if err != nil {
		return zero, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return zero, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return zero, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	var out T
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBodyBytes)).Decode(&out); err != nil {
		return zero, fmt.Errorf("decode response: %w", err)
	}
	return out, nil
}
