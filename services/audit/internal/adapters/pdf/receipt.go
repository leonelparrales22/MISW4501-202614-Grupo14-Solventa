// Package pdf renders the receipt of a consent revocation (HU-M1.3-02
// criterion 4, decision D-4 a): audit generates it on request from the
// immutable record, so the same revocation always gives the same document.
// The text is addressed to the holder: Spanish by default, English on
// request.
package pdf

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-pdf/fpdf"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/ports"
)

// ContentType is the media type of the receipts.
const ContentType = "application/pdf"

// Renderer implements ports.ReceiptRenderer.
type Renderer struct {
	compress bool
}

var _ ports.ReceiptRenderer = Renderer{}

// New returns a Renderer that compresses the streams of the document.
func New() Renderer { return Renderer{compress: true} }

// NewUncompressed returns a Renderer that writes the text of the document as
// it is, so a test can read it.
func NewUncompressed() Renderer { return Renderer{compress: false} }

// texts are the labels of the receipt in one language.
type texts struct {
	title, subtitle, sectionRevocation, sectionConsent, sectionHolder, sectionScope, note string
	receipt, consent, entity, purpose, granted, requested, ended, channel, device, method string
	name, document                                                                        string
	channels                                                                              map[domain.RevocationChannel]string
	methods                                                                               map[domain.ConfirmationMethod]string
	sources                                                                               map[string]string
	categories                                                                            map[string]string
}

var spanish = texts{
	title:             "Comprobante de revocación del consentimiento",
	subtitle:          "Autorización de consulta de datos financieros (Open Finance)",
	sectionRevocation: "Revocación",
	sectionConsent:    "Consentimiento revocado",
	sectionHolder:     "Titular",
	sectionScope:      "Alcance revocado",
	note: "Desde la fecha y hora de finalización, Solventa no consulta los datos de este consentimiento. Este comprobante se " +
		"genera desde el registro inmutable de auditoría: la misma revocación produce siempre el mismo documento.",
	receipt: "Número de comprobante", consent: "Consentimiento", entity: "Entidad", purpose: "Finalidad",
	granted: "Otorgado el", requested: "Solicitud de revocación", ended: "Fin de la autorización",
	channel: "Canal", device: "Dispositivo", method: "Confirmación", name: "Nombre", document: "Documento",
	channels: map[domain.RevocationChannel]string{domain.RevocationChannelMobileApp: "Aplicación móvil de Solventa"},
	methods:  map[domain.ConfirmationMethod]string{domain.ConfirmationBiometric: "Biometría", domain.ConfirmationPassword: "Contraseña de Solventa"},
	sources: map[string]string{
		domain.SourceOpenFinance: "Open Finance", domain.SourceOpenData: "Datos abiertos", domain.SourceKYC: "Verificación de identidad",
	},
	categories: map[string]string{
		"credit_products": "productos de crédito", "balances": "saldos", "payment_behavior": "comportamiento de pago",
		"public_records": "registros públicos", "identity_verification": "verificación de identidad",
	},
}

var english = texts{
	title:             "Consent revocation receipt",
	subtitle:          "Authorization to query financial data (Open Finance)",
	sectionRevocation: "Revocation",
	sectionConsent:    "Revoked consent",
	sectionHolder:     "Holder",
	sectionScope:      "Revoked scope",
	note: "From the end date and time, Solventa does not query the data of this consent. This receipt is generated from the " +
		"immutable audit record: the same revocation always produces the same document.",
	receipt: "Receipt number", consent: "Consent", entity: "Entity", purpose: "Purpose",
	granted: "Granted on", requested: "Revocation requested", ended: "Authorization ended",
	channel: "Channel", device: "Device", method: "Confirmation", name: "Name", document: "Document",
	channels: map[domain.RevocationChannel]string{domain.RevocationChannelMobileApp: "Solventa mobile app"},
	methods:  map[domain.ConfirmationMethod]string{domain.ConfirmationBiometric: "Biometrics", domain.ConfirmationPassword: "Solventa password"},
	sources: map[string]string{
		domain.SourceOpenFinance: "Open Finance", domain.SourceOpenData: "Open data", domain.SourceKYC: "Identity verification",
	},
	categories: map[string]string{
		"credit_products": "credit products", "balances": "balances", "payment_behavior": "payment behavior",
		"public_records": "public records", "identity_verification": "identity verification",
	},
}

