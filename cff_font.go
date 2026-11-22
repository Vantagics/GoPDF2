package gopdf

// CFF (Compact Font Format, PDF FontFile3 /Subtype /Type1C) parsing and a
// Type 2 charstring interpreter, enough to rasterize the simple (non-CID)
// CFF fonts that InDesign/Illustrator/Word exports embed. Glyph outlines
// are returned pre-flattened (cubic segments sampled), so the renderer's
// polygon path can draw them without changes.

import (
	"encoding/binary"
	"fmt"
)

// cffFont holds the parsed CFF tables needed for glyph rendering.
type cffFont struct {
	// charstrings[GID] = raw Type2 charstring; outlines are interpreted
	// LAZILY on first draw (rendering one page needs a fraction of the
	// font's glyphs — eager interpretation dominated render time)
	charstrings [][]byte
	glyphCache  [][][]glyphPt
	// per-glyph subr tables captured at parse time
	glyphSubrs [][][]byte
	glyphBias  []float64
	nameToGID  map[string]uint16
	// isCID: CID-keyed CFF (CIDFontType0C) — glyphs are addressed by the
	// 2-byte CID (Identity-H), resolved through cidToGID
	isCID    bool
	cidToGID map[int]int
	// advWidths per GID in font units (from charstring width hints when
	// present; nominal when absent)
	advWidths  []float64
	unitsPerEm float64
	gsubrs     [][]byte
	globalBias float64
}

// cffIndex parses one CFF INDEX structure; returns the entries' byte slices.
func cffIndex(data []byte, off int) ([][]byte, int, error) {
	if off+2 > len(data) {
		return nil, off, fmt.Errorf("cff: index truncated")
	}
	count := int(binary.BigEndian.Uint16(data[off:]))
	off += 2
	if count == 0 {
		return nil, off, nil
	}
	if off >= len(data) {
		return nil, off, fmt.Errorf("cff: index offSize truncated")
	}
	offSize := int(data[off])
	off++
	if offSize < 1 || offSize > 4 || off+offSize*(count+1) > len(data) {
		return nil, off, fmt.Errorf("cff: index offsets truncated")
	}
	// capture the offsets array location BEFORE advancing off — the
	// readOff closure must not observe the moved offset
	offsetsAt := off
	readOff := func(i int) int {
		v := 0
		for b := 0; b < offSize; b++ {
			v = v<<8 | int(data[offsetsAt+i*offSize+b])
		}
		return v
	}
	start := readOff(0) - 1 // offsets are 1-based relative to after the array
	end := readOff(count) - 1
	off += offSize * (count + 1)
	if start < 0 || end < start || off+end > len(data) {
		return nil, off, fmt.Errorf("cff: index offsets invalid")
	}
	entries := make([][]byte, count)
	for i := 0; i < count; i++ {
		s := readOff(i) - 1
		e := readOff(i+1) - 1
		if s < 0 || e < s || off+e > len(data) {
			return nil, off, fmt.Errorf("cff: index entry %d out of range", i)
		}
		entries[i] = data[off+s : off+e]
	}
	_ = start
	_ = end
	off += end
	return entries, off, nil
}

// cffDictOp decodes a DICT (TopDICT/PrivateDICT): returns operand stack and
// operator per entry.
type cffDictEntry struct {
	op  int
	ops []float64
}

