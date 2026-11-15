package gopdf

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"strconv"
	"strings"
)

// ============================================================
// Type1 font rendering: parses /FontFile (PFB/PFA) font programs,
// decrypts eexec data, and interprets Type 1 charstrings into glyph
// outlines so LaTeX/Nimbus documents (Type1 embedded fonts) rasterize.
// ============================================================

// type1Font holds decrypted charstrings keyed by glyph name.
type type1Font struct {
	glyphs map[string][]byte // glyph name -> decrypted charstring
	subrs  [][]byte          // decrypted /Subrs entries (index = subr number)
	widths map[string]float64
	// encoding maps character codes to glyph names; populated from the
	// PDF font's /Encoding when available, else StandardEncoding.
	encoding map[byte]string
	// interpreted-glyph cache (outlines are expensive to re-execute)
	outlineCache map[string][]glyphContour
	widthCache   map[string]float64
	// codeWidths from the PDF font's /Widths array — authoritative advance
	// widths indexed by character code (overrides charstring hsbw).
	codeWidths map[int]float64
}

// glyphContour is one finished outline contour in font units.
type glyphContour = []glyphPt

// pfbSegments splits a PFB (0x80 header) or PFA (plain ASCII) font
// program into its concatenated sections.
func pfbSegments(data []byte) []byte {
	if len(data) > 2 && data[0] == 0x80 && (data[1] == 0x01 || data[1] == 0x02) {
		var out []byte
		for i := 0; i+6 < len(data); {
			if data[i] != 0x80 {
				break
			}
			typ := data[i+1]
			if typ == 0x03 { // EOF
				break
			}
			length := int(uint32(data[i+2]) | uint32(data[i+3])<<8 | uint32(data[i+4])<<16 | uint32(data[i+5])<<24)
			start := i + 6
			if start+length > len(data) {
				length = len(data) - start
			}
			out = append(out, data[start:start+length]...)
			i = start + length
		}
		if len(out) > 0 {
			return out
		}
	}
	return data // PFA or raw stream
}

// eexecDecrypt decrypts an eexec section (R=55665), skipping the four
// random leading bytes.
func eexecDecrypt(data []byte) []byte {
	return type1Decrypt(data, 55665)
}

// charstringDecrypt decrypts charstrings (R=4330), skipping the four
// random leading bytes.
func charstringDecrypt(data []byte) []byte {
	return type1Decrypt(data, 4330)
}

// type1Decrypt runs the Type 1 cipher over data:
// plain = cipher ^ (r>>8); r = ((cipher + r) * c1 + c2) mod 65536.
func type1Decrypt(data []byte, r uint16) []byte {
	const c1, c2 = 52845, 22719
	out := make([]byte, 0, len(data))
	for _, b := range data {
		plain := b ^ byte(r>>8)
		r = uint16(((int(b) + int(r)) * c1 + c2) & 0xFFFF)
		out = append(out, plain)
	}
	// the first four bytes are random — drop them
	if len(out) > 4 {
		return out[4:]
	}
	return out[:0]
}

// parseType1Font extracts the CharStrings dict from a font program.
func parseType1Font(data []byte) (*type1Font, error) {
	program := pfbSegments(data)
	f := &type1Font{
		glyphs:       make(map[string][]byte),
		widths:       make(map[string]float64),
		encoding:     make(map[byte]string),
		outlineCache: make(map[string][]glyphContour),
		widthCache:   make(map[string]float64),
		codeWidths:   make(map[int]float64),
	}
	// split at the eexec marker
	idx := bytes.Index(program, []byte("eexec"))
	if idx < 0 {
		return nil, fmt.Errorf("type1: no eexec section")
	}
	clearPart := program[:idx]
	encrypted := program[idx+len("eexec"):]
	// skip whitespace after eexec
	trimmed := bytes.TrimLeft(encrypted, " \t\r\n")
	plain := eexecDecrypt(trimmed)

	parseType1Encoding(clearPart, f)
	parseType1Subrs(plain, f)
	parseType1CharStrings(plain, f)
	if len(f.glyphs) == 0 {
		return nil, fmt.Errorf("type1: no charstrings")
	}
	// default encoding: StandardEncoding approximated by ASCII names
	for c := 32; c < 127; c++ {
		if _, ok := f.encoding[byte(c)]; !ok {
			name := string(rune(c))
			if c == 39 {
				name = "quoteright"
			} else if c == 96 {
				name = "quoteleft"
			}
			f.encoding[byte(c)] = name
		}
	}
	return f, nil
}

