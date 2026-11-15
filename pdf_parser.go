package gopdf

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/andybalholm/brotli"
)

// Pre-compiled regexes for PDF parser — avoids recompilation on every call.
var (
	reObjHeader = regexp.MustCompile(`(\d+)\s+0\s+obj\b`)
	reRootRef   = regexp.MustCompile(`/Root\s+(\d+)\s+0\s+R`)
	reInfoRef   = regexp.MustCompile(`/Info\s+(\d+)\s+0\s+R`)
	reNamedRef  = regexp.MustCompile(`/(\w+)\s+(\d+)\s+0\s+R`)
	reMediaBox  = regexp.MustCompile(`/MediaBox\s*\[\s*([\d.]+)\s+([\d.]+)\s+([\d.]+)\s+([\d.]+)\s*\]`)
)

// ============================================================
// PDF raw parser — reads cross-ref table, objects, streams
// from raw PDF bytes. Used for text/image extraction.
// ============================================================

// rawPDFParser parses a raw PDF byte slice to extract objects.
type rawPDFParser struct {
	data    []byte
	objects map[int]rawPDFObject // objNum -> object
	pages   []rawPDFPage
	root    int // root catalog obj number
	info    int    // /Info metadata dict obj number (0 = none)
	infoDict string // inline trailer /Info dict (when not an indirect ref)

	// form caches (per parser instance = per extraction call): form streams
	// recur on every page (stamps, watermarks) — fonts, nested XObjects and
	// the un-offset runs are parsed once and translated on reuse. Runs are
	// cached per mediaBox height: base Y depends on the calling page's
	// height, and pages within one document may differ.
	fontCache map[int]map[string]*fontInfo
	xobjCache map[int]map[string]int
	formRuns  map[int]map[float64][]ExtractedText // objNum -> pageH -> runs
}

// rawPDFObject holds a parsed PDF object.
type rawPDFObject struct {
	num    int
	dict   string // dictionary content between << >>
	stream []byte // decompressed stream content (nil if not a stream)
	// body holds the object content for objects with no dictionary and no
	// stream (arrays, numbers, names — e.g. an indirect /Widths array).
	body string
}

// rawPDFPage holds parsed page info.
type rawPDFPage struct {
	objNum    int
	mediaBox  [4]float64
	cropBox   [4]float64 // /CropBox (visible area); valid when hasCrop
	hasCrop   bool
	rotate    int // /Rotate degrees clockwise (0/90/180/270)
	contents  []int // content stream obj numbers
	resources rawPDFResources
}

// rawPDFResources holds resource references for a page.
type rawPDFResources struct {
	fonts      map[string]int // /F1 -> obj number
	xobjs      map[string]int // /Im1 -> obj number
	extgstates map[string]int // /GS1 -> obj number (ExtGState dicts)
}

// newRawPDFParser creates a parser for the given PDF data.
func newRawPDFParser(data []byte) (*rawPDFParser, error) {
	p := &rawPDFParser{
		data:      data,
		objects:   make(map[int]rawPDFObject),
		fontCache: make(map[int]map[string]*fontInfo),
		xobjCache: make(map[int]map[string]int),
		formRuns:  make(map[int]map[float64][]ExtractedText),
	}
	if err := p.parse(); err != nil {
		return nil, fmt.Errorf("pdf parse: %w", err)
	}
	return p, nil
}

func (p *rawPDFParser) parse() error {
	p.parseObjects()
	p.parseObjectStreams()
	p.findRoot()
	p.parsePages()
	return nil
}

// objHeaderLoc locates one "N 0 obj" header: the object number, the start of
// the header (first digit) and the offset just past "obj".
type objHeaderLoc struct {
	num       int
	start     int // offset of the first digit of N
	bodyStart int // offset just past "obj"
}

// isPDFSpace reports whether c is PDF whitespace.
func isPDFSpace(c byte) bool {
	switch c {
	case ' ', '\n', '\r', '\t', '\f', 0:
		return true
	}
	return false
}

// scanObjectHeaders finds every "N 0 obj" header without regexp. The regex
// equivalent dominated extraction CPU on large files; a needle scan for
// " obj" with backward parsing is an order of magnitude faster.
func scanObjectHeaders(data []byte) []objHeaderLoc {
	var out []objHeaderLoc
	const needle = " obj"
	pos := 0
	for {
		idx := bytes.Index(data[pos:], []byte(needle))
		if idx < 0 {
			break
		}
		i := pos + idx
		pos = i + len(needle)
		// parse backwards: skip spaces, expect generation '0', skip spaces,
		// parse digits, verify a space separates N and the generation
		j := i - 1
		for j >= 0 && isPDFSpace(data[j]) {
			j--
		}
		if j < 0 || data[j] != '0' {
			continue
		}
		j--
		for j >= 0 && isPDFSpace(data[j]) {
			j--
		}
		e := j // last digit of N
		for j >= 0 && data[j] >= '0' && data[j] <= '9' {
			j--
		}
		if j == e || e+1 >= len(data) || !isPDFSpace(data[e+1]) {
			continue // no digits, or the "0" was part of a multi-digit generation
		}
		if j >= 0 {
			c := data[j]
			if c != '>' && c != '%' && !isPDFSpace(c) {
				continue // not a token boundary before N
			}
		}
		num, err := strconv.Atoi(string(data[j+1 : e+1]))
		if err != nil {
			continue
		}
		out = append(out, objHeaderLoc{num: num, start: j + 1, bodyStart: i + len(needle)})
	}
	return out
}

