package gopdf

// Generation-capability showcase: build one document exercising the
// table, formula, and complex-graphics writers, then read it back with
// the extraction pipeline and verify the round trip. This pins the
// generation APIs against real-world reuse of the parsed artifacts.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGenerationShowcase_TablesFormulasGraphics renders a three-page
// document (ruled table, math formulas, complex vector graphics) and
// verifies extraction recovers the structure.
func TestGenerationShowcase_TablesFormulasGraphics(t *testing.T) {
	pdf := &GoPdf{}
	pdf.Start(Config{PageSize: *PageSizeA4})
	if err := pdf.AddTTFFont("lib", "test/res/LiberationSerif-Regular.ttf"); err != nil {
		t.Fatal(err)
	}
	if err := pdf.SetFont("lib", "", 11); err != nil {
		t.Fatal(err)
	}

	// --- page 1: ruled table with header, styled rows and colspan -------
	pdf.AddPage()
	table := pdf.NewTableLayout(50, 60, 22, 0)
	table.SetAutoRowHeight(true)
	table.SetTableStyle(CellStyle{
		BorderStyle: BorderStyle{
			Top: true, Left: true, Right: true, Bottom: true,
			Width: 0.8, RGBColor: RGBColor{R: 40, G: 40, B: 40},
		},
	})
	table.SetHeaderStyle(CellStyle{
		BorderStyle: BorderStyle{Top: true, Left: true, Right: true, Bottom: true, Width: 1.0},
		FillColor:   RGBColor{R: 31, G: 78, B: 121},
		TextColor:   RGBColor{R: 255, G: 255, B: 255},
		FontSize:    12,
	})
	table.AddColumn("Quarter", 80, "L")
	table.AddColumn("Revenue ($M)", 90, "R")
	table.AddColumn("Cost ($M)", 80, "R")
	table.AddColumn("Margin %", 70, "R")
	rows := [][]string{
		{"Q1", "1250.5", "812.3", "35.0"},
		{"Q2", "1310.2", "840.1", "35.9"},
		{"Q3", "1425.8", "890.9", "37.5"},
		{"Q4", "1502.4", "920.2", "38.8"},
		{"FY", "5488.9", "3463.5", "36.9"},
	}
	for _, r := range rows {
		table.AddRow(r)
	}
	if err := table.DrawTable(); err != nil {
		t.Fatal(err)
	}

	// --- page 2: math formulas ------------------------------------------
	pdf.AddPage()
	formulas := []string{
		`x = \frac{-b \pm \sqrt{b^2 - 4ac}}{2a}`,
		`\int_0^\infty e^{-x^2} dx = \frac{\sqrt{\pi}}{2}`,
		`\sum_{i=1}^{n} i = \frac{n(n+1)}{2}`,
		`E = mc^2`,
		`\alpha + \beta = \gamma`,
	}
	y := 60.0
	for _, f := range formulas {
		h, err := pdf.DrawFormula(60, y, f, 13, &FormulaOption{Italic: true})
		if err != nil {
			t.Fatalf("formula %q: %v", f, err)
		}
		y += h + 30
	}

	// --- page 3: complex vector graphics ---------------------------------
	pdf.AddPage()
	pdf.SetStrokeColor(200, 30, 30)
	pdf.SetLineWidth(1.5)
	// cubic bezier
	pdf.Curve(50, 700, 150, 780, 250, 620, 350, 700, "S")
	// closed polygon
	pdf.SetFillColor(30, 120, 200)
	pdf.Polygon([]Point{{X: 400, Y: 650}, {X: 480, Y: 720}, {X: 560, Y: 650}, {X: 480, Y: 600}}, "FD")
	// polyline
	pdf.Polyline([]Point{{X: 50, Y: 560}, {X: 150, Y: 500}, {X: 250, Y: 560}, {X: 350, Y: 500}})
	// arc sector
	pdf.Sector(450, 540, 60, 0, 270, "S")
	// oval
	pdf.Oval(60, 430, 200, 360)
	// rounded rectangle
	if err := pdf.Rectangle(260, 360, 420, 430, "S", 18, 8); err != nil {
		t.Fatal(err)
	}
	// dashed + dotted lines
	pdf.SetLineType("dashed")
	pdf.Line(50, 320, 350, 320)
	pdf.SetLineType("dotted")
	pdf.Line(50, 300, 350, 300)
	pdf.SetLineType("")
	// transparency group fill
	if err := pdf.SetTransparency(Transparency{Alpha: 0.5}); err != nil {
		t.Fatal(err)
	}
	pdf.SetFillColor(255, 200, 0)
	if err := pdf.RectFromLowerLeftWithOpts(DrawableRectOptions{
		X:          400,
		Y:          250,
		Rect:       Rect{W: 120, H: 80},
		PaintStyle: FillPaintStyle,
	}); err != nil {
		t.Fatal(err)
	}
	if err := pdf.SetTransparency(Transparency{Alpha: 1}); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "showcase.pdf")
	if err := pdf.WritePdf(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// --- read back --------------------------------------------------------
	doc, err := OpenPDFDocument(data)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if doc.PageCount() != 3 {
		t.Errorf("page count = %d, want 3", doc.PageCount())
	}

	// formulas round-trip as text runs
	fText, err := doc.Text(1)
	if err != nil {
		t.Fatalf("text p1: %v", err)
	}
	if !strings.Contains(fText, "mc") || !strings.Contains(fText, "dx") {
		t.Errorf("formula text not recovered: %.120q", fText)
	}

	// graphics page: strokes, fills, rects must be parseable
	dr, err := doc.Drawings(2)
	if err != nil {
		t.Fatalf("drawings p2: %v", err)
	}
	if len(dr) < 8 {
		t.Errorf("drawings = %d, want >= 8 paths on graphics page", len(dr))
	}
	var rects, curves, polylines int
	for _, d := range dr {
		if d.IsRect {
			rects++
		} else if len(d.Points) > 8 {
			curves++ // beziers/arcs/ovals flatten to many segments
		} else if len(d.Points) >= 2 {
			polylines++
		}
	}
	if curves < 2 {
		t.Errorf("curve-like paths = %d, want >= 2 (bezier, oval, sector)", curves)
	}
	if rects < 1 {
		t.Errorf("rect paths = %d, want >= 1", rects)
	}
}

