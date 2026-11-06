package gopdf

import (
	"encoding/binary"
	"os"
	"testing"
)

func TestDbgGlyf3(t *testing.T) {
	data, _ := os.ReadFile("test/out/glyphs.pdf")
	parser, _ := newRawPDFParser(data)
	obj := parser.objects[7]
	m, err := parseMinTTF(obj.stream)
	if err != nil {
		t.Fatal(err)
	}
	gid := uint16(43)
	g := m.glyf[m.loca[gid]:m.loca[gid+1]]
	// walk with spec-exact logic and dump each point's flags/deltas
	pos := 10
	endPts := []int{int(binary.BigEndian.Uint16(g[10:12]))}
	pos = 12
	instrLen := int(binary.BigEndian.Uint16(g[pos : pos+2]))
	pos += 2 + instrLen
	numPoints := endPts[0] + 1
	t.Logf("instrLen=%d numPoints=%d pos=%d tail=%d", instrLen, numPoints, pos, len(g)-pos)
	flags := make([]byte, numPoints)
	i := 0
	for i < numPoints && pos < len(g) {
		f := g[pos]
		pos++
		flags[i] = f
		i++
		if f&0x08 != 0 && pos < len(g) {
			rep := int(g[pos])
			pos++
			for j := 0; j < rep && i < numPoints; j++ {
				flags[i] = f
				i++
			}
		}
	}
	t.Logf("flags consumed to pos=%d", pos)
	x := 0.0
	y := 0.0
	for k := 0; k < numPoints; k++ {
		f := flags[k]
		xs := 0.0
	_ = xs
		dys := 0.0
	_ = dys
		if f&0x02 != 0 {
			d := int(g[pos])
			if f&0x10 == 0 {
				d = -d
			}
			x += float64(d)
			xs = float64(d)
			pos++
		} else if f&0x10 == 0 {
			d := int16(binary.BigEndian.Uint16(g[pos : pos+2]))
			x += float64(d)
			xs = float64(d)
			pos += 2
		}
		if f&0x04 != 0 {
			d := int(g[pos])
			if f&0x20 == 0 {
				d = -d
			}
			y += float64(d)
			dys = float64(d)
			pos++
		} else if f&0x20 == 0 {
			d := int16(binary.BigEndian.Uint16(g[pos : pos+2]))
			y += float64(d)
			dys = float64(d)
			pos += 2
		}
		if k < 6 {
			t.Logf("pt %d: flag=%02X dx=%.0f dy=%.0f -> (%.0f, %.0f) pos=%d", k, f, xs, dys, x, y, pos)
		}
	}
	t.Logf("end pos=%d len=%d", pos, len(g))
}
