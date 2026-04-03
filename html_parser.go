package gopdf

import (
	"fmt"
	"strconv"
	"strings"
)

// htmlNodeType represents the type of an HTML node.
type htmlNodeType int

const (
	htmlNodeText    htmlNodeType = iota // plain text
	htmlNodeElement                     // an HTML element like <b>, <p>, etc.
)

// htmlNode represents a parsed HTML node (either text or element).
type htmlNode struct {
	Type       htmlNodeType
	Tag        string // lowercase tag name, e.g. "b", "p", "br", "img"
	Text       string // text content (for text nodes)
	Attrs      map[string]string
	Children   []*htmlNode
	SelfClose  bool // e.g. <br/>, <img ... />
}

// parseHTML parses a simplified HTML string into a tree of htmlNode.
// Supported tags: b, i, u, s, br, p, span, font, img, h1-h6, ul, ol, li, a, hr, sub, sup, center
func parseHTML(html string) []*htmlNode {
	p := &htmlParser{input: html}
	return p.parseNodes()
}

type htmlParser struct {
	input string
	pos   int
}

func (p *htmlParser) parseNodes() []*htmlNode {
	var nodes []*htmlNode
	for p.pos < len(p.input) {
		if p.input[p.pos] == '<' {
			// check for closing tag
			if p.pos+1 < len(p.input) && p.input[p.pos+1] == '/' {
				// closing tag — return to parent
				return nodes
			}
			// check for comment <!-- ... -->
			if p.pos+3 < len(p.input) && p.input[p.pos:p.pos+4] == "<!--" {
				p.skipComment()
				continue
			}
			node := p.parseElement()
			if node != nil {
				nodes = append(nodes, node)
			}
		} else {
			node := p.parseText()
			if node != nil {
				nodes = append(nodes, node)
			}
		}
	}
	return nodes
}

func (p *htmlParser) skipComment() {
	p.pos += 4 // skip "<!--"
	for p.pos < len(p.input)-2 {
		if p.input[p.pos:p.pos+3] == "-->" {
			p.pos += 3
			return
		}
		p.pos++
	}
	p.pos = len(p.input)
}

func (p *htmlParser) parseText() *htmlNode {
	start := p.pos
	for p.pos < len(p.input) && p.input[p.pos] != '<' {
		p.pos++
	}
	text := p.input[start:p.pos]
	text = decodeHTMLEntities(text)
	if len(text) == 0 {
		return nil
	}
	return &htmlNode{Type: htmlNodeText, Text: text}
}

func (p *htmlParser) parseElement() *htmlNode {
	if p.pos >= len(p.input) || p.input[p.pos] != '<' {
		return nil
	}
	p.pos++ // skip '<'
	p.skipWhitespace()

	tag := p.readTagName()
	if tag == "" {
		return nil
	}

	attrs := p.parseAttributes()
	p.skipWhitespace()

	selfClose := false
	if p.pos < len(p.input) && p.input[p.pos] == '/' {
		selfClose = true
		p.pos++
	}
	if p.pos < len(p.input) && p.input[p.pos] == '>' {
		p.pos++
	}

	node := &htmlNode{
		Type:      htmlNodeElement,
		Tag:       strings.ToLower(tag),
		Attrs:     attrs,
		SelfClose: selfClose,
	}

	// self-closing or void elements
	if selfClose || isVoidElement(node.Tag) {
		return node
	}

	// parse children until closing tag
	node.Children = p.parseNodes()

	// consume closing tag </tag>
	p.consumeClosingTag(node.Tag)

	return node
}

func (p *htmlParser) readTagName() string {
	start := p.pos
	for p.pos < len(p.input) {
		ch := p.input[p.pos]
		if ch == ' ' || ch == '>' || ch == '/' || ch == '\t' || ch == '\n' || ch == '\r' {
			break
		}
		p.pos++
	}
	return p.input[start:p.pos]
}

func (p *htmlParser) parseAttributes() map[string]string {
	attrs := make(map[string]string)
	for {
		p.skipWhitespace()
		if p.pos >= len(p.input) {
			break
		}
		ch := p.input[p.pos]
		if ch == '>' || ch == '/' {
			break
		}
		key := p.readAttrName()
		if key == "" {
			p.pos++
			continue
		}
		p.skipWhitespace()
		value := ""
		if p.pos < len(p.input) && p.input[p.pos] == '=' {
			p.pos++ // skip '='
			p.skipWhitespace()
			value = p.readAttrValue()
		}
		attrs[strings.ToLower(key)] = value
	}
	return attrs
}

func (p *htmlParser) readAttrName() string {
	start := p.pos
	for p.pos < len(p.input) {
		ch := p.input[p.pos]
		if ch == '=' || ch == ' ' || ch == '>' || ch == '/' || ch == '\t' || ch == '\n' {
			break
		}
		p.pos++
	}
	return p.input[start:p.pos]
}

