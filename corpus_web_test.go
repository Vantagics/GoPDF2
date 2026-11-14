package gopdf

// Targeted assertions for the web-downloaded corpus additions
// (see test/corpus). Each test pins concrete extraction quality checks
// discovered while probing the corpus, so regressions in parsing
// (tables, formulas, paragraphs, fonts) are caught by go test.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- PDF 2.0 Brotli-compressed sample (pdf.js) --------------------------

func TestCorpusWeb_BrotliFilter(t *testing.T) {
	data := corpusData(t, "Brotli-Prototype-FileA.pdf")
	count, err := GetSourcePDFPageCountFromBytes(data)
	if err != nil {
		t.Fatalf("page count: %v", err)
	}
	if count != 25 {
		t.Fatalf("page count = %d, want 25", count)
	}
	lines, err := ExtractPageLines(data, 0)
	if err != nil || len(lines) == 0 {
		t.Fatalf("page 0 lines: %d lines, err %v", len(lines), err)
	}
	// metadata: the /Info dict lives in the xref stream; the producer is
	// an AutoCAD plot driver
	info, err := GetPDFInfo(data)
	if err != nil {
		t.Fatalf("info: %v", err)
	}
	if info.Producer == "" {
		t.Fatal("producer is empty — /Info not read from xref stream")
	}
}

// --- arXiv paper (two-column, formulas, citation kerning, CM fonts) -----

func TestCorpusWeb_BERTArxiv(t *testing.T) {
	data := corpusData(t, "arxiv_bert.pdf")
	lines, err := ExtractPageLines(data, 0)
	if err != nil {
		t.Fatalf("lines: %v", err)
	}
	var all strings.Builder
	for _, ln := range lines {
		all.WriteString(ln.Text)
		all.WriteString("\n")
	}
	text := all.String()
	for _, want := range []string{
		"Bidirectional Transformers for",
		"{jacobdevlin,mingweichang,kentonl,kristout}@google.com",
		"al., 2018a;", // TJ-array kerning must not swallow inter-word spaces
		"language repre", // ligature-split word
	} {
		if !strings.Contains(text, want) {
			t.Errorf("page 0 text missing %q", want)
		}
	}
	if !strings.Contains(text, "ﬁne-") && !strings.Contains(text, "fine-") {
		t.Errorf("ﬁ ligature (Differences [2 /fi]) not decoded: %q", text[:min(len(text), 400)])
	}
	if strings.Contains(text, "\x02") {
		t.Error("raw byte \\x02 leaked — /Differences for ligature not applied")
	}
	if strings.Contains(text, "fjacobdevlin") {
		t.Error("CMSY braceleft decoded as 'f' — TeX symbol encoding missing")
	}
}

// --- GNU gawk manual (601 pages, long text) ------------------------------

func TestCorpusWeb_GawkManual(t *testing.T) {
	data := corpusData(t, "gawk.pdf")
	count, err := GetSourcePDFPageCountFromBytes(data)
	if err != nil {
		t.Fatalf("page count: %v", err)
	}
	if count < 500 {
		t.Fatalf("page count = %d, want >= 500", count)
	}
	// front matter pages may be blank; find the first page with real text
	found := false
	for p := 0; p < 40 && !found; p++ {
		lines, err := ExtractPageLines(data, p)
		if err != nil {
			t.Fatalf("page %d lines: %v", p, err)
		}
		if len(lines) >= 5 {
			found = true
		}
	}
	if !found {
		t.Fatal("no page with text found in the first 40 pages")
	}
}

// --- IRS forms: ruled tables + AcroForm fields ---------------------------

func TestCorpusWeb_1040Tables(t *testing.T) {
	data := corpusData(t, "f1040.pdf")
	tables, err := ExtractTablesFromPage(data, 0)
	if err != nil {
		t.Fatalf("tables: %v", err)
	}
	if len(tables) < 3 {
		t.Fatalf("tables = %d, want >= 3", len(tables))
	}
	found := false
	for _, tb := range tables {
		for _, row := range tb.Rows {
			for _, c := range row {
				if strings.Contains(c.Text, "Permanently and totally disabled") {
					found = true
				}
			}
		}
	}
	if !found {
		t.Error("cell text 'Permanently and totally disabled' not found — degenerate row spacing scrambles word order")
	}
}

func TestCorpusWeb_W4FormFields(t *testing.T) {
	data := corpusData(t, "fw4.pdf")
	fields, err := ExtractFormFieldsFromPage(data, 0)
	if err != nil {
		t.Fatalf("fields: %v", err)
	}
	if len(fields) < 10 {
		t.Fatalf("fields = %d, want >= 10", len(fields))
	}
	foundNamed := false
	checkboxOff := false
	for _, f := range fields {
		if strings.Contains(f.Name, "topmostSubform") {
			foundNamed = true
		}
		if f.Type == "button" && f.Value == "Off" {
			checkboxOff = true
		}
	}
	if !foundNamed {
		t.Error("hierarchical field names (topmostSubform…) missing — /Kids tree not walked")
	}
	if !checkboxOff {
		t.Error("checkbox /V /Off state not read")
	}
}

