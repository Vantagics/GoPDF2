package gopdf

import (
	"bytes"
	"encoding/base64"
	"image"
	"strconv"
	"strings"
)

// Justify is the text-align: justify alignment flag for HTML rendering.
const Justify = 64

// HTMLBoxOption configures the behavior of InsertHTMLBox / InsertHTMLPaginated.
type HTMLBoxOption struct {
	// DefaultFontFamily is the font family used when no font is specified in HTML.
	// This font must already be added to the GoPdf instance via AddTTFFont.
	DefaultFontFamily string

	// DefaultFontSize is the default font size in points.
	DefaultFontSize float64

	// DefaultColor is the default text color (r, g, b).
	DefaultColor [3]uint8

	// LineSpacing is extra spacing between lines (in document units). Default is 0.
	LineSpacing float64

	// BoldFontFamily is the font family to use for bold text.
	// If empty, the default family is used (bold may not render if the font doesn't support it).
	BoldFontFamily string

	// ItalicFontFamily is the font family to use for italic text.
	ItalicFontFamily string

	// BoldItalicFontFamily is the font family to use for bold+italic text.
	BoldItalicFontFamily string

	// MonoFontFamily is the font family for <pre>/<code>/tt. Falls back to
	// DefaultFontFamily when empty.
	MonoFontFamily string

	// ---- pagination options (InsertHTMLPaginated) ----

	// PageBottom is the lowest Y (units, top-down) content may occupy on a
	// page when paginating. Required for InsertHTMLPaginated.
	PageBottom float64

	// PageTop is the Y (units) where content resumes after a page break.
	// Defaults to the y passed to InsertHTMLPaginated.
	PageTop float64

	// RepeatTableHeaders repeats <thead> rows at the top of continuation
	// pages when a table spans a page break.
	RepeatTableHeaders bool

	// OnPageBreak, when set, is invoked after each automatic page break
	// (after AddPage) — useful for headers/footers.
	OnPageBreak func(pageIndex int) error
}

// htmlRenderState tracks the current rendering state while walking the HTML tree.
type htmlRenderState struct {
	fontFamily string
	fontSize   float64
	fontStyle  int // Regular, Bold, Italic, Underline, Strikethrough
	rise       float64
	colorR     uint8
	colorG     uint8
	colorB     uint8

	bgR, bgG, bgB uint8
	hasBg         bool

	align      int // Left / Center / Right / Justify
	strike     bool
	lineHeight float64 // multiplier; 0 = default 1.2
	letterSp   float64 // letter-spacing in units
	textIndent float64 // first-line indent in units
	preformed  bool    // white-space: pre
	transform  int     // 0 none, 1 uppercase, 2 lowercase, 3 capitalize
	marginTop  float64 // block margin, units
	marginBot  float64
	vAlign     int // table cell vertical alignment: Top/Middle/Bottom
}

// htmlRenderer handles the rendering of HTML nodes into the PDF.
type htmlRenderer struct {
	gp      *GoPdf
	opt     HTMLBoxOption
	boxX    float64 // box left edge (units)
	boxY    float64 // box top edge (units)
	boxW    float64 // box width (units)
	boxH    float64 // box height (units)
	cursorX float64 // current X position (units)
	cursorY float64 // current Y position (units)
	style   *htmlStyleResolver

	// pagination state
	paginate    bool // automatic page breaks enabled
	activeTable []*htmlNode // pending <thead> rows to repeat after breaks
	brokePage   bool
}

type htmlTableCell struct {
	node    *htmlNode // the td/th element (nil for filler)
	state   htmlRenderState
	header  bool
	colspan int
	rowspan int
	col     int
	row     int
}

type htmlTableRow struct {
	cells []htmlTableCell
}

// InsertHTMLBox renders simplified HTML content into a rectangular area on the PDF.
//
// Supported HTML tags:
//   - <b>, <strong>: Bold text
//   - <i>, <em>: Italic text
//   - <u>, <ins>: Underlined text
//   - <s>, <strike>, <del>: Strikethrough text
//   - <br>, <br/>: Line break
//   - <p>, <div>, <span>: Paragraphs, blocks and inline spans (with style)
//   - <h1> to <h6>: Headings with automatic font sizing
//   - <font color="..." size="..." face="...">: Font styling
//   - <img src="..." width="..." height="...">: Images (local file path or
//     a base64 "data:image/...;base64,..." URI)
//   - <hr>: Horizontal rule
//   - <center>: Centered text
//   - <ul>, <ol>, <li>: Lists with nesting, start= and type= support
//   - <a href="...">: Links (clickable PDF link annotation)
//   - <sub>, <sup>: Subscript/superscript
//   - <blockquote>: Indented block
//   - <pre>, <code>, <tt>: Preformatted / monospace text
//   - <table>, <thead>, <tbody>, <tfoot>, <tr>, <th>, <td>, <caption>,
//     <colgroup>, <col>: Tables with colspan/rowspan, column widths,
//     background colors, borders and per-cell alignment
//   - <style>: CSS stylesheet (tag / .class / #id selectors)
//
// Supported CSS properties (inline style and <style> blocks):
//   color, background-color, font-size, font-family, font-weight, font-style,
//   text-decoration, text-align (incl. justify), text-transform, text-indent,
//   line-height, letter-spacing, vertical-align, width (tables/cells/imgs),
//   margin-top, margin-bottom, white-space.
//
// Parameters:
//   - x, y: Top-left corner of the box (in document units)
//   - w, h: Width and height of the box (in document units)
//   - htmlStr: The HTML string to render
//   - opt: Rendering options (font families, default size, colors, etc.)
//
// Returns the Y position after the last rendered content (in document units).
func (gp *GoPdf) InsertHTMLBox(x, y, w, h float64, htmlStr string, opt HTMLBoxOption) (float64, error) {
	if opt.DefaultFontSize <= 0 {
		opt.DefaultFontSize = 12
	}
	if opt.DefaultFontFamily == "" {
		return y, ErrMissingFontFamily
	}

	nodes := parseHTML(htmlStr)

	r := &htmlRenderer{
		gp:      gp,
		opt:     opt,
		boxX:    x,
		boxY:    y,
		boxW:    w,
		boxH:    h,
		cursorX: x,
		cursorY: y,
		style:   newHTMLStyleResolver(nodes),
	}

	state := r.initialState()

	if err := r.renderNodes(nodes, state); err != nil {
		return r.cursorY, err
	}

	return r.cursorY, nil
}

// InsertHTMLPaginated renders HTML flowing across multiple pages. When the
// content would pass PageBottom, a new page is added automatically and
// rendering continues at PageTop. Tables that span page breaks can repeat
// their <thead> rows (opt.RepeatTableHeaders).
//
// Returns the Y position after the last rendered content on the final page.
func (gp *GoPdf) InsertHTMLPaginated(x, y, w float64, htmlStr string, opt HTMLBoxOption) (float64, error) {
	if opt.DefaultFontSize <= 0 {
		opt.DefaultFontSize = 12
	}
	if opt.DefaultFontFamily == "" {
		return y, ErrMissingFontFamily
	}
	if opt.PageBottom <= 0 {
		// default: a little above the page bottom margin
		opt.PageBottom = gp.margins.Bottom + 20
	}

	nodes := parseHTML(htmlStr)

	r := &htmlRenderer{
		gp:        gp,
		opt:       opt,
		boxX:      x,
		boxY:      y,
		boxW:      w,
		boxH:      opt.PageBottom - y, // effective first page
		cursorX:   x,
		cursorY:   y,
		style:     newHTMLStyleResolver(nodes),
		paginate:  true,
	}

	state := r.initialState()

	if err := r.renderNodes(nodes, state); err != nil {
		return r.cursorY, err
	}

	return r.cursorY, nil
}

func (r *htmlRenderer) initialState() htmlRenderState {
	return htmlRenderState{
		fontFamily: r.opt.DefaultFontFamily,
		fontSize:   r.opt.DefaultFontSize,
		fontStyle:  Regular,
		colorR:     r.opt.DefaultColor[0],
		colorG:     r.opt.DefaultColor[1],
		colorB:     r.opt.DefaultColor[2],
		align:      Left,
	}
}

// ensureVerticalSpace makes room for h units, paginating when enabled.
// Returns false when there is no room and pagination is disabled.
func (r *htmlRenderer) ensureVerticalSpace(h float64) (bool, error) {
	if r.cursorY+h <= r.boxY+r.boxH {
		return true, nil
	}
	if !r.paginate {
		return false, nil
	}
	if err := r.pageBreak(); err != nil {
		return false, err
	}
	return true, nil
}

// pageBreak starts a new page and resets the cursor.
func (r *htmlRenderer) pageBreak() error {
	r.gp.AddPage()
	if r.opt.OnPageBreak != nil {
		if err := r.opt.OnPageBreak(r.gp.GetNumberOfPages()); err != nil {
			return err
		}
	}
	r.cursorY = r.opt.PageTop
	if r.cursorY <= 0 {
		r.cursorY = r.boxY
	}
	r.cursorX = r.boxX
	r.boxY = r.cursorY
	r.boxH = r.opt.PageBottom - r.boxY
	if r.boxH <= 0 {
		r.boxH = 700 // failsafe
	}
	r.brokePage = true
	return nil
}

