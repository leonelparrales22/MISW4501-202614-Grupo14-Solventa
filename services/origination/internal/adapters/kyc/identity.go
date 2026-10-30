package kyc

import (
	"context"
	"fmt"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/kyc"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// IdentityClient is the part of the shared client the sign-up needs.
type IdentityClient interface {
	ReadDocument(ctx context.Context, front, back []byte) (kyc.DocumentRead, error)
	Verify(ctx context.Context, req kyc.VerificationRequest) (kyc.Verification, error)
}

// Verifier implements ports.IdentityVerifier over the KYC provider
// (HU-M1.1-01).
type Verifier struct {
	client IdentityClient
}

// NewVerifier wraps a shared KYC client. It must be a client of its own, built
// with kyc.IdentityTimeout, not the one of the profiling.
func NewVerifier(client IdentityClient) *Verifier { return &Verifier{client: client} }

var _ ports.IdentityVerifier = (*Verifier)(nil)

// ReadDocument maps the provider's read to the domain. Every failure of the
// provider is ports.ErrIdentityCheckUnavailable.
func (v *Verifier) ReadDocument(ctx context.Context, front, back domain.IdentityDocument) (domain.DocumentData, error) {
	read, err := v.client.ReadDocument(ctx, front.Content, back.Content)
	if err != nil {
		return domain.DocumentData{}, fmt.Errorf("%w: %w", ports.ErrIdentityCheckUnavailable, err)
	}
	return domain.DocumentData{
		DocumentType: read.DocumentType, DocumentID: read.DocumentID,
		GivenNames: read.GivenNames, Surnames: read.Surnames, BirthDate: read.BirthDate,
	}, nil
}

// Verify maps the provider's verdict to the domain. A reason the domain does
// not know is kept as the most cautious one that still lets the person retry:
// unverifiable.
func (v *Verifier) Verify(ctx context.Context, check domain.IdentityCheck) (domain.IdentityVerdict, error) {
	got, err := v.client.Verify(ctx, kyc.VerificationRequest{
		DocumentID: check.DocumentID, FullName: check.FullName, BirthDate: check.BirthDate.Format("2006-01-02"),
		DocumentFrontSHA256: check.FrontSHA256, DocumentBackSHA256: check.BackSHA256,
	})
	if err != nil {
		return domain.IdentityVerdict{}, fmt.Errorf("%w: %w", ports.ErrIdentityCheckUnavailable, err)
	}
	verdict := domain.IdentityVerdict{VerificationID: got.VerificationID, Approved: got.Approved(),
		CheckedAt: got.CheckedAt.UTC()}
	if verdict.CheckedAt.IsZero() {
		verdict.CheckedAt = time.Now().UTC()
	}
	if !verdict.Approved {
		verdict.Rejection = rejection(got.Reason)
	}
	return verdict, nil
}

func rejection(reason string) domain.IdentityRejection {
	switch reason {
	case kyc.ReasonRestrictedList:
		return domain.RejectionRestrictedList
	case kyc.ReasonUnreadable:
		return domain.RejectionUnreadable
	default:
		return domain.RejectionUnverifiable
	}
}
