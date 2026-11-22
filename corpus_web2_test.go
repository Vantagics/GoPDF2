package gopdf

// Targeted assertions for the second batch of web-downloaded corpus files
// (see test/corpus): complex tables, mathematical formulas, and
// graphically-heavy documents. Each test pins concrete extraction-quality
// checks discovered while probing the corpus, so regressions in parsing
// are caught by go test. Files skip when absent (CI clean checkouts).
//
// Corpus additions:
//   - berkshirehathaway.com 2024 annual report (financial tables)
//   - Wikipedia "Comparison of web browsers" export (dense borderless tables)
//   - CTAN lshort (LaTeX intro — math glyphs)
//   - pgf/TikZ manual (1324 pages of vector graphics)
//   - pgfplots manual (plot-heavy, ruled tables)
//   - CTAN comprehensive LaTeX symbol tables (604 pages of math glyphs)
//   - arXiv 1406.2661 (Generative Adversarial Nets — formulas)
//   - matplotlib user guide (1947 pages)
//   - TfL standard tube map (AES-256/R6 xref-stream encrypted)

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// --- Berkshire Hathaway 2024 annual report (ruled financial tables) ------

func TestCorpusWeb2_BerkshireTables(t *testing.T) {
	data := corpusData(t, "berkshire_2024_ar.pdf")
	doc, err := OpenPDFDocument(data)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if got := doc.PageCount(); got < 100 {
		t.Fatalf("page count = %d, want >= 100", got)
	}
	var tableCount, pagesWithTables int
	for i := 0; i < doc.PageCount(); i++ {
		tbs, err := doc.Tables(i)
		if err != nil {
			t.Fatalf("tables p%d: %v", i, err)
		}
		if len(tbs) > 0 {
			pagesWithTables++
			tableCount += len(tbs)
		}
	}
	if tableCount < 50 || pagesWithTables < 30 {
		t.Fatalf("ruled tables: %d on %d pages, want >= 50 on >= 30 pages", tableCount, pagesWithTables)
	}
	txt, err := doc.Text(0)
	if err != nil || !strings.Contains(strings.ToLower(txt), "berkshire") {
		t.Fatalf("cover text missing Berkshire: err=%v txt=%.80q", err, txt)
	}
}

// --- Wikipedia Comparison of web browsers (dense vector-rule tables) -----

func TestCorpusWeb2_WikiBrowsers(t *testing.T) {
	data := corpusData(t, "wiki_comparison_browsers.pdf")
	doc, err := OpenPDFDocument(data)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if got := doc.PageCount(); got != 29 {
		t.Fatalf("page count = %d, want 29", got)
	}
	all := corpusJoinDoc(doc, t)
	for _, marker := range []string{"Gecko", "Trident"} {
		if !strings.Contains(all, marker) {
			t.Errorf("missing browser engine marker %q", marker)
		}
	}
	// the comparison grids are drawn as hundreds of filled rectangles
	maxDrawings := 0
	for i := 0; i < doc.PageCount() && i < 10; i++ {
		dr, err := doc.Drawings(i)
		if err != nil {
			t.Fatalf("drawings p%d: %v", i, err)
		}
		if len(dr) > maxDrawings {
			maxDrawings = len(dr)
		}
	}
	if maxDrawings < 500 {
		t.Errorf("max drawings in first 10 pages = %d, want >= 500 (dense table rules)", maxDrawings)
	}
}

// --- lshort (LaTeX intro, math glyph extraction) -------------------------

func TestCorpusWeb2_LShortMathGlyphs(t *testing.T) {
	data := corpusData(t, "lshort.pdf")
	pages, err := ExtractTextFromAllPages(data)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(pages) < 140 {
		t.Fatalf("pages = %d, want >= 140", len(pages))
	}
	all := corpusJoinPages(pages)
	if !strings.Contains(all, "LaTeX") {
		t.Errorf("missing LaTeX marker")
	}
	// Greek letters and arrows must survive font CMap decoding
	for _, sym := range []string{"α", "π", "∞", "→", "λ"} {
		if !strings.Contains(all, sym) {
			t.Errorf("math glyph %q not extracted", sym)
		}
	}
}

// --- TikZ manual (1000+ pages of vector graphics) -------------------------

func TestCorpusWeb2_PGFManual(t *testing.T) {
	data := corpusData(t, "pgfmanual.pdf")
	doc, err := OpenPDFDocument(data)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if got := doc.PageCount(); got < 1000 {
		t.Fatalf("page count = %d, want >= 1000", got)
	}
	txt, err := doc.Text(0)
	if err != nil {
		t.Fatalf("text p0: %v", err)
	}
	if !strings.Contains(txt, "TikZ") && !strings.Contains(txt, "pgf") {
		t.Errorf("p0 text missing TikZ marker: %.80q", txt)
	}
	// sample drawings across the manual
	dr, err := doc.Drawings(100)
	if err != nil || len(dr) == 0 {
		t.Errorf("drawings p100 = %d, err %v; want vector content", len(dr), err)
	}
}

