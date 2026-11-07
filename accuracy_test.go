package gopdf

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCellPositionAccuracy generates text at known positions and verifies
// that the extracted positions match (within tolerance).
func TestCellPositionAccuracy(t *testing.T) {
	pdf := &GoPdf{}
	pdf.Start(Config{PageSize: *PageSizeA4})
	pdf.AddPage()
	if err := pdf.AddTTFFont("lib", "test/res/LiberationSerif-Regular.ttf"); err != nil {
		t.Fatal(err)
	}
	if err := pdf.SetFont("lib", "", 12); err != nil {
		t.Fatal(err)
	}

	// generate text at known positions
	type pos struct {
		x, y     float64
		fontSize int
		text     string
	}
	positions := []pos{
		{50, 100, 12, "Alpha"},
		{200, 200, 12, "Beta"},
		{350, 300, 12, "Gamma"},
	}
	for _, p := range positions {
		pdf.SetXY(p.x, p.y)
		_ = pdf.Cell(nil, p.text)
	}

	path := filepath.Join(t.TempDir(), "acc.pdf")
	if err := pdf.WritePdf(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	texts, err := ExtractTextFromPage(data, 0)
	if err != nil {
		t.Fatal(err)
	}
	// match extracted runs to expected positions.
	// ExtractedText X/Y are in top-down page coordinates; Y is the text
	// baseline, which for a Cell sits between the requested top (p.y) and
	// one font size below it.
	for _, p := range positions {
		found := false
		for _, tx := range texts {
			if tx.Text == p.text {
				dx := tx.X - p.x
				if dx < 0 {
					dx = -dx
				}
				if dx < 2 && tx.Y >= p.y-1 && tx.Y <= p.y+float64(int(p.fontSize))+1 {
					found = true
					break
				}
			}
		}
		if !found {
			t.Errorf("text %q at (%.0f,%.0f) not found at expected position", p.text, p.x, p.y)
		}
	}
}

// TestExtractionRoundTrip generates text, extracts it, and verifies
// the full round-trip preserves content.
func TestExtractionRoundTrip(t *testing.T) {
	pdf := &GoPdf{}
	pdf.Start(Config{PageSize: *PageSizeA4})
	pdf.AddPage()
	if err := pdf.AddTTFFont("lib", "test/res/LiberationSerif-Regular.ttf"); err != nil {
		t.Fatal(err)
	}
	if err := pdf.SetFont("lib", "", 12); err != nil {
		t.Fatal(err)
	}
	sentences := []string{
		"The quick brown fox jumps over the lazy dog.",
		"Pack my box with five dozen liquor jugs.",
		"How vexingly quick daft zebras jump!",
	}
	y := 60.0
	for _, s := range sentences {
		pdf.SetXY(50, y)
		_ = pdf.Cell(nil, s)
		y += 30
	}
	path := filepath.Join(t.TempDir(), "rt.pdf")
	if err := pdf.WritePdf(path); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	lines, err := ExtractPageLines(data, 0)
	if err != nil {
		t.Fatal(err)
	}
	extracted := ""
	for _, ln := range lines {
		extracted += ln.Text + "\n"
	}
	for _, s := range sentences {
		if !strings.Contains(extracted, s) {
			t.Errorf("sentence not preserved in extraction: %q", s)
		}
	}
}