func (p *htmlParser) readAttrValue() string {
	if p.pos >= len(p.input) {
		return ""
	}
	ch := p.input[p.pos]
	if ch == '"' || ch == '\'' {
		quote := ch
		p.pos++
		start := p.pos
		for p.pos < len(p.input) && p.input[p.pos] != quote {
			p.pos++
		}
		val := p.input[start:p.pos]
		if p.pos < len(p.input) {
			p.pos++ // skip closing quote
		}
		return decodeHTMLEntities(val)
	}
	// unquoted value
	start := p.pos
	for p.pos < len(p.input) {
		ch := p.input[p.pos]
		if ch == ' ' || ch == '>' || ch == '/' || ch == '\t' || ch == '\n' {
			break
		}
		p.pos++
	}
	return decodeHTMLEntities(p.input[start:p.pos])
}

func (p *htmlParser) consumeClosingTag(tag string) {
	// look for </tag>
	if p.pos+1 < len(p.input) && p.input[p.pos] == '<' && p.input[p.pos+1] == '/' {
		p.pos += 2
		p.skipWhitespace()
		p.readTagName() // consume tag name
		p.skipWhitespace()
		if p.pos < len(p.input) && p.input[p.pos] == '>' {
			p.pos++
		}
	}
}

func (p *htmlParser) skipWhitespace() {
	for p.pos < len(p.input) {
		ch := p.input[p.pos]
		if ch != ' ' && ch != '\t' && ch != '\n' && ch != '\r' {
			break
		}
		p.pos++
	}
}

func isVoidElement(tag string) bool {
	switch tag {
	case "br", "hr", "img":
		return true
	}
	return false
}

func decodeHTMLEntities(s string) string {
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&quot;", "\"")
	s = strings.ReplaceAll(s, "&apos;", "'")
	s = strings.ReplaceAll(s, "&nbsp;", " ")
	return s
}

// parseInlineStyle parses a CSS-like inline style string into a map.
// e.g. "color: red; font-size: 14px" => {"color":"red", "font-size":"14px"}
func parseInlineStyle(style string) map[string]string {
	result := make(map[string]string)
	parts := strings.Split(style, ";")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		idx := strings.Index(part, ":")
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(part[:idx])
		val := strings.TrimSpace(part[idx+1:])
		result[strings.ToLower(key)] = val
	}
	return result
}

// parseCSSColor parses a color string and returns r, g, b values.
// Supports: #RGB, #RRGGBB, rgb(r,g,b), and named colors.
func parseCSSColor(color string) (uint8, uint8, uint8, bool) {
	color = strings.TrimSpace(strings.ToLower(color))

	// named colors
	if rgb, ok := cssNamedColors[color]; ok {
		return rgb[0], rgb[1], rgb[2], true
	}

	// hex colors
	if strings.HasPrefix(color, "#") {
		hex := color[1:]
		if len(hex) == 3 {
			hex = string([]byte{hex[0], hex[0], hex[1], hex[1], hex[2], hex[2]})
		}
		if len(hex) == 6 {
			r, err1 := strconv.ParseUint(hex[0:2], 16, 8)
			g, err2 := strconv.ParseUint(hex[2:4], 16, 8)
			b, err3 := strconv.ParseUint(hex[4:6], 16, 8)
			if err1 == nil && err2 == nil && err3 == nil {
				return uint8(r), uint8(g), uint8(b), true
			}
		}
		return 0, 0, 0, false
	}

	// rgb(r, g, b)
	if strings.HasPrefix(color, "rgb(") && strings.HasSuffix(color, ")") {
		inner := color[4 : len(color)-1]
		parts := strings.Split(inner, ",")
		if len(parts) == 3 {
			r, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
			g, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
			b, err3 := strconv.Atoi(strings.TrimSpace(parts[2]))
			if err1 == nil && err2 == nil && err3 == nil {
				return uint8(r), uint8(g), uint8(b), true
			}
		}
	}

	return 0, 0, 0, false
}

// parseFontSize parses a CSS font-size value and returns the size in points.
// Supports: "12pt", "16px", "1.5em", plain numbers, and named sizes.
func parseFontSize(val string, currentSize float64) (float64, bool) {
	val = strings.TrimSpace(strings.ToLower(val))

	// named sizes
	switch val {
	case "xx-small":
		return 6, true
	case "x-small":
		return 7.5, true
	case "small":
		return 10, true
	case "medium":
		return 12, true
	case "large":
		return 14, true
	case "x-large":
		return 18, true
	case "xx-large":
		return 24, true
	}

	if strings.HasSuffix(val, "pt") {
		f, err := strconv.ParseFloat(strings.TrimSuffix(val, "pt"), 64)
		if err == nil {
			return f, true
		}
	}
	if strings.HasSuffix(val, "px") {
		f, err := strconv.ParseFloat(strings.TrimSuffix(val, "px"), 64)
		if err == nil {
			return f * 0.75, true // px to pt
		}
	}
	if strings.HasSuffix(val, "em") {
		f, err := strconv.ParseFloat(strings.TrimSuffix(val, "em"), 64)
		if err == nil {
			return currentSize * f, true
		}
	}
	if strings.HasSuffix(val, "%") {
		f, err := strconv.ParseFloat(strings.TrimSuffix(val, "%"), 64)
		if err == nil {
			return currentSize * f / 100.0, true
		}
	}

	// plain number (treat as pt)
	f, err := strconv.ParseFloat(val, 64)
	if err == nil {
		return f, true
	}

	return 0, false
}