// parseObjects finds all "N 0 obj ... endobj" blocks.
func (p *rawPDFParser) parseObjects() {
	headers := scanObjectHeaders(p.data)
	// Locate all "endobj" markers once, then pair them with object headers
	// using a two-pointer merge (both are in file order). Scanning from each
	// object start would be O(objects × filesize) on large PDFs.
	endObj := []byte("endobj")
	endPositions := make([]int, 0, len(headers))
	for pos := 0; ; {
		idx := bytes.Index(p.data[pos:], endObj)
		if idx < 0 {
			break
		}
		pos += idx
		endPositions = append(endPositions, pos)
		pos += len(endObj)
	}
	nextEnd := 0
	for _, h := range headers {
		num := h.num
		objStart := h.start
		// find the first endobj at or after this object's start
		for nextEnd < len(endPositions) && endPositions[nextEnd] < objStart {
			nextEnd++
		}
		if nextEnd >= len(endPositions) {
			continue
		}
		endPos := endPositions[nextEnd]
		objData := p.data[objStart : endPos+len(endObj)]
		obj := rawPDFObject{num: num}
		// extract dictionary
		if dictStart := bytes.Index(objData, []byte("<<")); dictStart >= 0 {
			obj.dict = extractDict(objData[dictStart:])
		} else if bytes.Index(objData, []byte("stream")) < 0 {
			// no dict and no stream: keep the body (array/number/name objects)
			obj.body = trimObjBody(objData)
		}
		// extract stream
		if streamStart := bytes.Index(objData, []byte("stream")); streamStart >= 0 {
			streamData := objData[streamStart+6:]
			// skip \r\n or \n after "stream"
			if len(streamData) > 0 && streamData[0] == '\r' {
				streamData = streamData[1:]
			}
			if len(streamData) > 0 && streamData[0] == '\n' {
				streamData = streamData[1:]
			}
			if endStream := bytes.Index(streamData, []byte("endstream")); endStream >= 0 {
				raw := streamData[:endStream]
				// Prefer /Length: the EOL before "endstream" is a separator,
				// not data. Trim-right would corrupt streams whose payload
				// legitimately ends with a newline byte (zlib checksums can).
				if l, direct := streamLengthDirect(obj.dict); direct && l > 0 && int(l) <= len(raw) {
					raw = raw[:l]
				} else {
					raw = bytes.TrimRight(raw, "\r\n")
				}
				obj.stream = p.decodeStream(raw, obj.dict)
			}
		}
		p.objects[num] = obj
	}
}

// streamLengthDirect reads /Length as a DIRECT integer (not an indirect
// "N 0 R" reference). ok is false when /Length is absent or indirect.
func streamLengthDirect(dict string) (int64, bool) {
	for idx := 0; ; {
		i := strings.Index(dict[idx:], "/Length")
		if i < 0 {
			return 0, false
		}
		idx += i
		// exact key: /Length must not prefix a longer name (/Length1)
		end := idx + len("/Length")
		if end < len(dict) {
			c := dict[end]
			if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
				idx = end
				continue
			}
		}
		return streamLengthDirectAt(dict[end:], dict)
	}
}

// streamLengthDirectAt parses a direct integer at the start of rest (which
// follows an exact "/Length" key).
func streamLengthDirectAt(rest, dict string) (int64, bool) {
	// skip whitespace
	i := 0
	for i < len(rest) && isPDFSpace(rest[i]) {
		i++
	}
	start := i
	for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
		i++
	}
	if i == start {
		return 0, false
	}
	n, err := strconv.ParseInt(rest[start:i], 10, 32)
	if err != nil {
		return 0, false
	}
	// "N 0 R" → indirect reference, not a direct length
	tail := strings.TrimLeft(rest[i:], " \t\r\n")
	if strings.HasPrefix(tail, "0 R") {
		return 0, false
	}
	return n, true
}

// trimObjBody strips the "N 0 obj" header and "endobj" trailer, returning the
// bare object content (used for array/number/name objects).
func trimObjBody(objData []byte) string {
	if m := reObjHeader.FindSubmatchIndex(objData); m != nil && m[0] == 0 {
		objData = objData[m[1]:]
	}
	objData = bytes.TrimSpace(objData)
	objData = bytes.TrimSuffix(objData, []byte("endobj"))
	return string(bytes.TrimSpace(objData))
}

