// Package pdf renders the contractual document of a policy (HU-W1.3-03). The
// text of the document is addressed to the insured, so it is in Spanish.
package pdf

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/go-pdf/fpdf"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// ContentType is the media type of the generated documents.
const ContentType = "application/pdf"

// Generator implements ports.DocumentGenerator.
type Generator struct{}

var _ ports.DocumentGenerator = Generator{}

// New returns a Generator.
func New() Generator { return Generator{} }

var coverageNames = map[string]string{
	domain.CoverageDeath:      "Fallecimiento del asegurado",
	domain.CoverageDisability: "Incapacidad total y permanente",
}

// Generate renders the document: policy data, holder, coverages, premium,
// validity and the general conditions. The output is deterministic for the
// same data (creation date, compression and catalog order are fixed), so its checksum proves
// what the insured received.
func (Generator) Generate(ctx context.Context, d domain.ContractData) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if d.PolicyID == "" || len(d.Coverages) == 0 || d.Premium <= 0 {
		return nil, errors.New("pdf: policy id, coverages and premium are required")
	}

	p := fpdf.New("P", "mm", "A4", "")
	p.SetCompression(false)
	// Without this fpdf writes the catalog from a map, so the bytes (and the checksum) vary between runs.
	p.SetCatalogSort(true)
	p.SetCreationDate(d.GeneratedAt)
	p.SetTitle("Documento contractual · Póliza "+d.PolicyID, true)
	p.SetAuthor("Solventa", true)
	tr := p.UnicodeTranslatorFromDescriptor("")
	p.SetMargins(20, 20, 20)
	p.AddPage()

	p.SetFont("Helvetica", "B", 20)
	p.CellFormat(0, 10, tr("Solventa"), "", 1, "L", false, 0, "")
	p.SetFont("Helvetica", "B", 14)
	p.CellFormat(0, 8, tr("Documento contractual · Seguro de vida hipotecario"), "", 1, "L", false, 0, "")
	p.Ln(4)

	section(p, tr, "Datos de la póliza")
	row(p, tr, "Número de póliza", d.PolicyID)
	row(p, tr, "Versión del documento", strconv.Itoa(d.Version))
	row(p, tr, "Cobertura activa desde", date(d.CoverageStart))
	row(p, tr, "Vigencia hasta", date(d.CoverageEnd))
	row(p, tr, "Cobertura activada el", dateTime(d.ActivatedAt))
	row(p, tr, "Documento generado el", dateTime(d.GeneratedAt))

	section(p, tr, "Asegurado y tomador")
	row(p, tr, "Nombre", d.Holder.FullName)
	row(p, tr, "Documento de identidad", d.Holder.DocumentID)
	row(p, tr, "Correo electrónico", d.Holder.Email)

	section(p, tr, "Coberturas")
	for _, c := range d.Coverages {
		name := coverageNames[c.Code]
		if name == "" {
			name = c.Code
		}
		row(p, tr, name, money(d.Currency, c.InsuredAmount))
	}

	section(p, tr, "Prima")
	row(p, tr, "Prima mensual", money(d.Currency, d.Premium))
	row(p, tr, "Valor asegurado", money(d.Currency, d.InsuredAmount))

	section(p, tr, "Condiciones generales")
	p.SetFont("Helvetica", "", 10)
	for _, line := range conditions {
		p.MultiCell(0, 5, tr(line), "", "L", false)
		p.Ln(1)
	}

	var buf bytes.Buffer
	if err := p.Output(&buf); err != nil {
		return nil, fmt.Errorf("pdf: %w", err)
	}
	return buf.Bytes(), nil
}

// conditions is the summary of the general conditions printed in the document.
var conditions = []string{
	"1. El seguro cubre al asegurado por el saldo de su crédito hipotecario durante la vigencia indicada, en caso de fallecimiento o de incapacidad total y permanente.",
	"2. La cobertura está activa desde el pago de la primera prima; el pago oportuno de la prima mensual la mantiene vigente.",
	"3. El asegurado declara que los datos suministrados son ciertos. La inexactitud o la reticencia pueden afectar la cobertura.",
	"4. Para presentar un reclamo, el asegurado o sus beneficiarios deben informar a Solventa y aportar los soportes que se les soliciten.",
	"5. Este documento y el registro de su emisión se conservan asociados a la póliza y están disponibles para el asegurado y el equipo autorizado de Solventa.",
}

func section(p *fpdf.Fpdf, tr func(string) string, title string) {
	p.Ln(3)
	p.SetFont("Helvetica", "B", 12)
	p.SetFillColor(235, 238, 245)
	p.CellFormat(0, 7, tr(title), "", 1, "L", true, 0, "")
	p.Ln(1)
}

func row(p *fpdf.Fpdf, tr func(string) string, label, value string) {
	p.SetFont("Helvetica", "B", 10)
	p.CellFormat(60, 6, tr(label), "", 0, "L", false, 0, "")
	p.SetFont("Helvetica", "", 10)
	p.CellFormat(0, 6, tr(value), "", 1, "L", false, 0, "")
}

func date(t time.Time) string { return t.UTC().Format("02/01/2006") }

func dateTime(t time.Time) string { return t.UTC().Format("02/01/2006 15:04:05 UTC") }

// money formats 250000000 as "COP 250.000.000" (dots as thousands separator).
func money(currency string, amount float64) string {
	digits := strconv.FormatFloat(amount, 'f', 0, 64)
	var b strings.Builder
	for i, r := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 && digits[i-1] != '-' {
			b.WriteByte('.')
		}
		b.WriteRune(r)
	}
	return currency + " " + b.String()
}
