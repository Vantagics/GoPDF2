package gopdf

import (
	"os"
	"testing"
)

func TestDbgGlyf(t *testing.T) {
	data, _ := os.ReadFile("test/out/glyphs.pdf")
	parser, _ := newRawPDFParser(data)
	// obj 7 = FontFile2 (from earlier trace)
	obj := parser.objects[7]
	m, err := parseMinTTF(obj.stream)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("numGlyphs=%d loca len=%d glyf len=%d", m.numGlyphs, len(m.loca), len(m.glyf))
	for _, gid := range []uint16{0x2B, 1, 2, 3, 4, 5} {
		start, end := m.loca[gid], m.loca[gid+1]
		t.Logf("gid %d: loca [%d,%d) glyflen=%d", gid, start, end, len(m.glyf))
		if end > start {
			nCont := int16(m.glyf[start])<<8 | int16(m.glyf[start+1])
			t.Logf("  numberOfContours=%d", nCont)
			c, err := m.glyphOutline(gid, 0)
			t.Logf("  outline err=%v contours=%d", err, len(c))
		}
	}
}
