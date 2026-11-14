package gopdf

import (
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildMetaDoc creates a two-page doc with metadata, a two-level TOC and
// known text positions.
func buildMetaDoc(t *testing.T, path string) {
	t.Helper()
	pdf := &GoPdf{}
	pdf.Start(Config{PageSize: *PageSizeA4})
	pdf.AddTTFFont("lib", "test/res/LiberationSerif-Regular.ttf")
	pdf.SetFont("lib", "", 12)

	pdf.SetInfo(PdfInfo{
		Title:    "Parity Test Doc",
		Author:   "GoPDF2",
		Subject:  "PyMuPDF parity",
		Keywords: "pdf, golang, parity",
		Creator:  "creator-app",
		Producer: "GoPDF2",
	})

	pdf.AddPage()
	pdf.SetXY(50, 60)
	_ = pdf.Cell(nil, "Chapter one body text")
	pdf.SetXY(50, 300)
	_ = pdf.Cell(nil, "Deep in page one")

	pdf.AddPage()
	pdf.SetXY(50, 60)
	_ = pdf.Cell(nil, "Chapter two body text")

	if err := pdf.SetTOC([]TOCItem{
		{Level: 1, Title: "Chapter 1", PageNo: 1, Y: 50},
		{Level: 2, Title: "Details", PageNo: 1, Y: 290},
		{Level: 1, Title: "Chapter 2", PageNo: 2},
	}); err != nil {
		t.Fatal(err)
	}
	if err := pdf.WritePdf(path); err != nil {
		t.Fatal(err)
	}
}

// TestGetPDFInfo verifies metadata round trip through the /Info dict.
func TestGetPDFInfo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "meta.pdf")
	buildMetaDoc(t, path)
	data, _ := os.ReadFile(path)

	info, err := GetPDFInfo(data)
	if err != nil {
		t.Fatal(err)
	}
	if info.Title != "Parity Test Doc" {
		t.Errorf("Title = %q", info.Title)
	}
	if info.Author != "GoPDF2" {
		t.Errorf("Author = %q", info.Author)
	}
	if info.Keywords != "pdf, golang, parity" {
		t.Errorf("Keywords = %q", info.Keywords)
	}
	if info.Subject != "PyMuPDF parity" {
		t.Errorf("Subject = %q", info.Subject)
	}
	if info.Creator != "creator-app" || info.Producer != "GoPDF2" {
		t.Errorf("Creator/Producer = %q/%q", info.Creator, info.Producer)
	}
}

// TestExtractTOCRoundTrip verifies SetTOC -> write -> ExtractTOC returns the
// same level/title/page tree.
func TestExtractTOCRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "toc.pdf")
	buildMetaDoc(t, path)
	data, _ := os.ReadFile(path)

	entries, err := ExtractTOC(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		for i, e := range entries {
			t.Logf("%d: L%d %q p%d y%.0f", i, e.Level, e.Title, e.Page, e.Y)
		}
		t.Fatalf("got %d entries, want 3", len(entries))
	}
	want := []TOCEntry{
		{Level: 1, Title: "Chapter 1", Page: 1},
		{Level: 2, Title: "Details", Page: 1},
		{Level: 1, Title: "Chapter 2", Page: 2},
	}
	for i, w := range want {
		if entries[i].Level != w.Level || entries[i].Title != w.Title || entries[i].Page != w.Page {
			t.Errorf("entry %d = L%d %q p%d, want L%d %q p%d",
				i, entries[i].Level, entries[i].Title, entries[i].Page, w.Level, w.Title, w.Page)
		}
	}
	// Y positions: Chapter 1 -> 50, Details -> 290
	if e := entries[0]; e.Y < 40 || e.Y > 60 {
		t.Errorf("entry 0 Y = %.0f, want ~50", e.Y)
	}
	if e := entries[1]; e.Y < 280 || e.Y > 300 {
		t.Errorf("entry 1 Y = %.0f, want ~290", e.Y)
	}
}

