package gopdf

// Base-14 font fallback: PDFs frequently reference the standard base-14
// fonts (Helvetica, Times, Courier, ...) WITHOUT embedding any font data.
// This module resolves those names to system-installed TrueType fonts
// (Arial/Times New Roman/Courier New on Windows, DejaVu/Liberation on
// Linux, Helvetica-family on macOS) and falls back to CJK system fonts
// for common Chinese/Japanese family names. When no system font resolves,
// the text is skipped — the same behavior as before this fallback existed.

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// base14Candidates maps a base-14/known family (lowercased, style suffix
// stripped, bold/italic flags) to candidate font file names per platform.
type fontStyle struct {
	bold   bool
	italic bool
}

var base14FamilyFiles = map[string][4]string{
	// [regular, bold, italic, boldItalic] — Windows first, then common
	// Linux font names; the search also tries the family name itself.
	"helvetica": {"arial.ttf", "arialbd.ttf", "ariali.ttf", "arialbi.ttf"},
	"arial":     {"arial.ttf", "arialbd.ttf", "ariali.ttf", "arialbi.ttf"},
	"liberation sans": {"LiberationSans-Regular.ttf", "LiberationSans-Bold.ttf", "LiberationSans-Italic.ttf", "LiberationSans-BoldItalic.ttf"},
	"dejavusans":      {"DejaVuSans.ttf", "DejaVuSans-Bold.ttf", "DejaVuSans-Oblique.ttf", "DejaVuSans-BoldOblique.ttf"},
	"times":           {"times.ttf", "timesbd.ttf", "timesi.ttf", "timesbi.ttf"},
	"times new roman": {"times.ttf", "timesbd.ttf", "timesi.ttf", "timesbi.ttf"},
	"liberation serif": {"LiberationSerif-Regular.ttf", "LiberationSerif-Bold.ttf", "LiberationSerif-Italic.ttf", "LiberationSerif-BoldItalic.ttf"},
	"dejavuserif":      {"DejaVuSerif.ttf", "DejaVuSerif-Bold.ttf", "DejaVuSerif-Italic.ttf", "DejaVuSerif-BoldItalic.ttf"},
	"courier":          {"cour.ttf", "courbd.ttf", "couri.ttf", "courbi.ttf"},
	"courier new":      {"cour.ttf", "courbd.ttf", "couri.ttf", "courbi.ttf"},
	"liberation mono":  {"LiberationMono-Regular.ttf", "LiberationMono-Bold.ttf", "LiberationMono-Italic.ttf", "LiberationMono-BoldItalic.ttf"},
	"dejavusansmono":   {"DejaVuSansMono.ttf", "DejaVuSansMono-Bold.ttf", "DejaVuSansMono-Oblique.ttf", "DejaVuSansMono-BoldOblique.ttf"},
	// CJK system fonts (Windows SimSun family + common Linux fallbacks)
	"黑体":      {"simhei.ttf"},
	"simhei":   {"simhei.ttf"},
	"宋体":       {"simsun.ttc"},
	"simsun":   {"simsun.ttc"},
	"仿宋":       {"simfang.ttf"},
	"仿宋_gb2312": {"simfang.ttf"},
	"simfang":  {"simfang.ttf"},
	"楷体":       {"simkai.ttf"},
	"simkai":   {"simkai.ttf"},
	"heiseimin":           {"msmincho.ttc"},
	"heiseikakugo":        {"msgothic.ttc"},
	"msgothic":            {"msgothic.ttc"},
	"msmincho":            {"msmincho.ttc"},
}

// stripFontStyle splits a BaseFont name into (familyLower, style): subset
// prefixes ("ABCDEF+Name") and style suffixes ("-Bold", "-Light", ...).
func stripFontStyle(base string) (string, fontStyle) {
	if i := strings.IndexByte(base, '+'); i >= 0 {
		base = base[i+1:]
	}
	var st fontStyle
	lower := strings.ToLower(base)
	for _, sfx := range []string{"boldoblique", "bolditalic", "oblique", "italic", "bold", "light", "regular", "medium"} {
		if strings.HasSuffix(lower, "-"+sfx) || strings.HasSuffix(lower, sfx) && len(lower) > len(sfx) && lower[len(lower)-len(sfx)-1] == '-' {
			lower = lower[:len(lower)-len(sfx)-1]
			switch sfx {
			case "bold", "boldoblique", "bolditalic":
				st.bold = true
			}
			switch sfx {
			case "oblique", "italic", "boldoblique", "bolditalic":
				st.italic = true
			}
			break
		}
	}
	return strings.TrimSuffix(lower, "-"), st
}