func cffParseDict(data []byte) []cffDictEntry {
	var entries []cffDictEntry
	var stack []float64
	i := 0
	readInt := func(i int) (float64, int, bool) {
		if i >= len(data) {
			return 0, i, false
		}
		b0 := int(data[i])
		switch {
		case b0 == 28 && i+2 < len(data):
			v := int16(binary.BigEndian.Uint16(data[i+1:]))
			return float64(v), i + 3, true
		case b0 == 29 && i+4 < len(data):
			v := int32(binary.BigEndian.Uint32(data[i+1:]))
			return float64(v), i + 5, true
		case b0 == 30:
			// real number (BCD-ish); parse until end-of-real op (0xf)
			j := i + 1
			val := 0.0
			done := false
			var digits []byte
			for j < len(data) && !done {
				b := data[j]
				for _, nib := range []byte{b >> 4, b & 0xf} {
					switch {
					case nib <= 9:
						digits = append(digits, '0'+nib)
					case nib == 0xa:
						digits = append(digits, '.')
					case nib == 0xb:
						digits = append(digits, 'E')
					case nib == 0xc:
						digits = append(digits, 'E', '-')
					case nib == 0xe:
						digits = append(digits, '-')
					case nib == 0xf:
						done = true
					}
					if done {
						break
					}
				}
				j++
			}
			var f float64
			fmt.Sscanf(string(digits), "%g", &f)
			val = f
			return val, j, true
		case b0 >= 32 && b0 <= 246:
			return float64(b0 - 139), i + 1, true
		case b0 >= 247 && b0 <= 250 && i+1 < len(data):
			return float64((b0-247)*256 + int(data[i+1]) + 108), i + 2, true
		case b0 >= 251 && b0 <= 254 && i+1 < len(data):
			return float64(-(b0-251)*256 - int(data[i+1]) - 108), i + 2, true
		}
		return 0, i, false
	}
	for i < len(data) {
		v, ni, ok := readInt(i)
		if ok {
			stack = append(stack, v)
			i = ni
			continue
		}
		b0 := int(data[i])
		if b0 == 12 {
			if i+1 >= len(data) {
				break
			}
			op := 1200 + int(data[i+1])
			// copy — the stack slice's backing array is reused after
			// stack[:0], which would clobber previously stored entries
			ops := make([]float64, len(stack))
			copy(ops, stack)
			entries = append(entries, cffDictEntry{op: op, ops: ops})
			stack = stack[:0]
			i += 2
			continue
		}
		if b0 <= 21 {
			ops := make([]float64, len(stack))
			copy(ops, stack)
			entries = append(entries, cffDictEntry{op: b0, ops: ops})
			stack = stack[:0]
			i++
			continue
		}
		i++ // skip unknown byte
	}
	return entries
}

// cffStandardStrings: SIDs 1..189 of the CFF standard strings (ISO Latin-1
// order plus the duplicated hyphen at 108). Names beyond this table (custom
// strings) are not needed for WinAnsi text.
var cffStandardStrings = []string{
	"", "space", "exclam", "quotedbl", "numbersign", "dollar", "percent",
	"ampersand", "quoteright", "parenleft", "parenright", "asterisk",
	"plus", "comma", "hyphen", "period", "slash", "zero", "one", "two",
	"three", "four", "five", "six", "seven", "eight", "nine", "colon",
	"semicolon", "less", "equal", "greater", "question", "at", "A", "B",
	"C", "D", "E", "F", "G", "H", "I", "J", "K", "L", "M", "N", "O", "P",
	"Q", "R", "S", "T", "U", "V", "W", "X", "Y", "Z", "bracketleft",
	"backslash", "bracketright", "asciicircum", "underscore", "quoteleft",
	"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l", "m", "n",
	"o", "p", "q", "r", "s", "t", "u", "v", "w", "x", "y", "z",
	"braceleft", "bar", "braceright", "asciitilde", "exclamdown", "cent",
	"sterling", "currency", "yen", "brokenbar", "section", "dieresis",
	"copyright", "ordfeminine", "guillemotleft", "logicalnot", "hyphen",
	"registered", "macron", "degree", "plusminus", "twosuperior",
	"threesuperior", "acute", "mu", "paragraph", "middledot", "cedilla",
	"onesuperior", "ordmasculine", "guillemotright", "onequarter",
	"onehalf", "threequarters", "questiondown", "Agrave", "Aacute",
	"Acircumflex", "Atilde", "Adieresis", "Aring", "AE", "Ccedilla",
	"Egrave", "Eacute", "Ecircumflex", "Edieresis", "Igrave", "Iacute",
	"Icircumflex", "Idieresis", "Eth", "Ntilde", "Ograve", "Oacute",
	"Ocircumflex", "Otilde", "Odieresis", "multiply", "Oslash", "Ugrave",
	"Uacute", "Ucircumflex", "Udieresis", "Yacute", "Thorn", "germandbls",
	"agrave", "aacute", "acircumflex", "atilde", "adieresis", "aring",
	"ae", "ccedilla", "egrave", "eacute", "ecircumflex", "edieresis",
	"igrave", "iacute", "icircumflex", "idieresis", "eth", "ntilde",
	"ograve", "oacute", "ocircumflex", "otilde", "odieresis", "divide",
	"oslash", "ugrave", "uacute", "ucircumflex", "udieresis", "yacute",
	"thorn", "ydieresis",
}

