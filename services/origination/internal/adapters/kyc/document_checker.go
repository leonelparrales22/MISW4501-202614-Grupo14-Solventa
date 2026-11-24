package kyc

import (
	"context"
	"log/slog"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/kyc"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// DocumentClient is the part of the shared client the document check of an
// endorsement needs.
type DocumentClient interface {
	CheckDocument(ctx context.Context, req kyc.DocumentCheckRequest) (kyc.DocumentCheck, error)
}

// DocumentChecker implements ports.IdentityDocumentChecker over the KYC
// provider (HU-W1.4-02, decision D-5 a): it asks the registry whether the
// document of a new beneficiary is valid before an endorsement is applied.
type DocumentChecker struct {
	client DocumentClient
}

// NewDocumentChecker wraps a KYC client of its own, built with
// kyc.DocumentCheckTimeout: not the client of the profiling nor the one of
// the sign-up.
func NewDocumentChecker(client DocumentClient) *DocumentChecker {
	return &DocumentChecker{client: client}
}

var _ ports.IdentityDocumentChecker = (*DocumentChecker)(nil)

// CheckDocument maps the answer of the registry. Every failure of the provider
// is DocumentUnverifiable: the validation cannot be decided, so nothing is
// applied.
func (c *DocumentChecker) CheckDocument(ctx context.Context, b domain.Beneficiary) domain.DocumentStatus {
	check, err := c.client.CheckDocument(ctx, kyc.DocumentCheckRequest{DocumentType: string(b.DocumentType),
		DocumentNumber: b.DocumentNumber, FullName: b.FullName})
	if err != nil {
		// Never the number nor the name of the person.
		slog.WarnContext(ctx, "the document check of an endorsement is unverifiable", "document_type", string(b.DocumentType),
			"error", err)
		return domain.DocumentUnverifiable
	}
	if check.Valid() {
		return domain.DocumentValid
	}
	return domain.DocumentNotFound
}