// extractDict extracts the outermost <<...>> from data.
func extractDict(data []byte) string {
	depth := 0
	start := -1
	for i := 0; i < len(data)-1; i++ {
		if data[i] == '<' && data[i+1] == '<' {
			if depth == 0 {
				start = i
			}
			depth++
			i++
		} else if data[i] == '>' && data[i+1] == '>' {
			depth--
			if depth == 0 {
				return string(data[start : i+2])
			}
			i++
		}
	}
	if start >= 0 {
		return string(data[start:])
	}
	return ""
}

// parseObjectStreams expands objects stored inside object streams
// (/Type /ObjStm), used by PDF 1.5+ files. Without this, pages, fonts and
// ToUnicode CMaps of modern PDFs are invisible to the raw parser.
func (p *rawPDFParser) parseObjectStreams() {
	// Collect ObjStm object numbers first (map iteration + insert is unsafe).
	var stmNums []int
	for num, obj := range p.objects {
		if obj.stream != nil && strings.Contains(obj.dict, "/ObjStm") {
			stmNums = append(stmNums, num)
		}
	}
	for _, num := range stmNums {
		p.expandObjectStream(num)
	}
}

// expandObjectStream decodes one ObjStm into its contained objects.
func (p *rawPDFParser) expandObjectStream(num int) {
	obj := p.objects[num]
	n := extractIntValue(obj.dict, "/N")
	first := extractIntValue(obj.dict, "/First")
	if n <= 0 || first <= 0 || first > len(obj.stream) {
		return
	}
	header := string(obj.stream[:first])
	fields := strings.Fields(header)
	if len(fields) < 2*n {
		return
	}
	type entry struct {
		objNum int
		offset int
	}
	entries := make([]entry, 0, n)
	for i := 0; i < n; i++ {
		on, err1 := strconv.Atoi(fields[2*i])
		off, err2 := strconv.Atoi(fields[2*i+1])
		if err1 != nil || err2 != nil {
			return
		}
		entries = append(entries, entry{on, off})
	}
	for i, e := range entries {
		start := first + e.offset
		if start < 0 || start > len(obj.stream) {
			continue
		}
		end := len(obj.stream)
		if i+1 < len(entries) {
			end = first + entries[i+1].offset
			if end > len(obj.stream) {
				end = len(obj.stream)
			}
		}
		body := obj.stream[start:end]
		inner := p.parseInnerObject(e.objNum, body)
		p.objects[e.objNum] = inner
	}
}

// parseInnerObject parses the body of an object inside an ObjStm. Bodies lack
// the "N 0 obj" wrapper but otherwise look like normal objects.
func (p *rawPDFParser) parseInnerObject(num int, body []byte) rawPDFObject {
	res := rawPDFObject{num: num}
	s := string(body)
	// strip leading "N 0 obj" if a producer included it
	if m := reObjHeader.FindSubmatchIndex(body); m != nil && m[0] == 0 {
		body = body[m[1]:]
		s = string(body)
	}
	if dictStart := strings.Index(s, "<<"); dictStart >= 0 {
		res.dict = extractDict(body[dictStart:])
	} else if strings.Index(s, "stream") < 0 {
		// no dict and no stream: keep the body (array/number/name objects)
		res.body = trimObjBody(body)
	}
	if streamStart := strings.Index(s, "stream"); streamStart >= 0 {
		streamData := body[streamStart+6:]
		if len(streamData) > 0 && streamData[0] == '\r' {
			streamData = streamData[1:]
		}
		if len(streamData) > 0 && streamData[0] == '\n' {
			streamData = streamData[1:]
		}
		if endStream := bytes.Index(streamData, []byte("endstream")); endStream >= 0 {
			raw := streamData[:endStream]
			raw = bytes.TrimRight(raw, "\r\n")
			res.stream = p.decodeStream(raw, res.dict)
		}
	}
	return res
}

// decodeStream applies the filter chain named in dict to raw stream data.
// Supports FlateDecode (with or without predictors), ASCIIHexDecode and
// ASCII85Decode — the combinations covering virtually all content streams,
// ToUnicode CMaps and object streams.
func (p *rawPDFParser) decodeStream(raw []byte, dict string) []byte {
	data := raw
	for _, name := range streamFilterNames(dict) {
		data = p.applyFilter(name, data, dict)
	}
	return data
}

// streamFilterNames extracts the filter list from a dict's /Filter entry —
// a single name (/FlateDecode) or an array ([/ASCII85Decode /FlateDecode]) —
// in application order.
func streamFilterNames(dict string) []string {
	idx := strings.Index(dict, "/Filter")
	if idx < 0 {
		return nil
	}
	rest := strings.TrimLeft(dict[idx+len("/Filter"):], " \t\r\n")
	if strings.HasPrefix(rest, "[") {
		// array of names
		end := strings.Index(rest, "]")
		if end < 0 {
			rest = rest[1:]
		} else {
			rest = rest[1:end]
		}
		var names []string
		for _, m := range reFilterEntry.FindAllStringSubmatch(rest, -1) {
			names = append(names, m[1])
		}
		return names
	}
	if !strings.HasPrefix(rest, "/") {
		return nil
	}
	rest = rest[1:]
	// single name: ends at the next delimiter
	end := strings.IndexAny(rest, " \t\r\n/<>[]")
	if end < 0 {
		end = len(rest)
	}
	if end == 0 {
		return nil
	}
	return []string{rest[:end]}
}