// parseType1Encoding reads the cleartext /Encoding (custom array form).
// StandardEncoding leaves the fallback table in place.
func parseType1Encoding(clear []byte, f *type1Font) {
	iterGuard := 0
	idx := bytes.Index(clear, []byte("/Encoding"))
	if idx < 0 {
		return
	}
	rest := clear[idx:]
	if bytes.HasPrefix(bytes.TrimSpace(rest[len("/Encoding"):]), []byte("StandardEncoding")) {
		return
	}
	start := bytes.IndexByte(rest, '[')
	if start < 0 {
		return
	}
	end := bytes.IndexByte(rest[start:], ']')
	if end < 0 {
		return
	}
	arr := rest[start+1 : start+end]
	for pos := 0; pos < len(arr); {
		iterGuard++
		if iterGuard > 100000 {
			return
		}
		// tokens: dup <code> /<name> put
		j := pos
		for j < len(arr) && arr[j] != '/' {
			j++
		}
		if j >= len(arr) {
			break
		}
		// walk back over the number before '/'
		k := j
		for k > pos && (arr[k-1] == ' ' || arr[k-1] == '\n' || arr[k-1] == '\r' || arr[k-1] == '\t') {
			k--
		}
		numStart := k
		for numStart > pos && arr[numStart-1] >= '0' && arr[numStart-1] <= '9' {
			numStart--
		}
		if k > numStart {
			code, err := strconv.Atoi(string(arr[numStart:k]))
			if err == nil && code >= 0 && code <= 255 {
				nameEnd := j + 1
				for nameEnd < len(arr) && isNameChar(arr[nameEnd]) {
					nameEnd++
				}
				if nameEnd > j+1 {
					f.encoding[byte(code)] = string(arr[j+1 : nameEnd])
				}
			}
		}
		// skip past this name
		pos = j + 1
		for pos < len(arr) && isNameChar(arr[pos]) {
			pos++
		}
	}
}

// parseType1Subrs reads the /Subrs array from the decrypted private
// section: entries are "dup <index> <len> RD <binary> NP".
func parseType1Subrs(plain []byte, f *type1Font) {
	idx := bytes.Index(plain, []byte("/Subrs"))
	if idx < 0 {
		return
	}
	rest := plain[idx+len("/Subrs"):]
	end := bytes.Index(rest, []byte("NP"))
	if end < 0 {
		return
	}
	countStart := bytes.Index(rest, []byte("array"))
	if countStart < 0 {
		return
	}
	countStr := strings.TrimSpace(string(rest[:countStart]))
	count, err := strconv.Atoi(strings.Fields(countStr)[0])
	if err != nil || count <= 0 {
		return
	}
	f.subrs = make([][]byte, 0, count)
	pos := countStart + len("array")
	for len(f.subrs) < count {
		// expect: dup <index> <len> RD <binary> NP
		dupIdx := bytes.Index(rest[pos:], []byte("dup"))
		if dupIdx < 0 {
			return
		}
		pos += dupIdx + 3
		for pos < len(rest) && (rest[pos] == 32 || rest[pos] == 10 || rest[pos] == 13 || rest[pos] == 9) {
			pos++
		}
		// index (ignored, sequential)
		for pos < len(rest) && rest[pos] >= 48 && rest[pos] <= 57 {
			pos++
		}
		for pos < len(rest) && (rest[pos] == 32 || rest[pos] == 10 || rest[pos] == 13) {
			pos++
		}
		numStart := pos
		for pos < len(rest) && rest[pos] >= 48 && rest[pos] <= 57 {
			pos++
		}
		length, err2 := strconv.Atoi(string(rest[numStart:pos]))
		if err2 != nil || length <= 0 || length > 65536 {
			return
		}
		for pos < len(rest) && (rest[pos] == 32 || rest[pos] == 10 || rest[pos] == 13) {
			pos++
		}
		if pos+2 > len(rest) {
			return
		}
		op := string(rest[pos : pos+2])
		if op != "RD" {
			return
		}
		pos += 2
		for pos < len(rest) && (rest[pos] == 32 || rest[pos] == 10 || rest[pos] == 13 || rest[pos] == 9) {
			pos++
		}
		if pos+length > len(rest) {
			return
		}
		f.subrs = append(f.subrs, charstringDecrypt(rest[pos:pos+length]))
		pos += length
	}
}

