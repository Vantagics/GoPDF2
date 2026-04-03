package gopdf

import (
	"bytes"
	"image"
	"strconv"
	"strings"
)

// HTMLBoxOption configures the behavior of InsertHTMLBox.
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
}

// htmlRenderState tracks the current rendering state while walking the HTML tree.
type htmlRenderState struct {
	fontFamily string
	fontSize   float64
	fontStyle  int // Regular, Bold, Italic, Underline
	colorR     uint8
	colorG     uint8
	colorB     uint8
	align      int
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
}

type htmlTableCell struct {
	text   string
	state  htmlRenderState
	header bool
}

type htmlTableRow struct {
	cells []htmlTableCell
}

// InsertHTMLBox renders simplified HTML content into a rectangular area on the PDF.
//
// Supported HTML tags:
//   - <b>, <strong>: Bold text
//   - <i>, <em>: Italic text
//   - <u>: Underlined text
//   - <s>, <strike>, <del>: Strikethrough (rendered as underline for simplicity)
//   - <br>, <br/>: Line break
//   - <p>: Paragraph (adds vertical spacing)
//   - <h1> to <h6>: Headings with automatic font sizing
//   - <font color="..." size="..." face="...">: Font styling
//   - <span style="...">: Inline styling (color, font-size, font-family)
//   - <img src="..." width="..." height="...">: Images (src must be a local file path)
//   - <hr>: Horizontal rule
//   - <center>: Centered text
//   - <ul>, <ol>, <li>: Lists (basic bullet/number)
//   - <a href="...">: Links (rendered as colored text, link annotation added)
//   - <sub>, <sup>: Subscript/superscript (approximated with smaller font)
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
	}

	state := htmlRenderState{
		fontFamily: opt.DefaultFontFamily,
		fontSize:   opt.DefaultFontSize,
		fontStyle:  Regular,
		colorR:     opt.DefaultColor[0],
		colorG:     opt.DefaultColor[1],
		colorB:     opt.DefaultColor[2],
		align:      Left,
	}

	if err := r.renderNodes(nodes, state); err != nil {
		return r.cursorY, err
	}

	return r.cursorY, nil
}

func (r *htmlRenderer) renderNodes(nodes []*htmlNode, state htmlRenderState) error {
	for _, node := range nodes {
		if r.cursorY-r.boxY >= r.boxH {
			break // exceeded box height
		}
		if err := r.renderNode(node, state); err != nil {
			return err
		}
	}
	return nil
}