// winAnsiGlyphNames: char code (32..126) -> standard glyph name for the
// ASCII range; higher WinAnsi codes map through the Latin-1 names above.
func winAnsiGlyphName(code int) string {
	switch {
	case code >= 32 && code <= 126:
		// 95 names start at SID 1 (space) for codes 32..126:
		// code-32+1? No — code 32=space(1), 33=exclam(2) ... 126=asciitilde(95)
		idx := code - 31
		if idx < len(cffStandardStrings) {
			return cffStandardStrings[idx]
		}
	case code == 128:
		return "quotedblbase"
	case code == 138:
		return "Scaron" // not in the table above; uncommon
	case code == 154:
		return "scaron"
	case code == 145:
		return "quoteleft"
	case code == 146:
		return "quoteright"
	case code == 147:
		return "quotedblleft"
	case code == 148:
		return "quotedblright"
	case code == 149:
		return "bullet"
	case code == 150:
		return "endash"
	case code == 151:
		return "emdash"
	case code == 132:
		return "quotedblbase"
	case code == 133:
		return "ellipsis"
	case code == 139:
		return "guilsinglleft"
	case code == 155:
		return "guilsinglright"
	case code >= 160 && code <= 255:
		// Latin-1 supplement names follow asciitilde (SID 95+1=96 ...)
		idx := 96 + code - 160
		if idx < len(cffStandardStrings) {
			return cffStandardStrings[idx]
		}
	}
	return ""
}

