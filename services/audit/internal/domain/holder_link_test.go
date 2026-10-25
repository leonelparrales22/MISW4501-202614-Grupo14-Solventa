package domain_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
)

func TestNormalizeDocumentTrimsAndUpperCases(t *testing.T) {
	assert.Equal(t, "1020304050", domain.NormalizeDocument(" 1020304050 "))
	assert.Equal(t, "AB12345", domain.NormalizeDocument("ab12345"))
	assert.Equal(t, "", domain.NormalizeDocument("   "))
}

func validLink() domain.HolderLink {
	return domain.HolderLink{
		Subject: "sub-1", DocumentID: "1020304050", CustomerID: "cus-1",
		LinkedAt: time.Date(2026, 10, 24, 20, 0, 0, 0, time.UTC), EventID: "customer-linked-cus-1",
	}
}

func TestValidateAcceptsACompleteLink(t *testing.T) {
	assert.NoError(t, validLink().Validate())
}

func TestValidateRefusesAnIncompleteOrInvalidLink(t *testing.T) {
	for name, breakIt := range map[string]func(*domain.HolderLink){
		"subject":             func(l *domain.HolderLink) { l.Subject = "" },
		"long subject":        func(l *domain.HolderLink) { l.Subject = strings.Repeat("s", 129) },
		"customer":            func(l *domain.HolderLink) { l.CustomerID = "" },
		"event":               func(l *domain.HolderLink) { l.EventID = "" },
		"document":            func(l *domain.HolderLink) { l.DocumentID = "" },
		"short document":      func(l *domain.HolderLink) { l.DocumentID = "1234" },
		"symbols":             func(l *domain.HolderLink) { l.DocumentID = "10-20-30" },
		"not normalized case": func(l *domain.HolderLink) { l.DocumentID = "ab12345" },
		"not trimmed":         func(l *domain.HolderLink) { l.DocumentID = " 1020304050" },
		"linked at":           func(l *domain.HolderLink) { l.LinkedAt = time.Time{} },
	} {
		t.Run(name, func(t *testing.T) {
			l := validLink()
			breakIt(&l)
			assert.ErrorIs(t, l.Validate(), domain.ErrInvalidHolderLink)
		})
	}
}