// parseType1CharStrings reads /CharStrings entries from the decrypted
// private section: /name length RD <binary> ND.
func parseType1CharStrings(plain []byte, f *type1Font) {
	idx := bytes.Index(plain, []byte("/CharStrings"))
	if idx < 0 {
		return
	}
	rest := plain[idx:]
	pos := 0
	for {
		rel := bytes.Index(rest[pos:], []byte("/"))
		if rel < 0 {
			return
		}
		nameIdx := pos + rel
		nameEnd := nameIdx + 1
		for nameEnd < len(rest) && isNameChar(rest[nameEnd]) {
			nameEnd++
		}
		name := string(rest[nameIdx+1 : nameEnd])
		scan := nameEnd
		consumed := false
		for scan < len(rest) {
			for scan < len(rest) && (rest[scan] == 32 || rest[scan] == 10 || rest[scan] == 9) {
				scan++
			}
			if scan < len(rest) && rest[scan] == '/' {
				// next glyph before a valid entry — jump to it
				pos = scan
				consumed = true
				break
			}
			numStart := scan
			for scan < len(rest) && rest[scan] >= '0' && rest[scan] <= '9' {
				scan++
			}
			length, err := strconv.Atoi(string(rest[numStart:scan]))
			if err != nil || length <= 0 || length > 65536 {
				scan++
				continue
			}
			for scan < len(rest) && (rest[scan] == 32 || rest[scan] == 10) {
				scan++
			}
			if scan+2 > len(rest) {
				return
			}
			op := string(rest[scan : scan+2])
			if op != "RD" && op != "-|" && op != "|-" {
				scan++
				continue
			}
			scan += 2
			// the separator between RD and the binary may be a space
			for scan < len(rest) && (rest[scan] == 32 || rest[scan] == 10 || rest[scan] == 13 || rest[scan] == 9) {
				scan++
			}
			if scan+length > len(rest) {
				return
			}
			f.glyphs[name] = charstringDecrypt(rest[scan : scan+length])
			pos = scan + length // past the binary: '/' bytes inside it are data
			consumed = true
			break
		}
		if !consumed {
			return
		}
		if len(f.glyphs) > 4096 {
			return
		}
	}
}

// glyphOutline interprets the charstring of a glyph into contours
// (all points on-curve; cubics flattened to segments) plus its advance
// width in 1/1000 em.
func (f *type1Font) glyphOutline(name string) ([][]glyphPt, float64) {
	if c, ok := f.outlineCache[name]; ok {
		return c, f.widthCache[name]
	}
	cs, ok := f.glyphs[name]
	if !ok {
		return nil, 0
	}
	p := &type1Interp{f: f}
	p.run(cs, 0)
	if len(p.cur) >= 3 {
		p.contours = append(p.contours, p.cur)
	}
	f.outlineCache[name] = p.contours
	f.widthCache[name] = p.width
	return p.contours, p.width
}

// glyphWidth returns the cached advance width (1/1000 em) of a glyph.
func (f *type1Font) glyphWidth(name string) float64 {
	_, w := f.glyphOutline(name)
	return w
}

// codeAdvance returns the advance width for a character code, preferring
// the PDF /Widths array over the charstring's hsbw value.
func (f *type1Font) codeAdvance(code byte, name string) float64 {
	if w, ok := f.codeWidths[int(code)]; ok && w > 0 {
		return w
	}
	return f.glyphWidth(name)
}

