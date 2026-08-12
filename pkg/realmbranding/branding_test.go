package realmbranding

import (
	"encoding/base64"
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
