package domain

// CatalogVersionV1 names the catalog of claim types and supports of the web
// filing (HU-W2.1-01, decision D4). It is not defined by the business yet: if
// it changes, it changes its version, and every claim keeps the version it
// was filed with.
const CatalogVersionV1 = "claim-catalog-v1"

// Claim types of the catalog that the mobile assistant (HU-M2.2-01) does not
// offer. IncidentType.Valid, the rule of the mobile filing, does not accept
// them.
const (
	IncidentGlass       IncidentType = "glass"
	IncidentWaterDamage IncidentType = "water_damage"
)

// FileFormat is the format of a support, told by the content of the file.
type FileFormat string

const (
	FormatJPEG FileFormat = "jpeg"
	FormatPNG  FileFormat = "png"
	FormatPDF  FileFormat = "pdf"
)

// ContentType is the media type of the format.
func (f FileFormat) ContentType() string {
	switch f {
	case FormatJPEG:
		return "image/jpeg"
	case FormatPNG:
		return "image/png"
	case FormatPDF:
		return "application/pdf"
	}
	return ""
}

// FormatOf maps a media type back to its format.
func FormatOf(contentType string) (FileFormat, bool) {
	for _, f := range []FileFormat{FormatJPEG, FormatPNG, FormatPDF} {
		if f.ContentType() == contentType {
			return f, true
		}
	}
	return "", false
}

// Codes of the supports of the catalog.
const (
	SupportVehiclePhotos      = "vehicle_photos"
	SupportDriverDocument     = "driver_document"
	SupportTrafficReport      = "traffic_report"
	SupportPoliceReport       = "police_report"
	SupportGlassPhotos        = "glass_photos"
	SupportSupportingDocument = "supporting_document"
	SupportDamagePhotos       = "damage_photos"
	SupportRepairQuote        = "repair_quote"
)

// Requirement is one support a claim type asks for. MinFiles is 0 when the
// support is optional.
type Requirement struct {
	Code     string
	Label    string
	Required bool
	MinFiles int
	MaxFiles int
	Formats  []FileFormat
}

// Accepts tells whether a file of the format can answer the requirement.
func (r Requirement) Accepts(f FileFormat) bool {
	for _, accepted := range r.Formats {
		if accepted == f {
			return true
		}
	}
	return false
}

// IncidentOption is a claim type of the catalog: the line it is filed under,
// the label the insured sees and the supports it asks for.
type IncidentOption struct {
	Type         IncidentType
	Line         PolicyLine
	Label        string
	Requirements []Requirement
}

// Requirement returns the requirement of the type with the code.
func (o IncidentOption) Requirement(code string) (Requirement, bool) {
	for _, r := range o.Requirements {
		if r.Code == code {
			return r, true
		}
	}
	return Requirement{}, false
}

// Catalog is a version of the claim types and their supports. A type belongs
// to a single line.
type Catalog struct {
	Version   string
	Incidents []IncidentOption
}

// Incident returns the claim type of the catalog.
func (c Catalog) Incident(t IncidentType) (IncidentOption, bool) {
	for _, o := range c.Incidents {
		if o.Type == t {
			return o, true
		}
	}
	return IncidentOption{}, false
}

// OptionsFor returns, in the order of the catalog, the claim types that can be
// filed on the policy: those of its line that it covers.
func (c Catalog) OptionsFor(p Policy) []IncidentOption {
	var out []IncidentOption
	for _, o := range c.Incidents {
		if p.Covers(c, o.Type) {
			out = append(out, o)
		}
	}
	return out
}

// CatalogV1 returns the catalog of the mockup (Autos and Hogar). Travel is
// parametric and has no claim types that are filed. Each call returns a fresh
// copy, so no caller can change the catalog of the others.
func CatalogV1() Catalog {
	image := func() []FileFormat { return []FileFormat{FormatJPEG, FormatPNG} }
	document := func() []FileFormat { return []FileFormat{FormatPDF, FormatJPEG, FormatPNG} }

	vehiclePhotos := func(required bool) Requirement {
		r := Requirement{Code: SupportVehiclePhotos, Label: "Fotos del vehículo", MaxFiles: 10, Formats: image()}
		if required {
			r.Required, r.MinFiles = true, 4
		}
		return r
	}
	driverDocument := func(required bool) Requirement {
		r := Requirement{Code: SupportDriverDocument, Label: "Documento del conductor", MaxFiles: 2, Formats: document()}
		if required {
			r.Required, r.MinFiles = true, 1
		}
		return r
	}
	trafficReport := func() Requirement {
		return Requirement{Code: SupportTrafficReport, Label: "Informe de tránsito o croquis", Required: true,
			MinFiles: 1, MaxFiles: 3, Formats: document()}
	}

	return Catalog{
		Version: CatalogVersionV1,
		Incidents: []IncidentOption{
			{Type: IncidentCollision, Line: LineAuto, Label: "Colisión con daños al vehículo",
				Requirements: []Requirement{vehiclePhotos(true), driverDocument(true), trafficReport()}},
			{Type: IncidentTheft, Line: LineAuto, Label: "Hurto",
				Requirements: []Requirement{
					{Code: SupportPoliceReport, Label: "Denuncia ante autoridad", Required: true, MinFiles: 1, MaxFiles: 3, Formats: document()},
					driverDocument(true), vehiclePhotos(false)}},
			{Type: IncidentThirdPartyDamage, Line: LineAuto, Label: "Daño a terceros",
				Requirements: []Requirement{vehiclePhotos(true), driverDocument(true), trafficReport()}},
			{Type: IncidentGlass, Line: LineAuto, Label: "Cristales",
				Requirements: []Requirement{
					{Code: SupportGlassPhotos, Label: "Fotos de los cristales", Required: true, MinFiles: 2, MaxFiles: 6, Formats: image()},
					driverDocument(false)}},
			{Type: IncidentOther, Line: LineAuto, Label: "Otro",
				Requirements: []Requirement{
					{Code: SupportSupportingDocument, Label: "Documento de soporte", Required: true, MinFiles: 1, MaxFiles: 5, Formats: document()}}},
			{Type: IncidentWaterDamage, Line: LineHome, Label: "Daño por agua",
				Requirements: []Requirement{
					{Code: SupportDamagePhotos, Label: "Fotos del daño", Required: true, MinFiles: 2, MaxFiles: 10, Formats: image()},
					{Code: SupportRepairQuote, Label: "Factura o cotización de la reparación", Required: true, MinFiles: 1, MaxFiles: 3, Formats: document()}}},
		},
	}
}