var (
	sysFontOnce  sync.Once
	sysFontDirs  []string
	sysFontCache sync.Map // familyLower+style -> resolved path ("" = none)
)

// systemFontDirs returns the standard per-OS font directories.
func systemFontDirs() []string {
	sysFontOnce.Do(func() {
		var dirs []string
		if windir := os.Getenv("WINDIR"); windir != "" {
			dirs = append(dirs, filepath.Join(windir, "Fonts"))
		} else if _, err := os.Stat(`C:\Windows\Fonts`); err == nil {
			dirs = append(dirs, `C:\Windows\Fonts`)
		}
		dirs = append(dirs,
			"/usr/share/fonts",
			"/usr/local/share/fonts",
			"/System/Library/Fonts",
			"/Library/Fonts",
		)
		if home, err := os.UserHomeDir(); err == nil {
			dirs = append(dirs, filepath.Join(home, "Library", "Fonts"),
				filepath.Join(home, ".fonts"),
				filepath.Join(home, ".local", "share", "fonts"))
		}
		sysFontDirs = dirs
	})
	return sysFontDirs
}

// findSystemFontFile locates a font file for the given family/style by
// walking the system font directories once and matching candidate names.
func findSystemFontFile(family string, st fontStyle) string {
	key := family
	if st.bold {
		key += "\x00b"
	}
	if st.italic {
		key += "\x00i"
	}
	if v, ok := sysFontCache.Load(key); ok {
		return v.(string)
	}
	path := ""

	// index the font directories once per family lookup: name -> path
	index := map[string]string{}
	for _, dir := range systemFontDirs() {
		_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			lower := strings.ToLower(info.Name())
			if !strings.HasSuffix(lower, ".ttf") && !strings.HasSuffix(lower, ".ttc") && !strings.HasSuffix(lower, ".otf") {
				return nil
			}
			if _, exists := index[lower]; !exists {
				index[lower] = p
			}
			return nil
		})
	}

	if cands, ok := base14FamilyFiles[family]; ok {
		// pick by style: regular/bold/italic/boldItalic with graceful
		// degradation to whatever variant exists
		var order []string
		switch {
		case st.bold && st.italic:
			order = []string{cands[3], cands[1], cands[2], cands[0]}
		case st.bold:
			order = []string{cands[1], cands[3], cands[0], cands[2]}
		case st.italic:
			order = []string{cands[2], cands[3], cands[0], cands[1]}
		default:
			order = []string{cands[0], cands[1], cands[2], cands[3]}
		}
		for _, cand := range order {
			if cand == "" {
				continue
			}
			if p, ok := index[strings.ToLower(cand)]; ok {
				path = p
				break
			}
		}
		// family-named files on Linux (e.g. "LiberationSans-Regular.ttf"
		// under a versioned directory) — try the family name as prefix
		if path == "" {
			for name, p := range index {
				if strings.HasPrefix(name, familyNormalizedPrefix(family)) {
					path = p
					break
				}
			}
		}
	} else {
		// unknown family: try "<family>.ttf/ttc" and family-prefixed names
		for _, suffix := range []string{".ttf", ".ttc", ".otf"} {
			if p, ok := index[strings.ToLower(family)+suffix]; ok {
				path = p
				break
			}
		}
		if path == "" {
			prefix := familyNormalizedPrefix(family)
			for name, p := range index {
				if strings.HasPrefix(name, prefix) {
					path = p
					break
				}
			}
		}
	}

	sysFontCache.Store(key, path)
	return path
}

// familyNormalizedPrefix converts spaces to hyphens for Linux font file
// naming (e.g. "liberation sans" → "liberationsans").
func familyNormalizedPrefix(family string) string {
	return strings.ReplaceAll(strings.ReplaceAll(family, " ", ""), "-", "")
}

// resolveSystemTTF parses a system font for a non-embedded PDF font.
// Returns nil when no system font matches.
func resolveSystemTTF(baseFont string) *minTTF {
	family, st := stripFontStyle(baseFont)
	if family == "" || family == "symbol" || family == "zapfdingbats" {
		// symbol encodings have no usable Unicode system mapping
		return nil
	}
	// base-14 names map onto their metric-compatible families
	switch family {
	case "helvetica":
		family = "arial"
	case "times", "timesroman":
		family = "times new roman"
	case "courier":
		family = "courier new"
	}
	path := findSystemFontFile(family, st)
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	m, err := parseMinTTF(data)
	if err != nil {
		return nil
	}
	return m
}
