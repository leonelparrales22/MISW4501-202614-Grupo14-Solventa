// Package pdf renders the filing receipt of a claim (HU-W2.1-01 criterion 4).
// The text is addressed to the insured, so it is in Spanish.
package pdf

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/go-pdf/fpdf"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// ReceiptRenderer implements ports.ReceiptRenderer.
type ReceiptRenderer struct{}

var _ ports.ReceiptRenderer = ReceiptRenderer{}

// NewReceiptRenderer returns a ReceiptRenderer.
func NewReceiptRenderer() ReceiptRenderer { return ReceiptRenderer{} }

var statusNames = map[domain.Status]string{
	domain.StatusActive: "Radicado",
}

// nextSteps is what the confirmation screen tells the insured comes next.
var nextSteps = []string{
	"Un asesor de siniestros revisará la radicación en un máximo de 2 días hábiles. Si falta algún soporte, te la devolverá con el motivo y podrás corregirla desde Mis siniestros.",
	"Si el asesor la aprueba, el caso pasa a evaluación con la prioridad que le asigne el triage.",
	"Cita el número de radicado en cualquier comunicación con Solventa sobre este siniestro.",
}

// Render draws the receipt. The output is the same for the same receipt (the
// creation and modification dates are the filing date, and neither
// compression nor the catalog order vary), so downloading it twice gives the same file. It never prints
// the content of the supports.
func (ReceiptRenderer) Render(r domain.Receipt) ([]byte, error) {
	if r.FilingNumber == "" {
		return nil, errors.New("pdf: the filing number is required")
	}

	p := fpdf.New("P", "mm", "A4", "")
	p.SetCompression(false)
	// Without this fpdf writes the catalog from a map, so the bytes vary between runs.
	p.SetCatalogSort(true)
	p.SetCreationDate(r.FiledAt)
	// Without this fpdf stamps the current time, and the file changes on every download.
	p.SetModificationDate(r.FiledAt)
	p.SetTitle("Radicado del siniestro "+r.FilingNumber, true)
	p.SetAuthor("Solventa", true)
	tr := p.UnicodeTranslatorFromDescriptor("")
	p.SetMargins(20, 20, 20)
	p.AddPage()

	p.SetFont("Helvetica", "B", 20)
	p.CellFormat(0, 10, tr("Solventa"), "", 1, "L", false, 0, "")
	p.SetFont("Helvetica", "B", 14)
	p.CellFormat(0, 8, tr("Radicado del siniestro "+r.FilingNumber), "", 1, "L", false, 0, "")
	p.Ln(4)

	section(p, tr, "Radicación")
	row(p, tr, "Número de radicado", r.FilingNumber)
	row(p, tr, "Estado", statusName(r.Status))
	row(p, tr, "Radicado el", dateTime(r.FiledAt))

	section(p, tr, "Póliza afectada")
	row(p, tr, "Número de póliza", r.PolicyID)
	if r.Product != "" {
		row(p, tr, "Producto", r.Product)
	}
	if r.InsuredObject != "" {
		row(p, tr, "Bien asegurado", r.InsuredObject)
	}

	section(p, tr, "Hecho")
	row(p, tr, "Tipo de siniestro", r.IncidentLabel)
	row(p, tr, "Fecha y hora del hecho", dateTime(r.OccurredAt))
	p.SetFont("Helvetica", "B", 10)
	p.CellFormat(60, 6, tr("Lugar"), "", 0, "L", false, 0, "")
	p.SetFont("Helvetica", "", 10)
	p.MultiCell(0, 6, tr(r.Place), "", "L", false)

	section(p, tr, "Soportes adjuntos")
	s := r.Supports
	row(p, tr, "Obligatorios", fmt.Sprintf("%d de %d", s.RequiredAttached, s.RequiredTotal))
	row(p, tr, "Archivos", strconv.Itoa(s.Files))
	for _, item := range s.Items {
		row(p, tr, item.Label, files(len(item.Files)))
	}

	section(p, tr, "Qué sigue")
	p.SetFont("Helvetica", "", 10)
	for _, line := range nextSteps {
		p.MultiCell(0, 5, tr(line), "", "L", false)
		p.Ln(1)
	}

	var buf bytes.Buffer
	if err := p.Output(&buf); err != nil {
		return nil, fmt.Errorf("pdf: %w", err)
	}
	return buf.Bytes(), nil
}

func statusName(s domain.Status) string {
	if name, ok := statusNames[s]; ok {
		return name
	}
	return string(s)
}

func files(n int) string {
	if n == 1 {
		return "1 archivo"
	}
	return strconv.Itoa(n) + " archivos"
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

// dateTime prints the instant in Colombian time, which is how the insured
// reads it.
func dateTime(t time.Time) string {
	return t.In(domain.ColombiaTime).Format("02/01/2006 15:04") + " (hora de Colombia)"
}