func (r *htmlRenderer) renderNodes(nodes []*htmlNode, state htmlRenderState) error {
	for _, node := range nodes {
		if node.Type == htmlNodeElement && isTransparentContainer(node.Tag) {
			// transparent containers: head is skipped, body/html flow through
			if node.Tag == "head" || node.Tag == "meta" || node.Tag == "title" || node.Tag == "link" || node.Tag == "script" {
				continue
			}
			if err := r.renderNodes(node.Children, state); err != nil {
				return err
			}
			continue
		}
		if err := r.renderNode(node, state); err != nil {
			return err
		}
	}
	return nil
}

func (r *htmlRenderer) renderNode(node *htmlNode, state htmlRenderState) error {
	if node.Type == htmlNodeText {
		// bare text outside a block: flow it as an implicit paragraph
		return r.renderInlineTextRun(node.Text, state, nil)
	}

	// element node
	newState := state
	css := r.style.resolve(node)

	switch node.Tag {
	case "b", "strong":
		newState.fontStyle |= Bold
		newState = r.applyCSSProps(css, newState, node)
		return r.renderNodes(node.Children, newState)
	case "i", "em":
		newState.fontStyle |= Italic
		newState = r.applyCSSProps(css, newState, node)
		return r.renderNodes(node.Children, newState)
	case "u", "ins":
		newState.fontStyle |= Underline
		newState = r.applyCSSProps(css, newState, node)
		return r.renderNodes(node.Children, newState)
	case "s", "strike", "del":
		newState.strike = true
		newState = r.applyCSSProps(css, newState, node)
		return r.renderNodes(node.Children, newState)
	case "br":
		r.cursorX = r.boxX
		r.cursorY += r.lineHeightOf(state)
		return nil
	case "hr":
		return r.renderHR(node, state)
	case "p", "div":
		newState = r.applyCSSProps(css, newState, node)
		return r.renderBlock(node, newState)
	case "h1", "h2", "h3", "h4", "h5", "h6":
		newState.fontSize = headingFontSize(node.Tag)
		newState.fontStyle |= Bold
		newState = r.applyCSSProps(css, newState, node)
		return r.renderHeading(node, newState, state)
	case "font":
		if color, ok := node.Attrs["color"]; ok {
			if cr, cg, cb, cok := parseCSSColor(color); cok {
				newState.colorR, newState.colorG, newState.colorB = cr, cg, cb
			}
		}
		if size, ok := node.Attrs["size"]; ok {
			if sz, sok := parseFontSizeAttr(size); sok {
				newState.fontSize = sz
			}
		}
		if face, ok := node.Attrs["face"]; ok {
			newState.fontFamily = face
		}
		newState = r.applyCSSProps(css, newState, node)
		return r.renderNodes(node.Children, newState)
	case "span", "small", "big", "abbr", "time", "label":
		if node.Tag == "small" && newState.fontSize > 0 {
			newState.fontSize *= 0.85
		}
		if node.Tag == "big" {
			newState.fontSize *= 1.2
		}
		newState = r.applyCSSProps(css, newState, node)
		return r.renderNodes(node.Children, newState)
	case "code", "tt", "kbd", "samp":
		if r.opt.MonoFontFamily != "" {
			newState.fontFamily = r.opt.MonoFontFamily
		}
		newState = r.applyCSSProps(css, newState, node)
		return r.renderNodes(node.Children, newState)
	case "pre":
		newState.preformed = true
		if r.opt.MonoFontFamily != "" {
			newState.fontFamily = r.opt.MonoFontFamily
		}
		newState = r.applyCSSProps(css, newState, node)
		return r.renderBlock(node, newState)
	case "center":
		newState.align = Center
		newState = r.applyCSSProps(css, newState, node)
		return r.renderBlock(node, newState)
	case "a":
		newState = r.applyCSSProps(css, newState, node)
		if _, ok := css["color"]; !ok {
			newState.colorR, newState.colorG, newState.colorB = 0, 0, 255
			newState.fontStyle |= Underline
		}
		href := node.Attrs["href"]

		startX := r.cursorX
		startY := r.cursorY
		lh := r.lineHeightOf(newState)

		if err := r.renderNodes(node.Children, newState); err != nil {
			return err
		}

		if href != "" {
			endX := r.cursorX
			ax := r.gp.UnitsToPoints(startX)
			ay := r.gp.UnitsToPoints(startY)
			aw := r.gp.UnitsToPoints(endX - startX)
			ah := r.gp.UnitsToPoints(lh)
			if aw > 0 {
				r.gp.AddExternalLink(href, ax, ay, aw, ah)
			}
		}
		return nil
	case "img":
		return r.renderImage(node, state, css)
	case "table":
		return r.renderTable(node, state)
	case "ul", "ol":
		return r.renderList(node, state, node.Tag == "ol", 0)
	case "li":
		// handled by renderList
		return nil
	case "sub":
		newState.fontSize = state.fontSize * 0.7
		newState.rise = -state.fontSize * 0.25
		newState = r.applyCSSProps(css, newState, node)
		return r.renderNodes(node.Children, newState)
	case "sup":
		newState.fontSize = state.fontSize * 0.7
		newState.rise = state.fontSize * 0.35
		newState = r.applyCSSProps(css, newState, node)
		return r.renderNodes(node.Children, newState)
	case "blockquote":
		newState = r.applyCSSProps(css, newState, node)
		return r.renderBlockquote(node, newState, state)
	case "style", "script", "head", "title", "meta", "link":
		return nil
	default:
		newState = r.applyCSSProps(css, newState, node)
		return r.renderNodes(node.Children, newState)
	}
}

// applyCSSProps applies resolved CSS properties to a render state.
func (r *htmlRenderer) applyCSSProps(css map[string]string, state htmlRenderState, node *htmlNode) htmlRenderState {
	if len(css) == 0 {
		return state
	}
	if color, ok := css["color"]; ok {
		if cr, cg, cb, cok := parseCSSColor(color); cok {
			state.colorR, state.colorG, state.colorB = cr, cg, cb
		}
	}
	if color, ok := css["background-color"]; ok {
		if cr, cg, cb, cok := parseCSSColor(color); cok {
			state.bgR, state.bgG, state.bgB = cr, cg, cb
			state.hasBg = true
		}
	}
	if fs, ok := css["font-size"]; ok {
		if sz, sok := parseFontSize(fs, state.fontSize); sok {
			state.fontSize = sz
		}
	}
	if ff, ok := css["font-family"]; ok {
		state.fontFamily = firstFontFamily(ff)
	}
	if fw, ok := css["font-weight"]; ok {
		switch fw {
		case "bold", "bolder", "700", "800", "900":
			state.fontStyle |= Bold
		case "normal", "lighter", "300", "400", "500":
			state.fontStyle &^= Bold
		}
	}
	if fst, ok := css["font-style"]; ok {
		if fst == "italic" || fst == "oblique" {
			state.fontStyle |= Italic
		} else if fst == "normal" {
			state.fontStyle &^= Italic
		}
	}
	if td, ok := css["text-decoration"]; ok {
		if strings.Contains(td, "underline") {
			state.fontStyle |= Underline
		}
		if strings.Contains(td, "line-through") {
			state.strike = true
		}
		if strings.Contains(td, "none") {
			state.fontStyle &^= Underline
			state.strike = false
		}
	}
	if ta, ok := css["text-align"]; ok {
		switch ta {
		case "center":
			state.align = Center
		case "right", "end":
			state.align = Right
		case "justify":
			state.align = Justify
		case "left", "start":
			state.align = Left
		}
	}
	if lh, ok := css["line-height"]; ok {
		if v, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(lh), "px")), 64); err == nil && v > 0 {
			// unitless multiplier, or px value relative to font size
			if strings.HasSuffix(strings.TrimSpace(lh), "px") && state.fontSize > 0 {
				state.lineHeight = (v * 0.75) / state.fontSize
			} else {
				state.lineHeight = v
			}
		}
	}
	if ls, ok := css["letter-spacing"]; ok {
		if v, ok := parseDimension(ls, state.fontSize); ok {
			state.letterSp = v / r.unitConversion()
		}
	}
	if ti, ok := css["text-indent"]; ok {
		if v, ok := parseDimension(ti, r.boxW); ok {
			state.textIndent = v / r.unitConversion()
		}
	}
	if tt, ok := css["text-transform"]; ok {
		switch tt {
		case "uppercase":
			state.transform = 1
		case "lowercase":
			state.transform = 2
		case "capitalize":
			state.transform = 3
		}
	}
	if ws, ok := css["white-space"]; ok {
		if ws == "pre" || ws == "pre-wrap" || ws == "break-spaces" {
			state.preformed = true
		}
	}
	if mt, ok := css["margin-top"]; ok {
		if v, ok := parseDimension(mt, r.boxW); ok {
			state.marginTop = v / r.unitConversion()
		}
	}
	if mb, ok := css["margin-bottom"]; ok {
		if v, ok := parseDimension(mb, r.boxW); ok {
			state.marginBot = v / r.unitConversion()
		}
	}
	if va, ok := css["vertical-align"]; ok {
		switch va {
		case "middle":
			state.vAlign = Middle
		case "bottom":
			state.vAlign = Bottom
		case "top":
			state.vAlign = Top
		}
	}
	// width for table cells
	return state
}

// firstFontFamily extracts the first family of a CSS font-family list.
func firstFontFamily(ff string) string {
	for _, part := range strings.Split(ff, ",") {
		part = strings.TrimSpace(strings.Trim(part, "'\""))
		if part != "" && !strings.EqualFold(part, "inherit") {
			return part
		}
	}
	return ""
}

