package gopdf

import (
	"fmt"
	"strings"
)

// ============================================================
// Form field extraction — PyMuPDF parity: page.widgets(). Walks the
// /AcroForm /Fields tree (including /Kids branches and merged
// field/widget dicts), resolving page numbers via the widget's /P.
// Falls back to per-page /Annots when the document has no /AcroForm.
// ============================================================

// ExtractedFormField is one form field read from an existing PDF.
type ExtractedFormField struct {
	// Name is the fully qualified field name ("parent.child" joined with
	// dots when the field has /Parent ancestors).
	Name string
	// Type is the field type: "text", "button", "choice", "signature".
	Type string
	// Value is the current value: text content for text fields, the
	// state name ("Yes", "Off", ...) for checkboxes, the selected option
	// for dropdowns.
	Value string
	// X, Y, W, H locate the widget rectangle in top-down page points.
	X, Y, W, H float64
	// Page is the 1-based page number (0 when the page is not resolvable).
	Page int
}

// ExtractFormFieldsFromPage reads the form fields of one page (0-based).
//
// Example:
//
//	fields, err := gopdf.ExtractFormFieldsFromPage(data, 0)
//	for _, f := range fields {
//	    fmt.Println(f.Name, f.Type, f.Value)
//	}
func ExtractFormFieldsFromPage(pdfData []byte, pageIndex int) ([]ExtractedFormField, error) {
	all, err := ExtractAllFormFields(pdfData)
	if err != nil {
		return nil, err
	}
	return all[pageIndex], nil
}

// ExtractAllFormFields reads the form fields of every page, keyed by
// 0-based page index. Fields whose page cannot be resolved are keyed
// under page 0.
func ExtractAllFormFields(pdfData []byte) (map[int][]ExtractedFormField, error) {
	parser, err := newRawPDFParser(pdfData)
	if err != nil {
		return nil, err
	}
	return extractAllFormFieldsWithParser(parser), nil
}

// ExtractFormFieldsFromFile reads the form fields of a page from a file.
func ExtractFormFieldsFromFile(path string, pageIndex int) ([]ExtractedFormField, error) {
	data, err := readFileBytes(path)
	if err != nil {
		return nil, err
	}
	return ExtractFormFieldsFromPage(data, pageIndex)
}

// extractAllFormFieldsWithParser walks the AcroForm field tree of a parsed PDF.
func extractAllFormFieldsWithParser(parser *rawPDFParser) map[int][]ExtractedFormField {
	out := make(map[int][]ExtractedFormField)
	// page object number → 0-based index, for resolving widget /P refs
	pageIdx := make(map[int]int, len(parser.pages))
	for i := range parser.pages {
		pageIdx[parser.pages[i].objNum] = i
	}

	if parser.root > 0 {
		if rootObj, ok := parser.objects[parser.root]; ok {
			if acroRef := extractRef(rootObj.dict, "/AcroForm"); acroRef > 0 {
				if acroObj, ok := parser.objects[acroRef]; ok {
					visited := make(map[int]bool)
					for _, ref := range extractRefArray(acroObj.dict, "/Fields") {
						walkFormField(parser, ref, "", "", "", pageIdx, visited, out)
					}
					if len(out) > 0 {
						return out
					}
				}
			}
		}
	}

	// No /AcroForm (or it was empty): fall back to per-page /Annots.
	for i := range parser.pages {
		for _, f := range extractPageWidgets(parser, i) {
			out[i] = append(out[i], f)
		}
	}
	return out
}

