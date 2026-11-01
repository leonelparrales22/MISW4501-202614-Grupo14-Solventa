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

func TestTheDeclarationOfASupportIsValidatedByItsParts(t *testing.T) {
	digest := strings.Repeat("a", 64)
	tests := []struct {
		name string
		req  domain.SupportUploadRequest
		want error
	}{
		{"a JPG", domain.SupportUploadRequest{FileName: "a.jpg", ContentType: "image/jpeg", ByteSize: 1, SHA256: digest}, nil},
		{"a PNG", domain.SupportUploadRequest{FileName: "a.png", ContentType: "image/png", ByteSize: 10, SHA256: digest}, nil},
		{"a PDF of exactly 10 MiB", domain.SupportUploadRequest{FileName: "a.pdf", ContentType: "application/pdf", ByteSize: domain.MaxSupportBytes, SHA256: digest}, nil},
		{"one byte over", domain.SupportUploadRequest{FileName: "a.pdf", ContentType: "application/pdf", ByteSize: domain.MaxSupportBytes + 1, SHA256: digest}, domain.ErrFileTooLarge},
		{"zero bytes", domain.SupportUploadRequest{FileName: "a.pdf", ContentType: "application/pdf", ByteSize: 0, SHA256: digest}, domain.ErrEmptyFile},
		{"a negative size", domain.SupportUploadRequest{FileName: "a.pdf", ContentType: "application/pdf", ByteSize: -1, SHA256: digest}, domain.ErrEmptyFile},
		{"a type that is not accepted", domain.SupportUploadRequest{FileName: "a.exe", ContentType: "application/octet-stream", ByteSize: 5, SHA256: digest}, domain.ErrUnsupportedFileType},
		{"no type", domain.SupportUploadRequest{FileName: "a", ByteSize: 5, SHA256: digest}, domain.ErrUnsupportedFileType},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u, err := tt.req.Validate()
			if tt.want != nil {
				assert.ErrorIs(t, err, tt.want)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tt.req.ByteSize, u.ByteSize)
			assert.Equal(t, digest, u.SHA256)
		})
	}
}

func TestTheNameOfADeclaredSupportIsSanitized(t *testing.T) {
	u, err := domain.SupportUploadRequest{FileName: "../../etc/passwd\x00.pdf", ContentType: "application/pdf",
		ByteSize: 5, SHA256: strings.Repeat("b", 64)}.Validate()
	assert.NoError(t, err)
	assert.Equal(t, "passwd.pdf", u.FileName)
}

func TestTheObjectOfASupportHangsFromItsOwner(t *testing.T) {
	f := domain.SupportFile{ID: "sup-1", OwnerSubject: "sub-ana"}
	assert.Equal(t, "supports/sub-ana/sup-1", f.ObjectKey())
	assert.False(t, f.Confirmed())
}
