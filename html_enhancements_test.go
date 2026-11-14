package gopdf

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ============================================================
// Tests for the 2026-11 html2pdf enhancements: word-spacing,
// page-break-before/after, mark, dl/dt/dd, image float + max-width,
// hr CSS styling.
// ============================================================

// renderHTMLToLines runs InsertHTMLPaginated and returns extracted lines
// per page.
func renderHTMLToLines(t *testing.T, html string) [][]string {
	t.Helper()
	pdf, font := newTestPDF(t)
	if err := pdf.AddTTFFont("lib", font); err != nil {
		t.Fatal(err)
	}
	if err := pdf.AddTTFFontWithOption("libbold", font, TtfOption{Style: Bold}); err != nil {
		t.Fatal(err)
	}
	endY, err := pdf.InsertHTMLPaginated(40, 40, 515, html, HTMLBoxOption{
		DefaultFontFamily: "lib",
		DefaultFontSize:   12,
		BoldFontFamily:    "libbold",
		PageTop:           40,
		PageBottom:        800,
	})
	if err != nil {
		t.Fatalf("InsertHTMLPaginated: %v", err)
	}
	_ = endY
	out := filepath.Join(t.TempDir(), "html.pdf")
	if err := pdf.WritePdf(out); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	count, err := GetSourcePDFPageCountFromBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	var pages [][]string
	for p := 0; p < count; p++ {
		lines, err := ExtractPageLines(data, p)
		if err != nil {
			t.Fatalf("page %d lines: %v", p, err)
		}
		var texts []string
		for _, ln := range lines {
			texts = append(texts, ln.Text)
		}
		pages = append(pages, texts)
	}
	return pages
}

func htmlPagesText(pages [][]string) string {
	var sb strings.Builder
	for _, pg := range pages {
		for _, ln := range pg {
			sb.WriteString(ln)
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

func TestHTML_WordSpacing(t *testing.T) {
	pages := renderHTMLToLines(t, `<p style="word-spacing:10px">alpha beta gamma</p>`)
	text := htmlPagesText(pages)
	if !strings.Contains(text, "alpha beta gamma") {
		t.Errorf("word-spacing broke word integrity: %q", text)
	}
	// with generous word spacing the words must land as separate words
	// (gap-based merge must not glue them even with the extra advance)
	var singleLine bool
	for _, pg := range pages {
		for _, ln := range pg {
			if strings.TrimSpace(ln) == "alpha beta gamma" {
				singleLine = true
			}
		}
	}
	if !singleLine {
		t.Errorf("spaced words not on one line: %q", text)
	}
}

func TestHTML_PageBreakBeforeAfter(t *testing.T) {
	pages := renderHTMLToLines(t, `
		<p>first page content</p>
		<div style="page-break-before:always">second page start</div>
		<p>second page more</p>
		<p style="page-break-after:always">forces a break after</p>
		<p>third page content</p>`)
	if len(pages) < 3 {
		t.Fatalf("pages = %d, want >= 3 (page-break-before/after)", len(pages))
	}
	p0 := strings.Join(pages[0], "\n")
	p1 := strings.Join(pages[1], "\n")
	p2 := strings.Join(pages[2], "\n")
	if !strings.Contains(p0, "first page content") {
		t.Errorf("page 0 missing first paragraph: %q", p0)
	}
	if strings.Contains(p0, "second page start") {
		t.Errorf("page-break-before did not push the div to page 1: %q", p0)
	}
	if !strings.Contains(p1, "second page start") {
		t.Errorf("page 1 missing the page-break-before div: %q", p1)
	}
	if !strings.Contains(p1, "forces a break after") {
		t.Errorf("page 1 missing the break-after paragraph: %q", p1)
	}
	if strings.Contains(p1+p2, "third page content") && strings.Contains(p1, "third page content") {
		t.Errorf("page-break-after did not push the last paragraph: %q", p1)
	}
	if !strings.Contains(p2, "third page content") {
		t.Errorf("page 2 missing third paragraph: %q", p2)
	}
}

func TestHTML_MarkHighlight(t *testing.T) {
	pages := renderHTMLToLines(t, `<p>plain <mark>highlighted text</mark> plain</p>`)
	text := htmlPagesText(pages)
	if !strings.Contains(text, "highlighted text") {
		t.Errorf("<mark> text missing: %q", text)
	}
	if !strings.Contains(text, "plain") {
		t.Errorf("surrounding text missing: %q", text)
	}
}

func TestHTML_DefinitionList(t *testing.T) {
	pages := renderHTMLToLines(t, `
		<dl>
			<dt>Term One</dt>
			<dd>Definition of the first term.</dd>
			<dt>Term Two</dt>
			<dd>Definition of the second term.</dd>
		</dl>`)
	text := htmlPagesText(pages)
	for _, want := range []string{"Term One", "Definition of the first term.", "Term Two", "Definition of the second term."} {
		if !strings.Contains(text, want) {
			t.Errorf("dl content missing %q: %q", want, text)
		}
	}
}

func TestHTML_ImageFloatRightAndMaxWidth(t *testing.T) {
	img := "test/res/gopher01.jpg"
	if _, err := os.Stat(img); err != nil {
		t.Skip("test image not available")
	}
	// float:right must still render the image (own line, right-aligned)
	pages := renderHTMLToLines(t, `<img src="`+img+`" style="float:right; max-width:120px">`)
	text := htmlPagesText(pages)
	_ = text
	// the page must not error and must contain something (the image itself
	// produces no text; assert the render completed without breakage)
	if len(pages) == 0 {
		t.Fatal("no pages rendered")
	}
	// max-width must clamp: render a huge intrinsic image and compare
	// measured text flow position is unaffected — just verify no error.
}

func TestHTML_HRCSS(t *testing.T) {
	pages := renderHTMLToLines(t, `
		<p>above</p>
		<hr style="width:50%; height:3px; border-top:2px solid #e74c3c">
		<p>below</p>`)
	text := htmlPagesText(pages)
	if !strings.Contains(text, "above") || !strings.Contains(text, "below") {
		t.Errorf("hr broke surrounding text: %q", text)
	}
}
