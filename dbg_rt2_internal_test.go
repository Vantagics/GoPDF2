package gopdf

import (
	"os"
	"strings"
	"testing"
)

func TestDbgRenderChain2(t *testing.T) {
	data, _ := os.ReadFile("test/out/glyphs.pdf")
	parser, _ := newRawPDFParser(data)
	fonts := resolveRenderFonts(parser, parser.pages[0])
	for name, rf := range fonts {
		t.Logf("font %s: type0=%v chars=%d", name, rf.isType0, len(rf.ttf.chars))
	}
	stream := parser.getPageContentStream(0)
	s := string(stream[:min(500, len(stream))])
	t.Logf("stream: %s", strings.ReplaceAll(s, "\n", "|"))
}