// parseCFFFont parses a CFF (Type1C) font program.
func parseCFFFont(data []byte) (*cffFont, error) {
	if len(data) < 8 || data[0] != 1 {
		return nil, fmt.Errorf("cff: bad header")
	}
	hdrSize := int(data[2])
	if hdrSize <= 0 || hdrSize >= len(data) {
		return nil, fmt.Errorf("cff: bad header size")
	}
	off := hdrSize

	// INDEX order: Name, TopDICT, String, GlobalSubr
	_, noff, err := cffIndex(data, off)
	if err != nil {
		return nil, fmt.Errorf("cff: name index: %w", err)
	}
	topEntries, noff2, err := cffIndex(data, noff)
	if err != nil || len(topEntries) == 0 {
		return nil, fmt.Errorf("cff: top dict: %w", err)
	}
	_, _, err = cffIndex(data, noff2) // string INDEX
	if err != nil {
		return nil, fmt.Errorf("cff: string index: %w", err)
	}
	gsubrs, _, err := cffIndex(data, noff2)
	if err != nil {
		return nil, fmt.Errorf("cff: gsubr index: %w", err)
	}

	top := cffParseDict(topEntries[0])
	var charstringsOff, charsetOff, fdArrayOff, fdSelectOff int
	for _, e := range top {
		switch e.op {
		case 15:
			if len(e.ops) > 0 {
				charsetOff = int(e.ops[len(e.ops)-1])
			}
		case 17:
			if len(e.ops) > 0 {
				charstringsOff = int(e.ops[len(e.ops)-1])
			}
		case 1236: // 12 36 FDArray — CID-keyed CFF
			if len(e.ops) > 0 {
				fdArrayOff = int(e.ops[len(e.ops)-1])
			}
		case 1237: // 12 37 FDSelect
			if len(e.ops) > 0 {
				fdSelectOff = int(e.ops[len(e.ops)-1])
			}
		}
	}
	if charstringsOff <= 0 || charstringsOff >= len(data) {
		return nil, fmt.Errorf("cff: no charstrings")
	}

	// Private DICT (for local subrs)
	privateOff, privateLen := 0, 0
	for _, e := range top {
		if e.op == 18 && len(e.ops) >= 2 {
			privateLen = int(e.ops[0])
			privateOff = int(e.ops[1])
		}
	}
	var localSubrs [][]byte
	if privateOff > 0 && privateOff+privateLen <= len(data) {
		priv := cffParseDict(data[privateOff : privateOff+privateLen])
		for _, e := range priv {
			if e.op == 19 && len(e.ops) > 0 { // Subrs (offset relative to PrivateDICT)
				so := privateOff + int(e.ops[len(e.ops)-1])
				localSubrs, _, _ = cffIndex(data, so)
			}
		}
	}

	charStrings, _, err := cffIndex(data, charstringsOff)
	if err != nil {
		return nil, fmt.Errorf("cff: charstrings: %w", err)
	}

	// charset: GID -> SID -> name (format 0 only; formats 1/2 are rare in
	// subset fonts and fall back to position-based names)
	nameToGID := make(map[string]uint16, len(charStrings))
	glyphNames := make([]string, len(charStrings))
	glyphNames[0] = ".notdef"
	cidToGID := map[int]int{0: 0} // .notdef; populated for CID-keyed fonts
	if charsetOff > 0 && charsetOff < len(data) && fdArrayOff > 0 {
		// CID-keyed charset: GID -> CID (format 0 u16 array, or 1/2
		// ranges). Build the CID->GID inversion the renderer needs —
		// subset fonts renumber, so CID != GID in general.
		cidToGID[0] = 0 // .notdef
		co := charsetOff + 1
		gid := 1
		switch data[charsetOff] {
		case 0:
			for gid < len(charStrings) && co+2 <= len(data) {
				cid := int(binary.BigEndian.Uint16(data[co:]))
				co += 2
				cidToGID[cid] = gid
				gid++
			}
		case 1, 2:
			for gid < len(charStrings) && co+3 <= len(data) {
				first := int(binary.BigEndian.Uint16(data[co:]))
				var nLeft int
				if data[charsetOff] == 1 {
					nLeft = int(data[co+2])
					co += 3
				} else {
					if co+4 > len(data) {
						break
					}
					nLeft = int(binary.BigEndian.Uint16(data[co+2:]))
					co += 4
				}
				for k := 0; k <= nLeft && gid < len(charStrings); k++ {
					cidToGID[first+k] = gid
					gid++
				}
			}
		}
	}
	if charsetOff > 0 && charsetOff < len(data) && fdArrayOff == 0 {
		switch data[charsetOff] {
		case 0:
			co := charsetOff + 1
			for gid := 1; gid < len(charStrings) && co+2 <= len(data); gid++ {
				sid := int(binary.BigEndian.Uint16(data[co:]))
				co += 2
				var name string
				if sid < len(cffStandardStrings) {
					name = cffStandardStrings[sid]
				} else {
					// custom strings — not mapped (would need the string
					// INDEX's own data; uncommon in subset text fonts)
					name = ""
				}
				glyphNames[gid] = name
				if name != "" {
					if _, exists := nameToGID[name]; !exists {
						nameToGID[name] = uint16(gid)
					}
				}
			}
		case 1, 2:
			// Range-based charset: groups of (first SID, nLeft) with
			// nLeft as u8 (format 1) or u16 (format 2).
			co := charsetOff + 1
			gid := 1
			for gid < len(charStrings) && co+3 <= len(data) {
				first := int(binary.BigEndian.Uint16(data[co:]))
				var nLeft int
				if data[charsetOff] == 1 {
					nLeft = int(data[co+2])
					co += 3
				} else {
					if co+4 > len(data) {
						break
					}
					nLeft = int(binary.BigEndian.Uint16(data[co+2:]))
					co += 4
				}
				for k := 0; k <= nLeft && gid < len(charStrings); k++ {
					sid := first + k
					var name string
					if sid < len(cffStandardStrings) {
						name = cffStandardStrings[sid]
					}
					glyphNames[gid] = name
					if name != "" {
						if _, exists := nameToGID[name]; !exists {
							nameToGID[name] = uint16(gid)
						}
					}
					gid++
				}
			}
		}
	}

	f := &cffFont{
		nameToGID:  nameToGID,
		advWidths:  make([]float64, len(charStrings)),
		unitsPerEm: 1000,
		cidToGID:   cidToGID,
	}
	f.cidToGID[0] = 0
	subrBias := func(n int) float64 {
		switch {
		case n < 1240:
			return 107
		case n < 33700:
			return 1131
		default:
			return 32768
		}
	}
	globalBias := subrBias(len(gsubrs))

	// per-glyph local subrs: CID-keyed fonts carry one Private DICT per
	// FD (FDArray + FDSelect); simple fonts share the single top-level
	// Private DICT
	f.isCID = fdArrayOff > 0
	glyphSubrs := make([][][]byte, len(charStrings))
	glyphBias := make([]float64, len(charStrings))
	if f.isCID {
		if fds, _, err := cffIndex(data, fdArrayOff); err == nil {
			fdSubrs := make([][][]byte, len(fds))
			fdBias := make([]float64, len(fds))
			for i, fd := range fds {
				for _, e := range cffParseDict(fd) {
					if e.op == 18 && len(e.ops) >= 2 { // Private [size offset]
						plen, poff := int(e.ops[0]), int(e.ops[1])
						if poff > 0 && poff+plen <= len(data) {
							for _, pe := range cffParseDict(data[poff : poff+plen]) {
								if pe.op == 19 && len(pe.ops) > 0 { // Subrs
									subrs, _, _ := cffIndex(data, poff+int(pe.ops[len(pe.ops)-1]))
									fdSubrs[i] = subrs
									fdBias[i] = subrBias(len(subrs))
								}
							}
						}
					}
				}
			}
			assignFD := func(gid, fdi int) {
				if fdi >= 0 && fdi < len(fdSubrs) {
					glyphSubrs[gid] = fdSubrs[fdi]
					glyphBias[gid] = fdBias[fdi]
				}
			}
			if fdSelectOff > 0 && fdSelectOff < len(data) {
				switch data[fdSelectOff] {
				case 0:
					co := fdSelectOff + 1
					for gid := 0; gid < len(charStrings) && co < len(data); gid++ {
						assignFD(gid, int(data[co]))
						co++
					}
				case 3:
					co := fdSelectOff + 1
					if co+2 <= len(data) {
						nRanges := int(binary.BigEndian.Uint16(data[co:]))
						co += 2
						prev := 0
						lastFD := 0
						for r := 0; r < nRanges && co+3 <= len(data); r++ {
							first := int(binary.BigEndian.Uint16(data[co:]))
							fdi := int(data[co+2])
							co += 3
							for g := prev; g < first && g < len(charStrings); g++ {
								assignFD(g, fdi)
							}
							prev = first
							lastFD = fdi
						}
						if co+2 <= len(data) {
							sentinel := int(binary.BigEndian.Uint16(data[co:]))
							for g := prev; g < sentinel && g < len(charStrings); g++ {
								assignFD(g, lastFD)
							}
						}
					}
				}
			}
		}
	} else {
		for gid := range glyphSubrs {
			glyphSubrs[gid] = localSubrs
			glyphBias[gid] = subrBias(len(localSubrs))
		}
	}

	f.charstrings = charStrings
	f.glyphCache = make([][][]glyphPt, len(charStrings))
	f.glyphSubrs = glyphSubrs
	f.glyphBias = glyphBias
	f.gsubrs = gsubrs
	f.globalBias = globalBias
	return f, nil
}