// TestExtractTextInRect verifies region-scoped text extraction.
func TestExtractTextInRect(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clip.pdf")
	buildMetaDoc(t, path)
	data, _ := os.ReadFile(path)

	// only the lower text of page 1
	txt, err := ExtractTextInRect(data, 0, ClipRect{X: 0, Y: 250, W: 595, H: 200})
	if err != nil {
		t.Fatal(err)
	}
	if txt == "" || !contains(txt, "Deep in page one") {
		t.Errorf("clip text missing target: %q", txt)
	}
	if contains(txt, "Chapter one body") {
		t.Errorf("clip text leaked outside region: %q", txt)
	}

	words, err := ExtractTextWordsInRect(data, 0, ClipRect{X: 0, Y: 0, W: 595, H: 200})
	if err != nil {
		t.Fatal(err)
	}
	if len(words) == 0 || words[0].Text != "Chapter" {
		t.Errorf("top-region words = %v", words)
	}
}

// TestClipRender verifies clip-scoped rendering produces a smaller canvas.
func TestClipRender(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clip.pdf")
	buildMetaDoc(t, path)
	data, _ := os.ReadFile(path)

	full, err := RenderPageToImage(data, 0, RenderOption{DPI: 72})
	if err != nil {
		t.Fatal(err)
	}
	part, err := RenderPageToImage(data, 0, RenderOption{DPI: 72, Clip: &ClipRect{X: 0, Y: 0, W: 200, H: 150}})
	if err != nil {
		t.Fatal(err)
	}
	fb, pb := full.Bounds(), part.Bounds()
	if pb.Dx() >= fb.Dx() || pb.Dy() >= fb.Dy() {
		t.Errorf("clip render %dx%d not smaller than full %dx%d", pb.Dx(), pb.Dy(), fb.Dx(), fb.Dy())
	}
	if pb.Dx() != 200 || pb.Dy() != 150 {
		t.Errorf("clip render = %dx%d, want 200x150", pb.Dx(), pb.Dy())
	}
	_ = image.Rect
}

