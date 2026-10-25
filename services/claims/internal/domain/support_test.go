package domain_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
)

// The rules of an uploaded support (HU-W2.1-01 criterion 5).

func TestTheFormatIsToldByTheContent(t *testing.T) {
	tests := []struct {
		name   string
		head   []byte
		want   domain.FileFormat
		wantOK bool
	}{
		{"jpeg", []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10}, domain.FormatJPEG, true},
		{"png", []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0x00}, domain.FormatPNG, true},
		{"pdf", []byte("%PDF-1.7\n"), domain.FormatPDF, true},
		{"truncated png signature", []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A}, "", false},
		{"two bytes of a jpeg", []byte{0xFF, 0xD8}, "", false},
		{"windows executable", []byte("MZ\x90\x00"), "", false},
		{"html", []byte("<!doctype html>"), "", false},
		{"gif", []byte("GIF89a"), "", false},
		{"pdf signature not at the start", []byte(" %PDF-1.7"), "", false},
		{"empty", nil, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := domain.DetectFormat(tt.head)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestTheLimitsOfTheSupports(t *testing.T) {
	assert.Equal(t, 10485760, domain.MaxSupportBytes, "10 MiB")
	assert.Equal(t, 20, domain.MaxSupportsPerClaim)
	assert.Equal(t, 50, domain.MaxPendingSupports)
}

func TestTheFileNameIsSanitized(t *testing.T) {
	tests := map[string]string{
		"croquis.pdf":                    "croquis.pdf",
		"  foto 1.jpg  ":                 "foto 1.jpg",
		"../../etc/passwd":               "passwd",
		`C:\Users\carolina\denuncia.pdf`: "denuncia.pdf",
		"fo\x00to\r\n.png":               "foto.png",
		"dir/":                           "support",
		"..":                             "support",
		"":                               "support",
		"informe de tránsito.pdf":        "informe de tránsito.pdf",
	}
	for in, want := range tests {
		assert.Equal(t, want, domain.SanitizeFileName(in), in)
	}
}

func TestALongFileNameIsCutAt120Characters(t *testing.T) {
	assert.Len(t, []rune(domain.SanitizeFileName(strings.Repeat("ñ", 120))), 120)
	assert.Len(t, []rune(domain.SanitizeFileName(strings.Repeat("ñ", 121))), 120)
}

func TestTheShapeOfASupportID(t *testing.T) {
	assert.True(t, domain.ValidSupportID("sup-"+strings.Repeat("a1", 16)))
	for _, id := range []string{"", "sup-", "sup-" + strings.Repeat("a", 31), "sup-" + strings.Repeat("A", 32),
		"cns-" + strings.Repeat("a", 32), "sup-" + strings.Repeat("a", 33)} {
		assert.False(t, domain.ValidSupportID(id), id)
	}
}

func TestTheContentTypeOfASupportComesFromItsFormat(t *testing.T) {
	assert.Equal(t, "application/pdf", domain.SupportFile{Format: domain.FormatPDF}.ContentType())
}