func TestCorpusWeb_FormTwoPages(t *testing.T) {
	data := corpusData(t, "form_two_pages.pdf")
	fields, err := ExtractAllFormFields(data)
	if err != nil {
		t.Fatalf("fields: %v", err)
	}
	total := 0
	for _, fs := range fields {
		total += len(fs)
	}
	if total < 4 {
		t.Fatalf("total fields = %d, want >= 4 (Text1/Text2/Text3/Text4/Button…)", total)
	}
	// Text1 lives behind a /Kids indirection
	names := ""
	for _, fs := range fields {
		for _, f := range fs {
			names += f.Name + ","
		}
	}
	for _, want := range []string{"Text1", "Text2", "Text3"} {
		if !strings.Contains(names, want) {
			t.Errorf("field %q missing (have %s)", want, names)
		}
	}
}

// --- Degenerate leading (pdf.js bug1057544) ------------------------------

func TestCorpusWeb_DegenerateLeading(t *testing.T) {
	data := corpusData(t, "bug1057544.pdf")
	lines, err := ExtractPageLines(data, 0)
	if err != nil {
		t.Fatalf("lines: %v", err)
	}
	if len(lines) != 4 {
		t.Fatalf("lines = %d, want 4 (one per drawn line)", len(lines))
	}
	want := []string{
		"An Annual Report marks the",
		"end of a year’s work, but it is",
		"also an opportunity to reflect",
		"on the principles that",
	}
	for i, w := range want {
		if got := strings.TrimSpace(lines[i].Text); got != w {
			t.Errorf("line %d = %q, want %q", i, lines[i].Text, w)
		}
	}
}

// --- GBK text in a Type3 wrapper with hex-escaped BaseFont ----------------

func TestCorpusWeb_SimFangGBK(t *testing.T) {
	data := corpusData(t, "SimFang-variant.pdf")
	lines, err := ExtractPageLines(data, 0)
	if err != nil || len(lines) == 0 {
		t.Fatalf("lines: %d, err %v", len(lines), err)
	}
	text := lines[0].Text
	// 全世界无产者，联合起来！ drawn with GBK bytes under WinAnsiEncoding
	for _, want := range []string{"全", "世", "界", "无", "产", "者", "联", "合", "起", "来"} {
		if !strings.Contains(text, want) {
			t.Errorf("GBK text missing %q in %q", want, text)
		}
	}
	if strings.Contains(text, "È") || strings.Contains(text, "Â") {
		t.Errorf("GBK bytes decoded as Latin-1 mojibake: %q", text)
	}
}

// --- Symbol / Type3 / odd encodings must not blow up ----------------------

func TestCorpusWeb_SymbolFonts(t *testing.T) {
	for _, name := range []string{
		"ZapfDingbats.pdf",
		"Test-plusminus.pdf",
		"Type3WordSpacing.pdf",
		"ContentStreamNoCycleType3insideType3.pdf",
		"TrueType_without_cmap.pdf",
		"90ms_rksj_h_sample.pdf",
		"encrypted-attachment.pdf",
		"bug1937438_af_from_latex.pdf",
		"bug1937438_mml_from_latex.pdf",
		"w3c_dummy.pdf",
	} {
		t.Run(name, func(t *testing.T) {
			data := corpusData(t, name)
			count, err := GetSourcePDFPageCountFromBytes(data)
			if err != nil || count == 0 {
				t.Fatalf("page count: %d, err %v", count, err)
			}
			for p := 0; p < count; p++ {
				if _, err := ExtractPageLines(data, p); err != nil {
					t.Fatalf("page %d lines: %v", p, err)
				}
				if _, err := ExtractPageParagraphs(data, p); err != nil {
					t.Fatalf("page %d paragraphs: %v", p, err)
				}
			}
		})
	}
}

// --- large IRS instructions: 126 pages must extract fast ------------------

func TestCorpusWeb_IRSInstructions(t *testing.T) {
	data := corpusData(t, "i1040gi.pdf")
	count, err := GetSourcePDFPageCountFromBytes(data)
	if err != nil {
		t.Fatalf("page count: %v", err)
	}
	if count < 100 {
		t.Fatalf("page count = %d, want >= 100", count)
	}
	lines, err := ExtractPageLines(data, 1)
	if err != nil || len(lines) < 5 {
		t.Fatalf("page 1 lines: %d, err %v", len(lines), err)
	}
}

// --- round-trip: generate with our own writer, read back ------------------

func TestCorpusWeb_RoundTripOwnOutput(t *testing.T) {
	outDir := t.TempDir()
	out := filepath.Join(outDir, "roundtrip.pdf")
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
	pdf.Cell(nil, "Round trip text with kerning and spaces.")
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
	if !strings.Contains(lines[0].Text, "Round trip text") {
		t.Errorf("round-trip text mismatch: %q", lines[0].Text)
	}
}
