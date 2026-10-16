package issuance

import (
	"context"
	"errors"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// ContractView is a contractual issuance with its audit trail.
type ContractView struct {
	Issuance domain.ContractIssuance
	Audit    []domain.IssuanceAuditEntry
}

// ContractReader serves the contractual document and the state of its
// issuance: to the insured who owns the policy and the authorized internal
// team (HU-W1.3-03 criterion 2), and to whoever holds a valid secure link.
type ContractReader struct {
	repo   ports.ContractRepository
	store  ports.DocumentStore
	audit  ports.IssuanceAudit
	signer ports.LinkSigner
	clock  ports.Clock
}

// NewContractReader builds a ContractReader.
func NewContractReader(repo ports.ContractRepository, store ports.DocumentStore, audit ports.IssuanceAudit,
	signer ports.LinkSigner, clock ports.Clock) *ContractReader {
	return &ContractReader{repo: repo, store: store, audit: audit, signer: signer, clock: clock}
}

func authorizeContract(ctx context.Context, c domain.ContractIssuance) error {
	return security.RequireOwnerOrRole(ctx, c.Event.CustomerID, security.RoleAdvisor, security.RoleOperations)
}

// Status returns the issuance of the policy and its audit trail. Only the
// owner, an advisor or an operations analyst may read it; a policy of another
// customer is reported as forbidden.
func (r *ContractReader) Status(ctx context.Context, policyID string) (ContractView, error) {
	c, err := r.repo.Get(ctx, policyID)
	if err != nil {
		return ContractView{}, err
	}
	if err := authorizeContract(ctx, c); err != nil {
		return ContractView{}, err
	}
	entries, err := r.audit.List(ctx, policyID)
	if err != nil {
		return ContractView{}, err
	}
	return ContractView{Issuance: c, Audit: entries}, nil
}

// Document returns the contractual document of the policy. It returns
// ports.ErrDocumentNotFound while the document is not ready.
func (r *ContractReader) Document(ctx context.Context, policyID string) (domain.ContractDocument, []byte, error) {
	c, err := r.repo.Get(ctx, policyID)
	if err != nil {
		return domain.ContractDocument{}, nil, err
	}
	if err := authorizeContract(ctx, c); err != nil {
		return domain.ContractDocument{}, nil, err
	}
	return r.load(ctx, c)
}

// DocumentByLink returns the document a secure link points to, without any
// session: the signature and the expiry are the authorization. A forged or
// expired link is ports.ErrInvalidLink.
func (r *ContractReader) DocumentByLink(ctx context.Context, token string) (domain.ContractDocument, []byte, error) {
	policyID, version, err := r.signer.Verify(token, r.clock.Now())
	if err != nil {
		return domain.ContractDocument{}, nil, ports.ErrInvalidLink
	}
	c, err := r.repo.Get(ctx, policyID)
	if err != nil {
		if errors.Is(err, ports.ErrContractNotFound) {
			return domain.ContractDocument{}, nil, ports.ErrInvalidLink
		}
		return domain.ContractDocument{}, nil, err
	}
	if c.Document == nil || c.Document.Version != version {
		return domain.ContractDocument{}, nil, ports.ErrInvalidLink
	}
	return r.load(ctx, c)
}

func (r *ContractReader) load(ctx context.Context, c domain.ContractIssuance) (domain.ContractDocument, []byte, error) {
	if c.Document == nil {
		return domain.ContractDocument{}, nil, ports.ErrDocumentNotFound
	}
	content, err := r.store.Get(ctx, c.Document.StorageKey)
	if err != nil {
		return domain.ContractDocument{}, nil, err
	}
	return *c.Document, content, nil
}