var reFilterEntry = regexp.MustCompile(`/([A-Za-z0-9]+)`)

// applyFilter decodes raw data with a single named filter.
func (p *rawPDFParser) applyFilter(filter string, data []byte, dict string) []byte {
	switch filter {
	case "FlateDecode", "Fl":
		if decoded, err := zlibDecompress(data); err == nil {
			return decodePredictor(decoded, dict)
		}
		return data
	case "ASCIIHexDecode", "AHx":
		return decodeASCIIHex(data)
	case "ASCII85Decode", "A85":
		return decodeASCII85(data)
	case "RunLengthDecode", "RL":
		return decodeRunLength(data)
	case "LZWDecode", "LZW":
		return decodeLZW(data, dict)
	case "BrotliDecode":
		return decodeBrotli(data)
	default:
		// DCTDecode (JPEG), JPXDecode, CCITTFaxDecode — images stay
		// encoded; the image decoders handle them downstream.
		return data
	}
}

// decodeBrotli decompresses a /BrotliDecode stream (PDF 2.0 extension).
func decodeBrotli(data []byte) []byte {
	r := brotli.NewReader(bytes.NewReader(data))
	out, err := io.ReadAll(r)
	if err != nil && len(out) == 0 {
		return data
	}
	return out
}

// decodeRunLength decodes a /RunLengthDecode stream (length runs 0-127 are
// literals, 129-257 repeats the next byte 257-n times, 128 ends the data).
func decodeRunLength(data []byte) []byte {
	out := make([]byte, 0, len(data)*2)
	for i := 0; i < len(data); {
		n := int(data[i])
		i++
		if n == 128 { // EOD
			break
		}
		if n < 128 { // literal run of n+1 bytes
			if i+n+1 > len(data) {
				break
			}
			out = append(out, data[i:i+n+1]...)
			i += n + 1
			continue
		}
		// repeat run: next byte copied 257-n times
		if i >= len(data) {
			break
		}
		b := data[i]
		i++
		for k := 0; k < 257-n; k++ {
			out = append(out, b)
		}
	}
	return out
}

// decodeLZW decodes a /LZWDecode stream (PDF's LZW variant: 9-12 bit codes
// read MSB-first with the /EarlyChange convention; table code 256 = clear
// table, 257 = EOD, per spec table 8). Semantics follow pdf.js/reference
// decoders: the code width grows when nextCode+early reaches the next
// power of two.
func decodeLZW(data []byte, dict string) []byte {
	// /EarlyChange is read for completeness; the empirically verified
	// pairing (see addEntry below) uses the same width threshold for both
	// its values.
	if strings.Contains(dict, "/EarlyChange") {
		if m := regexpEarlyChange.FindStringSubmatch(dict); m != nil {
			_, _ = strconv.Atoi(m[1])
		}
	}
	table := make([][]byte, 258)
	for c := 0; c < 256; c++ {
		table[c] = []byte{byte(c)}
	}
	width := 9
	var (
		buf, cnt uint32
		out      []byte
		prev     = -1
	)
	addEntry := func(entry []byte) {
		table = append(table, entry)
		// The decoder runs one assignment behind the encoder, so its width
		// switch happens one emit earlier than the encoder's (the encoder
		// flips when nextCode reaches 1<<width). Verified against pypdf's
		// reference codec in both directions: the threshold is
		// (1<<width)-1 for the default EarlyChange=1, and empirically also
		// for EarlyChange=0 (the spec's "postpone as long as possible"
		// wording cannot pair with any encoder threshold otherwise).
		if len(table) == (1<<width)-1 && width < 12 {
			width++
		}
	}
	for _, b := range data {
		buf = buf<<8 | uint32(b)
		cnt += 8
		for int(cnt) >= width {
			code := int((buf >> (cnt - uint32(width))) & ((1 << width) - 1))
			cnt -= uint32(width)
			switch {
			case code == 256: // clear table
				table = table[:258]
				width = 9
				prev = -1
			case code == 257: // EOD
				return out
			case prev < 0:
				if code >= 256 {
					return out // corrupt: first code must be a literal
				}
				out = append(out, table[code]...)
				prev = code
			case code < len(table):
				entry := table[code]
				out = append(out, entry...)
				addEntry(append(append([]byte{}, table[prev]...), entry[0]))
				prev = code
			case code == len(table) && prev >= 0:
				entry := append(append([]byte{}, table[prev]...), table[prev][0])
				out = append(out, entry...)
				addEntry(entry)
				prev = code
			default:
				return out // corrupt code
			}
		}
	}
	return out
}

var regexpEarlyChange = regexp.MustCompile(`/EarlyChange\s+(\d+)`)