// type1Interp executes Type 1 charstrings: persistent operand stack,
// shared path state across callsubr, and the callothersubr flex model
// (matching fontTools' T1OutlineExtractor).
type type1Interp struct {
	f        *type1Font
	contours [][]glyphPt
	cur      []glyphPt
	x, y     float64
	width    float64
	stack    []float64
	flexing  bool
	stems    int
}

func (p *type1Interp) flushContour() {
	if len(p.cur) >= 3 {
		p.contours = append(p.contours, p.cur)
	}
	p.cur = p.cur[:0]
}

func (p *type1Interp) moveTo(px, py float64) {
	p.flushContour()
	p.cur = append(p.cur[:0], glyphPt{x: px, y: py, onCurve: true})
	p.x, p.y = px, py
}

func (p *type1Interp) lineTo(px, py float64) {
	p.x, p.y = px, py
	p.cur = append(p.cur, glyphPt{x: px, y: py, onCurve: true})
}

// curveTo flattens a cubic bezier into on-curve segments.
func (p *type1Interp) curveTo(x1, y1, x2, y2, x3, y3 float64) {
	const steps = 8
	x0, y0 := p.x, p.y
	for t := 1; t <= steps; t++ {
		tt := float64(t) / steps
		mt := 1 - tt
		px := mt*mt*mt*x0 + 3*mt*mt*tt*x1 + 3*mt*tt*tt*x2 + tt*tt*tt*x3
		py := mt*mt*mt*y0 + 3*mt*mt*tt*y1 + 3*mt*tt*tt*y2 + tt*tt*tt*y3
		p.cur = append(p.cur, glyphPt{x: px, y: py, onCurve: true})
	}
	p.x, p.y = x3, y3
}

var type1Trace bool

// run executes one charstring (or subr) against the shared state.
func (p *type1Interp) run(cs []byte, depth int) {
	if depth > 32 {
		return
	}
	for i := 0; i < len(cs); {
		v, n, isOp := decodeCharstringToken(cs[i:])
		i += n
		if n == 0 {
			break
		}
		if !isOp {
			p.stack = append(p.stack, float64(v))
			continue
		}
		args := p.stack
		p.stack = p.stack[:0]
		if type1Trace {
			fmt.Printf("T1 op=%d args=%v pts=%d", v, args, len(p.cur))
		}
		switch v {
		case 13: // hsbw: sbx wx (sbx pushed first — fontTools popall order)
			if len(args) >= 2 {
				p.width = args[len(args)-1]
				p.moveTo(args[len(args)-2], 0)
			} else if len(args) == 1 {
				p.width = args[0]
			}
		case 14: // endchar
			if len(p.cur) >= 3 {
				p.contours = append(p.contours, p.cur)
			}
			p.cur = p.cur[:0]
			if p.width == 0 && len(args) >= 1 {
				p.width = args[len(args)-1]
			}
			return
		case 21: // rmoveto: dx dy (odd → first value is a residual)
			if p.flexing {
				continue
			}
			switch {
			case len(args) >= 2:
				p.moveTo(p.x+args[len(args)-2], p.y+args[len(args)-1])
			case len(args) == 1:
				p.moveTo(p.x+args[0], p.y)
			}
		case 22: // hmoveto
			if p.flexing {
				continue
			}
			if len(args) >= 1 {
				p.moveTo(p.x+args[len(args)-1], p.y)
			}
		case 4: // vmoveto
			if p.flexing {
				continue
			}
			if len(args) >= 1 {
				p.moveTo(p.x, p.y+args[len(args)-1])
			}
		case 33: // setcurrentpoint
			if len(args) >= 2 {
				p.x, p.y = args[len(args)-2], args[len(args)-1]
			}
		case 5: // rlineto: pairs of dx dy
			for k := 0; k+1 < len(args); k += 2 {
				p.lineTo(p.x+args[k], p.y+args[k+1])
			}
		case 6: // hlineto (alternating h/v when given several)
			vert := false
			for _, d := range args {
				if vert {
					p.lineTo(p.x, p.y+d)
				} else {
					p.lineTo(p.x+d, p.y)
				}
				vert = !vert
			}
		case 7: // vlineto
			vert := true
			for _, d := range args {
				if vert {
					p.lineTo(p.x, p.y+d)
				} else {
					p.lineTo(p.x+d, p.y)
				}
				vert = !vert
			}
		case 8: // rrcurveto: 3 pairs of control/end deltas
			if len(args) >= 6 {
				a := args[len(args)-6:]
				p.curveTo(p.x+a[0], p.y+a[1], p.x+a[0]+a[2], p.y+a[1]+a[3], p.x+a[0]+a[2]+a[4], p.y+a[1]+a[3]+a[5])
			}
		case 30: // vhcurveto
			p.flexCurve(args, true)
		case 31: // hvcurveto
			p.flexCurve(args, false)
		case 9: // closepath
			p.flushContour()
		case 2:
			p.flushContour()
		case 1, 3, 18, 23: // hstem / vstem / stem counts
			p.stems += len(args) / 2
		case 12 + 0: // dotsection — no-op
		case 12 + 12: // div: num den div → push quotient
			if len(args) >= 2 {
				den := args[len(args)-1]
				if den != 0 {
					p.stack = append(p.stack, args[len(args)-2]/den)
				}
			}
		case 10: // callsubr
			if len(args) >= 1 && p.f != nil {
				idx := int(args[len(args)-1])
				if idx >= 0 && idx < len(p.f.subrs) {
					p.run(p.f.subrs[idx], depth+1)
				}
			}
		case 11: // return
			return
		case 12 + 7: // sbw: wx sbx wy wxy
			if len(args) >= 4 {
				p.width = args[len(args)-4]
			}
		case 12 + 16: // callothersubr: args… nArgs subrIndex
			if len(args) >= 2 {
				subrIndex := int(args[len(args)-1])
				nArgs := int(args[len(args)-2])
				switch {
				case subrIndex == 0 && nArgs == 3:
					p.doFlex()
				case subrIndex == 1:
					p.flexing = true
				case subrIndex == 2:
					p.flexing = false
				}
			}
		case 12 + 17: // pop — no-op
		default:
			// unknown op: drop args
		}
	}
}

