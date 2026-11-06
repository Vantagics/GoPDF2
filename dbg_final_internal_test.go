package gopdf

import (
	"os"
	"testing"
)

func TestDbgFinal(t *testing.T) {
	data, _ := os.ReadFile("showcase.pdf")
	parser, _ := newRawPDFParser(data)
	fonts := resolveRenderFonts(parser, parser.pages[0])
	t.Logf("resolved: %d", len(fonts))
	for name, rf := range fonts {
		// glyph 0x2A (first char of title "GoPDF2" G=42?)
		c, err := rf.ttf.glyphOutline(42, 0)
		t.Logf("  %s: gid42 contours=%d err=%v chars=%d", name, len(c), err, len(rf.ttf.chars))
	}
	stream := parser.getPageContentStream(0)
	t.Logf("stream len: %d", len(stream))
	// count TJ ops
	count := 0
	for i := 0; i+1 < len(stream); i++ {
		if stream[i] == 'T' && stream[i+1] == 'J' {
			count++
		}
	}
	t.Logf("TJ ops: %d", count)
	img, err := RenderPageToImage(data, 0, RenderOption{DPI: 96})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("rendered, dark=%d", countDarkPixels(img))
}