// decodePredictor handles /DecodeParms predictors (PNG predictors used by
// xref and object streams).
func decodePredictor(data []byte, dict string) []byte {
	idx := strings.Index(dict, "/Predictor")
	if idx < 0 {
		return data
	}
	rest := dict[idx:]
	// predictor number
	pred := 1
	if m := regexpNum.FindStringSubmatch(rest); m != nil {
		pred, _ = strconv.Atoi(m[1])
	}
	if pred <= 1 {
		return data
	}
	columns := 1
	if m := regexpColumns.FindStringSubmatch(rest); m != nil {
		columns, _ = strconv.Atoi(m[1])
	}
	if columns <= 0 {
		columns = 1
	}
	colors := 1
	bpc := 8
	if m := regexpColors.FindStringSubmatch(rest); m != nil {
		colors, _ = strconv.Atoi(m[1])
	}
	if m := regexpBPC.FindStringSubmatch(rest); m != nil {
		bpc, _ = strconv.Atoi(m[1])
	}
	if bpc != 8 {
		return data // only 8-bit PNG predictors supported
	}
	bpp := colors // bytes per pixel for PNG filters
	rowLen := columns * bpp
	if rowLen <= 0 || len(data) < rowLen+1 {
		return data
	}
	out := make([]byte, 0, len(data))
	prev := make([]byte, rowLen)
	pos := 0
	for pos < len(data) {
		ftype := data[pos]
		pos++
		if pos+rowLen > len(data) {
			break
		}
		row := make([]byte, rowLen)
		copy(row, data[pos:pos+rowLen])
		pos += rowLen
		switch ftype {
		case 0: // None
		case 1: // Sub
			for i := bpp; i < rowLen; i++ {
				row[i] += row[i-bpp]
			}
		case 2: // Up
			for i := 0; i < rowLen; i++ {
				row[i] += prev[i]
			}
		case 3: // Average
			for i := 0; i < rowLen; i++ {
				var left byte
				if i >= bpp {
					left = row[i-bpp]
				}
				row[i] += byte((int(left) + int(prev[i])) / 2)
			}
		case 4: // Paeth
			for i := 0; i < rowLen; i++ {
				var left, upLeft byte
				if i >= bpp {
					left = row[i-bpp]
					upLeft = prev[i-bpp]
				}
				row[i] += paeth(left, prev[i], upLeft)
			}
		default:
			// unknown filter type — stop
			return data
		}
		out = append(out, row...)
		copy(prev, row)
	}
	return out
}

var (
	regexpNum     = regexp.MustCompile(`/Predictor\s+(\d+)`)
	regexpColumns = regexp.MustCompile(`/Columns\s+(\d+)`)
	regexpColors  = regexp.MustCompile(`/Colors\s+(\d+)`)
	regexpBPC     = regexp.MustCompile(`/BitsPerComponent\s+(\d+)`)
)

func paeth(a, b, c byte) byte {
	p := int(a) + int(b) - int(c)
	pa, pb, pc := abs64(p-int(a)), abs64(p-int(b)), abs64(p-int(c))
	if pa <= pb && pa <= pc {
		return a
	}
	if pb <= pc {
		return b
	}
	return c
}

