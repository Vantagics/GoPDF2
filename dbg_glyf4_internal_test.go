package gopdf

import (
	"os"
	"testing"
)

func TestDbgGlyf4(t *testing.T) {
	data, _ := os.ReadFile("test/out/glyphs.pdf")
	parser, _ := newRawPDFParser(data)
	obj := parser.objects[7]
	m, err := parseMinTTF(obj.stream)
	if err != nil {
		t.Fatal(err)
	}
	g := m.glyf[m.loca[43]:m.loca[44]]
	t.Logf("g len=%d nc=%d", len(g), int16(binaryNCTypo(g)))
	// call parseSimpleGlyf directly with the correct nc
	out, err := m.parseSimpleGlyf(g, 1)
	t.Logf("direct parseSimpleGlyf(g, 1): err=%v contours=%d", err, len(out))
	// via glyphOutline
	out2, err2 := m.glyphOutline(43, 0)
	t.Logf("glyphOutline(43): err=%v contours=%d", err2, len(out2))
}

func binaryNCTypo(g []byte) int16 {
	return int16(g[0])<<8 | int16(g[1])
}