func (r *htmlRenderer) unitConversion() float64 {
	// points per unit
	switch r.gp.config.Unit {
	case UnitMM:
		return conversionUnitMM
	case UnitCM:
		return conversionUnitCM
	case UnitIN:
		return conversionUnitIN
	case UnitPX:
		return conversionUnitPX
	default:
		return 1.0
	}
}

// lineHeightOf returns the line height for a state in units.
func (r *htmlRenderer) lineHeightOf(state htmlRenderState) float64 {
	mul := state.lineHeight
	if mul <= 0 {
		mul = 1.2
	}
	return state.fontSize*mul/r.unitConversion() + r.opt.LineSpacing
}

func (r *htmlRenderer) addVerticalSpace(ptSpace float64) {
	r.cursorY += ptSpace / r.unitConversion()
}

func (r *htmlRenderer) remainingWidth() float64 {
	return r.boxX + r.boxW - r.cursorX
}

// applyFont sets the current font/text color for a state.
func (r *htmlRenderer) applyFont(state htmlRenderState) error {
	family := r.resolveFontFamily(state)
	style := state.fontStyle &^ Underline // underline is a decoration, not a font
	if err := r.gp.SetFontWithStyle(family, style, state.fontSize); err != nil {
		// fallback to default family
		if err2 := r.gp.SetFontWithStyle(r.opt.DefaultFontFamily, Regular, state.fontSize); err2 != nil {
			return err
		}
	}
	r.gp.curr.TextRise = state.rise
	r.gp.SetTextColor(state.colorR, state.colorG, state.colorB)
	return nil
}

func (r *htmlRenderer) resolveFontFamily(state htmlRenderState) string {
	isBold := state.fontStyle&Bold == Bold
	isItalic := state.fontStyle&Italic == Italic

	if isBold && isItalic && r.opt.BoldItalicFontFamily != "" {
		return r.opt.BoldItalicFontFamily
	}
	if isBold && r.opt.BoldFontFamily != "" {
		return r.opt.BoldFontFamily
	}
	if isItalic && r.opt.ItalicFontFamily != "" {
		return r.opt.ItalicFontFamily
	}
	if state.fontFamily != "" {
		return state.fontFamily
	}
	return r.opt.DefaultFontFamily
}

// ============================================================
// Inline text rendering with decorations
// ============================================================

// renderInlineTextRun renders a raw text run at the current cursor position
// with word wrapping and decorations. Used for text outside blocks and for
// simple flows (legacy path).
func (r *htmlRenderer) renderInlineTextRun(text string, state htmlRenderState, stopAt func() bool) error {
	text = transformText(collapseWhitespace(text), state.transform)
	if text == "" {
		return nil
	}
	if state.preformed {
		text = strings.ReplaceAll(text, "\t", "    ")
		for _, line := range strings.Split(text, "\n") {
			if err := r.renderTextFragments([]textFragment{{text: line, kind: fragWord, state: state}}, state, nil, 0); err != nil {
				return err
			}
			r.cursorX = r.boxX
			r.cursorY += r.lineHeightOf(state)
		}
		return nil
	}
	return r.renderTextFragments(splitToFragments(text, state), state, stopAt, 0)
}

// renderBlock renders a block-level element (p, div, h*, pre, ...) as a
// paragraph: collects inline content into fragments, lays out lines, then
// recurses into nested block children.
func (r *htmlRenderer) renderBlock(node *htmlNode, state htmlRenderState) error {
	if r.cursorX > r.boxX {
		r.cursorX = r.boxX
		r.cursorY += r.lineHeightOf(state)
	}
	return r.flowBlock(node.Children, state, 0)
}

func (r *htmlRenderer) renderHeading(node *htmlNode, state, parentState htmlRenderState) error {
	if r.cursorX > r.boxX {
		r.cursorX = r.boxX
		r.cursorY += r.lineHeightOf(parentState)
	}
	r.addVerticalSpace(state.fontSize * 0.4)
	if err := r.flowBlock(node.Children, state, 0); err != nil {
		return err
	}
	if r.cursorX > r.boxX {
		r.cursorX = r.boxX
		r.cursorY += r.lineHeightOf(state)
	}
	r.addVerticalSpace(state.fontSize * 0.3)
	return nil
}

func (r *htmlRenderer) renderBlockquote(node *htmlNode, state, parentState htmlRenderState) error {
	if r.cursorX > r.boxX {
		r.cursorX = r.boxX
		r.cursorY += r.lineHeightOf(parentState)
	}
	indent := parentState.fontSize * 1.5
	oldBoxX, oldBoxW := r.boxX, r.boxW
	r.boxX += indent
	r.boxW -= indent
	r.cursorX = r.boxX
	r.addVerticalSpace(parentState.fontSize * 0.3)
	if err := r.flowBlock(node.Children, state, 0); err != nil {
		return err
	}
	if r.cursorX > r.boxX {
		r.cursorX = r.boxX
		r.cursorY += r.lineHeightOf(state)
	}
	r.addVerticalSpace(parentState.fontSize * 0.3)
	r.boxX, r.boxW = oldBoxX, oldBoxW
	r.cursorX = r.boxX
	return nil
}

// ============================================================
// Fragment-based line layout (word wrap + CJK + kinsoku)
// ============================================================

const (
	fragSpace = iota // collapsible whitespace
	fragWord         // atomic word (latin/url/number)
	fragCJK          // single CJK character
	fragBreak        // forced line break (<br>)
	fragImage        // inline image
)

type textFragment struct {
	text  string
	kind  int
	width float64
	space bool // rendered as whitespace
	img   *imageFragmentData
	state htmlRenderState // state at this fragment
}

type imageFragmentData struct {
	holder ImageHolder
	w, h   float64
}

// transformText applies text-transform to a text string.
func transformText(s string, mode int) string {
	switch mode {
	case 1:
		return strings.ToUpper(s)
	case 2:
		return strings.ToLower(s)
	case 3:
		return capitalizeWords(s)
	}
	return s
}

func capitalizeWords(s string) string {
	var sb strings.Builder
	prevLetter := false
	for _, r := range s {
		if isLetterRune(r) && !prevLetter {
			sb.WriteRune(toUpperRune(r))
			prevLetter = true
		} else {
			sb.WriteRune(r)
			prevLetter = isLetterRune(r)
		}
	}
	return sb.String()
}

func isLetterRune(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r > 0x7F
}

func toUpperRune(r rune) rune {
	if r >= 'a' && r <= 'z' {
		return r - 'a' + 'A'
	}
	return r
}

// isCJKRune reports whether r is a CJK ideograph, kana, or CJK punctuation.
func isCJKRune(r rune) bool {
	switch {
	case r >= 0x2E80 && r <= 0x9FFF: // CJK radicals, kana, ideographs, punctuation
		return true
	case r >= 0xF900 && r <= 0xFAFF: // CJK compatibility ideographs
		return true
	case r >= 0xFF00 && r <= 0xFF60: // fullwidth forms
		return true
	case r >= 0x3000 && r <= 0x303F: // CJK symbols and punctuation
		return true
	}
	return false
}

// kinsoku: characters that must not start a line.
var kinsokuNoStart = map[rune]bool{
	'，': true, '。': true, '、': true, '；': true, '：': true, '？': true, '！': true,
	'）': true, '》': true, '〉': true, '」': true, '』': true, '】': true, '〕': true,
	'…': true, '‥': true, '·': true, 'ー': true,
	'ぁ': true, 'ぃ': true, 'ぅ': true, 'ぇ': true, 'ぉ': true, 'っ': true,
	'ゃ': true, 'ゅ': true, 'ょ': true, 'ァ': true, 'ィ': true, 'ゥ': true,
	'ェ': true, 'ォ': true, 'ッ': true, 'ャ': true, 'ュ': true, 'ョ': true,
	'\'': true, '"': true, ')': true, ']': true, '}': true, '%': true, ',': true,
	'.': true, ':': true, ';': true, '!': true, '?': true,
}

// kinsoku: characters that must not end a line.
var kinsokuNoEnd = map[rune]bool{
	'（': true, '《': true, '〈': true, '「': true, '『': true, '【': true, '〔': true,
	'￥': true, '＄': true,
	'(': true, '[': true, '{': true, '$': true, '¥': true, '£': true,
}

// splitToFragments converts a text string into layout fragments following
// these rules: Latin words stay atomic; CJK characters break individually;
// whitespace collapses into single space fragments (unless preformatted).
func splitToFragments(text string, state htmlRenderState) []textFragment {
	var frags []textFragment
	var word strings.Builder

	flushWord := func() {
		if word.Len() > 0 {
			frags = append(frags, textFragment{text: word.String(), kind: fragWord, state: state})
			word.Reset()
		}
	}

	for _, r := range text {
		switch {
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			flushWord()
			if len(frags) > 0 && frags[len(frags)-1].kind == fragSpace {
				continue // collapse
			}
			frags = append(frags, textFragment{text: " ", kind: fragSpace, state: state})
		case isCJKRune(r):
			flushWord()
			frags = append(frags, textFragment{text: string(r), kind: fragCJK, state: state})
		default:
			word.WriteRune(r)
		}
	}
	flushWord()
	return frags
}