// TestGenerationShowcase_TableRoundTrip builds a ruled table, then reads
// it back through the table detector and verifies the grid and cell text.
func TestGenerationShowcase_TableRoundTrip(t *testing.T) {
	pdf := &GoPdf{}
	pdf.Start(Config{PageSize: *PageSizeA4})
	pdf.AddPage()
	if err := pdf.AddTTFFont("lib", "test/res/LiberationSerif-Regular.ttf"); err != nil {
		t.Fatal(err)
	}
	if err := pdf.SetFont("lib", "", 10); err != nil {
		t.Fatal(err)
	}
	table := pdf.NewTableLayout(60, 70, 20, 0)
	table.SetTableStyle(CellStyle{
		BorderStyle: BorderStyle{
			Top: true, Left: true, Right: true, Bottom: true,
			Width: 0.7, RGBColor: RGBColor{R: 0, G: 0, B: 0},
		},
	})
	table.AddColumn("Region", 100, "L")
	table.AddColumn("Sales", 80, "R")
	table.AddColumn("Units", 60, "R")
	table.AddRow([]string{"North", "1200", "30"})
	table.AddRow([]string{"South", "980", "25"})
	table.AddRow([]string{"East", "1330", "41"})
	table.AddRow([]string{"West", "875", "22"})
	if err := table.DrawTable(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "table.pdf")
	if err := pdf.WritePdf(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	tables, err := ExtractTablesFromPage(data, 0)
	if err != nil {
		t.Fatalf("tables: %v", err)
	}
	if len(tables) == 0 {
		t.Fatal("no ruled table detected in generated document")
	}
	tb := tables[0]
	if len(tb.Rows) < 5 {
		t.Errorf("table rows = %d, want >= 5 (header + 4 data)", len(tb.Rows))
	}
	if tb.ColCount < 3 {
		t.Errorf("table cols = %d, want >= 3", tb.ColCount)
	}
	// cell text must round-trip
	var all strings.Builder
	for _, row := range tb.Rows {
		for _, c := range row {
			all.WriteString(c.Text)
			all.WriteString(" ")
		}
	}
	for _, marker := range []string{"North", "South", "1200", "1330"} {
		if !strings.Contains(all.String(), marker) {
			t.Errorf("cell text missing %q in %q", marker, all.String())
		}
	}
}
