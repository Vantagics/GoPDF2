package gopdf

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ============================================================
// Generation → parsing round-trip tests for the four core
// capabilities: tables, formulas, paragraphs, fonts. Each test
// builds a document with the writer API and verifies structure
// through the extraction APIs.
// ============================================================

// --- Tables ---------------------------------------------------------------

func TestRoundtrip_Table(t *testing.T) {
	pdf := &GoPdf{}
	pdf.Start(Config{PageSize: *PageSizeA4})
	pdf.AddPage()
	font := filepath.Join("test", "res", "LiberationSerif-Regular.ttf")
	if _, err := os.Stat(font); err != nil {
		t.Skip("Liberation font not available")
	}
	if err := pdf.AddTTFFont("lib", font); err != nil {
		t.Fatal(err)
	}
	if err := pdf.SetFont("lib", "", 10); err != nil {
		t.Fatal(err)
	}
	tbl := pdf.NewTableLayout(40, 40, 20, 100)
	tbl.AddColumn("Name", 120, "Left")
	tbl.AddColumn("City", 120, "Left")
	tbl.AddRow([]string{"Alice Smith", "Hangzhou"})
	tbl.AddRow([]string{"Bob Jones", "Berlin"})
	if err := tbl.DrawTable(); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "table.pdf")
	if err := pdf.WritePdf(out); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	tables, err := ExtractTablesFromPage(data, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(tables) == 0 {
		t.Fatal("no table detected in our own generated table")
	}
	// the largest detected table is our 3-line (header + 2 rows) grid
	tb := tables[0]
	if len(tb.Rows) < 3 {
		t.Fatalf("table rows = %d, want >= 3", len(tb.Rows))
	}
	if !strings.Contains(tb.Rows[0][0].Text, "Name") {
		t.Errorf("header cell: %q", tb.Rows[0][0].Text)
	}
	if !strings.Contains(tb.Rows[1][0].Text, "Alice Smith") {
		t.Errorf("row 1 cell: %q", tb.Rows[1][0].Text)
	}
	if !strings.Contains(tb.Rows[2][1].Text, "Berlin") {
		t.Errorf("row 2 city cell: %q", tb.Rows[2][1].Text)
	}
}

// --- Formulas -------------------------------------------------------------

func TestRoundtrip_Formula(t *testing.T) {
	pdf := &GoPdf{}
	pdf.Start(Config{PageSize: *PageSizeA4})
	pdf.AddPage()
	font := filepath.Join("test", "res", "LiberationSerif-Regular.ttf")
	if _, err := os.Stat(font); err != nil {
		t.Skip("Liberation font not available")
	}
	if err := pdf.AddTTFFont("lib", font); err != nil {
		t.Fatal(err)
	}
	if err := pdf.SetFont("lib", "", 12); err != nil {
		t.Fatal(err)
	}
	pdf.Cell(nil, "The quadratic formula:")
	if _, err := pdf.DrawFormula(40, 80, "x = \\frac{-b \\pm \\sqrt{b^2 - 4ac}}{2a}", 12, nil); err != nil {
		t.Fatalf("DrawFormula: %v", err)
	}
	out := filepath.Join(t.TempDir(), "formula.pdf")
	if err := pdf.WritePdf(out); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	text, err := ExtractPageText(data, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"quadratic formula", "x", "b", "4ac", "2a"} {
		if !strings.Contains(text, want) {
			t.Errorf("formula text missing %q", want)
		}
	}
}

// --- Paragraphs -----------------------------------------------------------