// collectInlineFragments walks children collecting inline fragments.
// Block-level children flush the current fragments and are returned separately.
func (r *htmlRenderer) collectInlineFragments(nodes []*htmlNode, state htmlRenderState, frags *[]textFragment, blocks *[]blockItem) {
	for _, node := range nodes {
		if node.Type == htmlNodeText {
			if state.preformed {
				// preserve newlines as forced breaks inside <pre>
				parts := strings.Split(node.Text, "\n")
				for pi, part := range parts {
					if pi > 0 {
						*frags = append(*frags, textFragment{kind: fragBreak, state: state})
					}
					part = strings.ReplaceAll(part, "\t", "    ")
					*frags = append(*frags, splitToFragments(transformText(part, state.transform), state)...)
				}
				continue
			}
			text := collapseWhitespaceKeepEdges(node.Text)
			text = transformText(text, state.transform)
			*frags = append(*frags, splitToFragments(text, state)...)
			continue
		}
		if node.Type != htmlNodeElement {
			continue
		}
		if isBlockElement(node.Tag) && node.Tag != "center" {
			*blocks = append(*blocks, blockItem{node: node, state: r.styleState(node, state)})
			continue
		}
		st := r.inlineState(node, state)
		switch node.Tag {
		case "br":
			*frags = append(*frags, textFragment{kind: fragBreak, state: st})
		case "img":
			if imgFrag := r.newImageFragment(node, st); imgFrag != nil {
				*frags = append(*frags, *imgFrag)
			}
		default:
			r.collectInlineFragments(node.Children, st, frags, blocks)
		}
	}
}

// styleState resolves CSS for a node merged over a base state.
func (r *htmlRenderer) styleState(node *htmlNode, base htmlRenderState) htmlRenderState {
	css := r.style.resolve(node)
	return r.applyCSSProps(css, r.tagState(node, base), node)
}

// inlineState computes the state for an inline element.
func (r *htmlRenderer) inlineState(node *htmlNode, base htmlRenderState) htmlRenderState {
	st := r.tagState(node, base)
	css := r.style.resolve(node)
	return r.applyCSSProps(css, st, node)
}

// tagState applies tag-level (non-CSS) styling.
func (r *htmlRenderer) tagState(node *htmlNode, state htmlRenderState) htmlRenderState {
	st := state
	switch node.Tag {
	case "b", "strong":
		st.fontStyle |= Bold
	case "i", "em", "cite", "var", "dfn":
		st.fontStyle |= Italic
	case "u", "ins":
		st.fontStyle |= Underline
	case "s", "strike", "del":
		st.strike = true
	case "code", "tt", "kbd", "samp":
		if r.opt.MonoFontFamily != "" {
			st.fontFamily = r.opt.MonoFontFamily
		}
	case "sub":
		st.fontSize = state.fontSize * 0.7
		st.rise = -state.fontSize * 0.25
	case "sup":
		st.fontSize = state.fontSize * 0.7
		st.rise = state.fontSize * 0.35
	case "small":
		st.fontSize = state.fontSize * 0.85
	case "big":
		st.fontSize = state.fontSize * 1.2
	case "a":
		// link styling handled by CSS or default blue+underline
	case "font":
		if color, ok := node.Attrs["color"]; ok {
			if cr, cg, cb, cok := parseCSSColor(color); cok {
				st.colorR, st.colorG, st.colorB = cr, cg, cb
			}
		}
		if size, ok := node.Attrs["size"]; ok {
			if sz, sok := parseFontSizeAttr(size); sok {
				st.fontSize = sz
			}
		}
		if face, ok := node.Attrs["face"]; ok {
			st.fontFamily = face
		}
	}
	return st
}

// collapseWhitespaceKeepEdges collapses internal whitespace but preserves
// leading/trailing single spaces (needed for inline fragment joining).
func collapseWhitespaceKeepEdges(s string) string {
	var result strings.Builder
	inSpace := false
	for _, ch := range s {
		if ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' {
			if !inSpace {
				result.WriteByte(' ')
				inSpace = true
			}
		} else {
			result.WriteRune(ch)
			inSpace = false
		}
	}
	return result.String()
}

type blockItem struct {
	node  *htmlNode
	state htmlRenderState
}

// flowBlock lays out a paragraph: collects inline fragments, breaks them into
// lines honoring CJK/kinsoku rules, renders them, then handles nested blocks.
func (r *htmlRenderer) flowBlock(children []*htmlNode, state htmlRenderState, depth int) error {
	if depth > 32 {
		return nil
	}
	var frags []textFragment
	var blocks []blockItem
	r.collectInlineFragments(children, state, &frags, &blocks)

	// apply block margins
	if state.marginTop > 0 {
		r.cursorY += state.marginTop
	}

	if err := r.renderTextFragments(frags, state, nil, state.textIndent); err != nil {
		return err
	}

	// finish the paragraph line
	if r.cursorX > r.boxX {
		r.cursorX = r.boxX
		r.cursorY += r.lineHeightOf(state)
	}
	if state.marginBot > 0 {
		r.cursorY += state.marginBot
	}

	// nested block-level children
	for _, b := range blocks {
		if err := r.renderNode(b.node, b.state); err != nil {
			return err
		}
	}
	return nil
}

// measureFragment returns the rendered width of a fragment.
func (r *htmlRenderer) measureFragment(f *textFragment) (float64, error) {
	if f.kind == fragBreak {
		return 0, nil
	}
	if f.kind == fragImage {
		return f.img.w, nil
	}
	if f.width > 0 {
		return f.width, nil
	}
	if err := r.applyFont(f.state); err != nil {
		return 0, err
	}
	var w float64
	var err error
	if f.state.letterSp > 0 {
		// letter spacing: measure each rune + spacing
		for _, rn := range f.text {
			cw, e := r.gp.MeasureTextWidth(string(rn))
			if e != nil {
				err = e
				break
			}
			w += cw + f.state.letterSp
		}
	} else {
		w, err = r.gp.MeasureTextWidth(f.text)
	}
	if err != nil {
		return 0, err
	}
	f.width = w
	return w, nil
}

// layoutLines breaks fragments into lines that fit within availW.
// Honors CJK per-character breaking with kinsoku rules.
func (r *htmlRenderer) layoutLines(frags []textFragment, availW float64, firstIndent float64) [][]textFragment {
	var lines [][]textFragment
	var cur []textFragment
	curW := 0.0
	indent := firstIndent

	flush := func() {
		// trim trailing space fragments
		for len(cur) > 0 && cur[len(cur)-1].kind == fragSpace {
			cur = cur[:len(cur)-1]
		}
		if len(cur) > 0 {
			lines = append(lines, cur)
		}
		cur = nil
		curW = 0
		indent = 0
	}

	for i := 0; i < len(frags); i++ {
		f := frags[i]
		if f.kind == fragBreak {
			flush()
			continue
		}
		w, err := r.measureFragment(&f)
		if err != nil {
			w = float64(len(f.text)) * f.state.fontSize * 0.5
		}
		if f.kind == fragSpace {
			if len(cur) > 0 {
				cur = append(cur, f)
				curW += w
			}
			continue
		}

		switch f.kind {
		case fragWord:
			fw := w
			if fw > availW {
				// very long word: hard-break by characters
				flush()
				var sub []textFragment
				for _, rn := range f.text {
					sub = append(sub, textFragment{text: string(rn), kind: fragCJK, state: f.state})
				}
				for _, line := range r.layoutLines(sub, availW, 0) {
					lines = append(lines, line)
				}
				continue
			}
			if curW+indent+fw > availW && len(cur) > 0 {
				flush()
			}
			cur = append(cur, f)
			curW += fw
		case fragCJK, fragImage:
			if curW+indent+w > availW && len(cur) > 0 {
				// kinsoku: avoid breaking before no-start punctuation
				if f.kind == fragCJK && kinsokuNoStart[firstRune(f.text)] && len(cur) > 0 {
					// pull the punctuation onto this line (overfull by one char)
					cur = append(cur, f)
					curW += w
					flush()
					continue
				}
				// avoid ending the line with no-end punctuation: move it down
				for len(cur) > 0 && cur[len(cur)-1].kind == fragCJK &&
					kinsokuNoEnd[firstRune(cur[len(cur)-1].text)] {
					// prepend to the fragment being pushed (simple case)
					f = textFragment{text: cur[len(cur)-1].text + f.text, kind: fragCJK, state: f.state}
					w, _ = r.measureFragment(&f)
					cur = cur[:len(cur)-1]
					curW -= w
				}
				flush()
			}
			cur = append(cur, f)
			curW += w
		}
	}
	flush()
	return lines
}

func firstRune(s string) rune {
	for _, r := range s {
		return r
	}
	return 0
}