// flexCurve handles vhcurveto/hvcurveto: alternating-axis curve chains.
func (p *type1Interp) flexCurve(args []float64, vertFirst bool) {
	// groups of 4 deltas (d1 d2 d3 d4): curve control/end pairs; an
	// optional trailing 5th delta adds a final line.
	k := 0
	vert := vertFirst
	for k+4 <= len(args) {
		d1, d2, d3, d4 := args[k], args[k+1], args[k+2], args[k+3]
		var c1x, c1y, c2x, c2y, ex, ey float64
		if vert {
			c1x, c1y = p.x, p.y+d1
			c2x, c2y = c1x+d2, c1y+d3
			ex, ey = c2x, c2y+d4
		} else {
			c1x, c1y = p.x+d1, p.y
			c2x, c2y = c1x+d2, c1y+d3
			ex, ey = c2x+d4, c2y
		}
		p.curveTo(c1x, c1y, c2x, c2y, ex, ey)
		vert = !vert
		k += 4
	}
	if k < len(args) {
		d := args[k]
		if vert {
			p.lineTo(p.x, p.y+d)
		} else {
			p.lineTo(p.x+d, p.y)
		}
	}
}

// doFlex draws the two rrcurvetos of the Type 1 flex sequence, popping
// the 16 accumulated values (flex height + 12 curve deltas + 2 ref
// point deltas + 2 finals) like fontTools' T1OutlineExtractor.
func (p *type1Interp) doFlex() {
	pop := func() float64 {
		if len(p.stack) == 0 {
			return 0
		}
		v := p.stack[len(p.stack)-1]
		p.stack = p.stack[:len(p.stack)-1]
		return v
	}
	_ = pop()          // finaly
	finalx := pop()    // finalx
	_ = pop()          // flex height
	p3y := pop()       // p3
	p3x := pop()
	bcp4y := pop()     // bcp4
	bcp4x := pop()
	bcp3y := pop()     // bcp3
	bcp3x := pop()
	p2y := pop()       // p2
	p2x := pop()
	bcp2y := pop()     // bcp2
	bcp2x := pop()
	bcp1y := pop()     // bcp1
	bcp1x := pop()
	_ = pop()          // rp y
	rpx := pop()       // rp x
	_ = rpx
	p.curveTo(bcp1x, bcp1y, bcp2x, bcp2y, p2x, p2y)
	p.curveTo(bcp3x, bcp3y, bcp4x, bcp4y, p3x, p3y)
	p.lineTo(finalx, p.y)
}