func abs64(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// decodeASCIIHex decodes an ASCIIHexDecode stream.
func decodeASCIIHex(data []byte) []byte {
	out := make([]byte, 0, len(data)/2)
	var hi byte = 0xFF
	for _, c := range data {
		if c == '>' {
			break
		}
		v := hexVal(c)
		if c == ' ' || c == '\n' || c == '\r' || c == '\t' || (c == 0 && v == 0 && c != '0') {
			continue
		}
		if isHexDigit(c) {
			if hi == 0xFF {
				hi = v
			} else {
				out = append(out, hi<<4|v)
				hi = 0xFF
			}
		}
	}
	return out
}

func isHexDigit(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// decodeASCII85 decodes an ASCII85Decode stream (up to '->' terminator).
func decodeASCII85(data []byte) []byte {
	out := make([]byte, 0, len(data)*4/5)
	var group [5]byte
	groupLen := 0
	for i := 0; i < len(data); i++ {
		c := data[i]
		if c == ' ' || c == '\n' || c == '\r' || c == '\t' || c == 0 {
			continue
		}
		if c == '~' {
			break
		}
		if c == 'z' && groupLen == 0 {
			out = append(out, 0, 0, 0, 0)
			continue
		}
		if c < '!' || c > 'u' {
			continue
		}
		group[groupLen] = c - '!'
		groupLen++
		if groupLen == 5 {
			v := uint32(0)
			for _, g := range group {
				v = v*85 + uint32(g)
			}
			out = append(out, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
			groupLen = 0
		}
	}
	if groupLen > 1 {
		// pad with 'u' (84)
		for j := groupLen; j < 5; j++ {
			group[j] = 84
		}
		v := uint32(0)
		for _, g := range group {
			v = v*85 + uint32(g)
		}
		for j := 0; j < groupLen-1; j++ {
			out = append(out, byte(v>>(24-8*j)))
		}
	}
	return out
}

func zlibDecompress(data []byte) ([]byte, error) {
	r, err := zlib.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

func (p *rawPDFParser) findRoot() {
	// Look for /Root N 0 R and /Info N 0 R in each trailer, scanning
	// forward so the LAST trailer that carries them wins — incremental
	// update trailers may legitimately omit /Root or /Info and inherit
	// them from a previous generation.
	for pos := 0; ; {
		i := bytes.Index(p.data[pos:], []byte("trailer"))
		if i < 0 {
			break
		}
		pos += i
		seg := p.data[pos:]
		if m := reRootRef.FindSubmatch(seg); m != nil {
			p.root, _ = strconv.Atoi(string(m[1]))
		}
		if m := reInfoRef.FindSubmatch(seg); m != nil {
			p.info, _ = strconv.Atoi(string(m[1]))
		} else if i := bytes.Index(seg, []byte("/Info <<")); i >= 0 {
			// inline /Info dict in the trailer (GoPdf's own writer)
			p.infoDict = extractDict(seg[i:])
		}
		pos += 7
	}
	if p.root == 0 {
		if m := reRootRef.FindSubmatch(p.data); m != nil {
			p.root, _ = strconv.Atoi(string(m[1]))
		}
	}
	if p.root == 0 || p.info == 0 {
		// Xref-stream PDFs have no "trailer" keyword: /Root (and /Info)
		// live inside the /Type/XRef stream dictionary. Take the LAST
		// /Info ref — with incremental updates the newest xref stream
		// wins.
		if p.info == 0 {
			for _, m := range reInfoRef.FindAllSubmatch(p.data, -1) {
				p.info, _ = strconv.Atoi(string(m[1]))
			}
		}
	}
}

func (p *rawPDFParser) parsePages() {
	rootObj, ok := p.objects[p.root]
	if !ok {
		return
	}
	// Find /Pages reference
	pagesRef := extractRef(rootObj.dict, "/Pages")
	if pagesRef <= 0 {
		return
	}
	p.collectPages(pagesRef)
}

func (p *rawPDFParser) collectPages(objNum int) {
	p.collectPagesInherited(objNum, [4]float64{}, 0, nil)
}

// collectPagesInherited walks the page tree, propagating inherited
// attributes (MediaBox) from Pages nodes down to their Page leaves.
func (p *rawPDFParser) collectPagesInherited(objNum int, inherited [4]float64, inheritedRotate int, inheritedCrop *([4]float64)) {
	obj, ok := p.objects[objNum]
	if !ok {
		return
	}
	switch pdfObjectType(obj.dict) {
	case "Page":
		page := rawPDFPage{objNum: objNum}
		page.mediaBox = extractMediaBox(obj.dict)
		if page.mediaBox == [4]float64{0, 0, 612, 792} && inherited != [4]float64{} {
			// page lacks its own MediaBox — inherit from the Pages tree
			page.mediaBox = inherited
		}
		if cb, has := extractBox(obj.dict, "/CropBox"); has {
			page.cropBox, page.hasCrop = cb, true
		} else if inheritedCrop != nil {
			page.cropBox, page.hasCrop = *inheritedCrop, true
		}
		page.rotate = extractPageRotate(obj.dict)
		if page.rotate == 0 {
			page.rotate = inheritedRotate
		}
		page.contents = extractContentRefs(obj.dict)
		page.resources = p.extractResources(obj.dict, objNum)
		p.pages = append(p.pages, page)
	case "Pages":
		// It's a /Pages node — recurse into /Kids with its MediaBox
		inheritedMedia := extractMediaBox(obj.dict)
		if inheritedMedia == [4]float64{0, 0, 612, 792} && inherited != [4]float64{} {
			inheritedMedia = inherited
		}
		localCrop := inheritedCrop
		if cb, has := extractBox(obj.dict, "/CropBox"); has {
			localCrop = &cb
		}
		inheritedRot := extractPageRotate(obj.dict)
		if inheritedRot == 0 {
			inheritedRot = inheritedRotate
		}
		kids := extractRefArray(obj.dict, "/Kids")
		for _, kid := range kids {
			p.collectPagesInherited(kid, inheritedMedia, inheritedRot, localCrop)
		}
	}
}

// extractPageRotate parses /Rotate (0/90/180/270) from a page or Pages dict.
// Non-multiples of 90 are ignored (returns 0).
func extractPageRotate(dict string) int {
	v := int(extractNumValue(dict, "/Rotate"))
	switch v {
	case 90, 180, 270:
		return v
	}
	return 0
}

// pdfObjectType extracts the /Type value from a dict string with proper token
// boundaries ("/Page" must not match "/Pages") and nested-dictionary
// awareness ("/Group<</…/Type/Group>>" placed before /Type/Page by some
// generators must not win).
func pdfObjectType(dict string) string {
	idx := 0
	for {
		pos := strings.Index(dict[idx:], "/Type")
		if pos < 0 {
			return ""
		}
		pos += idx
		// Skip /Type tokens nested inside a sub-dictionary. The outermost
		// << puts the dict's own keys at depth 1; anything deeper (e.g.
		// /Group<</…/Type/Group>>) belongs to a nested object.
		if dictNestingAt(dict, pos) > 1 {
			idx = pos + len("/Type")
			continue
		}
		pos += len("/Type")
		// skip whitespace
		for pos < len(dict) && (dict[pos] == ' ' || dict[pos] == '\t' || dict[pos] == '\r' || dict[pos] == '\n') {
			pos++
		}
		if pos >= len(dict) || dict[pos] != '/' {
			idx = pos
			continue
		}
		pos++
		start := pos
		for pos < len(dict) && isNameChar(dict[pos]) {
			pos++
		}
		if pos > start {
			return dict[start:pos]
		}
		idx = pos
	}
}

// dictNestingAt reports the open-dictionary depth at byte offset pos of
// dict. Depth 0 means the token sits at the top level of the dictionary.
// The scan stops at pos, so a dict whose /Type precedes its own "<<"
// opener (truncated dict) still yields depth 0.
func dictNestingAt(dict string, pos int) int {
	depth := 0
	for i := 0; i < len(dict) && i < pos; i++ {
		if dict[i] == '<' {
			if i+1 < len(dict) && dict[i+1] == '<' {
				depth++
				i++
			}
		} else if dict[i] == '>' {
			if i+1 < len(dict) && dict[i+1] == '>' {
				depth--
				if depth < 0 {
					depth = 0
				}
				i++
			}
		} else if dict[i] == '(' {
			// skip literal string content — parens and brackets inside
			// strings must not affect the nesting count
			for i++; i < len(dict); i++ {
				if dict[i] == '\\' {
					i++
					continue
				}
				if dict[i] == ')' {
					break
				}
			}
		} else if dict[i] == '<' && i+1 < len(dict) && dict[i+1] != '<' {
			// hex string <...> — skip to the closing '>'
			for i++; i < len(dict) && dict[i] != '>'; i++ {
			}
		}
	}
	return depth
}

func isNameChar(c byte) bool {
	return c > 0x20 && c != '/' && c != '<' && c != '>' && c != '[' && c != ']' &&
		c != '(' && c != ')' && c != '%' && c != '{' && c != '}'
}

// parseObjRefAt scans a "N 0 R" reference at the start of s (after optional
// whitespace). Returns the object number and the index just past 'R', or -1.
// Hand-rolled: extractRef runs on every dictionary key lookup, and the regex
// cost showed up at ~10% of extraction CPU profiles.
func parseObjRefAt(s string) (int, int) {
	isWS := func(c byte) bool { return c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == 0 }
	i := 0
	for i < len(s) && isWS(s[i]) {
		i++
	}
	start := i
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == start {
		return 0, -1
	}
	n, err := strconv.Atoi(s[start:i])
	if err != nil {
		return 0, -1
	}
	for i < len(s) && isWS(s[i]) {
		i++
	}
	// "0" ws "R"
	if i+1 < len(s) && s[i] == '0' && isWS(s[i+1]) {
		i += 2
		for i < len(s) && isWS(s[i]) {
			i++
		}
		if i < len(s) && s[i] == 'R' {
			return n, i + 1
		}
	}
	return 0, -1
}

// extractRef extracts a single "N 0 R" reference for a given key.
// Also accepts array-wrapped values (e.g. /DescendantFonts [9 0 R]), taking
// the first reference. Uses string search + pre-compiled regex.
func extractRef(dict, key string) int {
	idx := strings.Index(dict, key)
	if idx < 0 {
		return 0
	}
	rest := dict[idx+len(key):]
	rest = strings.TrimLeft(rest, " \t\r\n")
	if strings.HasPrefix(rest, "[") {
		// Array form: use the first reference inside the brackets.
		end := strings.Index(rest, "]")
		if end < 0 {
			return 0
		}
		if n, _ := parseObjRefAt(rest[1:end]); n > 0 {
			return n
		}
		return 0
	}
	if n, _ := parseObjRefAt(rest); n > 0 {
		return n
	}
	return 0
}

// extractRefArray extracts an array of "N 0 R" references for a given key.
func extractRefArray(dict, key string) []int {
	idx := strings.Index(dict, key)
	if idx < 0 {
		return nil
	}
	rest := dict[idx+len(key):]
	// find [ ... ]
	start := strings.Index(rest, "[")
	if start < 0 {
		return nil
	}
	end := strings.Index(rest[start:], "]")
	if end < 0 {
		return nil
	}
	arr := rest[start+1 : start+end]
	var refs []int
	for pos := 0; pos < len(arr); {
		n, next := parseObjRefAt(arr[pos:])
		if next < 0 {
			pos++
			continue
		}
		refs = append(refs, n)
		pos += next
	}
	return refs
}

// extractBox parses an arbitrary box array (/CropBox, /BleedBox, ...)
// from a dict. ok is false when the box is absent or malformed.
func extractBox(dict string, key string) ([4]float64, bool) {
	if arr := extractNumberArray(dict, key); len(arr) >= 4 {
		return [4]float64{arr[0], arr[1], arr[2], arr[3]}, true
	}
	return [4]float64{}, false
}

func extractMediaBox(dict string) [4]float64 {
	m := reMediaBox.FindStringSubmatch(dict)
	if m == nil {
		return [4]float64{0, 0, 612, 792} // default letter
	}
	var box [4]float64
	for i := 0; i < 4; i++ {
		box[i], _ = strconv.ParseFloat(m[i+1], 64)
	}
	return box
}

func extractContentRefs(dict string) []int {
	// /Contents N 0 R  or  /Contents [N 0 R M 0 R]
	idx := strings.Index(dict, "/Contents")
	if idx < 0 {
		return nil
	}
	rest := dict[idx+9:]
	rest = strings.TrimLeft(rest, " \t\r\n")
	if len(rest) > 0 && rest[0] == '[' {
		return extractRefArray(dict, "/Contents")
	}
	ref := extractRef(dict, "/Contents")
	if ref > 0 {
		return []int{ref}
	}
	return nil
}

func (p *rawPDFParser) extractResources(dict string, pageObjNum int) rawPDFResources {
	res := rawPDFResources{
		fonts:      make(map[string]int),
		xobjs:      make(map[string]int),
		extgstates: make(map[string]int),
	}
	// Resources can be inline or a reference
	resDict := dict
	resRef := extractRef(dict, "/Resources")
	if resRef > 0 {
		if obj, ok := p.objects[resRef]; ok {
			resDict = obj.dict
		}
	}
	// Extract /Font << /F1 N 0 R ... >>
	p.extractNamedRefs(resDict, "/ExtGState", res.extgstates)
	p.extractNamedRefs(resDict, "/Font", res.fonts)
	// Extract /XObject << /Im1 N 0 R ... >>
	p.extractNamedRefs(resDict, "/XObject", res.xobjs)
	return res
}

func (p *rawPDFParser) extractNamedRefs(dict, key string, out map[string]int) {
	idx := strings.Index(dict, key)
	if idx < 0 {
		return
	}
	rest := dict[idx+len(key):]
	rest = strings.TrimLeft(rest, " \t\r\n")
	if len(rest) == 0 {
		return
	}
	if rest[0] == '<' && len(rest) > 1 && rest[1] == '<' {
		// inline dict
		inner := extractDict([]byte(rest))
		matches := reNamedRef.FindAllStringSubmatch(inner, -1)
		for _, m := range matches {
			n, _ := strconv.Atoi(m[2])
			out["/"+m[1]] = n
		}
	} else {
		// reference to another object
		if n, _ := parseObjRefAt(rest); n > 0 {
			if obj, ok := p.objects[n]; ok {
				matches := reNamedRef.FindAllStringSubmatch(obj.dict, -1)
				for _, mm := range matches {
					nn, _ := strconv.Atoi(mm[2])
					out["/"+mm[1]] = nn
				}
			}
		}
	}
}

// getPageContentStream returns the concatenated, decompressed content
// stream(s) for a page.
func (p *rawPDFParser) getPageContentStream(pageIdx int) []byte {
	if pageIdx < 0 || pageIdx >= len(p.pages) {
		return nil
	}
	page := p.pages[pageIdx]
	var buf bytes.Buffer
	for _, ref := range page.contents {
		obj, ok := p.objects[ref]
		if !ok {
			continue
		}
		if obj.stream != nil {
			buf.Write(obj.stream)
			buf.WriteByte('\n')
		}
	}
	return buf.Bytes()
}

// ============================================================
// Content stream text operator parser
// ============================================================

// ExtractedText represents a piece of text extracted from a PDF page.
type ExtractedText struct {
	Text     string  // The extracted text string
	X        float64 // Horizontal position
	Y        float64 // Vertical position
	FontName string  // PDF font resource name (e.g. "/F1")
	FontSize float64 // Font size in points
	Width    float64 // Estimated advance width (set by MergeTextRuns; 0 when unknown)
	// Color is the fill color the text was painted with (0-255 RGB).
	ColorR, ColorG, ColorB uint8
	// FontFlags bit 1 = bold, bit 2 = italic, detected from the font name.
	FontFlags int
	// Alpha is the fill alpha from ExtGState /ca (255 = opaque).
	Alpha uint8
}

// ExtractedImage represents an image found on a PDF page.
type ExtractedImage struct {
	// Name is the XObject resource name (e.g. "/Im1").
	Name string
	// Width is the image width in pixels.
	Width int
	// Height is the image height in pixels.
	Height int
	// BitsPerComponent is the bits per color component.
	BitsPerComponent int
	// ColorSpace is the color space name.
	ColorSpace string
	// Filter is the compression filter.
	Filter string
	// Data is the raw (possibly compressed) image data.
	Data []byte
	// ObjNum is the PDF object number.
	ObjNum int
	// X, Y are the position on the page (from the CTM).
	X float64
	// Y position on the page.
	Y float64
	// DisplayWidth is the rendered width on the page.
	DisplayWidth float64
	// DisplayHeight is the rendered height on the page.
	DisplayHeight float64
}