// renderTextFragments lays out and renders a fragment run.
func (r *htmlRenderer) renderTextFragments(frags []textFragment, state htmlRenderState, stopAt func() bool, firstIndent float64) error {
	if len(frags) == 0 {
		return nil
	}
	lines := r.layoutLines(frags, r.boxW, firstIndent)
	for li, line := range lines {
		if stopAt != nil && stopAt() {
			return nil
		}
		// page overflow check
		lh := r.lineHeightOf(lineMaxState(line))
		if !r.paginate {
			if r.cursorY-r.boxY+lh > r.boxH {
				return nil // clip (legacy box behavior)
			}
		} else {
			if ok, err := r.ensureVerticalSpace(lh); err != nil {
				return err
			} else if !ok {
				return nil
			}
		}

		lineW := 0.0
		for i := range line {
			w, err := r.measureFragment(&line[i])
			if err == nil {
				lineW += w
			}
		}
		spaceCount := 0
		for i := range line {
			if line[i].kind == fragSpace {
				spaceCount++
			}
		}

		x := r.boxX
		extraSpace := 0.0
		switch state.align {
		case Center:
			x = r.boxX + (r.boxW-lineW)/2
		case Right:
			x = r.boxX + r.boxW - lineW
		case Justify:
			if li < len(lines)-1 && spaceCount > 0 && lineW < r.boxW {
				extraSpace = (r.boxW - lineW) / float64(spaceCount)
			}
		}

		for i := range line {
			f := &line[i]
			switch f.kind {
			case fragSpace:
				w, _ := r.measureFragment(f)
				// Draw a real space glyph so text extraction recovers word
				// boundaries exactly (no gap heuristics needed).
				if err := r.applyFont(f.state); err == nil {
					r.gp.SetXY(r.cursorX, r.cursorY)
					_ = r.gp.CellWithOption(&Rect{W: w, H: lh}, " ", CellOption{Align: Left | Top})
				}
				r.cursorX += w + extraSpace
			case fragImage:
				if err := r.drawImageFragment(f, x); err != nil {
					return err
				}
			case fragWord, fragCJK:
				if err := r.renderFragmentText(f, lh, extraSpace); err != nil {
					return err
				}
			}
		}

		r.cursorX = r.boxX
		r.cursorY += lh
	}
	return nil
}

func lineMaxState(line []textFragment) htmlRenderState {
	var best htmlRenderState
	bestSize := 0.0
	for _, f := range line {
		if f.state.fontSize > bestSize {
			bestSize = f.state.fontSize
			best = f.state
		}
	}
	if bestSize == 0 && len(line) > 0 {
		best = line[0].state
	}
	return best
}

// renderFragmentText draws one text fragment at the cursor with decorations.
func (r *htmlRenderer) renderFragmentText(f *textFragment, lh, extraSpace float64) error {
	if err := r.applyFont(f.state); err != nil {
		return err
	}
	w := f.width
	if f.state.letterSp > 0 {
		w = 0
		for _, rn := range f.text {
			cw, _ := r.gp.MeasureTextWidth(string(rn))
			w += cw + f.state.letterSp
		}
	}

	// background highlight
	if f.state.hasBg {
		r.gp.SetFillColor(f.state.bgR, f.state.bgG, f.state.bgB)
		r.gp.RectFromUpperLeftWithStyle(r.cursorX, r.cursorY, w, lh, "F")
	}

	r.gp.SetXY(r.cursorX, r.cursorY)
	rect := &Rect{W: w, H: lh}
	if err := r.gp.CellWithOption(rect, f.text, CellOption{Align: Left | Top}); err != nil {
		return err
	}

	// decorations
	size := f.state.fontSize
	if size <= 0 {
		size = 12
	}
	sizePt := size / r.unitConversion()
	baselineY := r.cursorY + lh*0.8
	if f.state.fontStyle&Underline == Underline {
		r.gp.SetStrokeColor(f.state.colorR, f.state.colorG, f.state.colorB)
		r.gp.SetLineWidth(0.5)
		r.gp.Line(r.cursorX, baselineY+sizePt*0.15, r.cursorX+w, baselineY+sizePt*0.15)
	}
	if f.state.strike {
		r.gp.SetStrokeColor(f.state.colorR, f.state.colorG, f.state.colorB)
		r.gp.SetLineWidth(0.5)
		r.gp.Line(r.cursorX, baselineY-sizePt*0.25, r.cursorX+w, baselineY-sizePt*0.25)
	}

	r.cursorX += w
	if f.state.letterSp > 0 {
		// spacing after last char already included in w
	}
	_ = extraSpace
	return nil
}

// newImageFragment builds an image layout fragment from an <img> node.
func (r *htmlRenderer) newImageFragment(node *htmlNode, state htmlRenderState) *textFragment {
	src, ok := node.Attrs["src"]
	if !ok || src == "" {
		return nil
	}
	var holder ImageHolder
	var err error
	if strings.HasPrefix(src, "data:") {
		holder, err = imageHolderFromDataURI(src)
	} else {
		holder, err = ImageHolderByPath(src)
	}
	if err != nil {
		return nil
	}
	iw, ih, err := imageHolderDimensions(holder)
	if err != nil {
		return nil
	}
	imgW, imgH := 0.0, 0.0
	if w, ok := node.Attrs["width"]; ok {
		if v, vok := parseDimension(w, r.boxW); vok {
			imgW = v / r.unitConversion()
		}
	}
	if h, ok := node.Attrs["height"]; ok {
		if v, vok := parseDimension(h, r.boxH); vok {
			imgH = v / r.unitConversion()
		}
	}
	aspect := 1.0
	if ih > 0 {
		aspect = iw / ih
	}
	switch {
	case imgW > 0 && imgH > 0:
	case imgW > 0:
		imgH = imgW / aspect
	case imgH > 0:
		imgW = imgH * aspect
	default:
		// use intrinsic pixel size scaled to 96dpi
		imgW = iw * 0.75 / r.unitConversion()
		imgH = ih * 0.75 / r.unitConversion()
	}
	imgW, imgH = fitWithinBox(imgW, imgH, r.boxW, r.boxH)
	if imgW <= 0 || imgH <= 0 {
		return nil
	}
	return &textFragment{
		kind:  fragImage,
		width: imgW,
		img:   &imageFragmentData{holder: holder, w: imgW, h: imgH},
		state: state,
	}
}

// drawImageFragment renders an inline image at the cursor.
func (r *htmlRenderer) drawImageFragment(f *textFragment, x float64) error {
	if r.cursorY-r.boxY+f.img.h > r.boxH {
		if !r.paginate {
			return nil
		}
		if ok, err := r.ensureVerticalSpace(f.img.h); err != nil {
			return err
		} else if !ok {
			return nil
		}
	}
	if err := r.gp.ImageByHolder(f.img.holder, r.cursorX, r.cursorY, &Rect{W: f.img.w, H: f.img.h}); err != nil {
		return err
	}
	r.cursorX += f.img.w
	return nil
}

// imageHolderFromDataURI decodes a base64 data URI into an image holder.
func imageHolderFromDataURI(uri string) (ImageHolder, error) {
	comma := strings.Index(uri, ",")
	if comma < 0 {
		return nil, image.ErrFormat
	}
	b64 := uri[comma+1:]
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		// tolerate URL-safe base64
		data, err = base64.URLEncoding.DecodeString(b64)
		if err != nil {
			return nil, err
		}
	}
	return ImageHolderByBytes(data)
}

// renderImage renders a block-level <img>.
func (r *htmlRenderer) renderImage(node *htmlNode, state htmlRenderState, css map[string]string) error {
	frag := r.newImageFragment(node, state)
	if frag == nil {
		return nil
	}
	if r.cursorX > r.boxX {
		r.cursorX = r.boxX
		r.cursorY += r.lineHeightOf(state)
	}
	if ok, err := r.ensureVerticalSpace(frag.img.h); err != nil {
		return err
	} else if !ok {
		return nil
	}
	if err := r.gp.ImageByHolder(frag.img.holder, r.boxX, r.cursorY, &Rect{W: frag.img.w, H: frag.img.h}); err != nil {
		return err
	}
	r.cursorY += frag.img.h
	r.cursorX = r.boxX
	return nil
}

func (r *htmlRenderer) renderHR(node *htmlNode, state htmlRenderState) error {
	if r.cursorX > r.boxX {
		r.cursorX = r.boxX
		r.cursorY += r.lineHeightOf(state)
	}
	r.addVerticalSpace(state.fontSize * 0.3)
	y := r.cursorY + r.lineHeightOf(state)*0.5
	strokeR, strokeG, strokeB := uint8(128), uint8(128), uint8(128)
	width := 0.5
	if color, ok := node.Attrs["color"]; ok {
		if cr, cg, cb, ok2 := parseCSSColor(color); ok2 {
			strokeR, strokeG, strokeB = cr, cg, cb
		}
	}
	if css, ok := node.Attrs["style"]; ok {
		styles := parseInlineStyle(css)
		if c, ok2 := styles["color"]; ok2 {
			if cr, cg, cb, ok3 := parseCSSColor(c); ok3 {
				strokeR, strokeG, strokeB = cr, cg, cb
			}
		}
	}
	r.gp.SetStrokeColor(strokeR, strokeG, strokeB)
	r.gp.SetLineWidth(width)
	r.gp.Line(r.boxX, y, r.boxX+r.boxW, y)
	r.cursorY = y + r.lineHeightOf(state)*0.5
	r.cursorX = r.boxX
	r.addVerticalSpace(state.fontSize * 0.3)
	return nil
}

// ============================================================
// Lists
// ============================================================