// parseDimension parses a CSS dimension value (e.g. "100px", "50%", "3em") and returns points.
func parseDimension(val string, relativeTo float64) (float64, bool) {
	val = strings.TrimSpace(strings.ToLower(val))
	if strings.HasSuffix(val, "px") {
		f, err := strconv.ParseFloat(strings.TrimSuffix(val, "px"), 64)
		if err == nil {
			return f * 0.75, true
		}
	}
	if strings.HasSuffix(val, "pt") {
		f, err := strconv.ParseFloat(strings.TrimSuffix(val, "pt"), 64)
		if err == nil {
			return f, true
		}
	}
	if strings.HasSuffix(val, "%") {
		f, err := strconv.ParseFloat(strings.TrimSuffix(val, "%"), 64)
		if err == nil {
			return relativeTo * f / 100.0, true
		}
	}
	if strings.HasSuffix(val, "em") {
		f, err := strconv.ParseFloat(strings.TrimSuffix(val, "em"), 64)
		if err == nil {
			return f * 12, true // assume 12pt base
		}
	}
	// plain number
	f, err := strconv.ParseFloat(val, 64)
	if err == nil {
		return f, true
	}
	return 0, false
}

// parseFontSizeAttr parses the HTML <font size="..."> attribute (1-7 scale).
func parseFontSizeAttr(val string) (float64, bool) {
	val = strings.TrimSpace(val)
	n, err := strconv.Atoi(val)
	if err != nil {
		return 0, false
	}
	// HTML font size 1-7 mapping to pt
	sizes := map[int]float64{
		1: 8, 2: 10, 3: 12, 4: 14, 5: 18, 6: 24, 7: 36,
	}
	if sz, ok := sizes[n]; ok {
		return sz, true
	}
	return 12, true
}

// headingFontSize returns the font size for h1-h6 tags.
func headingFontSize(tag string) float64 {
	switch tag {
	case "h1":
		return 24
	case "h2":
		return 20
	case "h3":
		return 16
	case "h4":
		return 14
	case "h5":
		return 12
	case "h6":
		return 10
	}
	return 12
}

// isBlockElement returns true if the tag is a block-level element.
func isBlockElement(tag string) bool {
	switch tag {
	case "p", "div", "h1", "h2", "h3", "h4", "h5", "h6", "ul", "ol", "li", "hr", "center", "blockquote", "table", "thead", "tbody", "tfoot", "tr":
		return true
	}
	return false
}

var cssNamedColors = map[string][3]uint8{
	"black":   {0, 0, 0},
	"white":   {255, 255, 255},
	"red":     {255, 0, 0},
	"green":   {0, 128, 0},
	"blue":    {0, 0, 255},
	"yellow":  {255, 255, 0},
	"cyan":    {0, 255, 255},
	"magenta": {255, 0, 255},
	"gray":    {128, 128, 128},
	"grey":    {128, 128, 128},
	"orange":  {255, 165, 0},
	"purple":  {128, 0, 128},
	"brown":   {165, 42, 42},
	"pink":    {255, 192, 203},
	"navy":    {0, 0, 128},
	"teal":    {0, 128, 128},
	"olive":   {128, 128, 0},
	"maroon":  {128, 0, 0},
	"silver":  {192, 192, 192},
	"lime":    {0, 255, 0},
	"aqua":    {0, 255, 255},
	"fuchsia": {255, 0, 255},
}

// htmlFontSizeToFloat converts an HTML <font size="N"> attribute to a float64 font size.
func htmlFontSizeToFloat(sizeStr string) float64 {
	sz, ok := parseFontSizeAttr(sizeStr)
	if !ok {
		return 12
	}
	return sz
}

// debugHTMLTree prints the HTML tree for debugging (not used in production).
func debugHTMLTree(nodes []*htmlNode, indent int) string {
	var sb strings.Builder
	prefix := strings.Repeat("  ", indent)
	for _, n := range nodes {
		if n.Type == htmlNodeText {
			sb.WriteString(fmt.Sprintf("%sTEXT: %q\n", prefix, n.Text))
		} else {
			sb.WriteString(fmt.Sprintf("%s<%s", prefix, n.Tag))
			for k, v := range n.Attrs {
				sb.WriteString(fmt.Sprintf(" %s=%q", k, v))
			}
			if n.SelfClose {
				sb.WriteString(" />\n")
			} else {
				sb.WriteString(">\n")
				sb.WriteString(debugHTMLTree(n.Children, indent+1))
				sb.WriteString(fmt.Sprintf("%s</%s>\n", prefix, n.Tag))
			}
		}
	}
	return sb.String()
}