// Render implements ports.ReceiptRenderer: the receipt number, the consent
// and its entity, the holder (name and document masked but its last four
// characters), the revoked scope, the request and the exact date and time the
// authorization ended (with seconds and offset, in Colombia and in UTC), the
// channel and the device. Only immutable fields are printed and the creation
// date is the effective time, so the bytes depend on the record alone. lang
// "en" (or "en-…") gives English; anything else, Spanish.
func (r Renderer) Render(rev domain.Revocation, c domain.Consent, lang string) ([]byte, error) {
	if rev.ReceiptNumber == "" || rev.ConsentID == "" || rev.EffectiveAt.IsZero() {
		return nil, errors.New("pdf: the receipt number, the consent and the effective time are required")
	}
	t := spanish
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(lang)), "en") {
		t = english
	}
	p := fpdf.New("P", "mm", "A4", "")
	p.SetCompression(r.compress)
	// Without this fpdf writes the catalog from a map and the bytes vary.
	p.SetCatalogSort(true)
	p.SetCreationDate(rev.EffectiveAt.UTC())
	p.SetModificationDate(rev.EffectiveAt.UTC())
	tr := p.UnicodeTranslatorFromDescriptor("")
	p.SetTitle(tr(t.title+" "+rev.ReceiptNumber), false)
	p.SetAuthor("Solventa", false)
	p.SetMargins(20, 20, 20)
	p.AddPage()

	p.SetFont("Helvetica", "B", 20)
	p.CellFormat(0, 10, "Solventa", "", 1, "L", false, 0, "")
	p.SetFont("Helvetica", "B", 14)
	p.CellFormat(0, 8, tr(t.title), "", 1, "L", false, 0, "")
	p.SetFont("Helvetica", "", 10)
	p.CellFormat(0, 6, tr(t.subtitle), "", 1, "L", false, 0, "")
	p.Ln(3)

	section(p, tr, t.sectionRevocation)
	row(p, tr, t.receipt, rev.ReceiptNumber)
	row(p, tr, t.requested, instant(rev.RequestedAt))
	row(p, tr, t.ended, instant(rev.EffectiveAt))
	row(p, tr, t.channel, label(t.channels, rev.Channel))
	if rev.Device != "" {
		row(p, tr, t.device, rev.Device)
	}
	if rev.ConfirmationMethod != "" {
		row(p, tr, t.method, label(t.methods, rev.ConfirmationMethod))
	}

	section(p, tr, t.sectionConsent)
	row(p, tr, t.consent, rev.ConsentID)
	row(p, tr, t.entity, entities(rev.Entities))
	row(p, tr, t.purpose, c.Purpose)
	if !c.GrantedAt.IsZero() {
		row(p, tr, t.granted, instant(c.GrantedAt))
	}

	section(p, tr, t.sectionHolder)
	row(p, tr, t.name, rev.Holder.FullName)
	row(p, tr, t.document, MaskDocument(rev.Holder.DocumentID))

	section(p, tr, t.sectionScope)
	for _, s := range rev.Scope {
		cats := make([]string, 0, len(s.DataCategories))
		for _, cat := range s.DataCategories {
			cats = append(cats, label(t.categories, cat))
		}
		row(p, tr, label(t.sources, s.Code), strings.Join(cats, ", "))
	}

	p.Ln(4)
	p.SetFont("Helvetica", "", 9)
	p.MultiCell(0, 5, tr(t.note), "", "L", false)

	var buf bytes.Buffer
	if err := p.Output(&buf); err != nil {
		return nil, fmt.Errorf("pdf: %w", err)
	}
	return buf.Bytes(), nil
}

// MaskDocument hides every character of the document but the last four.
func MaskDocument(doc string) string {
	runes := []rune(strings.TrimSpace(doc))
	if len(runes) <= 4 {
		return strings.Repeat("*", len(runes))
	}
	return strings.Repeat("*", len(runes)-4) + string(runes[len(runes)-4:])
}

// instant prints a time with seconds and offset in Colombia and in UTC, e.g.
// "24/10/2026 15:00:01 -05:00 (Colombia) · 24/10/2026 20:00:01 UTC".
func instant(at time.Time) string {
	return at.In(domain.ReceiptZone()).Format("02/01/2006 15:04:05 -07:00") + " (Colombia) · " +
		at.UTC().Format("02/01/2006 15:04:05") + " UTC"
}

func entities(es []domain.Entity) string {
	names := make([]string, 0, len(es))
	for _, e := range es {
		names = append(names, e.Name)
	}
	return strings.Join(names, ", ")
}

// label returns the text of a code, or the code itself when it has none.
func label[K ~string](m map[K]string, code K) string {
	if s, ok := m[code]; ok {
		return s
	}
	return string(code)
}

func section(p *fpdf.Fpdf, tr func(string) string, title string) {
	p.Ln(3)
	p.SetFont("Helvetica", "B", 12)
	p.SetFillColor(235, 238, 245)
	p.CellFormat(0, 7, tr(title), "", 1, "L", true, 0, "")
	p.Ln(1)
}

func row(p *fpdf.Fpdf, tr func(string) string, name, value string) {
	p.SetFont("Helvetica", "B", 10)
	p.CellFormat(55, 6, tr(name), "", 0, "L", false, 0, "")
	p.SetFont("Helvetica", "", 10)
	p.MultiCell(0, 6, tr(value), "", "L", false)
}
