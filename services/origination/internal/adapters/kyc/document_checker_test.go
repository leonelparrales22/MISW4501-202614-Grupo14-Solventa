package kyc_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/kyc"
	kycadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/kyc"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

type documentClient struct {
	check kyc.DocumentCheck
	err   error
	got   kyc.DocumentCheckRequest
}

func (c *documentClient) CheckDocument(_ context.Context, req kyc.DocumentCheckRequest) (kyc.DocumentCheck, error) {
	c.got = req
	return c.check, c.err
}

func TestTheDocumentOfABeneficiaryIsCheckedInTheRegistry(t *testing.T) {
	lucia := domain.Beneficiary{FullName: "Lucía Pérez Gómez", DocumentType: domain.DocumentTI, DocumentNumber: "1142998310"}
	for _, tc := range []struct {
		client *documentClient
		want   domain.DocumentStatus
	}{
		{&documentClient{check: kyc.DocumentCheck{Status: kyc.DocumentValid}}, domain.DocumentValid},
		{&documentClient{check: kyc.DocumentCheck{Status: kyc.DocumentNotFound}}, domain.DocumentNotFound},
		{&documentClient{err: errors.Join(kyc.ErrDocumentCheckUnavailable, errors.New("timeout"))}, domain.DocumentUnverifiable},
	} {
		got := kycadapter.NewDocumentChecker(tc.client).CheckDocument(context.Background(), lucia)

		assert.Equal(t, tc.want, got)
		assert.Equal(t, kyc.DocumentCheckRequest{DocumentType: "TI", DocumentNumber: "1142998310", FullName: "Lucía Pérez Gómez"}, tc.client.got)
	}
}
