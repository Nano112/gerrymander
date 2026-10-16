package catalog

import (
	"bytes"
	"image"
	"image/png"
	"testing"
)

func TestStaticLogos(t *testing.T) {
	good := []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><defs><linearGradient id="a"><stop offset="0" stop-color="#fff"/></linearGradient></defs><path fill="url(#a)" d="M0 0L10 10"/></svg>`)
	a, err := NewAsset(good)
	if err != nil || !ValidAssetID(a.ID) || a.MediaType != "image/svg+xml" {
		t.Fatalf("valid SVG: %+v %v", a, err)
	}
	for _, bad := range []string{
		`<svg><script>alert(1)</script></svg>`, `<svg onload="alert(1)"/>`,
		`<svg><foreignObject/></svg>`, `<svg><use href="https://example.com/x"/></svg>`,
		`<svg style="background:url(https://example.com)"/>`, `<svg><path fill="url(https://example.com)"/></svg>`,
		`<!DOCTYPE svg [<!ENTITY x SYSTEM "file:///etc/passwd">]><svg>&x;</svg>`,
		`<?xml-stylesheet href="https://example.com"?><svg/>`, `<svg/><svg/>`,
		`<html/>`, `not an image`, `<svg><animate attributeName="href"/></svg>`,
	} {
		if _, err := NewAsset([]byte(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	var buf bytes.Buffer
	png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 4, 4)))
	a, err = NewAsset(buf.Bytes())
	if err != nil || a.MediaType != "image/png" {
		t.Fatalf("PNG: %v", err)
	}
	if _, err := NewAsset(make([]byte, MaxAssetBytes+1)); err == nil {
		t.Fatal("oversized accepted")
	}
}