func (r *htmlRenderer) renderNode(node *htmlNode, state htmlRenderState) error {
	if node.Type == htmlNodeText {
		return r.renderText(node.Text, state)
	}

	// element node
	newState := state

	switch node.Tag {
	case "b", "strong":
		newState.fontStyle |= Bold
	case "i", "em":
		newState.fontStyle |= Italic
	case "u", "ins":
		newState.fontStyle |= Underline
	case "s", "strike", "del":
		// approximate strikethrough with underline
		newState.fontStyle |= Underline
	case "br":
		r.newLine(state)
		return nil
	case "hr":
		return r.renderHR(state)
	case "p", "div":
		newState = r.applyStyleAttr(node, newState)
		if r.cursorX > r.boxX {
			r.newLine(state)
		}
		r.addVerticalSpace(state.fontSize * 0.3)
		if err := r.renderNodes(node.Children, newState); err != nil {
			return err
		}
		if r.cursorX > r.boxX {
			r.newLine(state)
		}
		r.addVerticalSpace(state.fontSize * 0.3)
		return nil
	case "h1", "h2", "h3", "h4", "h5", "h6":
		newState.fontSize = headingFontSize(node.Tag)
		newState.fontStyle |= Bold
		newState = r.applyStyleAttr(node, newState)
		if r.cursorX > r.boxX {
			r.newLine(state)
		}
		r.addVerticalSpace(newState.fontSize * 0.4)
		if err := r.renderNodes(node.Children, newState); err != nil {
			return err
		}
		if r.cursorX > r.boxX {
			r.newLine(newState)
		}
		r.addVerticalSpace(newState.fontSize * 0.3)
		return nil
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
	case "span":
		newState = r.applyStyleAttr(node, newState)
	case "center":
		newState.align = Center
		if r.cursorX > r.boxX {
			r.newLine(state)
		}
		if err := r.renderNodes(node.Children, newState); err != nil {
			return err
		}
		if r.cursorX > r.boxX {
			r.newLine(newState)
		}
		return nil
	case "a":
		// render link text in blue with underline, then add PDF link annotation
		newState.colorR, newState.colorG, newState.colorB = 0, 0, 255
		newState.fontStyle |= Underline
		href := node.Attrs["href"]

		// record position before rendering link text
		startX := r.cursorX
		startY := r.cursorY
		lh := r.lineHeight(newState)

		if err := r.renderNodes(node.Children, newState); err != nil {
			return err
		}

		// add clickable link annotation if href is present
		if href != "" {
			endX := r.cursorX
			// convert to points for the annotation
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
		return r.renderImage(node, state)
	case "table":
		return r.renderTable(node, state)
	case "ul", "ol":
		return r.renderList(node, state, node.Tag == "ol")
	case "li":
		// handled by renderList
	case "sub":
		newState.fontSize = state.fontSize * 0.7
	case "sup":
		newState.fontSize = state.fontSize * 0.7
	case "blockquote":
		newState = r.applyStyleAttr(node, newState)
		if r.cursorX > r.boxX {
			r.newLine(state)
		}
		oldBoxX := r.boxX
		oldBoxW := r.boxW
		indent := state.fontSize * 1.5
		r.boxX += indent
		r.boxW -= indent
		r.cursorX = r.boxX
		r.addVerticalSpace(state.fontSize * 0.3)
		if err := r.renderNodes(node.Children, newState); err != nil {
			return err
		}
		if r.cursorX > r.boxX {
			r.newLine(newState)
		}
		r.addVerticalSpace(state.fontSize * 0.3)
		r.boxX = oldBoxX
		r.boxW = oldBoxW
		r.cursorX = r.boxX
		return nil
	}

	// render children with updated state
	if err := r.renderNodes(node.Children, newState); err != nil {
		return err
	}

	return nil
}

func (r *htmlRenderer) applyStyleAttr(node *htmlNode, state htmlRenderState) htmlRenderState {
	styleStr, ok := node.Attrs["style"]
	if !ok {
		return state
	}
	styles := parseInlineStyle(styleStr)

	if color, ok := styles["color"]; ok {
		if cr, cg, cb, cok := parseCSSColor(color); cok {
			state.colorR, state.colorG, state.colorB = cr, cg, cb
		}
	}
	if fs, ok := styles["font-size"]; ok {
		if sz, sok := parseFontSize(fs, state.fontSize); sok {
			state.fontSize = sz
		}
	}
	if ff, ok := styles["font-family"]; ok {
		state.fontFamily = strings.Trim(ff, "'\"")
	}
	if fw, ok := styles["font-weight"]; ok {
		if fw == "bold" || fw == "700" || fw == "800" || fw == "900" {
			state.fontStyle |= Bold
		}
	}
	if fst, ok := styles["font-style"]; ok {
		if fst == "italic" {
			state.fontStyle |= Italic
		}
	}
	if td, ok := styles["text-decoration"]; ok {
		if strings.Contains(td, "underline") {
			state.fontStyle |= Underline
		}
	}
	if ta, ok := styles["text-align"]; ok {
		switch ta {
		case "center":
			state.align = Center
		case "right":
			state.align = Right
		case "left":
			state.align = Left
		}
	}
	return state
}

func (r *htmlRenderer) applyFont(state htmlRenderState) error {
	family := r.resolveFontFamily(state)
	style := state.fontStyle &^ Underline // strip underline for font lookup
	if err := r.gp.SetFontWithStyle(family, style, state.fontSize); err != nil {
		// fallback to default family
		if err2 := r.gp.SetFontWithStyle(r.opt.DefaultFontFamily, Regular, state.fontSize); err2 != nil {
			return err
		}
	}
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

func (r *htmlRenderer) lineHeight(state htmlRenderState) float64 {
	return state.fontSize * 1.2 / r.unitConversion() + r.opt.LineSpacing
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

func (r *htmlRenderer) newLine(state htmlRenderState) {
	r.cursorX = r.boxX
	r.cursorY += r.lineHeight(state)
}

func (r *htmlRenderer) addVerticalSpace(ptSpace float64) {
	r.cursorY += ptSpace / r.unitConversion()
}

func (r *htmlRenderer) remainingWidth() float64 {
	return r.boxX + r.boxW - r.cursorX
}

func (r *htmlRenderer) renderText(text string, state htmlRenderState) error {
	if err := r.applyFont(state); err != nil {
		return err
	}

	// collapse whitespace
	text = collapseWhitespace(text)
	if text == "" {
		return nil
	}

	words := splitWords(text)
	lh := r.lineHeight(state)
	spaceWidth, err := r.gp.MeasureTextWidth(" ")
	if err != nil {
		return err
	}

	for i, word := range words {
		if r.cursorY-r.boxY+lh > r.boxH {
			break // exceeded box height
		}

		wordWidth, err := r.gp.MeasureTextWidth(word)
		if err != nil {
			return err
		}

		// check if word fits on current line
		if r.cursorX > r.boxX && r.cursorX+wordWidth > r.boxX+r.boxW {
			r.newLine(state)
		}

		// if a single word is wider than the box, force-render it
		if wordWidth > r.boxW {
			// render character by character with wrapping
			if err := r.renderLongWord(word, state); err != nil {
				return err
			}
			continue
		}

		// handle alignment for new lines
		if r.cursorX == r.boxX && state.align == Center {
			lineWidth := r.measureLineWidth(words[i:], spaceWidth, state)
			if lineWidth < r.boxW {
				r.cursorX = r.boxX + (r.boxW-lineWidth)/2
			}
		} else if r.cursorX == r.boxX && state.align == Right {
			lineWidth := r.measureLineWidth(words[i:], spaceWidth, state)
			if lineWidth < r.boxW {
				r.cursorX = r.boxX + r.boxW - lineWidth
			}
		}

		r.gp.SetXY(r.cursorX, r.cursorY)

		cellOpt := CellOption{
			Align: Left | Top,
		}

		rect := &Rect{W: wordWidth, H: lh}
		if err := r.gp.CellWithOption(rect, word, cellOpt); err != nil {
			return err
		}

		r.cursorX += wordWidth

		// add space after word (except last)
		if i < len(words)-1 {
			r.cursorX += spaceWidth
		}
	}

	return nil
}

func (r *htmlRenderer) renderLongWord(word string, state htmlRenderState) error {
	lh := r.lineHeight(state)
	for _, ch := range word {
		s := string(ch)
		chWidth, err := r.gp.MeasureTextWidth(s)
		if err != nil {
			return err
		}
		if r.cursorX+chWidth > r.boxX+r.boxW && r.cursorX > r.boxX {
			r.newLine(state)
		}
		r.gp.SetXY(r.cursorX, r.cursorY)
		rect := &Rect{W: chWidth, H: lh}
		if err := r.gp.CellWithOption(rect, s, CellOption{Align: Left | Top}); err != nil {
			return err
		}
		r.cursorX += chWidth
	}
	return nil
}

func (r *htmlRenderer) measureLineWidth(words []string, spaceWidth float64, state htmlRenderState) float64 {
	total := 0.0
	for i, word := range words {
		ww, err := r.gp.MeasureTextWidth(word)
		if err != nil {
			break
		}
		if total+ww > r.boxW {
			break
		}
		total += ww
		if i < len(words)-1 {
			total += spaceWidth
		}
	}
	return total
}

func (r *htmlRenderer) renderHR(state htmlRenderState) error {
	if r.cursorX > r.boxX {
		r.newLine(state)
	}
	r.addVerticalSpace(state.fontSize * 0.3)

	y := r.cursorY + r.lineHeight(state)*0.5
	r.gp.SetStrokeColor(128, 128, 128)
	r.gp.SetLineWidth(0.5)
	r.gp.Line(r.boxX, y, r.boxX+r.boxW, y)

	r.cursorY = y + r.lineHeight(state)*0.5
	r.cursorX = r.boxX
	r.addVerticalSpace(state.fontSize * 0.3)
	return nil
}

func (r *htmlRenderer) renderImage(node *htmlNode, state htmlRenderState) error {
	src, ok := node.Attrs["src"]
	if !ok || src == "" {
		return nil
	}

	imgHolder, err := ImageHolderByPath(src)
	if err != nil {
		return err
	}

	intrinsicW, intrinsicH, err := imageHolderDimensions(imgHolder)
	if err != nil {
		return err
	}

	// parse dimensions
	imgW := 0.0
	imgH := 0.0
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

	aspectRatio := 1.0
	if intrinsicH > 0 {
		aspectRatio = intrinsicW / intrinsicH
	}

	if imgW > 0 && imgH > 0 {
		// use explicit dimensions as-is
	} else if imgW > 0 {
		imgH = imgW / aspectRatio
	} else if imgH > 0 {
		imgW = imgH * aspectRatio
	} else {
		imgW = intrinsicW / r.unitConversion()
		imgH = intrinsicH / r.unitConversion()
	}

	imgW, imgH = fitWithinBox(imgW, imgH, r.boxW, r.boxH)
	if imgW <= 0 || imgH <= 0 {
		return nil
	}

	// check if image fits on current line, if not, new line
	if r.cursorX > r.boxX && r.cursorX+imgW > r.boxX+r.boxW {
		r.newLine(state)
	}

	// check if image fits vertically
	if r.cursorY-r.boxY+imgH > r.boxH {
		return nil // skip image if it doesn't fit
	}

	rect := &Rect{W: imgW, H: imgH}
	if err := r.gp.ImageByHolder(imgHolder, r.cursorX, r.cursorY, rect); err != nil {
		return err
	}

	r.cursorY += imgH
	r.cursorX = r.boxX
	return nil
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

func (r *htmlRenderer) renderTable(node *htmlNode, state htmlRenderState) error {
	if r.cursorX > r.boxX {
		r.newLine(state)
	}
	rows := collectHTMLTableRows(node, state)
	if len(rows) == 0 {
		return nil
	}

	maxCols := 0
	for _, row := range rows {
		if len(row.cells) > maxCols {
			maxCols = len(row.cells)
		}
	}
	if maxCols == 0 {
		return nil
	}

	cellPadding := state.fontSize * 0.25 / r.unitConversion()
	if cellPadding < 1 {
		cellPadding = 1
	}
	colWidth := r.boxW / float64(maxCols)
	if colWidth <= 0 {
		return nil
	}

	if r.cursorY-r.boxY >= r.boxH {
		return nil
	}
	r.addVerticalSpace(state.fontSize * 0.2)

	for _, row := range rows {
		rowHeight, err := r.measureHTMLTableRowHeight(row, colWidth, cellPadding, state)
		if err != nil {
			return err
		}
		if rowHeight <= 0 {
			rowHeight = r.lineHeight(state) + cellPadding*2
		}
		if r.cursorY-r.boxY+rowHeight > r.boxH {
			break
		}

		x := r.boxX
		for col := 0; col < maxCols; col++ {
			cell := htmlTableCell{state: state}
			if col < len(row.cells) {
				cell = row.cells[col]
			}
			if err := r.drawHTMLTableCell(x, r.cursorY, colWidth, rowHeight, cell, cellPadding, state); err != nil {
				return err
			}
			x += colWidth
		}
		r.cursorY += rowHeight
		r.cursorX = r.boxX
	}

	r.addVerticalSpace(state.fontSize * 0.2)
	return nil
}

func collectHTMLTableRows(node *htmlNode, state htmlRenderState) []htmlTableRow {
	var rows []htmlTableRow
	for _, child := range node.Children {
		if child.Type != htmlNodeElement {
			continue
		}
		switch child.Tag {
		case "tr":
			if row, ok := buildHTMLTableRow(child, state); ok {
				rows = append(rows, row)
			}
		case "thead", "tbody", "tfoot":
			rows = append(rows, collectHTMLTableRows(child, state)...)
		}
	}
	return rows
}

func buildHTMLTableRow(node *htmlNode, state htmlRenderState) (htmlTableRow, bool) {
	row := htmlTableRow{}
	for _, child := range node.Children {
		if child.Type != htmlNodeElement {
			continue
		}
		if child.Tag != "th" && child.Tag != "td" {
			continue
		}
		cellState := state
		if child.Tag == "th" {
			cellState.fontStyle |= Bold
			cellState.align = Center
		}
		cellState = applyInlineStyleRecursive(child, cellState)
		row.cells = append(row.cells, htmlTableCell{
			text:   extractHTMLNodeText(child),
			state:  cellState,
			header: child.Tag == "th",
		})
	}
	return row, len(row.cells) > 0
}

func applyInlineStyleRecursive(node *htmlNode, state htmlRenderState) htmlRenderState {
	state = applyTagState(node, state)
	for _, child := range node.Children {
		if child.Type == htmlNodeElement {
			state = applyInlineStyleRecursive(child, state)
		}
	}
	return state
}

func applyTagState(node *htmlNode, state htmlRenderState) htmlRenderState {
	state = (&htmlRenderer{}).applyStyleAttr(node, state)
	switch node.Tag {
	case "b", "strong":
		state.fontStyle |= Bold
	case "i", "em":
		state.fontStyle |= Italic
	case "u", "ins":
		state.fontStyle |= Underline
	case "s", "strike", "del":
		state.fontStyle |= Underline
	case "font":
		if color, ok := node.Attrs["color"]; ok {
			if cr, cg, cb, cok := parseCSSColor(color); cok {
				state.colorR, state.colorG, state.colorB = cr, cg, cb
			}
		}
		if size, ok := node.Attrs["size"]; ok {
			if sz, sok := parseFontSizeAttr(size); sok {
				state.fontSize = sz
			}
		}
		if face, ok := node.Attrs["face"]; ok {
			state.fontFamily = face
		}
	case "center":
		state.align = Center
	}
	return state
}

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
		height, err := r.measureWrappedTextHeight(cell.text, cellState, contentWidth)
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
		return r.lineHeight(state), nil
	}
	if maxWidth <= 0 {
		return r.lineHeight(state), nil
	}

	words := splitWords(text)
	spaceWidth, err := r.gp.MeasureTextWidth(" ")
	if err != nil {
		return 0, err
	}
	lineCount := 1
	lineWidth := 0.0
	for _, word := range words {
		wordWidth, err := r.gp.MeasureTextWidth(word)
		if err != nil {
			return 0, err
		}
		if wordWidth > maxWidth {
			if lineWidth > 0 {
				lineCount++
				lineWidth = 0
			}
			charLines, err := r.measureLongWordLines(word, maxWidth)
			if err != nil {
				return 0, err
			}
			lineCount += charLines - 1
			continue
		}
		candidate := wordWidth
		if lineWidth > 0 {
			candidate += lineWidth + spaceWidth
		}
		if lineWidth > 0 && candidate > maxWidth {
			lineCount++
			lineWidth = wordWidth
		} else if lineWidth > 0 {
			lineWidth += spaceWidth + wordWidth
		} else {
			lineWidth = wordWidth
		}
	}
	return float64(lineCount) * r.lineHeight(state), nil
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

func (r *htmlRenderer) drawHTMLTableCell(x, y, width, height float64, cell htmlTableCell, padding float64, fallback htmlRenderState) error {
	cellState := cell.state
	if cellState.fontSize <= 0 {
		cellState = fallback
	}
	if cell.header {
		r.gp.SetFillColor(240, 240, 240)
		r.gp.RectFromUpperLeftWithStyle(x, y, width, height, "F")
	}
		r.gp.SetStrokeColor(0, 0, 0)
		r.gp.SetLineWidth(0.5)
		r.gp.RectFromUpperLeftWithStyle(x, y, width, height, "D")

	if err := r.applyFont(cellState); err != nil {
		return err
	}
	prevX, prevY := r.cursorX, r.cursorY
	prevBoxX, prevBoxY, prevBoxW, prevBoxH := r.boxX, r.boxY, r.boxW, r.boxH
	r.boxX = x + padding
	r.boxY = y + padding
	r.boxW = width - padding*2
	r.boxH = height - padding*2
	r.cursorX = r.boxX
	r.cursorY = r.boxY
	err := r.renderText(cell.text, cellState)
	r.cursorX, r.cursorY = prevX, prevY
	r.boxX, r.boxY, r.boxW, r.boxH = prevBoxX, prevBoxY, prevBoxW, prevBoxH
	return err
}

func (r *htmlRenderer) renderList(node *htmlNode, state htmlRenderState, ordered bool) error {
	if r.cursorX > r.boxX {
		r.newLine(state)
	}
	r.addVerticalSpace(state.fontSize * 0.2)

	indent := state.fontSize * 1.2 / r.unitConversion()
	counter := 0

	for _, child := range node.Children {
		if child.Type != htmlNodeElement || child.Tag != "li" {
			continue
		}
		counter++

		if r.cursorY-r.boxY+r.lineHeight(state) > r.boxH {
			break
		}

		// render bullet or number
		if err := r.applyFont(state); err != nil {
			return err
		}

		r.cursorX = r.boxX + indent*0.5
		r.gp.SetXY(r.cursorX, r.cursorY)

		var marker string
		if ordered {
			marker = string(rune('0'+counter)) + ". "
			if counter > 9 {
				marker = strconv.Itoa(counter) + ". "
			}
		} else {
			marker = "• "
		}

		markerWidth, _ := r.gp.MeasureTextWidth(marker)
		rect := &Rect{W: markerWidth, H: r.lineHeight(state)}
		if err := r.gp.CellWithOption(rect, marker, CellOption{Align: Left | Top}); err != nil {
			return err
		}

		// render list item content with indent
		oldBoxX := r.boxX
		oldBoxW := r.boxW
		r.boxX += indent
		r.boxW -= indent
		r.cursorX = r.boxX

		childState := r.applyStyleAttr(child, state)
		if err := r.renderNodes(child.Children, childState); err != nil {
			return err
		}

		if r.cursorX > r.boxX {
			r.newLine(state)
		}

		r.boxX = oldBoxX
		r.boxW = oldBoxW
		r.cursorX = r.boxX
	}

	r.addVerticalSpace(state.fontSize * 0.2)
	return nil
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
