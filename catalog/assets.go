package catalog

import (
	"bytes"
	"crypto/sha256"
	"encoding/xml"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"strings"
)

const MaxAssetBytes = 512 * 1024

// Asset is immutable, content-addressed image data. Data uses base64 in JSON.
type Asset struct {
	ID        string `json:"id"`
	MediaType string `json:"media_type"`
	Data      []byte `json:"data,omitempty"`
}

func NewAsset(data []byte) (Asset, error) {
	if len(data) == 0 || len(data) > MaxAssetBytes {
		return Asset{}, fmt.Errorf("logo must be 1–512 KiB")
	}
	media := ""
	if cfg, format, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
		if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > 4096 || cfg.Height > 4096 {
			return Asset{}, fmt.Errorf("logo dimensions exceed 4096px")
		}
		if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
			return Asset{}, fmt.Errorf("invalid logo image: %w", err)
		}
		media = "image/" + format
	} else {
		if err := validateSVG(data); err != nil {
			return Asset{}, err
		}
		media = "image/svg+xml"
	}
	return Asset{ID: fmt.Sprintf("sha256:%x", sha256.Sum256(data)), MediaType: media, Data: data}, nil
}

// SVGs are static shapes only: no scripts, CSS, external references or embedded
// documents. Restricting the vocabulary is simpler than sanitizing active SVG.
func validateSVG(data []byte) error {
	elements := strings.Fields("svg g path rect circle ellipse line polyline polygon defs linearGradient radialGradient stop clipPath mask title desc use symbol")
	attributes := strings.Fields("id viewBox width height x y x1 y1 x2 y2 cx cy r rx ry d points fill fill-rule fill-opacity stroke stroke-width stroke-linecap stroke-linejoin stroke-miterlimit stroke-dasharray stroke-dashoffset stroke-opacity opacity transform gradientTransform gradientUnits offset stop-color stop-opacity clip-path clip-rule mask preserveAspectRatio href version")
	allowed := func(values []string, value string) bool {
		for _, v := range values {
			if v == value {
				return true
			}
		}
		return false
	}
	d := xml.NewDecoder(bytes.NewReader(data))
	depth, roots := 0, 0
	for {
		token, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("logo must be a PNG, JPEG or static SVG")
		}
		switch t := token.(type) {
		case xml.StartElement:
			if depth == 0 {
				roots++
				if t.Name.Local != "svg" || roots != 1 {
					return fmt.Errorf("SVG needs one svg root")
				}
			}
			depth++
			if depth > 64 || !allowed(elements, t.Name.Local) || (t.Name.Space != "" && t.Name.Space != "http://www.w3.org/2000/svg") {
				return fmt.Errorf("unsupported SVG element %s", t.Name.Local)
			}
			for _, a := range t.Attr {
				if a.Name.Local == "xmlns" && a.Name.Space == "" && a.Value == "http://www.w3.org/2000/svg" {
					continue
				}
				if a.Name.Space == "xmlns" && a.Name.Local == "xlink" && a.Value == "http://www.w3.org/1999/xlink" {
					continue
				}
				if !allowed(attributes, a.Name.Local) || (a.Name.Space != "" && !(a.Name.Space == "http://www.w3.org/1999/xlink" && a.Name.Local == "href")) {
					return fmt.Errorf("unsupported SVG attribute %s", a.Name.Local)
				}
				v := strings.TrimSpace(a.Value)
				if a.Name.Local == "href" && !strings.HasPrefix(v, "#") {
					return fmt.Errorf("SVG references must be local")
				}
				// Reject CSS escapes and external paint servers, including mixed case.
				if strings.ContainsAny(v, "\\\"'") {
					return fmt.Errorf("unsupported SVG attribute value")
				}
				lower := strings.ToLower(v)
				if strings.Contains(lower, "url") && !(strings.HasPrefix(lower, "url(#") && strings.HasSuffix(lower, ")") && !strings.ContainsAny(lower[5:len(lower)-1], "() \t\r\n")) {
					return fmt.Errorf("SVG paint references must be local")
				}
			}
		case xml.EndElement:
			depth--
		case xml.Directive:
			return fmt.Errorf("SVG directives are not supported")
		case xml.ProcInst:
			if t.Target != "xml" || roots != 0 {
				return fmt.Errorf("SVG processing instructions are not supported")
			}
		case xml.CharData:
			if depth == 0 && strings.TrimSpace(string(t)) != "" {
				return fmt.Errorf("unexpected SVG text")
			}
		}
	}
	if roots != 1 || depth != 0 {
		return fmt.Errorf("invalid SVG")
	}
	return nil
}
