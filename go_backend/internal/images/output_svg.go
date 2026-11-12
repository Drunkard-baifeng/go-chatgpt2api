package images

import (
	"bytes"
	"encoding/xml"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html/charset"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

const maxSVGBytes = 4 << 20
const svgNamespace = "http://www.w3.org/2000/svg"

var svgElements = func() map[string]bool {
	elements := strings.Fields("svg g defs path rect circle ellipse line polyline polygon text tspan title desc metadata style clipPath mask pattern linearGradient radialGradient stop use symbol marker filter feBlend feColorMatrix feComponentTransfer feComposite feConvolveMatrix feDiffuseLighting feDisplacementMap feDistantLight feDropShadow feFlood feFuncA feFuncB feFuncG feFuncR feGaussianBlur feMerge feMergeNode feMorphology feOffset fePointLight feSpecularLighting feSpotLight feTile feTurbulence")
	result := make(map[string]bool, len(elements))
	for _, name := range elements {
		result[name] = true
	}
	return result
}()
var svgLocalURL = regexp.MustCompile(`(?i)url\s*\(\s*['"]?#[-a-z0-9_.:]+['"]?\s*\)`)

// ExtractSVGDocumentText follows Python's inline-SVG contract: an explanation
// or sandbox link is insufficient, and unsafe candidates are skipped.
func ExtractSVGDocumentText(text string) []byte {
	lower := strings.ToLower(text)
	offset := 0
	for offset < len(lower) {
		relative := strings.Index(lower[offset:], "<svg")
		if relative < 0 {
			return nil
		}
		start := offset + relative
		end := strings.Index(lower[start:], "</svg>")
		if end < 0 {
			return nil
		}
		candidate := []byte(strings.TrimSpace(text[start : start+end+len("</svg>")]))
		if _, _, valid := svgDimensions(candidate); valid {
			return candidate
		}
		offset = start + len("<svg")
	}
	return nil
}

func safeSVGStyle(value string) bool {
	if strings.ContainsAny(value, "\\@") {
		return false
	}
	value = svgLocalURL.ReplaceAllString(strings.ToLower(value), "")
	for _, token := range []string{"url", "expression(", "javascript:", "data:", "http:", "https:", "file:", "blob:"} {
		if strings.Contains(value, token) {
			return false
		}
	}
	return true
}

func svgLength(value string) float64 {
	value = strings.ToLower(strings.TrimSpace(value))
	for _, suffix := range []string{"px", "pt", "mm", "cm"} {
		if strings.HasSuffix(value, suffix) {
			value = strings.TrimSpace(strings.TrimSuffix(value, suffix))
			break
		}
	}
	n, _ := strconv.ParseFloat(value, 64)
	return n
}

func svgRootDimensions(attrs []xml.Attr) (float64, float64) {
	width, height := float64(0), float64(0)
	var viewbox string
	for _, attr := range attrs {
		if attr.Name.Space != "" {
			continue
		}
		switch strings.ToLower(attr.Name.Local) {
		case "width":
			width = svgLength(attr.Value)
		case "height":
			height = svgLength(attr.Value)
		case "viewbox":
			viewbox = attr.Value
		}
	}
	values := strings.Fields(strings.ReplaceAll(viewbox, ",", " "))
	if len(values) == 4 {
		var parsed [4]float64
		for i, value := range values {
			var err error
			parsed[i], err = strconv.ParseFloat(value, 64)
			if err != nil {
				return width, height
			}
		}
		if width <= 0 {
			width = parsed[2]
		}
		if height <= 0 {
			height = parsed[3]
		}
	}
	return width, height
}

// svgDimensions accepts only a complete, bounded static vector document.
// XML is parsed structurally; bytes are preserved for the published asset.
func svgDimensions(data []byte) (int, int, bool) {
	if len(data) == 0 || len(data) > maxSVGBytes {
		return 0, 0, false
	}
	var source io.Reader = bytes.NewReader(data)
	utf16 := bytes.HasPrefix(data, []byte{0xff, 0xfe}) || bytes.HasPrefix(data, []byte{0xfe, 0xff})
	if utf16 {
		source = transform.NewReader(source, unicode.UTF16(unicode.LittleEndian, unicode.UseBOM).NewDecoder())
	}
	decoder := xml.NewDecoder(source)
	decoder.CharsetReader = func(label string, reader io.Reader) (io.Reader, error) {
		if utf16 && (strings.EqualFold(label, "utf-16") || strings.EqualFold(label, "utf-16le") || strings.EqualFold(label, "utf-16be")) {
			return reader, nil
		}
		return charset.NewReaderLabel(label, reader)
	}
	depth, nodes, styleDepth := 0, 0, 0
	seen, closed, prolog := false, false, true
	width, height := float64(0), float64(0)
	var style strings.Builder
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, 0, false
		}
		switch value := token.(type) {
		case xml.StartElement:
			prolog = false
			if closed || styleDepth > 0 || (value.Name.Space != "" && value.Name.Space != svgNamespace) || !svgElements[value.Name.Local] {
				return 0, 0, false
			}
			depth++
			nodes++
			if depth > 128 || nodes > 100000 {
				return 0, 0, false
			}
			if !seen {
				if value.Name.Local != "svg" {
					return 0, 0, false
				}
				seen = true
				width, height = svgRootDimensions(value.Attr)
			}
			attrs := map[xml.Name]bool{}
			for _, attr := range value.Attr {
				if attrs[attr.Name] {
					return 0, 0, false
				}
				attrs[attr.Name] = true
				if attr.Name.Space == "xmlns" || (attr.Name.Space == "" && attr.Name.Local == "xmlns") {
					continue
				}
				if attr.Name.Space != "" && attr.Name.Space != "http://www.w3.org/1999/xlink" && attr.Name.Space != "http://www.w3.org/XML/1998/namespace" {
					return 0, 0, false
				}
				key := strings.ToLower(attr.Name.Local)
				if strings.HasPrefix(key, "on") || key == "src" || key == "base" || !safeSVGStyle(attr.Value) || (key == "href" && !strings.HasPrefix(strings.TrimSpace(attr.Value), "#")) {
					return 0, 0, false
				}
			}
			if value.Name.Local == "style" {
				styleDepth = depth
				style.Reset()
			}
		case xml.EndElement:
			if styleDepth > 0 {
				if value.Name.Local != "style" || styleDepth != depth || !safeSVGStyle(style.String()) {
					return 0, 0, false
				}
				styleDepth = 0
			}
			depth--
			if depth == 0 {
				closed = true
			}
		case xml.CharData:
			if depth == 0 && strings.TrimSpace(strings.TrimPrefix(string(value), "\ufeff")) != "" {
				return 0, 0, false
			}
			if styleDepth > 0 {
				style.Write(value)
			}
		case xml.Directive:
			return 0, 0, false
		case xml.ProcInst:
			if !prolog || value.Target != "xml" {
				return 0, 0, false
			}
			prolog = false
		case xml.Comment:
			prolog = false
		}
	}
	if !seen || !closed || depth != 0 || math.IsNaN(width) || math.IsNaN(height) || math.IsInf(width, 0) || math.IsInf(height, 0) || width <= 0 || height <= 0 || width > 8192 || height > 8192 || width*height > 64_000_000 {
		return 0, 0, false
	}
	return int(math.Ceil(width)), int(math.Ceil(height)), true
}