// --- pgfplots manual (plot-heavy, ruled tables) ---------------------------

func TestCorpusWeb2_PgfPlotsTables(t *testing.T) {
	data := corpusData(t, "pgfplots.pdf")
	doc, err := OpenPDFDocument(data)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if got := doc.PageCount(); got < 500 {
		t.Fatalf("page count = %d, want >= 500", got)
	}
	tables := 0
	for i := 0; i < doc.PageCount(); i++ {
		tbs, err := doc.Tables(i)
		if err != nil {
			t.Fatalf("tables p%d: %v", i, err)
		}
		tables += len(tbs)
	}
	if tables < 50 {
		t.Errorf("ruled tables = %d, want >= 50", tables)
	}
}

// --- comprehensive LaTeX symbol tables (glyph extraction at scale) --------

func TestCorpusWeb2_SymbolTablesGlyphs(t *testing.T) {
	data := corpusData(t, "symbols_comprehensive.pdf")
	pages, err := ExtractTextFromAllPages(data)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(pages) < 500 {
		t.Fatalf("pages = %d, want >= 500", len(pages))
	}
	all := corpusJoinPages(pages)
	for _, sym := range []string{"∑", "∫", "α", "∈", "⊕", "⊗", "≤", "√"} {
		if !strings.Contains(all, sym) {
			t.Errorf("math glyph %q not extracted from symbol tables", sym)
		}
	}
}

// --- arXiv GAN paper (formulas + two-column layout) -----------------------

func TestCorpusWeb2_GANPaper(t *testing.T) {
	data := corpusData(t, "arxiv_gan.pdf")
	lines, err := ExtractPageLines(data, 0)
	if err != nil {
		t.Fatalf("lines: %v", err)
	}
	var head strings.Builder
	for _, ln := range lines {
		head.WriteString(ln.Text)
		head.WriteString("\n")
	}
	if !strings.Contains(head.String(), "Generative Adversarial") {
		t.Errorf("title missing on p0: %q", head.String())
	}
	pages, err := ExtractTextFromAllPages(data)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(pages) < 8 {
		t.Fatalf("pages = %d, want >= 8", len(pages))
	}
}

// --- matplotlib user guide (very large graphics-heavy manual) -------------

func TestCorpusWeb2_MatplotlibGuide(t *testing.T) {
	data := corpusData(t, "matplotlib_guide.pdf")
	doc, err := OpenPDFDocument(data)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if got := doc.PageCount(); got < 1900 {
		t.Fatalf("page count = %d, want >= 1900", got)
	}
	// images embedded across the guide (sample pages)
	found := false
	for i := 100; i < 120 && !found; i++ {
		im, err := doc.Images(i)
		if err != nil {
			t.Fatalf("images p%d: %v", i, err)
		}
		found = len(im) > 0
	}
	if !found {
		t.Errorf("no images found in pages 100-120")
	}
}

// --- TfL tube map: AES-256/R6 encryption in an xref-stream PDF ------------

func TestCorpusWeb2_TfLR6EncryptedNoSilentFailure(t *testing.T) {
	data := corpusData(t, "tfl_tube_map.pdf")
	// The file is R6-encrypted with a non-empty user password. The library
	// must fail EXPLICITLY — historically it silently returned 0 pages
	// because detectEncryption only looked at classic trailers.
	if detectEncryption(data) == 0 {
		t.Fatal("/Encrypt in the xref-stream dict not detected")
	}
	_, err := OpenPDFDocumentWithPassword(data, "")
	if err == nil {
		t.Fatal("empty password opened a password-protected PDF — silent auth bypass")
	}
	n, err := GetSourcePDFPageCountFromBytes(data)
	if err == nil && n > 0 {
		t.Fatalf("page count succeeded on encrypted file: %d pages", n)
	}
}

// --- helpers ---------------------------------------------------------------

func corpusJoinPages(pages map[int][]ExtractedText) string {
	var sb strings.Builder
	for i := 0; i < len(pages); i++ {
		for _, tx := range pages[i] {
			sb.WriteString(tx.Text)
		}
	}
	return sb.String()
}

func corpusJoinDoc(doc *PDFDoc, t *testing.T) string {
	t.Helper()
	var sb strings.Builder
	for i := 0; i < doc.PageCount(); i++ {
		txt, err := doc.Text(i)
		if err != nil {
			t.Fatalf("text p%d: %v", i, err)
		}
		sb.WriteString(txt)
	}
	return sb.String()
}

// silence unused import when corpus files are absent
var _ = errors.New
var _ = os.ReadFile