func TestRoundtrip_Paragraphs(t *testing.T) {
	pdf := &GoPdf{}
	pdf.Start(Config{PageSize: *PageSizeA4})
	pdf.AddPage()
	font := filepath.Join("test", "res", "LiberationSerif-Regular.ttf")
	if _, err := os.Stat(font); err != nil {
		t.Skip("Liberation font not available")
	}
	if err := pdf.AddTTFFont("lib", font); err != nil {
		t.Fatal(err)
	}
	if err := pdf.SetFont("lib", "", 11); err != nil {
		t.Fatal(err)
	}
	paras := []string{
		"The first paragraph discusses the origin of the document format and its history.",
		"The second paragraph turns to the technical details of object streams and cross reference tables.",
		"A third paragraph concludes the introduction and points toward the implementation chapters.",
	}
	y := 40.0
	for _, p := range paras {
		pdf.SetXY(40, y)
		if err := pdf.Cell(&Rect{W: 480, H: 60}, p); err != nil {
			t.Fatal(err)
		}
		y += 70
	}
	out := filepath.Join(t.TempDir(), "paras.pdf")
	if err := pdf.WritePdf(out); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ExtractPageParagraphs(data, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 3 {
		t.Fatalf("paragraphs = %d, want >= 3", len(got))
	}
	joined := ""
	for _, pa := range got {
		joined += pa.Text + "\n"
	}
	for _, want := range []string{"origin of the document format", "object streams", "implementation chapters"} {
		if !strings.Contains(joined, want) {
			t.Errorf("paragraph text missing %q", want)
		}
	}
}

// --- Fonts: CJK round-trip + styles --------------------------------------

func TestRoundtrip_CJKFont(t *testing.T) {
	fontPath := findCJKFont(t)
	if fontPath == "" {
		t.Skip("no system CJK font available")
	}
	pdf := &GoPdf{}
	pdf.Start(Config{PageSize: *PageSizeA4})
	pdf.AddPage()
	if err := pdf.AddTTFFont("cjk", fontPath); err != nil {
		t.Fatal(err)
	}
	if err := pdf.SetFont("cjk", "", 14); err != nil {
		t.Fatal(err)
	}
	pdf.Cell(nil, "中文测试：表格、公式、段落与字体。")
	out := filepath.Join(t.TempDir(), "cjk.pdf")
	if err := pdf.WritePdf(out); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	lines, err := ExtractPageLines(data, 0)
	if err != nil || len(lines) == 0 {
		t.Fatalf("lines: %d, err %v", len(lines), err)
	}
	got := strings.Join(func() []string {
		var s []string
		for _, ln := range lines {
			s = append(s, ln.Text)
		}
		return s
	}(), "")
	for _, want := range []string{"中", "文", "测", "试", "表", "格", "字", "体"} {
		if !strings.Contains(got, want) {
			t.Errorf("CJK round-trip missing %q in %q", want, got)
		}
	}
}

func TestRoundtrip_FontStyles(t *testing.T) {
	pdf := &GoPdf{}
	pdf.Start(Config{PageSize: *PageSizeA4})
	pdf.AddPage()
	font := filepath.Join("test", "res", "LiberationSerif-Regular.ttf")
	if _, err := os.Stat(font); err != nil {
		t.Skip("Liberation font not available")
	}
	if err := pdf.AddTTFFont("lib", font); err != nil {
		t.Fatal(err)
	}
	if err := pdf.AddTTFFontWithOption("libbold", font, TtfOption{Style: Bold}); err != nil {
		t.Fatal(err)
	}
	if err := pdf.AddTTFFontWithOption("libitalic", font, TtfOption{Style: Italic}); err != nil {
		t.Fatal(err)
	}
	if err := pdf.SetFont("lib", "", 12); err != nil {
		t.Fatal(err)
	}
	pdf.SetXY(40, 40)
	pdf.Cell(nil, "regular style text")
	if err := pdf.SetFont("libbold", "B", 12); err != nil {
		t.Fatal(err)
	}
	pdf.SetXY(40, 60)
	pdf.Cell(nil, "bold style text")
	if err := pdf.SetFont("libitalic", "I", 12); err != nil {
		t.Fatal(err)
	}
	pdf.SetXY(40, 80)
	pdf.Cell(nil, "italic style text")
	out := filepath.Join(t.TempDir(), "styles.pdf")
	if err := pdf.WritePdf(out); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	texts, err := ExtractTextFromPage(data, 0)
	if err != nil {
		t.Fatal(err)
	}
	var boldSeen, italicSeen, regularSeen bool
	for _, tx := range texts {
		switch {
		case strings.Contains(tx.Text, "bold style text"):
			boldSeen = true
			if tx.FontFlags&1 == 0 {
				t.Errorf("bold text missing the bold flag")
			}
		case strings.Contains(tx.Text, "italic style text"):
			italicSeen = true
			if tx.FontFlags&2 == 0 {
				t.Errorf("italic text missing the italic flag")
			}
		case strings.Contains(tx.Text, "regular style text"):
			regularSeen = true
			if tx.FontFlags != 0 {
				t.Errorf("regular text has style flags: %d", tx.FontFlags)
			}
		}
	}
	if !boldSeen || !italicSeen || !regularSeen {
		t.Errorf("missing runs: bold=%v italic=%v regular=%v", boldSeen, italicSeen, regularSeen)
	}
}