func charstringNextValue(b []byte) (int, int) {
	if len(b) == 0 {
		return 0, 0
	}
	v := int(b[0])
	switch {
	case v <= 31:
		return v, 1
	case v <= 246:
		return v - 139, 1
	case v <= 250:
		if len(b) < 2 {
			return 0, 0
		}
		return (v-247)*256 + int(b[1]) + 108, 2
	case v <= 254:
		if len(b) < 2 {
			return 0, 0
		}
		return -(v-251)*256 - int(b[1]) - 108, 2
	default: // 255: 32-bit fixed/int (used as int in charstrings)
		if len(b) < 5 {
			return 0, 0
		}
		n := int(int32(uint32(b[1])<<24 | uint32(b[2])<<16 | uint32(b[3])<<8 | uint32(b[4])))
		return n, 5
	}
}

// standardType1Name returns the glyph name for a StandardEncoding byte.
var standardType1Names = func() map[byte]string {
	m := make(map[byte]string)
	for c := 32; c < 127; c++ {
		name := string(rune(c))
		if c == 39 {
			name = "quoteright"
		} else if c == 96 {
			name = "quoteleft"
		}
		m[byte(c)] = name
	}
	for c, name := range map[int]string{
		0x27: "quoteright", 0x60: "quoteleft",
		0xA0: "space", 0xA1: "exclamdown", 0xA2: "cent", 0xA3: "sterling",
		0xA4: "currency", 0xA5: "yen", 0xA6: "brokenbar", 0xA7: "section",
		0xA8: "dieresis", 0xA9: "copyright", 0xAA: "ordfeminine",
		0xAB: "guillemotleft", 0xAC: "logicalnot", 0xAD: "hyphen",
		0xAE: "registered", 0xAF: "macron", 0xB0: "degree",
		0xB1: "plusminus", 0xB2: "twosuperior", 0xB3: "threesuperior",
		0xB4: "acute", 0xB5: "mu", 0xB6: "paragraph", 0xB7: "periodcentered",
		0xB8: "cedilla", 0xB9: "onesuperior", 0xBA: "ordmasculine",
		0xBB: "guillemotright", 0xBC: "onequarter", 0xBD: "onehalf",
		0xBE: "threequarters", 0xBF: "questiondown",
		0xC0: "Agrave", 0xC1: "Aacute", 0xC2: "Acircumflex",
		0xC3: "Atilde", 0xC4: "Adieresis", 0xC5: "Aring",
		0xC6: "AE", 0xC7: "Ccedilla", 0xC8: "Egrave", 0xC9: "Eacute",
		0xCA: "Ecircumflex", 0xCB: "Edieresis", 0xCC: "Igrave",
		0xCD: "Iacute", 0xCE: "Icircumflex", 0xCF: "Idieresis",
		0xD0: "Eth", 0xD1: "Ntilde", 0xD2: "Ograve", 0xD3: "Oacute",
		0xD4: "Ocircumflex", 0xD5: "Otilde", 0xD6: "Odieresis",
		0xD7: "multiply", 0xD8: "Oslash", 0xD9: "Ugrave", 0xDA: "Uacute",
		0xDB: "Ucircumflex", 0xDC: "Udieresis", 0xDD: "Yacute",
		0xDE: "Thorn", 0xDF: "germandbls",
		0xE0: "agrave", 0xE1: "aacute", 0xE2: "acircumflex",
		0xE3: "atilde", 0xE4: "adieresis", 0xE5: "aring", 0xE6: "ae",
		0xE7: "ccedilla", 0xE8: "egrave", 0xE9: "eacute",
		0xEA: "ecircumflex", 0xEB: "edieresis", 0xEC: "igrave",
		0xED: "iacute", 0xEE: "icircumflex", 0xEF: "idieresis",
		0xF0: "eth", 0xF1: "ntilde", 0xF2: "ograve", 0xF3: "oacute",
		0xF4: "ocircumflex", 0xF5: "otilde", 0xF6: "odieresis",
		0xF7: "divide", 0xF8: "oslash", 0xF9: "ugrave", 0xFA: "uacute",
		0xFB: "ucircumflex", 0xFC: "udieresis", 0xFD: "yacute",
		0xFE: "thorn", 0xFF: "ydieresis",
	} {
		m[byte(c)] = name
	}
	return m
}()