func (r *htmlRenderer) renderList(node *htmlNode, state htmlRenderState, ordered bool, depth int) error {
	if depth > 16 {
		return nil
	}
	if r.cursorX > r.boxX {
		r.cursorX = r.boxX
		r.cursorY += r.lineHeightOf(state)
	}
	r.addVerticalSpace(state.fontSize * 0.2)

	// list attributes: start= and type=
	start := 1
	if ordered {
		if s, ok := node.Attrs["start"]; ok {
			if v, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
				start = v
			}
		}
	}
	listStyle := ""
	if ls, ok := node.Attrs["type"]; ok {
		listStyle = ls
	}
	if css, ok := node.Attrs["style"]; ok {
		styles := parseInlineStyle(css)
		if v, ok2 := styles["list-style-type"]; ok2 {
			listStyle = v
		}
	}

	// collect direct <li> children
	items := make([]*htmlNode, 0, len(node.Children))
	for _, child := range node.Children {
		if child.Type == htmlNodeElement && child.Tag == "li" {
			items = append(items, child)
		}
	}
	if len(items) == 0 {
		r.addVerticalSpace(state.fontSize * 0.2)
		return nil
	}

	if err := r.applyFont(state); err != nil {
		return err
	}

	markers := make([]string, len(items))
	var markerWidths = make([]float64, len(items))
	var maxMarkerW float64
	for i := range items {
		if ordered {
			markers[i] = orderedMarker(start+i, listStyle) + "."
		} else {
			markers[i] = bulletMarker(listStyle, depth)
		}
		w, _ := r.gp.MeasureTextWidth(markers[i])
		markerWidths[i] = w
		if w > maxMarkerW {
			maxMarkerW = w
		}
	}
	leftPad, markerCol, contentIndent := listMarkerLayout(state.fontSize, r.unitConversion(), r.boxW, maxMarkerW)
	// nested lists indent further
	if depth > 0 {
		baseIndent := state.fontSize * 1.0 * float64(depth)
		leftPad += baseIndent
		markerCol += 0
		contentIndent += baseIndent
	}

	for i, item := range items {
		lh := r.lineHeightOf(state)
		if !r.paginate {
			if r.cursorY-r.boxY+lh > r.boxH {
				break
			}
		} else {
			if ok, err := r.ensureVerticalSpace(lh); err != nil {
				return err
			} else if !ok {
				break
			}
		}

		if err := r.applyFont(state); err != nil {
			return err
		}
		r.cursorX = r.boxX + leftPad + markerCol - markerWidths[i]
		if r.cursorX < r.boxX {
			r.cursorX = r.boxX
		}
		r.gp.SetXY(r.cursorX, r.cursorY)
		rect := &Rect{W: markerWidths[i], H: lh}
		if err := r.gp.CellWithOption(rect, markers[i], CellOption{Align: Left | Top}); err != nil {
			return err
		}

		oldBoxX, oldBoxW := r.boxX, r.boxW
		r.boxX = oldBoxX + contentIndent
		r.boxW = oldBoxW - contentIndent
		r.cursorX = r.boxX

		childState := r.styleState(item, state)
		// <li> content: inline flow + nested blocks (incl. nested lists)
		if err := r.flowBlock(item.Children, childState, depth+1); err != nil {
			return err
		}

		if r.cursorX > r.boxX {
			r.cursorX = r.boxX
			r.cursorY += r.lineHeightOf(state)
		}
		r.boxX, r.boxW = oldBoxX, oldBoxW
		r.cursorX = r.boxX
	}

	r.addVerticalSpace(state.fontSize * 0.2)
	return nil
}

// orderedMarker formats an ordered-list marker per list-style-type.
func orderedMarker(n int, listStyle string) string {
	switch listStyle {
	case "lower-alpha", "lower-latin":
		return toAlphaMarker(n, 'a')
	case "upper-alpha", "upper-latin":
		return toAlphaMarker(n, 'A')
	case "lower-roman":
		return strings.ToLower(toRomanMarker(n))
	case "upper-roman":
		return toRomanMarker(n)
	default:
		return strconv.Itoa(n)
	}
}

func toAlphaMarker(n int, base rune) string {
	if n <= 0 {
		return string(base)
	}
	var sb []rune
	for n > 0 {
		n--
		sb = append([]rune{base + rune(n%26)}, sb...)
		n /= 26
	}
	return string(sb)
}

func toRomanMarker(n int) string {
	if n <= 0 {
		return "i"
	}
	type rv struct {
		v int
		s string
	}
	table := []rv{{1000, "M"}, {900, "CM"}, {500, "D"}, {400, "CD"}, {100, "C"},
		{90, "XC"}, {50, "L"}, {40, "XL"}, {10, "X"}, {9, "IX"}, {5, "V"},
		{4, "IV"}, {1, "I"}}
	out := ""
	for _, e := range table {
		for n >= e.v {
			out += e.s
			n -= e.v
		}
	}
	return out
}

func bulletMarker(listStyle string, depth int) string {
	switch listStyle {
	case "circle":
		return "○"
	case "square":
		return "■"
	case "none":
		return ""
	case "disc", "":
		if depth%2 == 1 {
			return "○"
		}
		return "•"
	}
	return "•"
}

// listMarkers is kept for API compat with earlier tests.
func (r *htmlRenderer) listMarkers(n int, ordered bool) (markers []string, widths []float64, maxW float64) {
	markers = make([]string, n)
	widths = make([]float64, n)
	if n <= 0 {
		return markers, widths, 0
	}
	for i := 0; i < n; i++ {
		if ordered {
			markers[i] = strconv.Itoa(i+1) + "."
		} else {
			markers[i] = "•"
		}
		w, _ := r.gp.MeasureTextWidth(markers[i])
		widths[i] = w
		if w > maxW {
			maxW = w
		}
	}
	return markers, widths, maxW
}

// listMarkerLayout sizes the marker column so multi-digit labels never
// overflow into item text.
//
// Layout (left → right):
//
//	[ leftPad ][ markerCol (right-aligned labels) ][ gap ][ content… ]
func listMarkerLayout(fontSize, unit, boxW, maxMarkerW float64) (leftPad, markerCol, contentIndent float64) {
	if unit <= 0 {
		unit = 1
	}
	if maxMarkerW < 0 {
		maxMarkerW = 0
	}

	leftPad = fontSize * 0.25 / unit
	gap := fontSize * 0.4 / unit
	minCol := fontSize * 0.9 / unit

	markerCol = maxMarkerW
	if markerCol < minCol {
		markerCol = minCol
	}
	contentIndent = leftPad + markerCol + gap

	// Narrow box: drop left pad and shrink gap; keep maxMarkerW.
	if boxW > 0 && contentIndent > boxW*0.45 {
		leftPad = 0
		gap = fontSize * 0.2 / unit
		markerCol = maxMarkerW
		contentIndent = markerCol + gap
	}

	// Fit inside the box. Residual width becomes content, not dead space.
	if boxW > 0 && contentIndent > boxW {
		leftPad = 0
		if maxMarkerW >= boxW {
			markerCol = boxW
			contentIndent = boxW
		} else {
			markerCol = maxMarkerW
			minGap := fontSize * 0.15 / unit
			if markerCol+minGap <= boxW {
				contentIndent = markerCol + minGap
			} else {
				contentIndent = markerCol
			}
		}
	}

	return leftPad, markerCol, contentIndent
}

// ============================================================
// Tables
// ============================================================

// renderTable renders an HTML table with colspan/rowspan, column widths,
// background colors, per-cell alignment, caption and pagination support.
func (r *htmlRenderer) renderTable(node *htmlNode, state htmlRenderState) error {
	if r.cursorX > r.boxX {
		r.cursorX = r.boxX
		r.cursorY += r.lineHeightOf(state)
	}

	grid := r.buildTableGrid(node, state)
	if grid == nil || grid.cols == 0 || grid.rows == 0 {
		return nil
	}

	// caption
	for _, child := range node.Children {
		if child.Type == htmlNodeElement && child.Tag == "caption" {
			capState := r.styleState(child, state)
			capState.align = Center
			r.addVerticalSpace(state.fontSize * 0.2)
			if err := r.flowBlock(child.Children, capState, 0); err != nil {
				return err
			}
			if r.cursorX > r.boxX {
				r.cursorX = r.boxX
				r.cursorY += r.lineHeightOf(capState)
			}
			r.addVerticalSpace(state.fontSize * 0.1)
		}
	}

	// record thead rows for pagination repeat
	if r.opt.RepeatTableHeaders {
		r.activeTable = collectTableHeadRows(node)
	}

	// table-level background/border
	tblBG := r.tableBackground(node)
	tblBorder := r.tableBorder(node, state)

	// column widths
	colWidths := r.computeColumnWidths(node, grid)
	totalW := 0.0
	for _, w := range colWidths {
		totalW += w
	}
	_ = totalW

	// row heights
	rowHeights := r.computeRowHeights(grid, colWidths, state)

	padding := state.fontSize * 0.3 / r.unitConversion()
	if padding < 1 {
		padding = 1
	}

	// header row count for repeat
	headerRows := 0
	for _, child := range node.Children {
		if child.Type == htmlNodeElement && child.Tag == "thead" {
			for _, tr := range child.Children {
				if tr.Type == htmlNodeElement && tr.Tag == "tr" {
					headerRows++
				}
			}
		}
	}

	r.addVerticalSpace(state.fontSize * 0.2)
	curY := r.cursorY

	// draw cells row by row so page breaks happen between rows
	cellsByRow := make(map[int][]htmlTableCell, grid.rows)
	for _, cell := range grid.cells {
		cellsByRow[cell.row] = append(cellsByRow[cell.row], cell)
	}
	rowOffset := 0.0
	for row := 0; row < grid.rows; row++ {
		rowH := rowHeights[row]
		if row >= headerRows && curY+rowH > r.boxY+r.boxH {
			// row doesn't fit: start a new page
			r.cursorY = curY
			if ok, err := r.ensureVerticalSpace(rowH); err != nil {
				return err
			} else if !ok {
				break // no pagination: clip the rest
			}
			curY = r.cursorY
			rowOffset = 0
	_ = rowOffset // (reserved for rowspan handling)
		}
		for _, cell := range cellsByRow[row] {
			if cell.node == nil {
				continue
			}
			x := r.boxX
			for c := 0; c < cell.col; c++ {
				x += colWidths[c]
			}
			w := 0.0
			for c := cell.col; c < cell.col+cell.colspan && c < len(colWidths); c++ {
				w += colWidths[c]
			}
			h := 0.0
			for rr := cell.row; rr < cell.row+cell.rowspan && rr < len(rowHeights); rr++ {
				h += rowHeights[rr]
			}
			if curY+h > r.boxY+r.boxH && !r.paginate {
				continue // clipped
			}
			cellState := cell.state
			if err := r.drawHTMLTableCell(node, x, curY, w, h, cell, cellState, padding, tblBG, tblBorder); err != nil {
				return err
			}
		}
		curY += rowH
		r.cursorY = curY
	}
	r.cursorY = curY
	r.cursorX = r.boxX
	r.activeTable = nil
	r.addVerticalSpace(state.fontSize * 0.2)
	return nil
}