// glyphContours returns the pre-flattened contours of glyph gid,
// interpreting its charstring on first access.
func (f *cffFont) glyphContours(gid int) [][]glyphPt {
	if gid < 0 || gid >= len(f.charstrings) {
		return nil
	}
	if c := f.glyphCache[gid]; c != nil {
		return c
	}
	contours, _ := interpretType2Charstring(f.charstrings[gid], f.gsubrs, f.glyphSubrs[gid], f.globalBias, f.glyphBias[gid])
	f.glyphCache[gid] = contours
	return contours
}

// glyphByName resolves a glyph name (e.g. from WinAnsi encoding) to its
// pre-flattened contours, or nil when the font has no such glyph.
func (f *cffFont) glyphGID(name string) (int, bool) {
	if name == "" {
		return 0, false
	}
	gid, ok := f.nameToGID[name]
	if !ok {
		return 0, false
	}
	return int(gid), true
}

// type2Ctx carries the charstring interpreter state.
type type2Ctx struct {
	x, y       float64
	stack      []float64
	contours   [][]glyphPt
	cur        []glyphPt
	open       bool
	width      float64
	haveWidth  bool
	transHints bool
}

// interpretType2Charstring executes a Type 2 charstring and returns
// pre-flattened contours (all points on-curve; cubic segments sampled) and
// the advance width when the charstring carries a width hint.
func interpretType2Charstring(cs []byte, gsubrs, subrs [][]byte, gBias, lBias float64) ([][]glyphPt, float64) {
	ctx := &type2Ctx{width: -1}
	ctx.run(cs, gsubrs, subrs, gBias, lBias, 0)
	if ctx.cur != nil && len(ctx.cur) >= 3 {
		ctx.contours = append(ctx.contours, ctx.cur)
	}
	return ctx.contours, ctx.width
}