// applyType1Encoding fills the font's code→name map from a PDF font's
// /Encoding (base name + Differences).
func (f *type1Font) applyType1Encoding(baseEncoding string, diffs map[byte]string) {
	for c, name := range standardType1Names {
		f.encoding[c] = name
	}
	switch baseEncoding {
	case "WinAnsiEncoding":
		for c := 0x80; c < 0x100; c++ {
			if r, ok := decodeWinAnsiByte(byte(c)); ok {
				f.encoding[byte(c)] = aglNameForRune(r)
			}
		}
	case "MacRomanEncoding":
		for c := 0x80; c < 0x100; c++ {
			if r, ok := decodeMacRomanByte(byte(c)); ok {
				f.encoding[byte(c)] = aglNameForRune(r)
			}
		}
	}
	for c, name := range diffs {
		if name != "" {
			f.encoding[c] = name
		}
	}
}

// aglNameForRune reverse-maps common runes to AGL names for encoding
// tables that only provide Unicode values.
func aglNameForRune(r rune) string {
	if r >= ' ' && r < 0x7F {
		return string(r)
	}
	for name, u := range glyphNameUnicode {
		if u == r {
			return name
		}
	}
	return string(r)
}

var _ = strings.TrimSpace

// drawType1Run rasterizes a run of single-byte codes through a Type1 font.
// Contours stay in font space (y-up, 1000/em); drawGlyphContours performs
// the y flip and device translation like it does for TrueType glyphs.
func drawType1Run(img *image.RGBA, font *type1Font, codes []byte, fontSize float64, penX, penY float64, fill color.RGBA, csPx, wsPx float64) {
	scale := fontSize / 1000.0
	for _, code := range codes {
		name := font.encoding[code]
		if name == "" {
			name = standardType1Names[code]
		}
		if name == "" {
			continue
		}
		contours, _ := font.glyphOutline(name)
		if len(contours) > 0 {
			drawGlyphContours(img, contours, scale, penX, penY, fill)
		}
		penX += font.codeAdvance(code, name)*scale + csPx
		if name == "space" {
			penX += wsPx
		}
	}
}// decodeCharstringToken decodes one Type 1 charstring token.
// Returns the value, the bytes consumed, and whether the token is an
// operator. The distinction is made on the RAW byte: values below 32 are
// operators; decoded numbers span -108..1131, which overlaps the operator
// range (e.g. byte 139 decodes to the number 0).
func decodeCharstringToken(b []byte) (int, int, bool) {
	if len(b) == 0 {
		return 0, 0, false
	}
	v := int(b[0])
	switch {
	case v <= 31:
		return v, 1, true
	case v <= 246:
		return v - 139, 1, false
	case v <= 250:
		if len(b) < 2 {
			return 0, 0, false
		}
		return (v-247)*256 + int(b[1]) + 108, 2, false
	case v <= 254:
		if len(b) < 2 {
			return 0, 0, false
		}
		return -(v-251)*256 - int(b[1]) - 108, 2, false
	default: // 255: 32-bit integer
		if len(b) < 5 {
			return 0, 0, false
		}
		n := int(int32(uint32(b[1])<<24 | uint32(b[2])<<16 | uint32(b[3])<<8 | uint32(b[4])))
		return n, 5, false
	}
}