// walkFormField recursively processes one /Fields entry. parentName and
// inherited carry the dotted prefix and the inherited /FT, /V values.
func walkFormField(parser *rawPDFParser, ref int, parentName, inheritedType, inheritedValue string, pageIdx map[int]int, visited map[int]bool, out map[int][]ExtractedFormField) {
	if ref <= 0 || visited[ref] {
		return
	}
	visited[ref] = true
	obj, ok := parser.objects[ref]
	if !ok {
		return
	}
	dict := obj.dict

	name := extractStringFromDict(dict, "/T")
	full := name
	if parentName != "" {
		if name != "" {
			full = parentName + "." + name
		} else {
			full = parentName
		}
	}
	ft := extractName(dict, "/FT")
	if ft == "" {
		ft = inheritedType
	}
	val := fieldStringValue(dict, "/V")
	if val == "" {
		val = inheritedValue
	}

	// Rect (may be on this field or on kid widgets).
	rect, hasRect := extractBox(dict, "/Rect")
	page := 0
	if pRef := extractRef(dict, "/P"); pRef > 0 {
		if idx, ok := pageIdx[pRef]; ok {
			page = idx + 1
		}
	}

	kidRefs := extractRefArray(dict, "/Kids")
	if len(kidRefs) == 0 {
		// terminal field (or merged field/widget dict)
		if hasRect {
			pageH := 842.0
			if idx := page - 1; idx >= 0 && idx < len(parser.pages) {
				pageH = parser.pages[idx].mediaBox[3] - parser.pages[idx].mediaBox[1]
			}
			f := ExtractedFormField{
				Name:  full,
				Type:  fieldTypeDescription(ft),
				Value: val,
				Page:  page,
				X:     rect[0], W: rect[2] - rect[0],
				H: rect[3] - rect[1], Y: pageH - rect[3],
			}
			if page == 0 {
				out[0] = append(out[0], f)
			} else {
				out[page-1] = append(out[page-1], f)
			}
		} else if full != "" && ft != "" {
			// field without a widget rectangle (hidden / meta field)
			out[0] = append(out[0], ExtractedFormField{
				Name: full, Type: fieldTypeDescription(ft), Value: val,
			})
		}
		return
	}
	for _, kid := range kidRefs {
		walkFormField(parser, kid, full, ft, val, pageIdx, visited, out)
	}
}

// extractPageWidgets reads one page's widgets from /Annots (fallback path).
func extractPageWidgets(parser *rawPDFParser, pageIndex int) []ExtractedFormField {
	if pageIndex < 0 || pageIndex >= len(parser.pages) {
		return nil
	}
	page := parser.pages[pageIndex]
	pageObj, ok := parser.objects[page.objNum]
	if !ok {
		return nil
	}
	pageH := page.mediaBox[3] - page.mediaBox[1]

	annotRefs := extractRefArray(pageObj.dict, "/Annots")
	if len(annotRefs) == 0 {
		if r := extractRef(pageObj.dict, "/Annots"); r > 0 {
			annotRefs = []int{r}
		}
	}

	var out []ExtractedFormField
	visited := make(map[int]bool, len(annotRefs))
	for _, ref := range annotRefs {
		if visited[ref] {
			continue
		}
		visited[ref] = true
		obj, ok := parser.objects[ref]
		if !ok {
			continue
		}
		if extractName(obj.dict, "/Subtype") != "Widget" {
			continue
		}
		f := ExtractedFormField{Page: pageIndex + 1}

		// Walk the /Parent chain to collect name parts, type and value.
		var nameParts []string
		ft, val := "", ""
		dict := obj.dict
		chainVisited := make(map[int]bool)
		for {
			if t := extractStringFromDict(dict, "/T"); t != "" {
				nameParts = append([]string{t}, nameParts...)
			}
			if ft == "" {
				ft = extractName(dict, "/FT")
			}
			if val == "" {
				val = fieldStringValue(dict, "/V")
			}
			parentRef := extractRef(dict, "/Parent")
			if parentRef <= 0 || chainVisited[parentRef] {
				break
			}
			chainVisited[parentRef] = true
			pobj, ok := parser.objects[parentRef]
			if !ok {
				break
			}
			dict = pobj.dict
		}
		f.Name = strings.Join(nameParts, ".")
		f.Type = fieldTypeDescription(ft)
		f.Value = val

		if r := extractNumberArray(obj.dict, "/Rect"); len(r) >= 4 {
			x0 := mathMinF(r[0], r[2])
			x1 := mathMaxF(r[0], r[2])
			y0 := mathMinF(r[1], r[3])
			y1 := mathMaxF(r[1], r[3])
			f.X, f.Y = x0, pageH-y1
			f.W, f.H = x1-x0, y1-y0
		}
		out = append(out, f)
	}
	return out
}

// fieldStringValue reads /V which may be a literal string, a hex string,
// or a name (/Yes, /Off, ...).
func fieldStringValue(dict, key string) string {
	if s := extractStringFromDict(dict, key); s != "" {
		return decodeInfoText(s)
	}
	if n := extractName(dict, key); n != "" {
		return n
	}
	return ""
}

// fieldTypeDescription maps the /FT code to a readable description.
func fieldTypeDescription(ft string) string {
	switch ft {
	case "Tx":
		return "text"
	case "Btn":
		return "button"
	case "Ch":
		return "choice"
	case "Sig":
		return "signature"
	case "":
		return ""
	default:
		return fmt.Sprintf("unknown(%s)", ft)
	}
}
