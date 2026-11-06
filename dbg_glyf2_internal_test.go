package gopdf

import (
	"encoding/binary"
	"os"
	"testing"
)

func TestDbgGlyf2(t *testing.T) {
	data, _ := os.ReadFile("test/out/glyphs.pdf")
	parser, _ := newRawPDFParser(data)
	obj := parser.objects[7]
	m, err := parseMinTTF(obj.stream)
	if err != nil {
		t.Fatal(err)
	}
	gid := uint16(43)
	g := m.glyf[m.loca[gid]:m.loca[gid+1]]
	nc := int16(binary.BigEndian.Uint16(g[0:2]))
	t.Logf("glyph len=%d nc=%d", len(g), nc)
	pos := 10
	endPts := make([]int, nc)
	for i := 0; i < int(nc); i++ {
		endPts[i] = int(binary.BigEndian.Uint16(g[pos : pos+2]))
		pos += 2
	}
	t.Logf("endPts=%v", endPts)
	instrLen := int(binary.BigEndian.Uint16(g[pos : pos+2]))
	pos += 2
	t.Logf("instrLen=%d pos=%d", instrLen, pos)
	pos += instrLen
	numPoints := endPts[nc-1] + 1
	t.Logf("numPoints=%d remaining=%d", numPoints, len(g)-pos)
	// walk flags
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
	t.Logf("flags consumed: i=%d pos=%d len=%d", i, pos, len(g))
	// x deltas
	x := 0.0
	xs := make([]float64, numPoints)
	xfail := false
	for k := 0; k < numPoints; k++ {
		f := flags[k]
		if f&0x02 != 0 {
			if pos >= len(g) {
				xfail = true
				break
			}
			d := int(g[pos])
			if f&0x10 == 0 {
				d = -d
			}
			x += float64(d)
			pos++
		} else if f&0x10 == 0 {
		} else {
			if pos+2 > len(g) {
				xfail = true
				break
			}
			x += float64(int16(binary.BigEndian.Uint16(g[pos : pos+2])))
			pos += 2
		}
		xs[k] = x
	}
	t.Logf("x done: fail=%v pos=%d", xfail, pos)
	// y deltas
	y := 0.0
	ys := make([]float64, numPoints)
	yfail := false
	for k := 0; k < numPoints; k++ {
		f := flags[k]
		if f&0x04 != 0 {
			if pos >= len(g) {
				yfail = true
				break
			}
			d := int(g[pos])
			if f&0x20 == 0 {
				d = -d
			}
			y += float64(d)
			pos++
		} else if f&0x20 == 0 {
		} else {
			if pos+2 > len(g) {
				yfail = true
				break
			}
			y += float64(int16(binary.BigEndian.Uint16(g[pos : pos+2])))
			pos += 2
		}
		ys[k] = y
	}
	t.Logf("y done: fail=%v pos=%d", yfail, pos)
	t.Logf("first pts: (%.1f,%.1f) (%.1f,%.1f) (%.1f,%.1f)", xs[0], ys[0], xs[1], ys[1], xs[2], ys[2])
}