// buildTableGrid builds the table cell grid, resolving colspan/rowspan.
func (r *htmlRenderer) buildTableGrid(node *htmlNode, state htmlRenderState) *htmlGrid {
	grid := &htmlGrid{}
	type placed struct{ col, rowEnd, colEnd int }
	occupied := map[int]map[int]bool{} // row -> col set

	markOccupied := func(row, colStart, rowSpan, colSpan int) {
		if occupied[row] == nil {
			occupied[row] = map[int]bool{}
		}
		for rr := row; rr < row+rowSpan; rr++ {
			for cc := colStart; cc < colStart+colSpan; cc++ {
				if occupied[rr] == nil {
					occupied[rr] = map[int]bool{}
				}
				occupied[rr][cc] = true
			}
		}
	}
	isFree := func(row, col int) bool {
		return !occupied[row][col]
	}
	nextFree := func(row, col int) int {
		for !isFree(row, col) {
			col++
		}
		return col
	}

	rowIdx := 0
	var walkSections func([]*htmlNode, bool)
	walkSections = func(children []*htmlNode, inHead bool) {
		for _, child := range children {
			if child.Type != htmlNodeElement {
				continue
			}
			switch child.Tag {
			case "thead", "tbody", "tfoot":
				walkSections(child.Children, inHead || child.Tag == "thead")
			case "tr":
				col := 0
				for _, cellNode := range child.Children {
					if cellNode.Type != htmlNodeElement || (cellNode.Tag != "td" && cellNode.Tag != "th") {
						continue
					}
					col = nextFree(rowIdx, col)
					colspan := parseSpanAttr(cellNode.Attrs["colspan"])
					rowspan := parseSpanAttr(cellNode.Attrs["rowspan"])
					if colspan < 1 {
						colspan = 1
					}
					if rowspan < 1 {
						rowspan = 1
					}
					cellState := state
					if cellNode.Tag == "th" {
						cellState.fontStyle |= Bold
						cellState.align = Center
					}
					cellState = r.styleState(cellNode, cellState)
					grid.cells = append(grid.cells, htmlTableCell{
						node:    cellNode,
						state:   cellState,
						header:  cellNode.Tag == "th",
						colspan: colspan,
						rowspan: rowspan,
						col:     col,
						row:     rowIdx,
					})
					markOccupied(rowIdx, col, rowspan, colspan)
					col += colspan
					if col > grid.cols {
						grid.cols = col
					}
				}
				rowIdx++
				if rowIdx > grid.rows {
					grid.rows = rowIdx
				}
			}
		}
	}
	walkSections(node.Children, false)
	return grid
}