func (c *type2Ctx) flushContour() {
	if len(c.cur) >= 3 {
		c.contours = append(c.contours, c.cur)
	}
	c.cur = nil
	c.open = false
}

func (c *type2Ctx) pop() (float64, bool) {
	if len(c.stack) == 0 {
		return 0, false
	}
	v := c.stack[len(c.stack)-1]
	c.stack = c.stack[:len(c.stack)-1]
	return v, true
}

// run interprets one charstring; depth guards against runaway recursion.
func (c *type2Ctx) run(cs []byte, gsubrs, subrs [][]byte, gBias, lBias float64, depth int) {
	if depth > 10 {
		return
	}
	i := 0
	for i < len(cs) {
		b0 := int(cs[i])
		switch {
		case b0 == 255:
			// 16.16 fixed number
			if i+4 < len(cs) {
				v := int32(binary.BigEndian.Uint32(cs[i+1 : i+5]))
				c.stack = append(c.stack, float64(v)/65536)
			}
			i += 5
		case b0 >= 32 && b0 <= 246:
			c.stack = append(c.stack, float64(b0-139))
			i++
		case b0 >= 247 && b0 <= 250 && i+1 < len(cs):
			c.stack = append(c.stack, float64(int(b0-247)*256+int(cs[i+1])+108))
			i += 2
		case b0 >= 251 && b0 <= 254 && i+1 < len(cs):
			c.stack = append(c.stack, float64(-int(b0-251)*256-int(cs[i+1])-108))
			i += 2
		case b0 == 28 && i+2 < len(cs):
			c.stack = append(c.stack, float64(int16(binary.BigEndian.Uint16(cs[i+1:]))))
			i += 3
		case b0 == 12:
			// escape op — flex and friends; approximate flex curves
			if i+1 >= len(cs) {
				return
			}
			e := int(cs[i+1])
			switch e {
			case 35: // flex: dx1..dx6 dy? — 13 args, draws 2 curves
				if len(c.stack) >= 13 {
					args := c.stack[len(c.stack)-13:]
					c.cubicRel(args[0], args[1], args[2], args[3], args[4], args[5])
					c.cubicRel(args[6]+args[10], args[7]+args[11], args[8]+args[10], args[9]+args[11], args[10], args[11])
					c.stack = c.stack[:len(c.stack)-13]
				}
			case 34: // hflex
			case 36: // hflex1
			case 37: // flex1
				// approximated by a straight segment when arg shapes are
				// unusual — flex curves are rare in text glyphs
				c.stack = c.stack[:0]
			default:
				c.stack = c.stack[:0]
			}
			i += 2
		default: // operators 1..31
			switch b0 {
			case 14: // endchar
				if !c.haveWidth && len(c.stack)%2 == 1 {
					c.width = c.stack[0]
					c.haveWidth = true
				}
				c.flushContour()
				return
			case 21: // rmoveto
				if !c.haveWidth && len(c.stack)%2 == 1 {
					c.width = c.stack[0]
					c.haveWidth = true
					c.stack = c.stack[1:]
				}
				if len(c.stack) >= 2 {
					c.flushContour()
					c.x += c.stack[len(c.stack)-2]
					c.y += c.stack[len(c.stack)-1]
					c.cur = append(c.cur, glyphPt{x: c.x, y: c.y, onCurve: true})
					c.open = true
				}
				c.stack = c.stack[:0]
			case 22: // hmoveto
				if !c.haveWidth && len(c.stack)%2 == 1 {
					c.width = c.stack[0]
					c.haveWidth = true
					c.stack = c.stack[1:]
				}
				if len(c.stack) >= 1 {
					c.flushContour()
					c.x += c.stack[len(c.stack)-1]
					c.cur = append(c.cur, glyphPt{x: c.x, y: c.y, onCurve: true})
					c.open = true
				}
				c.stack = c.stack[:0]
			case 4: // vmoveto
				if !c.haveWidth && len(c.stack)%2 == 1 {
					c.width = c.stack[0]
					c.haveWidth = true
					c.stack = c.stack[1:]
				}
				if len(c.stack) >= 1 {
					c.flushContour()
					c.y += c.stack[len(c.stack)-1]
					c.cur = append(c.cur, glyphPt{x: c.x, y: c.y, onCurve: true})
					c.open = true
				}
				c.stack = c.stack[:0]
			case 1, 3, 18, 23: // hstem/vstem/hstemhm/vstemhm — hinting only
				if !c.haveWidth && len(c.stack)%2 == 1 {
					c.width = c.stack[0]
					c.haveWidth = true
				}
				c.stack = c.stack[:0]
			case 5: // rlineto — pairs, may repeat
				for j := 0; j+1 < len(c.stack); j += 2 {
					c.x += c.stack[j]
					c.y += c.stack[j+1]
					c.cur = append(c.cur, glyphPt{x: c.x, y: c.y, onCurve: true})
				}
				c.stack = c.stack[:0]
			case 6: // hlineto — alternating h/v
				c.altLine(c.stack, true)
			case 7: // vlineto — alternating v/h
				c.altLine(c.stack, false)
			case 8: // rrcurveto — 6 args per curve, may repeat
				for j := 0; j+5 < len(c.stack); j += 6 {
					c.cubicRel(c.stack[j], c.stack[j+1], c.stack[j+2], c.stack[j+3], c.stack[j+4], c.stack[j+5])
				}
				c.stack = c.stack[:0]
			case 24: // rcurveline
				j := 0
				for j+11 < len(c.stack) {
					c.cubicRel(c.stack[j], c.stack[j+1], c.stack[j+2], c.stack[j+3], c.stack[j+4], c.stack[j+5])
					j += 6
				}
				if j+1 < len(c.stack) {
					c.x += c.stack[j]
					c.y += c.stack[j+1]
					c.cur = append(c.cur, glyphPt{x: c.x, y: c.y, onCurve: true})
				}
				c.stack = c.stack[:0]
			case 25: // rlinecurve
				j := 0
				for j+7 < len(c.stack)-5 {
					c.x += c.stack[j]
					c.y += c.stack[j+1]
					c.cur = append(c.cur, glyphPt{x: c.x, y: c.y, onCurve: true})
					j += 2
				}
				if j+5 < len(c.stack) {
					c.cubicRel(c.stack[j], c.stack[j+1], c.stack[j+2], c.stack[j+3], c.stack[j+4], c.stack[j+5])
				}
				c.stack = c.stack[:0]
			case 26: // vvcurveto
				j := 0
				if len(c.stack)%4 == 1 {
					c.x += c.stack[0]
					j = 1
				}
				for j+3 < len(c.stack) {
					dy1 := c.stack[j]
					c.cubicRel(0, dy1, c.stack[j+1], c.stack[j+2], 0, c.stack[j+3])
					j += 4
				}
				c.stack = c.stack[:0]
			case 27: // hhcurveto
				j := 0
				if len(c.stack)%4 == 1 {
					c.y += c.stack[0]
					j = 1
				}
				for j+3 < len(c.stack) {
					c.cubicRel(c.stack[j], 0, c.stack[j+1], c.stack[j+2], c.stack[j+3], 0)
					j += 4
				}
				c.stack = c.stack[:0]
			case 30: // vhcurveto
				c.altCurve(c.stack, true)
			case 31: // hvcurveto
				c.altCurve(c.stack, false)
			case 10: // callsubr
				if v, ok := c.pop(); ok {
					idx := int(v + lBias)
					if idx >= 0 && idx < len(subrs) {
						c.run(subrs[idx], gsubrs, subrs, gBias, lBias, depth+1)
					}
				}
			case 29: // callgsubr
				if v, ok := c.pop(); ok {
					idx := int(v + gBias)
					if idx >= 0 && idx < len(gsubrs) {
						c.run(gsubrs[idx], gsubrs, subrs, gBias, lBias, depth+1)
					}
				}
			case 11: // return
				return
			case 19, 20: // hintmask/cntrmask — hinting only
				c.stack = c.stack[:0]
			case 12: // handled above
			default: // drop unknown ops
				c.stack = c.stack[:0]
			}
			i++
		}
	}
}

