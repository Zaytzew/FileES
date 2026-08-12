package realmbranding

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"testing"
)

const onePixelPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="

func TestNormalize(t *testing.T) {
	got, err := Normalize(Branding{LeadingColor: "#008c45", LogoMediaType: "image/png", LogoBase64: onePixelPNG})
	if err != nil || got.LeadingColor != "#008C45" {
		t.Fatalf("Normalize()=%+v err=%v", got, err)
	}
	if _, err := base64.StdEncoding.DecodeString(got.LogoBase64); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareLogoReducesOrdinaryLargePNG(t *testing.T) {
	imageValue := image.NewNRGBA(image.Rect(0, 0, 1024, 1024))
	for y := 0; y < 1024; y++ {
		for x := 0; x < 1024; x++ {
			imageValue.SetNRGBA(x, y, color.NRGBA{R: uint8(x ^ y), G: uint8(x * y), B: uint8(x + y), A: 255})
		}
	}
	var source bytes.Buffer
	if err := png.Encode(&source, imageValue); err != nil {
		t.Fatal(err)
	}
	if source.Len() <= MaxLogoBytes {
		t.Fatalf("test source unexpectedly small: %d", source.Len())
	}
	got, err := PrepareLogo("#2D5A3D", "image/png", source.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := base64.StdEncoding.DecodeString(got.LogoBase64)
	if err != nil || len(encoded) > MaxLogoBytes || got.LeadingColor != "#2D5A3D" {
		t.Fatalf("prepared logo bytes=%d color=%s err=%v", len(encoded), got.LeadingColor, err)
	}
}

func TestNormalizeRejectsUnsafeLogoAndColor(t *testing.T) {
	for _, value := range []Branding{
		{LeadingColor: "red"},
		{LeadingColor: "#008C45", LogoMediaType: "image/svg+xml", LogoBase64: base64.StdEncoding.EncodeToString([]byte("<svg/>"))},
		{LeadingColor: "#008C45", LogoMediaType: "image/png", LogoBase64: base64.StdEncoding.EncodeToString([]byte("not an image"))},
	} {
		if _, err := Normalize(value); err == nil {
			t.Fatalf("Normalize(%+v) accepted unsafe input", value)
		}
	}
}