// TestSearchDehyphenated verifies search across hyphenated line wraps.
func TestSearchDehyphenated(t *testing.T) {
	pdf := &GoPdf{}
	pdf.Start(Config{PageSize: *PageSizeA4})
	pdf.AddTTFFont("lib", "test/res/LiberationSerif-Regular.ttf")
	pdf.SetFont("lib", "", 12)
	pdf.AddPage()
	pdf.SetXY(50, 60)
	_ = pdf.Cell(nil, "The model avoids recurrent computa-")
	pdf.SetXY(50, 76)
	_ = pdf.Cell(nil, "tional bottlenecks entirely.")
	path := filepath.Join(t.TempDir(), "dehyph.pdf")
	if err := pdf.WritePdf(path); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)

	// without dehyphenation: no match
	plain, err := SearchTextOnPageWithOptions(data, 0, "computational", SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(plain) != 0 {
		t.Errorf("plain search found %d matches, want 0", len(plain))
	}
	// with dehyphenation: one match on the first line
	deh, err := SearchTextOnPageWithOptions(data, 0, "computational", SearchOptions{Dehyphenate: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(deh) != 1 {
		t.Fatalf("dehyphenated search found %d matches, want 1", len(deh))
	}
	if deh[0].Y > 70 { // first line's top is near y=52..64
		t.Errorf("match line Y = %.1f, want on the first line", deh[0].Y)
	}
}

// TestFormatBlocksParagraphs verifies FormatBlocks breaks on indentation
// and merges wrapped lines.
func TestFormatBlocksParagraphs(t *testing.T) {
	pdf := &GoPdf{}
	pdf.Start(Config{PageSize: *PageSizeA4})
	pdf.AddTTFFont("lib", "test/res/LiberationSerif-Regular.ttf")
	pdf.SetFont("lib", "", 11)
	pdf.AddPage()
	pdf.SetXY(50, 60)
	_ = pdf.Cell(nil, "First paragraph wrapped onto")
	pdf.SetXY(50, 76)
	_ = pdf.Cell(nil, "a second line here.")
	pdf.SetXY(66, 100) // indented start of the next paragraph
	_ = pdf.Cell(nil, "Second paragraph begins indented.")
	path := filepath.Join(t.TempDir(), "blocks.pdf")
	if err := pdf.WritePdf(path); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	v, err := ExtractTextFormatted(data, 0, FormatBlocks)
	if err != nil {
		t.Fatal(err)
	}
	blocks, ok := v.([]TextBlock)
	if !ok || len(blocks) != 2 {
		for i, b := range blocks {
			t.Logf("block %d: %q", i, b.Lines[0].Text)
		}
		t.Fatalf("got %d blocks, want 2", len(blocks))
	}
	if len(blocks[0].Lines) != 2 {
		t.Errorf("block 0 has %d lines, want 2 (wrapped merge)", len(blocks[0].Lines))
	}
	if !strings.Contains(blocks[1].Lines[0].Text, "Second paragraph") {
		t.Errorf("block 1 = %q", blocks[1].Lines[0].Text)
	}
}

// TestStyledTextExtraction verifies color and font-style flags survive
// extraction: colored text reports its RGB, and a corpus heading detected
// as bold by font name.
func TestStyledTextExtraction(t *testing.T) {
	pdf := &GoPdf{}
	pdf.Start(Config{PageSize: *PageSizeA4})
	pdf.AddTTFFont("lib", "test/res/LiberationSerif-Regular.ttf")
	pdf.SetFont("lib", "", 12)
	pdf.AddPage()
	pdf.SetTextColor(220, 40, 40)
	pdf.SetXY(50, 60)
	_ = pdf.Cell(nil, "Red styled words")
	pdf.SetTextColor(0, 0, 0)
	pdf.SetXY(50, 100)
	_ = pdf.Cell(nil, "Plain black words")
	path := filepath.Join(t.TempDir(), "styled.pdf")
	if err := pdf.WritePdf(path); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)

	words, err := ExtractTextWordsInRect(data, 0, ClipRect{X: 0, Y: 0, W: 595, H: 90})
	if err != nil {
		t.Fatal(err)
	}
	if len(words) == 0 || words[0].Text != "Red" {
		for _, w := range words {
			t.Logf("w %q", w.Text)
		}
		t.Fatalf("styled words missing")
	}
	if words[0].ColorR != 220 || words[0].ColorG != 40 || words[0].ColorB != 40 {
		t.Errorf("color = %v %v %v, want 220/40/40", words[0].ColorR, words[0].ColorG, words[0].ColorB)
	}
	// black words later on the page
	plain, err := ExtractTextWordsInRect(data, 0, ClipRect{X: 0, Y: 90, W: 595, H: 60})
	if err != nil {
		t.Fatal(err)
	}
	if len(plain) == 0 || plain[0].ColorR != 0 || plain[0].ColorG != 0 || plain[0].ColorB != 0 {
		t.Errorf("plain color = %v", plain[0])
	}
}

// TestArxivBoldFlags verifies bold detection from font names on the real
// corpus (the Introduction heading is typeset bold).
func TestArxivBoldFlags(t *testing.T) {
	data := corpusData(t, "arxiv_1706.03762.pdf")
	words, err := ExtractTextWordsInRect(data, 1, ClipRect{X: 0, Y: 0, W: 842, H: 900})
	if err != nil {
		t.Fatal(err)
	}
	bold := 0
	for _, w := range words {
		if w.Flags&1 != 0 {
			bold++
		}
	}
	if bold == 0 {
		t.Errorf("no bold words detected on arXiv page 1")
	}
}

// TestPDFDocPageSize verifies visual page sizes honor /Rotate.
func TestPDFDocPageSize(t *testing.T) {
	pdf := &GoPdf{}
	pdf.Start(Config{PageSize: *PageSizeA4})
	pdf.AddPage()
	if err := pdf.SetPageRotation(1, 90); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "size.pdf")
	if err := pdf.WritePdf(path); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	doc, err := OpenPDFDocument(data)
	if err != nil {
		t.Fatal(err)
	}
	pi, err := doc.PageSize(0)
	if err != nil {
		t.Fatal(err)
	}
	if pi.Width != 842 || pi.Height != 595 {
		t.Errorf("size = %.0fx%.0f, want 842x595 (rotated A4)", pi.Width, pi.Height)
	}
	sizes := doc.PageSizes()
	if len(sizes) != 1 || sizes[0].Width != 842 {
		t.Errorf("PageSizes = %+v", sizes)
	}
}