func (c *type2Ctx) altLine(stack []float64, hFirst bool) {
	h := hFirst
	for j := 0; j < len(stack); j++ {
		if h {
			c.x += stack[j]
		} else {
			c.y += stack[j]
		}
		c.cur = append(c.cur, glyphPt{x: c.x, y: c.y, onCurve: true})
		h = !h
	}
	c.stack = c.stack[:0]
}

func (c *type2Ctx) altCurve(stack []float64, vertFirst bool) {
	// vhcurveto (30, vertFirst=true): dy1 dx2 dy2 dx3 [dy3] — first
	// displacement vertical. hvcurveto (31): dx1 dy2 dx2 dy3 [dx3] —
	// first horizontal. Groups of 4 repeat alternating; an optional 5th
	// argument on the final curve supplies both final displacements.
	j := 0
	v := vertFirst
	for j < len(stack) {
		remaining := len(stack) - j
		if remaining == 5 {
			a, b, cc, d, e := stack[j], stack[j+1], stack[j+2], stack[j+3], stack[j+4]
			if v {
				c.cubicRel(0, a, b, cc, d, e)
			} else {
				c.cubicRel(a, 0, b, cc, e, d)
			}
			j += 5
			break
		}
		if remaining < 4 {
			break
		}
		a, b, cc, d := stack[j], stack[j+1], stack[j+2], stack[j+3]
		if v {
			c.cubicRel(0, a, b, cc, d, 0)
		} else {
			c.cubicRel(a, 0, b, cc, 0, d)
		}
		v = !v
		j += 4
	}
	c.stack = c.stack[:0]
}

func (c *type2Ctx) cubicRel(dx1, dy1, dx2, dy2, dx3, dy3 float64) {
	// flatten the cubic from the current point with 8 samples
	if cap(c.cur)-len(c.cur) < 8 {
		grown := make([]glyphPt, len(c.cur), len(c.cur)+64)
		copy(grown, c.cur)
		c.cur = grown
	}
	x0, y0 := c.x, c.y
	x1, y1 := x0+dx1, y0+dy1
	x2, y2 := x1+dx2, y1+dy2
	x3, y3 := x2+dx3, y2+dy3
	const steps = 8
	for s := 1; s <= steps; s++ {
		t := float64(s) / steps
		mt := 1 - t
		px := mt*mt*mt*x0 + 3*mt*mt*t*x1 + 3*mt*t*t*x2 + t*t*t*x3
		py := mt*mt*mt*y0 + 3*mt*mt*t*y1 + 3*mt*t*t*y2 + t*t*t*y3
		c.cur = append(c.cur, glyphPt{x: px, y: py, onCurve: true})
	}
	c.x, c.y = x3, y3
}

// test marker 12345