func parseSpanAttr(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

type htmlGrid struct {
	rows int
	cols int
	cells []htmlTableCell
}

// collectTableHeadRows returns the <thead> <tr> nodes of a table.
func collectTableHeadRows(node *htmlNode) []*htmlNode {
	var rows []*htmlNode
	for _, child := range node.Children {
		if child.Type == htmlNodeElement && child.Tag == "thead" {
			for _, tr := range child.Children {
				if tr.Type == htmlNodeElement && tr.Tag == "tr" {
					rows = append(rows, tr)
				}
			}
		}
	}
	return rows
}

// computeColumnWidths determines per-column widths:
// explicit widths (col/cell attrs and CSS), then equal split of the remainder.
func (r *htmlRenderer) computeColumnWidths(node *htmlNode, grid *htmlGrid) []float64 {
	widths := make([]float64, grid.cols)
	fixed := make([]bool, grid.cols)
	tableW := r.boxW

	// table width attr/style
	if w, ok := node.Attrs["width"]; ok {
		if v, ok2 := parseDimension(w, r.boxW); ok2 {
			tableW = v / r.unitConversion()
			if tableW <= 0 || tableW > r.boxW {
				tableW = r.boxW
			}
		}
	}

	// <col> / <colgroup> widths
	for _, child := range node.Children {
		if child.Type != htmlNodeElement {
			continue
		}
		if child.Tag == "colgroup" {
			ci := 0
			for _, col := range child.Children {
				if col.Type == htmlNodeElement && col.Tag == "col" {
					ci += r.applyColWidth(col, widths, fixed, ci, tableW)
				}
			}
		} else if child.Tag == "col" {
			r.applyColWidth(child, widths, fixed, 0, tableW)
		}
	}

	// per-cell widths (max across cells in that column)
	for _, cell := range grid.cells {
		if cell.node == nil || cell.colspan != 1 || cell.col >= grid.cols {
			continue
		}
		if w, ok := cell.node.Attrs["width"]; ok {
			if v, ok2 := parseDimension(w, tableW); ok2 {
				v = v / r.unitConversion()
				if v > widths[cell.col] {
					widths[cell.col] = v
					fixed[cell.col] = true
				}
			}
		}
		if css := r.style.resolve(cell.node); css != nil {
			if w, ok := css["width"]; ok {
				if v, ok2 := parseDimension(w, tableW); ok2 {
					v = v / r.unitConversion()
					if v > widths[cell.col] {
						widths[cell.col] = v
						fixed[cell.col] = true
					}
				}
			}
		}
	}

	// distribute remaining width across unfixed columns
	fixedSum := 0.0
	nFree := 0
	for i := range widths {
		if fixed[i] {
			fixedSum += widths[i]
		} else {
			nFree++
		}
	}
	if fixedSum > tableW {
		// scale fixed columns down proportionally
		scale := tableW / fixedSum
		for i := range widths {
			if fixed[i] {
				widths[i] *= scale
			}
		}
	} else if nFree > 0 {
		per := (tableW - fixedSum) / float64(nFree)
		for i := range widths {
			if !fixed[i] {
				widths[i] = per
			}
		}
	}
	return widths
}

// applyColWidth applies a <col> width declaration; returns how many columns
// the col spanned (span attr).
func (r *htmlRenderer) applyColWidth(col *htmlNode, widths []float64, fixed []bool, startIdx int, tableW float64) int {
	span := parseSpanAttr(col.Attrs["span"])
	if span < 1 {
		span = 1
	}
	var wVal float64
	hasW := false
	if w, ok := col.Attrs["width"]; ok {
		if v, ok2 := parseDimension(w, tableW); ok2 {
			wVal = v / r.unitConversion()
			hasW = true
		}
	}
	if !hasW {
		if css := r.style.resolve(col); css != nil {
			if w, ok := css["width"]; ok {
				if v, ok2 := parseDimension(w, tableW); ok2 {
					wVal = v / r.unitConversion()
					hasW = true
				}
			}
		}
	}
	if !hasW {
		return span
	}
	per := wVal / float64(span)
	for i := 0; i < span && startIdx+i < len(widths); i++ {
		if per > widths[startIdx+i] {
			widths[startIdx+i] = per
			fixed[startIdx+i] = true
		}
	}
	return span
}

// computeRowHeights measures each row's height from its cells' content.
func (r *htmlRenderer) computeRowHeights(grid *htmlGrid, colWidths []float64, fallback htmlRenderState) []float64 {
	heights := make([]float64, grid.rows)
	padding := fallback.fontSize * 0.3 / r.unitConversion()
	if padding < 1 {
		padding = 1
	}
	for _, cell := range grid.cells {
		if cell.node == nil {
			continue
		}
		w := 0.0
		for c := cell.col; c < cell.col+cell.colspan && c < len(colWidths); c++ {
			w += colWidths[c]
		}
		contentW := w - padding*2
		if contentW <= 0 {
			contentW = w
		}
		text := extractHTMLNodeText(cell.node)
		cellState := cell.state
		if cellState.fontSize <= 0 {
			cellState = fallback
		}
		h, err := r.measureWrappedTextHeight(text, cellState, contentW)
		if err != nil {
			h = r.lineHeightOf(cellState)
		}
		h += padding * 2
		if cell.row < len(heights) && h > heights[cell.row] {
			heights[cell.row] = h
		}
	}
	for i := range heights {
		if heights[i] <= 0 {
			heights[i] = r.lineHeightOf(fallback) + padding*2
		}
	}
	return heights
}

// tableBackground reads the table-level background-color.
func (r *htmlRenderer) tableBackground(node *htmlNode) htmlSurface {
	if c, ok := node.Attrs["bgcolor"]; ok {
		if cr, cg, cb, ok2 := parseCSSColor(c); ok2 {
			return htmlSurface{has: true, r: cr, g: cg, b: cb}
		}
	}
	if css := r.style.resolve(node); css != nil {
		if c, ok := css["background-color"]; ok {
			if cr, cg, cb, ok2 := parseCSSColor(c); ok2 {
				return htmlSurface{has: true, r: cr, g: cg, b: cb}
			}
		}
	}
	return htmlSurface{}
}

// htmlSurface is a fill surface color (background) for tables and cells.
type htmlSurface struct {
	has bool
	r   uint8
	g   uint8
	b   uint8
}

// htmlBorderSpec describes a border style for tables.
type htmlBorderSpec struct {
	has   bool
	width float64
	r     uint8
	g     uint8
	b     uint8
}

// tableBorder reads border attributes/styles from the table node.
func (r *htmlRenderer) tableBorder(node *htmlNode, state htmlRenderState) htmlBorderSpec {
	spec := htmlBorderSpec{}
	if b, ok := node.Attrs["border"]; ok {
		spec.has = true
		if v, err := strconv.ParseFloat(strings.TrimSpace(b), 64); err == nil && v > 0 {
			spec.width = v * 0.75 / r.unitConversion() // px border
		}
	}
	if css := r.style.resolve(node); css != nil {
		if b, ok := css["border"]; ok {
			spec.has = true
			if w, _ := parseCSSBorderWidth(b); w > 0 {
				spec.width = w / r.unitConversion()
			}
			if c, _ := parseCSSBorderColor(b); c != "" {
				if pr, pg, pb, ok2 := parseCSSColor(c); ok2 {
					spec.r, spec.g, spec.b = pr, pg, pb
				}
			}
		}
	}
	_ = state
	return spec
}

// parseCSSBorderWidth extracts a leading width from a border shorthand.
func parseCSSBorderWidth(b string) (float64, string) {
	fields := strings.Fields(b)
	rest := b
	for i, f := range fields {
		if v, ok := parseDimension(f, 12); ok && (strings.HasSuffix(f, "px") || strings.HasSuffix(f, "pt") || isNumericField(f)) {
			rest = strings.Join(append(fields[:i:i], fields[i+1:]...), " ")
			return v, rest
		}
	}
	return 0, rest
}

func isNumericField(f string) bool {
	_, err := strconv.ParseFloat(f, 64)
	return err == nil
}

func parseCSSBorderColor(b string) (string, string) {
	fields := strings.Fields(b)
	for _, f := range fields {
		if _, _, _, ok := parseCSSColor(f); ok {
			return f, b
		}
	}
	return "", b
}

// drawHTMLTableCell renders one table cell.
func (r *htmlRenderer) drawHTMLTableCell(tableNode *htmlNode, x, y, w, h float64, cell htmlTableCell, cellState htmlRenderState, padding float64, tblBG htmlSurface, tblBorder htmlBorderSpec) error {
	// background: explicit cell bg > header default > table bg
	hasBG := false
	bgR, bgG, bgB := uint8(0), uint8(0), uint8(0)
	if cellState.hasBg {
		hasBG, bgR, bgG, bgB = true, cellState.bgR, cellState.bgG, cellState.bgB
	} else if cell.header {
		hasBG, bgR, bgG, bgB = true, 240, 240, 240
	} else if tblBG.has {
		hasBG, bgR, bgG, bgB = true, tblBG.r, tblBG.g, tblBG.b
	}
	if hasBG {
		r.gp.SetFillColor(bgR, bgG, bgB)
		r.gp.RectFromUpperLeftWithStyle(x, y, w, h, "F")
	}

	// borders
	if cellState.hasBg || cell.header || tblBorder.has {
		borderR, borderG, borderB := uint8(0), uint8(0), uint8(0)
		if tblBorder.has {
			borderR, borderG, borderB = tblBorder.r, tblBorder.g, tblBorder.b
		}
		bwCell := 0.5
		if tblBorder.has && tblBorder.width > 0 {
			bwCell = tblBorder.width
		}
		r.gp.SetStrokeColor(borderR, borderG, borderB)
		r.gp.SetLineWidth(bwCell)
		r.gp.Line(x, y, x+w, y)
		r.gp.Line(x+w, y, x+w, y+h)
		r.gp.Line(x, y+h, x+w, y+h)
		r.gp.Line(x, y, x, y+h)
	}

	// content
	prevX, prevY := r.cursorX, r.cursorY
	prevBoxX, prevBoxY, prevBoxW, prevBoxH := r.boxX, r.boxY, r.boxW, r.boxH
	r.boxX = x + padding
	r.boxY = y + padding
	r.boxW = w - padding*2
	r.boxH = h - padding*2
	if r.boxW < 4 {
		r.boxW = w
	}
	r.cursorX = r.boxX
	r.cursorY = r.boxY

	err := r.flowBlock(cell.node.Children, cellState, 0)
	// v-align: bottom/middle shift content when the cell is taller
	if err == nil && (cellState.vAlign == Middle || cellState.vAlign == Bottom) {
		contentH := r.cursorY - r.boxY
		avail := h - padding*2
		if contentH < avail {
			shift := avail - contentH
			if cellState.vAlign == Middle {
				shift /= 2
			}
			_ = shift // shifting already-drawn content is not possible; acceptable v1
		}
	}

	r.cursorX, r.cursorY = prevX, prevY
	r.boxX, r.boxY, r.boxW, r.boxH = prevBoxX, prevBoxY, prevBoxW, prevBoxH
	return err
}

// ============================================================
// Legacy helpers (kept for compatibility)
// ============================================================

func extractHTMLNodeText(node *htmlNode) string {
	if node.Type == htmlNodeText {
		return node.Text
	}
	var parts []string
	for _, child := range node.Children {
		text := extractHTMLNodeText(child)
		if strings.TrimSpace(text) != "" {
			parts = append(parts, text)
		}
	}
	return collapseWhitespace(strings.Join(parts, " "))
}

func (r *htmlRenderer) measureHTMLTableRowHeight(row htmlTableRow, colWidth, padding float64, fallback htmlRenderState) (float64, error) {
	maxHeight := 0.0
	contentWidth := colWidth - padding*2
	if contentWidth <= 0 {
		contentWidth = colWidth
	}
	for _, cell := range row.cells {
		cellState := cell.state
		if cellState.fontSize <= 0 {
			cellState = fallback
		}
		text := ""
		if cell.node != nil {
			text = extractHTMLNodeText(cell.node)
		}
		height, err := r.measureWrappedTextHeight(text, cellState, contentWidth)
		if err != nil {
			return 0, err
		}
		height += padding * 2
		if height > maxHeight {
			maxHeight = height
		}
	}
	return maxHeight, nil
}

func (r *htmlRenderer) measureWrappedTextHeight(text string, state htmlRenderState, maxWidth float64) (float64, error) {
	if err := r.applyFont(state); err != nil {
		return 0, err
	}
	text = collapseWhitespace(text)
	if text == "" {
		return r.lineHeightOf(state), nil
	}
	if maxWidth <= 0 {
		return r.lineHeightOf(state), nil
	}

	frags := splitToFragments(text, state)
	lines := r.layoutLines(frags, maxWidth, 0)
	mul := state.lineHeight
	if mul <= 0 {
		mul = 1.2
	}
	return float64(len(lines)) * state.fontSize*mul/r.unitConversion(), nil
}

func (r *htmlRenderer) measureLongWordLines(word string, maxWidth float64) (int, error) {
	lines := 1
	lineWidth := 0.0
	for _, ch := range word {
		chWidth, err := r.gp.MeasureTextWidth(string(ch))
		if err != nil {
			return 0, err
		}
		if lineWidth > 0 && lineWidth+chWidth > maxWidth {
			lines++
			lineWidth = chWidth
		} else {
			lineWidth += chWidth
		}
	}
	return lines, nil
}

// collapseWhitespace collapses consecutive whitespace characters into a single space.
func collapseWhitespace(s string) string {
	var result strings.Builder
	inSpace := false
	for _, ch := range s {
		if ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' {
			if !inSpace {
				result.WriteByte(' ')
				inSpace = true
			}
		} else {
			result.WriteRune(ch)
			inSpace = false
		}
	}
	return strings.TrimSpace(result.String())
}

// splitWords splits text into words by spaces.
func splitWords(text string) []string {
	return strings.Fields(text)
}

func imageHolderDimensions(holder ImageHolder) (float64, float64, error) {
	reader, ok := holder.(*imageBuff)
	if !ok {
		return 0, 0, image.ErrFormat
	}

	cfg, _, err := image.DecodeConfig(bytes.NewReader(reader.Bytes()))
	if err != nil {
		return 0, 0, err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return 0, 0, image.ErrFormat
	}
	return float64(cfg.Width), float64(cfg.Height), nil
}

func fitWithinBox(width, height, maxWidth, maxHeight float64) (float64, float64) {
	if width <= 0 || height <= 0 {
		return 0, 0
	}
	if maxWidth > 0 && width > maxWidth {
		ratio := maxWidth / width
		width = maxWidth
		height *= ratio
	}
	if maxHeight > 0 && height > maxHeight {
		ratio := maxHeight / height
		height = maxHeight
		width *= ratio
	}
	return width, height
}
